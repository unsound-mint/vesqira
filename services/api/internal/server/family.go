package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
)

// Family agreement (ADR 0070): maps that differ in one parameter, compared with a chosen reference and with each other. The worker
// (analysis.py family_agreement, family.py) computes; this file checks that the maps can be compared at all (one kind, one grid,
// angles stated the same way), runs it on this computer and registers its maps like any analysis result.

const maxFamily = 12

type familyReq struct {
	DatasetIDs  []int64 `json:"datasetIds"`
	ReferenceID int64   `json:"referenceId"`
	Spread      string  `json:"spread"` // std or mad, for scalar maps
	// Channel picks the plane of every member that is a cube; omitted for single-plane maps.
	Channel *int `json:"channel"`
	// Labels name the members in the result, in the order of DatasetIDs (for example "ker = 3" for a sweep); a member without one is named by its file.
	Labels []string `json:"labels"`
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
	if in.Labels != nil && len(in.Labels) != len(in.DatasetIDs) {
		fail(w, 422, "give one label for each map, or none")
		return
	}
	for _, l := range in.Labels {
		if utf8.RuneCountInString(l) > 80 || strings.ContainsFunc(l, unicode.IsControl) {
			fail(w, 422, "a label is at most 80 characters, without control characters")
			return
		}
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
		real, _, status, m := s.authorize(r.Context(), d)
		if status != 0 {
			fail(w, status, m)
			return
		}
		ds[i], reals[i] = d, real
		members[i] = familyMember{Path: real, Label: d.Name, Channel: in.Channel}
		if in.Labels != nil && strings.TrimSpace(in.Labels[i]) != "" {
			members[i].Label = strings.TrimSpace(in.Labels[i])
		}
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
	// The comparison is made where the maps are: here, or on one host by that host's copy of the program (ADR 0070). Maps in two places
	// are not copied to meet: that is a transfer the person decides on.
	if where := familyPlaces(ds); where != "" {
		fail(w, 409, where)
		return
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
	tag := hex.EncodeToString(b[:])
	fileName := fmt.Sprintf("family-%d-%s.fits", ref.ID, tag)
	args := []string{"--input", reals[refAt], "--family", string(famJSON), "--reference-index", fmt.Sprint(refAt), "--spread", in.Spread}
	var res analysisOutcome
	var spreadPath string
	var extra []string // the comparison maps' paths, in the order of the summary's comparison_maps
	var register func(string) (catalog.Dataset, int, string)
	cleanup := func() {}
	if ref.HostID != 0 {
		// On the host: nothing of the maps crosses, the host's derived folder receives the new files (all or none, hostrun.py) and only the
		// summary comes back. Vesqira deletes nothing there, so a result it cannot register stays as an inert file.
		if len(famJSON) > 15000 {
			fail(w, 413, "the maps' paths are too long to send to the host in one request; choose fewer maps or maps with shorter paths")
			return
		}
		h, code, msg := s.hostOf(ref.HostID)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		var out analysisOutcome
		if out, spreadPath, code, msg = s.hostRunAnalysis(r.Context(), h, "family", fileName, args, nil); code != 0 {
			if code == 422 && msg == "bad request" {
				msg = "the analysis program on " + h.Name + " does not support a family comparison (an older copy); check the host (Hosts, Check) to put the current one there, then try again"
			}
			fail(w, code, msg)
			return
		}
		res, extra = out, out.Files
		register = func(p string) (catalog.Dataset, int, string) { return s.registerRemoteFile(waiting(r.Context()), h, p) }
	} else {
		stem := filepath.Join(s.derivedDirOf(wsOf(r)), "family-"+fmt.Sprint(ref.ID)+"-"+tag)
		spreadPath = stem + ".fits"
		cleanup = func() {
			_ = os.Remove(spreadPath)
			for i := range ds {
				_ = os.Remove(fmt.Sprintf("%s-m%d.fits", stem, i))
			}
		}
		var code int
		var msg string
		res, code, msg = s.runAnalysisProgram(r, append([]string{"-P", s.Analysis.Script, "family_agreement"}, append(args, "--out", spreadPath)...))
		if code != 0 {
			cleanup()
			fail(w, code, msg)
			return
		}
		register = func(p string) (catalog.Dataset, int, string) { return s.registerPath(waiting(r.Context()), p) }
	}
	var sum struct {
		Comparison []int `json:"comparison_maps"`
	}
	if json.Unmarshal(res.Summary, &sum) != nil || len(sum.Comparison) != len(ds)-1 {
		cleanup()
		fail(w, 502, "the analysis answered unexpectedly")
		return
	}
	if ref.HostID != 0 { // the host names its files: each must be the comparison map asked for, beside the spread map
		ok := len(extra) == len(sum.Comparison)
		for k, i := range sum.Comparison {
			ok = ok && k < len(extra) && hostFileNamed(extra[k], fmt.Sprintf("family-%d-%s-m%d.fits", ref.ID, tag, i)) && path.Dir(extra[k]) == path.Dir(spreadPath)
		}
		if !ok {
			fail(w, 502, "the host answered unexpectedly about the comparison maps; no result was recorded")
			return
		}
	} else {
		for _, i := range sum.Comparison {
			extra = append(extra, fmt.Sprintf("%s-m%d.fits", strings.TrimSuffix(spreadPath, ".fits"), i))
		}
	}
	sources := slices.Clone(in.DatasetIDs)
	type registered struct {
		Role    string          `json:"role"` // "spread" or "difference"
		Member  int             `json:"member"`
		Dataset catalog.Dataset `json:"dataset"`
	}
	var out []registered
	save := func(file, role string, member int) bool {
		nd, code, msg := register(file)
		if code != 0 {
			cleanup()
			if ref.HostID != 0 {
				msg = "the comparison was made on the host (its files are in the host's derived folder) but could not be registered: " + msg
			}
			fail(w, code, msg)
			return false
		}
		pm := map[string]any{"role": role, "member": member, "referenceId": ref.ID, "spread": in.Spread, "channel": in.Channel, "datasetIds": in.DatasetIDs, "run": tag}
		if ref.HostID != 0 {
			pm["hostId"] = ref.HostID
		}
		if res.Corrections != nil {
			pm["corrections"] = res.Corrections // the correction state the maps were read with (ADR 0061)
		}
		params, _ := json.Marshal(pm)
		if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: "family_agreement", Params: params, Sources: sources, Summary: res.Summary, Software: res.Software}); err != nil {
			log.Printf("derived: %v", err)
		}
		out = append(out, registered{role, member, nd})
		return true
	}
	if !save(spreadPath, "spread", -1) {
		return
	}
	for k, i := range sum.Comparison {
		if !save(extra[k], "difference", i) {
			return
		}
	}
	writeJSON(w, 201, map[string]any{"summary": res.Summary, "maps": out, "datasetIds": in.DatasetIDs, "referenceId": ref.ID})
}

