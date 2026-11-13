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
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// runSource abstracts where a run folder lives: this machine (inside a source root) or a remote host.
type runSource interface {
	Key() string                                                                  // unique per folder (host-qualified for remote ones)
	RecordFile() string                                                           // the name of the run record, which a tool profile claims (run_metadata.toml for VGT.jl)
	Meta(ctx context.Context) (data []byte, dirName string, code int, msg string) // the run record and the folder's name
	Register(ctx context.Context, name string) (d catalog.Dataset, found bool, code int, msg string)
	// HostID is 0 on this machine. Folder is the run folder's real path on its machine and the names of the files in it (a
	// decomposition written as FITS maps is picked out of them); a host that does not answer is an error code, not an empty folder.
	HostID() int64
	Folder(ctx context.Context) (real string, names []string, code int, msg string)
}

type localRun struct {
	s    *Server
	dir  string
	meta string
}

func (l localRun) Key() string        { return l.dir }
func (l localRun) RecordFile() string { return filepath.Base(l.meta) }
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
func (l localRun) HostID() int64 { return 0 }
func (l localRun) Folder(context.Context) (string, []string, int, string) {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return "", nil, 422, "the run's folder cannot be read"
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return l.dir, names, 0, ""
}
func (l localRun) Register(ctx context.Context, name string) (catalog.Dataset, bool, int, string) {
	real, err := l.s.roots(wsCtx(ctx)).Resolve(filepath.Join(l.dir, name)) // outputs must sit next to the metadata, inside the roots
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
	// file is the run record's name; "" is the record of the VGT job template (a Vesqira job's output folder).
	file string
}

func (r remoteRun) Key() string { return fmt.Sprintf("h%d:%s", r.h.ID, r.dir) }
func (r remoteRun) RecordFile() string {
	if r.file != "" {
		return r.file
	}
	if p := r.s.profiles().ByKind("vgt"); p != nil {
		return p.Record.File
	}
	return "run_metadata.toml"
}
func (r remoteRun) Meta(ctx context.Context) ([]byte, string, int, string) {
	text, code, msg := r.s.remoteText(ctx, r.h, path.Join(r.dir, r.RecordFile()))
	if code != 0 {
		return nil, "", code, msg
	}
	return []byte(text), path.Base(r.dir), 0, ""
}
func (r remoteRun) HostID() int64 { return r.h.ID }

// remoteListLimit is how many entries the host's worker lists for one folder (plane_worker.host_op "list"): a folder that fills it
// may have more, and a layout picked out of its names would be built from part of them.
const remoteListLimit = 500

func (r remoteRun) Folder(ctx context.Context) (string, []string, int, string) {
	real, files, dirs, err := r.s.remoteList(ctx, r.h, r.dir)
	switch {
	case ctx.Err() != nil:
		return "", nil, 502, "the import was interrupted before it finished; nothing was saved"
	case err != nil:
		return "", nil, 502, "the host is not reachable (is the VPN up?)"
	case real == "":
		return "", nil, 403, "not available"
	case len(files)+len(dirs) >= remoteListLimit:
		return "", nil, 422, fmt.Sprintf("the run's folder holds %d or more entries, more than are listed on a host; the files are not picked out of a part of them", remoteListLimit)
	}
	return real, files, 0, ""
}
func (r remoteRun) Register(ctx context.Context, name string) (catalog.Dataset, bool, int, string) {
	d, code, msg := r.s.registerRemoteFile(ctx, r.h, path.Join(r.dir, name))
	if code == 403 {
		return catalog.Dataset{}, false, 0, "" // not available there: skipped
	}
	return d, code == 0, code, msg
}

// profiles are the tool profiles runs are recognised by (ADR 0068): the shipped ones unless the store was given others.
func (s *Server) profiles() *runs.Registry {
	if s.Runs != nil {
		return s.Runs.Registry()
	}
	return runs.Shipped()
}

