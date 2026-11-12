package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
)

// Family agreement (ADR 0069): maps that differ in one parameter, compared with a chosen reference and with each other. The worker
// (analysis.py family_agreement, family.py) computes; this file checks that the maps can be compared at all (one kind, one grid,
// angles stated the same way), runs it on this computer and registers its maps like any analysis result.

const maxFamily = 12

type familyReq struct {
	DatasetIDs  []int64 `json:"datasetIds"`
	ReferenceID int64   `json:"referenceId"`
	Spread      string  `json:"spread"` // std or mad, for scalar maps
	// Channel picks the plane of every member that is a cube; omitted for single-plane maps.
	Channel *int `json:"channel"`
}

type familyMember struct {
	Path         string `json:"path"`
	Label        string `json:"label"`
	AxialUnit    string `json:"axial_unit,omitempty"`
	DeclaredUnit string `json:"declared_unit,omitempty"`
	Channel      *int   `json:"channel,omitempty"`
}

// familyAgreement runs the agreement of the chosen maps. It refuses, with a sentence, maps that do not share a grid: nothing is resampled.
func (s *Server) familyAgreement(w http.ResponseWriter, r *http.Request) {
	var in familyReq
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if !s.analysisDeps() {
		fail(w, 503, "analysis is not available on this server")
		return
	}
	if n := len(in.DatasetIDs); n < 2 || n > maxFamily {
		fail(w, 422, fmt.Sprintf("choose between 2 and %d maps", maxFamily))
		return
	}
	if in.Spread == "" {
		in.Spread = "std"
	}
	if in.Spread != "std" && in.Spread != "mad" {
		fail(w, 422, "the spread is std or mad")
		return
	}
	if in.Channel != nil && (*in.Channel < 0 || *in.Channel > 1<<20) {
		fail(w, 422, "bad channel")
		return
	}
	refAt := 0
	if in.ReferenceID != 0 {
		refAt = slices.Index(in.DatasetIDs, in.ReferenceID)
		if refAt < 0 {
			fail(w, 422, "the reference is not one of the chosen maps")
			return
		}
	}
	ds := make([]catalog.Dataset, len(in.DatasetIDs))
	members := make([]familyMember, len(ds))
	reals := make([]string, len(ds))
	axial := 0
	for i, id := range in.DatasetIDs {
		if slices.Index(in.DatasetIDs, id) != i {
			fail(w, 422, "a map is listed twice")
			return
		}
		d, code, msg := s.imageDataset(wsOf(r), id)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if d.HostID != 0 {
			fail(w, 409, d.Name+" lives on another host: the agreement is computed on this computer, from maps registered here")
			return
		}
		real, _, status, m := s.authorize(r.Context(), d)
		if status != 0 {
			fail(w, status, m)
			return
		}
		ds[i], reals[i] = d, real
		members[i] = familyMember{Path: real, Label: d.Name, Channel: in.Channel}
		if d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation" {
			axial++
			if d.Interpretation.Stale {
				fail(w, 409, d.Name+" changed since you declared it as an orientation map; declare it again to confirm the unit and frame still apply")
				return
			}
			members[i].AxialUnit = d.Interpretation.Unit
			members[i].Channel = nil // an orientation map is a single plane
		} else if u := declaredUnitOf(d); u != "" {
			members[i].DeclaredUnit = u
		}
	}
	if axial != 0 && axial != len(ds) {
		fail(w, 422, "the chosen maps mix orientation maps with scalar maps: a difference of those is not meaningful, so choose maps of one kind")
		return
	}
	ref := ds[refAt]
	for i, d := range ds {
		if i == refAt {
			continue
		}
		if axial > 0 {
			if why := axialStatedDifferently(ref.Interpretation, d.Interpretation); why != "" {
				fail(w, 422, why)
				return
			}
		}
		if why, err := compat.Grids(effectiveHeader(ref), effectiveHeader(d)); err != nil || len(why) > 0 {
			writeJSON(w, 409, map[string]any{"error": d.Name + " and " + ref.Name + " are not known to share a grid, so a pixel-by-pixel comparison is not meaningful; nothing is resampled here. Align one onto the other first", "reasons": why})
			return
		}
	}
	famJSON, _ := json.Marshal(members)
	var b [4]byte
	_, _ = rand.Read(b[:])
	stem := filepath.Join(s.derivedDirOf(wsOf(r)), fmt.Sprintf("family-%d-%s", ref.ID, hex.EncodeToString(b[:])))
	spreadPath := stem + ".fits"
	cleanup := func() {
		_ = os.Remove(spreadPath)
		for i := range ds {
			_ = os.Remove(fmt.Sprintf("%s-m%d.fits", stem, i))
		}
	}
	args := []string{"-P", s.Analysis.Script, "family_agreement", "--input", reals[refAt], "--family", string(famJSON), "--reference-index", fmt.Sprint(refAt), "--spread", in.Spread, "--out", spreadPath}
	res, code, msg := s.runAnalysisProgram(r, args)
	if code != 0 {
		cleanup()
		fail(w, code, msg)
		return
	}
	var sum struct {
		Comparison []int `json:"comparison_maps"`
	}
	if json.Unmarshal(res.Summary, &sum) != nil || len(sum.Comparison) != len(ds)-1 {
		cleanup()
		fail(w, 502, "the analysis answered unexpectedly")
		return
	}
	sources := slices.Clone(in.DatasetIDs)
	type registered struct {
		Role    string          `json:"role"` // "spread" or "difference"
		Member  int             `json:"member"`
		Dataset catalog.Dataset `json:"dataset"`
	}
	var out []registered
	save := func(path, role string, member int) bool {
		nd, code, msg := s.registerPath(waiting(r.Context()), path)
		if code != 0 {
			cleanup()
			fail(w, code, msg)
			return false
		}
		params, _ := json.Marshal(map[string]any{"role": role, "member": member, "referenceId": ref.ID, "spread": in.Spread, "channel": in.Channel, "datasetIds": in.DatasetIDs})
		if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: "family_agreement", Params: params, Sources: sources, Summary: res.Summary, Software: res.Software}); err != nil {
			log.Printf("derived: %v", err)
		}
		out = append(out, registered{role, member, nd})
		return true
	}
	if !save(spreadPath, "spread", -1) {
		return
	}
	for _, i := range sum.Comparison {
		if !save(fmt.Sprintf("%s-m%d.fits", stem, i), "difference", i) {
			return
		}
	}
	writeJSON(w, 201, map[string]any{"summary": res.Summary, "maps": out, "datasetIds": in.DatasetIDs, "referenceId": ref.ID})
}
