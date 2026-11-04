package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// runSource abstracts where a run folder lives: this machine (inside a source root) or a remote host.
type runSource interface {
	Key() string                                                                  // unique per folder (host-qualified for remote ones)
	Meta(ctx context.Context) (data []byte, dirName string, code int, msg string) // run_metadata.toml and the folder's name
	Register(ctx context.Context, name string) (d catalog.Dataset, found bool, code int, msg string)
}

type localRun struct {
	s    *Server
	dir  string
	meta string
}

func (l localRun) Key() string { return l.dir }
func (l localRun) Meta(context.Context) ([]byte, string, int, string) {
	info, err := os.Stat(l.meta)
	if err != nil || info.Size() > runs.MaxMetadataBytes {
		return nil, "", 422, "the run metadata cannot be read"
	}
	data, err := os.ReadFile(l.meta)
	if err != nil {
		return nil, "", 422, "the run metadata cannot be read"
	}
	return data, filepath.Base(l.dir), 0, ""
}
func (l localRun) Register(ctx context.Context, name string) (catalog.Dataset, bool, int, string) {
	real, err := l.s.Roots.Resolve(filepath.Join(l.dir, name)) // outputs must sit next to the metadata, inside the roots
	if err != nil {
		return catalog.Dataset{}, false, 0, "" // a missing output is skipped, not invented
	}
	d, code, msg := l.s.registerPath(ctx, real)
	return d, code == 0, code, msg
}

type remoteRun struct {
	s   *Server
	h   hosts.Host
	dir string
}

func (r remoteRun) Key() string { return fmt.Sprintf("h%d:%s", r.h.ID, r.dir) }
func (r remoteRun) Meta(ctx context.Context) ([]byte, string, int, string) {
	text, code, msg := r.s.remoteText(ctx, r.h, path.Join(r.dir, "run_metadata.toml"))
	if code != 0 {
		return nil, "", code, msg
	}
	return []byte(text), path.Base(r.dir), 0, ""
}
func (r remoteRun) Register(ctx context.Context, name string) (catalog.Dataset, bool, int, string) {
	d, code, msg := r.s.registerRemoteFile(ctx, r.h, path.Join(r.dir, name))
	if code == 403 {
		return catalog.Dataset{}, false, 0, "" // not available there: skipped
	}
	return d, code == 0, code, msg
}

// importFrom parses the run's metadata, registers the outputs found next to it, links them as a Run, pairs the
// reliability map and proposes the axial interpretation as the adapter's (never the user's, never overriding theirs).
func (s *Server) importFrom(ctx context.Context, src runSource) (runs.Run, int, string) {
	data, name, code, msg := src.Meta(ctx)
	if code != 0 {
		return runs.Run{}, code, msg
	}
	parsed, err := runs.ParseVGT(data, name)
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	var outs []runs.Output
	byRole := map[string]catalog.Dataset{}
	for _, role := range []string{"psi", "reliability", "support", "fit_rbar2"} {
		file, ok := parsed.OutputFiles[role]
		if !ok {
			continue
		}
		d, found, code, msg := src.Register(ctx, file)
		if code != 0 {
			return runs.Run{}, code, file + ": " + msg
		}
		if !found {
			continue
		}
		byRole[role] = d
		outs = append(outs, runs.Output{Role: role, DatasetID: d.ID, Name: d.Name})
	}
	if len(outs) == 0 {
		return runs.Run{}, 422, "none of the run's output files were found next to its run_metadata.toml"
	}
	run, err := s.Runs.Save(src.Key(), parsed, outs)
	if err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	if psi, ok := byRole["psi"]; ok {
		cur, _ := s.Store.Get(psi.ID)
		if cur.Interpretation == nil || cur.Interpretation.DeclaredBy != "user" {
			var rel *int64
			if rd, ok := byRole["reliability"]; ok && rd.HostID == psi.HostID {
				if why, err := compat.Grids(psi.Header, rd.Header); err == nil && len(why) == 0 {
					rel = &rd.ID
				}
			}
			if err := s.Store.SetInterpretation(cur, catalog.Interpretation{Kind: "axial_orientation", Unit: "rad", ReliabilityID: rel, DeclaredBy: "vgt-run-adapter"}); err != nil {
				log.Printf("runs: %v", err)
			}
		}
	}
	return run, 0, ""
}

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
	run, code, msg := s.importFrom(r.Context(), localRun{s: s, dir: filepath.Dir(realMeta), meta: realMeta})
	if code != 0 {
		fail(w, code, msg)
		return
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
	Reproducibility []runs.Fact `json:"reproducibility"`
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
	out.Reproducibility = runs.Reproducibility(r, out.InputHint != nil)
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