// importFrom parses the run's record with the profile that claims it, registers the outputs the record names next to it, links
// them as a Run under their generic roles, pairs the reliability map and proposes the axial interpretation of the orientation map
// as the adapter's (never the user's, never overriding theirs).
func (s *Server) importFrom(ctx context.Context, src runSource) (runs.Run, int, string) {
	prof := s.profiles().ByRecordFile(src.RecordFile(), runs.ReaderRunMetadata)
	if prof == nil {
		return runs.Run{}, 422, src.RecordFile() + " is not a run record any tool profile reads"
	}
	data, name, code, msg := src.Meta(ctx)
	if code != 0 {
		return runs.Run{}, code, msg
	}
	parsed, err := runs.ParseRecord(prof, data, name)
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	if dec := s.profiles().DecompositionOf(prof); dec != nil && dec.Source.Kind == runs.SourceFitsMaps {
		return s.importMapsRun(ctx, src, prof, dec, parsed, data) // the tool writes its components as maps, not outputs the record names
	}
	var outs []runs.Output
	byRole := map[string]catalog.Dataset{}
	for _, ref := range prof.Products() {
		file, ok := parsed.OutputFiles[ref.Product]
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
		byRole[ref.Role] = d
		outs = append(outs, runs.Output{Role: ref.Role, Product: ref.Product, DatasetID: d.ID, Name: d.Name})
	}
	if len(outs) == 0 {
		return runs.Run{}, 422, "none of the run's output files were found next to its " + src.RecordFile()
	}
	run, err := s.Runs.Save(wsCtx(ctx), actorID(ctx), src.Key(), parsed, outs)
	if err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	if psi, ok := byRole[runs.RoleOrientation]; ok {
		cur, _ := s.Store.Get(wsCtx(ctx), psi.ID)
		if adapterMayDeclare(cur.Interpretation) {
			var rel *int64
			if rd, ok := byRole[runs.RoleReliability]; ok && rd.HostID == psi.HostID {
				if why, err := compat.Grids(effectiveHeader(psi), effectiveHeader(rd)); err == nil && len(why) == 0 {
					rel = &rd.ID
				}
			}
			if in, ok := proposedPsi(cur, rel); ok {
				if err := s.Store.SetInterpretation(cur, in); err != nil {
					log.Printf("runs: %v", err)
				}
			}
		}
	}
	return run, 0, ""
}

// importsFromRecord says whether a scanned run is one whose profile has the record itself name the outputs, so that a project
// scan imports it from the record (a row stored before profiles existed has only its record kind).
func (s *Server) importsFromRecord(r *discover.Run) bool {
	p := s.profiles().ByLabel(r.Profile)
	if p == nil && r.Profile == "" {
		p = s.profiles().ByKind(r.RecordKind)
	}
	return p != nil && p.NamesOutputs()
}

// recordPath is the run record an import path names: the record itself (a name some profile reads), or the folder holding one.
// A folder with none of them gets the first profile's name, so the refusal says which file was looked for.
func (s *Server) recordPath(ws workspace.ID, p string) string {
	files := s.profiles().RecordFiles(runs.ReaderRunMetadata)
	for _, f := range files {
		if filepath.Base(p) == f {
			return p
		}
	}
	if dir, err := s.roots(ws).ResolveDir(p); err == nil { // only a folder inside a source root is looked into
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return filepath.Join(p, f)
			}
		}
	}
	if len(files) > 0 {
		return filepath.Join(p, files[0])
	}
	return p
}

