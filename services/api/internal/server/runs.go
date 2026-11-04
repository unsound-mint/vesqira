package server

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// importRun reads a VGT run folder (or its run_metadata.toml), registers the output maps it finds next to the
// metadata, links them as a Run, pairs the reliability map and proposes the axial interpretation. The proposal is
// labelled as coming from the adapter (not the file, not the user) and the user can override it.
func (s *Server) importRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	meta := in.Path
	if filepath.Base(meta) != "run_metadata.toml" {
		meta = filepath.Join(meta, "run_metadata.toml")
	}
	realMeta, err := s.Roots.Resolve(meta) // same containment rules as any file: inside a source root, regular file
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	info, err := os.Stat(realMeta)
	if err != nil || info.Size() > runs.MaxMetadataBytes {
		fail(w, 422, "the run metadata cannot be read")
		return
	}
	data, err := os.ReadFile(realMeta)
	if err != nil {
		fail(w, 422, "the run metadata cannot be read")
		return
	}
	dir := filepath.Dir(realMeta)
	parsed, err := runs.ParseVGT(data, filepath.Base(dir))
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	var outs []runs.Output
	byRole := map[string]catalog.Dataset{}
	for _, role := range []string{"psi", "reliability", "support", "fit_rbar2"} {
		name, ok := parsed.OutputFiles[role]
		if !ok {
			continue
		}
		real, err := s.Roots.Resolve(filepath.Join(dir, name)) // outputs must sit next to the metadata, inside the roots
		if err != nil {
			continue // a missing output is skipped, not invented
		}
		d, code, msg := s.registerPath(r.Context(), real)
		if code != 0 {
			fail(w, code, name+": "+msg)
			return
		}
		byRole[role] = d
		outs = append(outs, runs.Output{Role: role, DatasetID: d.ID, Name: d.Name})
	}
	if len(outs) == 0 {
		fail(w, 422, "none of the run's output files were found next to its run_metadata.toml")
		return
	}
	run, err := s.Runs.Save(dir, parsed, outs)
	if err != nil {
		log.Printf("runs: %v", err)
		fail(w, 500, "internal error")
		return
	}
	// adapter proposal: psi is an axial angle in radians (VGT writes 0.5*atan2(U,Q)+pi/2); never overrides a user's choice
	if psi, ok := byRole["psi"]; ok {
		cur, _ := s.Store.Get(psi.ID)
		if cur.Interpretation == nil || cur.Interpretation.DeclaredBy != "user" {
			var rel *int64
			if rd, ok := byRole["reliability"]; ok {
				if why, err := compat.Grids(psi.Header, rd.Header); err == nil && len(why) == 0 {
					rel = &rd.ID
				}
			}
			if err := s.Store.SetInterpretation(cur, catalog.Interpretation{Kind: "axial_orientation", Unit: "rad", ReliabilityID: rel, DeclaredBy: "vgt-run-adapter"}); err != nil {
				log.Printf("runs: %v", err)
			}
		}
	}
	writeJSON(w, 201, s.runView(run))
}

// runJSON adds the inferred link from a run to a registered copy of its input cube (name and size only).
type runJSON struct {
	runs.Run
	InputHint *struct {
		DatasetID int64  `json:"datasetId"`
		Basis     string `json:"basis"`
	} `json:"inputHint"`
}

func (s *Server) runView(r runs.Run) runJSON {
	out := runJSON{Run: r}
	if r.InputName != "" && r.InputSizeBytes > 0 {
		if ds, err := s.Store.List(); err == nil {
			for _, d := range ds {
				if d.Name == r.InputName && d.SizeBytes == r.InputSizeBytes {
					out.InputHint = &struct {
						DatasetID int64  `json:"datasetId"`
						Basis     string `json:"basis"`
					}{d.ID, "inferred from file name and size; not proof of identity"}
					break
				}
			}
		}
	}
	return out
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Runs.List()
	if err != nil {
		log.Printf("runs: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := make([]runJSON, 0, len(rs))
	for _, x := range rs {
		out = append(out, s.runView(x))
	}
	writeJSON(w, 200, map[string]any{"runs": out})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	run, err := s.Runs.Get(id)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "run not found")
		return
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, s.runView(run))
}