// familyPlaces is a sentence when the maps are not all in one place: all on this computer, or all on one host.
func familyPlaces(ds []catalog.Dataset) string {
	var here, there []string
	hostID := int64(0)
	for _, d := range ds {
		if d.HostID == 0 {
			here = append(here, d.Name)
			continue
		}
		if hostID != 0 && d.HostID != hostID {
			return d.Name + " is on another host than " + there[0] + ": a comparison is made where the maps are, and Vesqira does not copy maps to meet. Choose maps on one host"
		}
		hostID = d.HostID
		there = append(there, d.Name)
	}
	if len(here) > 0 && len(there) > 0 {
		return strings.Join(there, ", ") + " is on a host and " + strings.Join(here, ", ") + " is on this computer: a comparison is made where the maps are, and Vesqira does not copy maps to meet. Choose maps in one place, or transfer one set first"
	}
	return ""
}

// A family candidate is a set of derived datasets made from one source by one operation with every option equal but one. It is
// recognised from the derived records (op, source and options as they were run), so nothing here guesses from file names.
type familyCandidate struct {
	Op       string             `json:"op"`
	SourceID int64              `json:"sourceId"`
	Varying  string             `json:"varying"` // the one option that differs
	Fixed    map[string]string  `json:"fixed"`
	Members  []familyCandMember `json:"members"`
}

type familyCandMember struct {
	DatasetID int64  `json:"datasetId"`
	Value     string `json:"value"`
}

// familyOps are the derived operations whose results form families: spectral smoothings of one cube, convolutions of one map.
var familyOps = []string{"smooth_spectral", "convolve"}

// familyCandidates groups the derived records of this workspace into families: for each option, the results of one source that agree on
// every other option and give at least two values of it.
func (s *Server) familyCandidates(w http.ResponseWriter, r *http.Request) {
	out := []familyCandidate{}
	if s.Derived != nil {
		for _, op := range familyOps {
			recs, err := s.Derived.ByOp(wsOf(r), op)
			if err != nil {
				log.Printf("derived: %v", err)
				fail(w, 500, "internal error")
				return
			}
			out = append(out, groupFamilies(op, recs)...)
		}
	}
	writeJSON(w, 200, map[string]any{"families": out})
}

func groupFamilies(op string, recs []derived.Record) []familyCandidate {
	type rec struct {
		id   int64
		src  int64
		opts map[string]string
	}
	var rs []rec
	keys := map[string]bool{}
	for _, r := range recs {
		var p struct {
			Options map[string]string `json:"options"`
		}
		if len(r.Sources) != 1 || json.Unmarshal(r.Params, &p) != nil || len(p.Options) == 0 {
			continue
		}
		rs = append(rs, rec{r.DatasetID, r.Sources[0], p.Options})
		for k := range p.Options {
			keys[k] = true
		}
	}
	var out []familyCandidate
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		groups := map[string]*familyCandidate{}
		var order []string
		for _, r := range rs {
			rest := map[string]string{}
			for kk, v := range r.opts {
				if kk != k {
					rest[kk] = v
				}
			}
			b, _ := json.Marshal(rest) // map keys are sorted: equal options give equal text
			id := fmt.Sprintf("%d|%s", r.src, b)
			g := groups[id]
			if g == nil {
				g = &familyCandidate{Op: op, SourceID: r.src, Varying: k, Fixed: rest}
				groups[id] = g
				order = append(order, id)
			}
			g.Members = append(g.Members, familyCandMember{r.id, r.opts[k]})
		}
		for _, id := range order {
			g := groups[id]
			vals := map[string]bool{}
			for _, m := range g.Members {
				vals[m.Value] = true
			}
			if len(vals) >= 2 {
				out = append(out, *g)
			}
		}
	}
	return out
}