func (s *Server) importRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
		// HostID imports a ROHSA fit that lives on that host (the owner runs ROHSA on the cluster); 0 is this machine.
		HostID int64 `json:"hostId"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if in.HostID != 0 {
		s.importROHSAFromHost(w, r, in.HostID, in.Path)
		return
	}
	if dat, found, code, msg := s.rohsaTarget(wsOf(r), in.Path); found {
		if code != 0 {
			fail(w, code, msg)
			return
		}
		run, code, msg := s.importROHSA(r.Context(), localROHSA{s: s, dat: dat})
		if code != 0 {
			fail(w, code, msg)
			return
		}
		writeJSON(w, 201, s.runView(wsOf(r), run))
		return
	}
	meta := s.recordPath(wsOf(r), in.Path)
	realMeta, err := s.roots(wsOf(r)).Resolve(meta) // same containment rules as any file: inside a source root, regular file
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	run, code, msg := s.importFrom(r.Context(), localRun{s: s, dir: filepath.Dir(realMeta), meta: realMeta})
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, s.runView(wsOf(r), run))
}

// runJSON adds the inferred link from a run to a registered copy of its input cube (name and size only).
type runJSON struct {
	runs.Run
	InputHint *struct {
		DatasetID int64  `json:"datasetId"`
		Basis     string `json:"basis"`
	} `json:"inputHint"`
	Reproducibility []runs.Fact `json:"reproducibility"`
	// Rohsa is the decomposition record of a ROHSA run (ADR 0046); null for every other kind.
	Rohsa *runs.RohsaInfo `json:"rohsa"`
	// JobID is the Vesqira job whose outputs this run is; ParentRunID is the run that job was drafted from.
	JobID       *int64 `json:"jobId"`
	ParentRunID *int64 `json:"parentRunId"`
	// RerunMode is how the draft repeated its parent (strict, best_effort, adapted); ParentComparison lines the facts both
	// runs observed up against each other (ADR 0054). Both are null for a run that repeats nothing, and the list leaves
	// ParentComparison out.
	RerunMode        *string           `json:"rerunMode"`
	ParentComparison *parentComparison `json:"parentComparison"`
	// ConfigHash identifies the configuration the run's job was rendered from, apart from its input and output paths
	// (jobs.ConfigHash); null when the run's job kept no settings. Runs that share it ran the same configuration. InputRerun is
	// set for a run made with "run this configuration on another cube": the source run, and the input as the catalog knew it.
	ConfigHash *string         `json:"configHash"`
	InputRerun *inputRerunView `json:"inputRerun"`
}

// inputRerunView is the record of a run made by running another run's configuration on a different cube.
type inputRerunView struct {
	SourceRunID       int64                   `json:"sourceRunId"`
	SourceConfigHash  string                  `json:"sourceConfigHash"`
	ConfigHash        string                  `json:"configHash"`
	SameConfiguration bool                    `json:"sameConfiguration"`
	PreviousInput     string                  `json:"previousInput"`
	Input             jobs.RerunInputIdentity `json:"input"`
	Notes             []string                `json:"notes"`
}

func inputRerunViewOf(l jobs.Lineage) *inputRerunView {
	rec := l.InputRerun
	if rec == nil {
		return nil
	}
	notes := rec.Notes
	if notes == nil {
		notes = []string{}
	}
	return &inputRerunView{SourceRunID: rec.SourceRunID, SourceConfigHash: rec.SourceConfigHash, ConfigHash: rec.ConfigHash,
		SameConfiguration: l.ConfigHash != "" && l.ConfigHash == rec.SourceConfigHash, PreviousInput: rec.PreviousInput, Input: rec.Input, Notes: notes}
}

// lineageOf is the lineage the runs list reads for every run, for one job.
func lineageOf(j jobs.Job) jobs.Lineage {
	l := jobs.Lineage{JobID: j.ID, ParentRunID: j.ParentRunID}
	if req, ok := j.ConfigRequest(); ok {
		l.ConfigHash, _ = jobs.ConfigHash(j.Template, req)
		l.InputRerun = req.InputRerun
	}
	return l
}

// hintSource is what the runs list reads the registered files' names and sizes through (the catalog; a test counts calls).
type hintSource interface {
	HintIndex(ws workspace.ID) (map[catalog.HintKey]int64, error)
}

// hintsFor reads the name and size index once, and only when some run has an input to look for.
func (s *Server) hintsFor(ws workspace.ID, rs []runs.Run) map[catalog.HintKey]int64 {
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
	idx, err := src.HintIndex(ws)
	if err != nil {
		log.Printf("runs: input hints: %v", err) // the hint is optional: the runs are still listed
		return nil
	}
	return idx
}

// runView describes one run. Lists use runViewWith and read the job links once for all runs.
func (s *Server) runView(ws workspace.ID, r runs.Run) runJSON {
	var lin map[int64]jobs.Lineage
	mode := ""
	if s.localJobs() != nil {
		if j, err := s.localJobs().ForRun(r.ID); err == nil {
			lin = map[int64]jobs.Lineage{r.ID: lineageOf(j)}
			mode = j.RerunMode()
		}
	}
	out := s.runViewWith(ws, r, lin, s.hintsFor(ws, []runs.Run{r}))
	if mode != "" {
		out.RerunMode = &mode
	}
	out.ParentComparison = s.compareWithParent(ws, r, out.ParentRunID, mode)
	return out
}

func (s *Server) runViewWith(ws workspace.ID, r runs.Run, lin map[int64]jobs.Lineage, hints map[catalog.HintKey]int64) runJSON {
	out := runJSON{Run: r}
	if l, ok := lin[r.ID]; ok {
		id := l.JobID
		out.JobID, out.ParentRunID = &id, l.ParentRunID
		if l.ConfigHash != "" {
			h := l.ConfigHash
			out.ConfigHash = &h
		}
		out.InputRerun = inputRerunViewOf(l)
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
	if p := s.profiles().ByLabel(r.Profile); r.Kind == "rohsa" || p != nil && s.profiles().DecompositionOf(p) != nil {
		if rr, err := s.Runs.ROHSA(ws, r.ID); err == nil {
			out.Rohsa = &rr.Info
		}
	}
	return out
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Runs.List(wsOf(r))
	if err != nil {
		log.Printf("runs: %v", err)
		fail(w, 500, "internal error")
		return
	}
	var lin map[int64]jobs.Lineage
	if s.localJobs() != nil {
		if lin, err = s.localJobs().Lineages(); err != nil {
			log.Printf("runs: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	hints := s.hintsFor(wsOf(r), rs) // one read of the catalog for every run, not one per run
	out := make([]runJSON, 0, len(rs))
	mode := archivedMode(r)
	for _, x := range rs {
		if !keepArchived(mode, x.Archived) {
			continue
		}
		out = append(out, s.runViewWith(wsOf(r), x, lin, hints))
	}
	writeJSON(w, 200, map[string]any{"runs": out})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	run, err := s.Runs.Get(wsOf(r), id)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "run not found")
		return
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, s.runView(wsOf(r), run))
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
	ParentRunID int64    `json:"parentRunId"`
	Source      string   `json:"source"` // job | imported
	HostID      *int64   `json:"hostId"`
	Template    string   `json:"template"`
	Name        string   `json:"name"`
	VGT         rerunVGT `json:"vgt"`
	// ROHSA is the same record for a ROHSA run (null for a VGT run), and Settings its editable settings as they were, in the
	// text a person types them: read from the config the job started from with the job's own changes over it, or, for an
	// imported run, those the run's own trace declares. A setting the records do not give is absent, never guessed.
	ROHSA    *rerunVGT         `json:"rohsa"`
	Settings map[string]string `json:"settings"`
	Params   []runs.Param      `json:"params"`
	Notes    []string          `json:"notes"`
	// ConfigChanged compares the stored config text with the file on the host now; null when not asked or not reachable.
	ConfigChanged *bool `json:"configChanged"`
	// LastJob is the job that produced the run and what the scheduler recorded it used, for sizing the next request
	// (ADR 0045). Null for an imported run.
	LastJob *rerunJob `json:"lastJob"`
}

type rerunJob struct {
	ID    int64       `json:"id"`
	Name  string      `json:"name"`
	Usage *jobs.Usage `json:"usage"`
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
	run, err := s.Runs.Get(wsOf(r), id)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "run not found")
		return
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	if run.Kind != "vgt" && run.Kind != "rohsa" {
		fail(w, 422, "running again is offered for VGT and ROHSA runs only; this run's kind is "+run.Kind)
		return
	}
	rohsa := run.Kind == "rohsa"
	out := rerunJSON{ParentRunID: id, Source: "imported", Template: run.Kind, Name: againName(run.Name), Params: run.Params, Notes: []string{}, VGT: rerunVGT{Set: map[string]string{}}, Settings: map[string]string{}}
	if rohsa {
		out.ROHSA = &rerunVGT{Set: map[string]string{}}
		out.Settings = declaredROHSASettings(run.Params)
	}
	if out.Params == nil {
		out.Params = []runs.Param{}
	}
	var job jobs.Job
	err = jobs.ErrNotFound // without a job store no job made the run
	if s.localJobs() != nil {
		if job, err = s.localJobs().ForRun(id); err != nil && !errors.Is(err, jobs.ErrNotFound) {
			log.Printf("runs: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	if err != nil { // no job of ours produced this run
		out.Notes = append(out.Notes, "This run was imported, so the host, code folder and config it ran with are not known here.")
		if rohsa {
			out.Notes = append(out.Notes, "The settings below are those the run's own trace declares for the pipeline's config. Choose the host and a config file there; the file supplies everything the trace does not say (the input cube, the crop, the spectral kernel), and these settings are applied over it.")
		}
		writeJSON(w, 200, out)
		return
	}
	hid := job.HostID
	out.HostID = &hid
	out.LastJob = &rerunJob{ID: job.ID, Name: job.Name, Usage: job.Usage}
	var req jobs.VGTRequest
	var ok bool
	if rohsa {
		req, ok = job.ROHSARequest()
	} else {
		req, ok = job.VGTRequest()
	}
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
	rr := rerunVGT{RepoDir: strp(req.RepoDir), ConfigPath: strp(req.ConfigPath), BaseConfigText: strp(req.BaseConfigText), Set: set,
		CPUs: &cpus, Mem: strp(req.Mem), Time: strp(req.Time), Partition: strp(req.Partition), Account: strp(req.Account)}
	if rohsa {
		out.ROHSA = &rr
		out.Settings = effectiveROHSASettings(req)
	} else {
		out.VGT = rr
	}
	if req.ConfigPath != "" && s.HostDB != nil && s.Remote != nil {
		if h, err := s.HostDB.Get(job.HostID); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			text, code, _ := s.remoteText(ctx, h, req.ConfigPath)
			cancel()
			if code == 0 {
				if req.InputRerun != nil { // the stored text names the new cube; compare the file with its input line the same
					if t, err := jobs.SetInputFile(job.Template, text, jobs.InputPathOf(job.Template, req.BaseConfigText)); err == nil {
						text = t
					}
				}
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

var fortranReal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?d[-+]?[0-9]+$`)

