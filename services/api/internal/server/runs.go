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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
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
	// JobID is the Vesqira job whose outputs this run is; ParentRunID is the run that job was drafted from.
	JobID       *int64 `json:"jobId"`
	ParentRunID *int64 `json:"parentRunId"`
}

// hintSource is what the runs list reads the registered files' names and sizes through (the catalog; a test counts calls).
type hintSource interface {
	HintIndex() (map[catalog.HintKey]int64, error)
}

// hintsFor reads the name and size index once, and only when some run has an input to look for.
func (s *Server) hintsFor(rs []runs.Run) map[catalog.HintKey]int64 {
	want := false
	for _, r := range rs {
		want = want || (r.InputName != "" && r.InputSizeBytes > 0)
	}
	if !want {
		return nil
	}
	var src hintSource = s.Store
	if s.Hints != nil {
		src = s.Hints
	}
	idx, err := src.HintIndex()
	if err != nil {
		log.Printf("runs: input hints: %v", err) // the hint is optional: the runs are still listed
		return nil
	}
	return idx
}

// runView describes one run. Lists use runViewWith and read the job links once for all runs.
func (s *Server) runView(r runs.Run) runJSON {
	var lin map[int64]jobs.Lineage
	if s.Jobs != nil {
		if j, err := s.Jobs.ForRun(r.ID); err == nil {
			lin = map[int64]jobs.Lineage{r.ID: {JobID: j.ID, ParentRunID: j.ParentRunID}}
		}
	}
	return s.runViewWith(r, lin, s.hintsFor([]runs.Run{r}))
}

func (s *Server) runViewWith(r runs.Run, lin map[int64]jobs.Lineage, hints map[catalog.HintKey]int64) runJSON {
	out := runJSON{Run: r}
	if l, ok := lin[r.ID]; ok {
		id := l.JobID
		out.JobID, out.ParentRunID = &id, l.ParentRunID
	}
	if r.InputName != "" && r.InputSizeBytes > 0 {
		if id, ok := hints[catalog.HintKey{Name: r.InputName, Size: r.InputSizeBytes}]; ok {
			out.InputHint = &struct {
				DatasetID int64  `json:"datasetId"`
				Basis     string `json:"basis"`
			}{id, "inferred from file name and size; not proof of identity"}
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
	var lin map[int64]jobs.Lineage
	if s.Jobs != nil {
		if lin, err = s.Jobs.Lineages(); err != nil {
			log.Printf("runs: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	hints := s.hintsFor(rs) // one read of the catalog for every run, not one per run
	out := make([]runJSON, 0, len(rs))
	for _, x := range rs {
		out = append(out, s.runViewWith(x, lin, hints))
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

// rerunVGT is the settings a new draft starts from. Every field is null when it is not known: an imported run does not
// say which host, code folder or config produced it, and nothing here is guessed.
type rerunVGT struct {
	RepoDir        *string           `json:"repoDir"`
	ConfigPath     *string           `json:"configPath"`
	BaseConfigText *string           `json:"baseConfigText"`
	Set            map[string]string `json:"set"`
	CPUs           *int              `json:"cpus"`
	Mem            *string           `json:"mem"`
	Time           *string           `json:"time"`
	Partition      *string           `json:"partition"`
	Account        *string           `json:"account"`
}

type rerunJSON struct {
	ParentRunID int64        `json:"parentRunId"`
	Source      string       `json:"source"` // job | imported
	HostID      *int64       `json:"hostId"`
	Template    string       `json:"template"`
	Name        string       `json:"name"`
	VGT         rerunVGT     `json:"vgt"`
	Params      []runs.Param `json:"params"`
	Notes       []string     `json:"notes"`
	// ConfigChanged compares the stored config text with the file on the host now; null when not asked or not reachable.
	ConfigChanged *bool `json:"configChanged"`
}

var notNameChar = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// againName suggests a job name that the job name rules accept (letters, digits, . _ -, at most 40).
func againName(base string) string {
	b := strings.Trim(notNameChar.ReplaceAllString(base, "-"), "-")
	if len(b) > 40-len("-again") {
		b = strings.TrimRight(b[:40-len("-again")], "-")
	}
	if b == "" {
		return "run-again"
	}
	return b + "-again"
}

func strp(v string) *string { return &v }

// rerunRun answers "run again with...": the exact request that produced the run, as a prefill for a new draft.
func (s *Server) rerunRun(w http.ResponseWriter, r *http.Request) {
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
	out := rerunJSON{ParentRunID: id, Source: "imported", Template: "vgt", Name: againName(run.Name), Params: run.Params, Notes: []string{}, VGT: rerunVGT{Set: map[string]string{}}}
	if out.Params == nil {
		out.Params = []runs.Param{}
	}
	var job jobs.Job
	if s.Jobs != nil {
		if job, err = s.Jobs.ForRun(id); err != nil && !errors.Is(err, jobs.ErrNotFound) {
			log.Printf("runs: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	if err != nil { // no job of ours produced this run
		out.Notes = append(out.Notes, "This run was imported, so the host, code folder and config it ran with are not known here.")
		writeJSON(w, 200, out)
		return
	}
	hid := job.HostID
	out.HostID = &hid
	req, ok := job.VGTRequest()
	if !ok {
		out.Notes = append(out.Notes, "This run came from a job recorded before its settings were kept, so the code folder, config and resources it ran with are not known here.")
		writeJSON(w, 200, out)
		return
	}
	out.Source = "job"
	set := req.Set
	if set == nil {
		set = map[string]string{}
	}
	cpus := req.CPUs
	out.VGT = rerunVGT{RepoDir: strp(req.RepoDir), ConfigPath: strp(req.ConfigPath), BaseConfigText: strp(req.BaseConfigText), Set: set,
		CPUs: &cpus, Mem: strp(req.Mem), Time: strp(req.Time), Partition: strp(req.Partition), Account: strp(req.Account)}
	if req.ConfigPath != "" && s.HostDB != nil && s.Remote != nil {
		if h, err := s.HostDB.Get(job.HostID); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			text, code, _ := s.remoteText(ctx, h, req.ConfigPath)
			cancel()
			if code == 0 {
				changed := strings.TrimRight(text, "\r\n") != strings.TrimRight(req.BaseConfigText, "\r\n")
				out.ConfigChanged = &changed
				if changed {
					out.Notes = append(out.Notes, "The config file on the host has changed since this run. The draft uses the config text the run started from, not the file as it is now.")
				}
			}
		}
	}
	writeJSON(w, 200, out)
}