// declaredROHSASettings are the editable settings a ROHSA run's trace declares for the pipeline's config, in the form the
// whitelist takes. The trace's [config] comes first, then its top-level n_gauss, and the namelist (source "parameters") only
// fills what neither says: all three are written from the same options by the pipeline (rohsa/configs.jl: the namelist and the
// trace config both read opts), so they agree, the namelist only spells reals as Fortran does (10.0d0) and booleans as .true..
// A value that does not pass the whitelist's own check is left out rather than repaired.
func declaredROHSASettings(ps []runs.Param) map[string]string {
	out := map[string]string{}
	rank := map[string]int{}
	for _, p := range ps {
		r := map[string]int{"config": 3, "run_trace": 2, "parameters": 1}[p.Source]
		if r == 0 || (p.Source == "run_trace" && p.Key != "n_gauss") || r <= rank[p.Key] {
			continue
		}
		v := strings.TrimSpace(p.Value)
		switch strings.ToLower(v) {
		case ".true.":
			v = "true"
		case ".false.":
			v = "false"
		}
		if fortranReal.MatchString(v) { // 10.0d0, 1.5d-3: a Fortran real as the namelist writes it
			v = strings.TrimSuffix(v, "d0")
			v = strings.Replace(v, "d", "e", 1)
		}
		if jobs.CheckROHSASetting(p.Key, v) != nil || p.Key == jobs.ROHSATied {
			continue
		}
		out[p.Key], rank[p.Key] = v, r
	}
	return out
}

// effectiveROHSASettings are a ROHSA job's settings as it ran: the config text it started from with its own changes over it.
func effectiveROHSASettings(req jobs.VGTRequest) map[string]string {
	out := map[string]string{}
	if cfg, err := jobs.ReadROHSAConfig(req.BaseConfigText); err == nil {
		for k, v := range cfg.Values {
			out[k] = v
		}
	}
	for k, v := range jobs.ROHSAExpand(req.Set) {
		out[k] = v
	}
	return out
}

// rohsaSizing answers "what does the owner's sizing table say for this crop and Gaussian count": a pure function of two
// numbers, so the form can ask again as they change. A number that is not given is read the way the script reads it, and
// the answer says so.
func (s *Server) rohsaSizing(w http.ResponseWriter, r *http.Request) {
	num := func(name string) (int, bool, bool) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return 0, false, true
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1000000 {
			fail(w, 400, name+" must be a whole number")
			return 0, false, false
		}
		return n, true, true
	}
	crop, haveCrop, ok := num("cropSize")
	if !ok {
		return
	}
	n, haveN, ok := num("nGauss")
	if !ok {
		return
	}
	writeJSON(w, 200, jobs.SuggestROHSASizing(crop, n, haveCrop, haveN))
}

// proposedPsi is the orientation declaration a run import writes for the psi map, and false when there is none to write. It is the
// file header's, read by an orientation profile (ADR 0068, slice 2) and labelled as declared by that profile ("profile:VGT.jl@1"):
// the frame (PIXEL, or a position angle east of north in GALACTIC or ICRS: the frame of the angle, not necessarily the grid's, since
// every consumer carries it through the sky), what the angle is and its unit, each as the profile places it. A map whose header the
// profile cannot place (no PSIFRAME, an older output, a header with no VGT.jl card) gets no declaration: Vesqira does not assume
// the pipeline's convention for it, and the map stays unresolved until the researcher declares it. The absence of PSIFRAME is never
// read as "pixel" from the WCS or from anything else in the file.
func proposedPsi(d catalog.Dataset, rel *int64) (catalog.Interpretation, bool) {
	if d.VGT == nil || d.VGT.Orientation == nil {
		return catalog.Interpretation{}, false
	}
	o := d.VGT.Orientation
	return catalog.Interpretation{Kind: "axial_orientation", Unit: o.Unit, ReliabilityID: rel, Frame: &o.Frame, Quantity: o.Quantity, DeclaredBy: o.Profile}, true
}

// adapterMayDeclare says whether a run import may write its proposed orientation over what is there: over nothing, over an earlier
// proposal of an adapter or an analysis. Never over the researcher's own declaration, nor over a rule's, which is their standing
// statement about a whole folder (ADR 0058).
func adapterMayDeclare(cur *catalog.Interpretation) bool {
	if cur == nil {
		return true
	}
	_, byRule := discover.RuleName(cur.DeclaredBy)
	return cur.DeclaredBy != "user" && !byRule
}
