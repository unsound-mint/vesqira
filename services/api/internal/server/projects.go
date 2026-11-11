package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// ---- readable roots: the configured ones plus the project folders that were opened ----

// roots is every folder the API may read: the roots it was started with and the folders of opened local projects.
// Opening a project adds its canonical folder; the projects are loaded again from the catalog at start.
func (s *Server) roots() sourceroot.Roots {
	s.rootsOnce.Do(func() {
		if s.Projects == nil {
			return
		}
		saved, err := s.Projects.Roots(0)
		if err != nil {
			log.Printf("projects: %v", err)
			return
		}
		for _, r := range saved {
			if real, err := filepath.EvalSymlinks(r); err == nil {
				if fi, err := os.Stat(real); err == nil && fi.IsDir() {
					s.projRoots = append(s.projRoots, real)
				}
			}
		}
	})
	s.rootsMu.RLock()
	defer s.rootsMu.RUnlock()
	if len(s.projRoots) == 0 {
		return s.Roots
	}
	out := make(sourceroot.Roots, 0, len(s.Roots)+len(s.projRoots))
	return append(append(out, s.Roots...), s.projRoots...)
}

func (s *Server) addProjectRoot(real string) {
	s.roots() // make sure the saved ones are loaded first
	s.rootsMu.Lock()
	defer s.rootsMu.Unlock()
	for _, r := range s.projRoots {
		if r == real {
			return
		}
	}
	s.projRoots = append(s.projRoots, real)
}

// ---- opening ----

const broadMessage = "pick a project folder, not the whole disk/home"

// refuseBroad says why a folder must not become a project (the whole disk, the home folder or a folder that holds it,
// credential folders), or "" when it may. Opening a folder makes everything in it readable, so the folder is the
// authorization and a broad choice is refused rather than honoured.
// credentialDirs hold keys, tokens or browser profiles; a project folder inside one would make them readable.
var credentialDirs = map[string]bool{".ssh": true, ".gnupg": true, ".config": true, ".local": true, ".aws": true, ".kube": true,
	".docker": true, ".password-store": true, ".mozilla": true, ".pki": true, ".netrc": true, ".git-credentials": true}

func refuseBroad(real string, home string) string {
	if real == "/" || real == "." || real == "" {
		return broadMessage
	}
	if home != "" {
		if real == home || strings.HasPrefix(home, strings.TrimRight(real, "/")+"/") {
			return broadMessage
		}
	}
	for _, part := range strings.Split(real, "/") {
		if credentialDirs[part] {
			return "that folder holds credentials; pick a project folder"
		}
	}
	return ""
}

func (s *Server) projectFrom(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return projects.Project{}, false
	}
	p, err := s.Projects.Get(id)
	if errors.Is(err, projects.ErrNotFound) {
		fail(w, 404, "project not found")
		return p, false
	}
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return p, false
	}
	return p, true
}

func (s *Server) openProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HostID int64  `json:"hostId"`
		Path   string `json:"path"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if in.Path == "" || len(in.Path) > 4096 || strings.ContainsRune(in.Path, 0) || !path.IsAbs(filepath.ToSlash(in.Path)) {
		fail(w, 422, "give the absolute path of a folder")
		return
	}
	var p projects.Project
	if in.HostID == 0 {
		real, err := filepath.EvalSymlinks(filepath.Clean(in.Path))
		if err != nil {
			fail(w, 422, "that folder cannot be opened")
			return
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			fail(w, 422, "that is not a folder")
			return
		}
		home, _ := os.UserHomeDir()
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if msg := refuseBroad(real, home); msg != "" {
			fail(w, 422, msg)
			return
		}
		if p, err = s.Projects.Touch(0, real, filepath.Base(real)); err != nil {
			log.Printf("projects: %v", err)
			fail(w, 500, "internal error")
			return
		}
		s.addProjectRoot(real)
	} else {
		var code int
		var msg string
		if p, code, msg = s.openRemote(r.Context(), in.HostID, in.Path); code != 0 {
			fail(w, code, msg)
			return
		}
	}
	s.Audit.Record("project opened", "project", p.ID, p.Name)
	s.startScan(p) // rescan on open; a scan already running is left to finish
	s.writeSummary(w, 200, p)
}

// openRemote adds a folder on a host as one of the host's roots (its worker is restarted with it, because the worker
// enforces the roots it was started with), checks that the worker can walk it, and records the project.
func (s *Server) openRemote(ctx context.Context, hostID int64, in string) (projects.Project, int, string) {
	if s.HostDB == nil || s.Remote == nil {
		return projects.Project{}, 503, "remote hosts are not configured"
	}
	h, err := s.HostDB.Get(hostID)
	if err != nil {
		return projects.Project{}, 404, "host not found"
	}
	if !path.IsAbs(in) || path.Clean(in) != strings.TrimRight(in, "/") && in != "/" {
		return projects.Project{}, 422, "give a clean absolute path on the host"
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	real, isDir, home, err := s.remoteCanon(ctx, h, path.Clean(in))
	if err != nil {
		code, msg := hostErrStatus(err)
		return projects.Project{}, code, msg
	}
	if !isDir {
		return projects.Project{}, 422, "that is not a folder on the host"
	}
	if msg := refuseBroad(real, home); msg != "" || strings.Count(strings.Trim(real, "/"), "/") < 1 {
		return projects.Project{}, 422, broadMessage
	}
	added := false
	if !h.InRoots(real) {
		if _, err := s.HostDB.AddRoot(h.ID, real); err != nil {
			if errors.Is(err, hosts.ErrInvalid) {
				return projects.Project{}, 422, err.Error()
			}
			log.Printf("projects: %v", err)
			return projects.Project{}, 500, "internal error"
		}
		s.Remote.Forget(h.ID)
		added = true
		h, _ = s.HostDB.Get(h.ID)
	}
	// the worker must now be able to walk the folder; if not, the root is taken away again
	if _, err := s.Remote.Do(ctx, h, map[string]any{"op": "walk", "path": real, "limit": 1, "skip_names": []string{}, "never_files": []string{}, "never_dirs": []string{}, "skip_globs": []string{}, "leaf_dir_suffixes": []string{}}); err != nil {
		if added {
			_ = s.HostDB.RemoveRoot(h.ID, real)
			s.Remote.Forget(h.ID)
		}
		code, msg := hostErrStatus(err)
		return projects.Project{}, code, msg
	}
	p, err := s.Projects.Touch(h.ID, real, path.Base(real))
	if err != nil {
		log.Printf("projects: %v", err)
		return projects.Project{}, 500, "internal error"
	}
	return p, 0, ""
}

// ---- reading ----

type summaryOut struct {
	ID         int64      `json:"id"`
	HostID     int64      `json:"hostId"`
	Path       string     `json:"path"`
	Name       string     `json:"name"`
	OpenedAt   time.Time  `json:"openedAt"`
	LastScanAt *time.Time `json:"lastScanAt"`
	Scan       scanView   `json:"scan"`
	Scanned    bool       `json:"scanned"`
	storedSummary
	Proposed   *string      `json:"proposedProjectFile"`
	Health     []healthItem `json:"health"`
	NeedsCount int          `json:"needsCount"`
}

func (s *Server) summaryOf(p projects.Project) (summaryOut, error) {
	out := summaryOut{ID: p.ID, HostID: p.HostID, Path: p.Path, Name: p.Name, OpenedAt: p.OpenedAt, LastScanAt: p.LastScanAt, Scan: s.scanOf(p.ID).snapshot(), Scanned: p.LastScanAt != nil}
	out.storedSummary = s.storedSummaryOf(p)
	if out.Counts.ByFormat == nil {
		out.Counts.ByFormat = map[string]int{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.Links.Examples == nil {
		out.Links.Examples = []discover.Link{}
	}
	if out.Registration.Errors == nil {
		out.Registration.Errors = []string{}
	}
	if out.ResultLike == nil {
		out.ResultLike = []string{}
	}
	if out.ProjectFile.HostRepos == nil {
		out.ProjectFile.HostRepos = map[string]string{}
	}
	if !out.ProjectFile.Exists && out.storedSummary.Proposed != "" {
		pr := out.storedSummary.Proposed
		out.Proposed = &pr
	}
	ns, err := s.computeNeeds(p)
	if err != nil {
		return out, err
	}
	out.Health, out.NeedsCount = health(p, out.storedSummary, ns), openCount(ns)
	if out.Health == nil {
		out.Health = []healthItem{}
	}
	return out, nil
}

func (s *Server) writeSummary(w http.ResponseWriter, code int, p projects.Project) {
	out, err := s.summaryOf(p)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, code, out)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Projects.List(50)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	type item struct {
		ID         int64      `json:"id"`
		HostID     int64      `json:"hostId"`
		Path       string     `json:"path"`
		Name       string     `json:"name"`
		OpenedAt   time.Time  `json:"openedAt"`
		LastScanAt *time.Time `json:"lastScanAt"`
		Runs       int        `json:"runs"`
		Data       int        `json:"data"`
		State      string     `json:"scanState"`
	}
	out := make([]item, 0, len(ps))
	for _, p := range ps {
		sum := s.storedSummaryOf(p)
		out = append(out, item{p.ID, p.HostID, p.Path, p.Name, p.OpenedAt, p.LastScanAt, sum.Counts.Runs, sum.Counts.Data, s.scanOf(p.ID).snapshot().State})
	}
	writeJSON(w, 200, map[string]any{"projects": out})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.projectFrom(w, r); ok {
		s.writeSummary(w, 200, p)
	}
}

func (s *Server) scanProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	st, started := s.startScan(p)
	writeJSON(w, 202, map[string]any{"started": started, "scan": st.snapshot()})
}

func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	st := s.scanOf(p.ID)
	st.mu.Lock()
	active, cancel := st.active(), st.cancel
	st.mu.Unlock()
	if active && cancel != nil {
		cancel()
	}
	writeJSON(w, 200, map[string]any{"canceled": active})
}

type treeFile struct {
	Size      int64  `json:"size"`
	MtimeNS   int64  `json:"mtimeNs"`
	Format    string `json:"format"`
	Viewer    bool   `json:"viewer"`
	Role      string `json:"role"`
	ReadOnly  bool   `json:"readOnly"`
	DatasetID *int64 `json:"datasetId"`
}

type treeRun struct {
	Record      string             `json:"record"`
	RecordKind  string             `json:"recordKind"`
	Fields      []discover.Field   `json:"fields"`
	Settings    []discover.Setting `json:"settings"`
	Differs     []discover.Setting `json:"differs"`
	Notes       []string           `json:"notes"`
	OutputCount int                `json:"outputCount"`
	OutputBytes int64              `json:"outputBytes"`
	RunID       *int64             `json:"runId"`
}

type treeEntry struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"` // relative to the project folder
	Kind  string    `json:"kind"` // dir | run | output | data | config | job
	Count *int      `json:"count"`
	File  *treeFile `json:"file"`
	Run   *treeRun  `json:"run"`
}

func (s *Server) projectTree(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	group := q.Get("group")
	if group != "runs" && group != "data" && group != "configs" && group != "jobs" {
		fail(w, 400, "group must be runs, data, configs or jobs")
		return
	}
	parent := strings.Trim(q.Get("path"), "/")
	if parent != "" {
		for _, part := range strings.Split(parent, "/") {
			if part == "" || part == ".." || strings.ContainsRune(part, 0) {
				fail(w, 400, "bad path")
				return
			}
		}
	}
	limit, offset := 200, 0
	if v := q.Get("limit"); v != "" {
		if n, ok := atoiIn(v, 1, 1000); ok {
			limit = n
		} else {
			fail(w, 400, "limit must be between 1 and 1000")
			return
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, ok := atoiIn(v, 0, 10_000_000); ok {
			offset = n
		} else {
			fail(w, 400, "bad offset")
			return
		}
	}
	rows, total, err := s.Projects.Children(p.ID, group, parent, limit, offset)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	facts, _ := s.Store.FactsByPath(p.HostID)
	out := make([]treeEntry, 0, len(rows))
	for _, row := range rows {
		e := treeEntry{Name: row.Name, Path: row.Path, Kind: row.Kind}
		switch row.Kind {
		case "dir":
			var d struct {
				Count int `json:"count"`
			}
			_ = json.Unmarshal(row.Data, &d)
			e.Count = &d.Count
		case "run":
			var run discover.Run
			_ = json.Unmarshal(row.Data, &run)
			tr := &treeRun{Record: run.Record, RecordKind: run.RecordKind, Fields: run.Fields, Settings: run.Settings, Differs: run.Differs, Notes: run.Notes, OutputCount: run.OutputCount, OutputBytes: run.OutputBytes}
			if run.RecordKind == "vgt" && s.Runs != nil {
				key := absFor(p, row.Path)
				if p.HostID != 0 {
					key = hostKey(p.HostID, key)
				}
				if id, _ := s.Runs.IDByDir(key); id != 0 {
					tr.RunID = &id
				}
			}
			e.Run = tr
		default:
			var f discover.File
			_ = json.Unmarshal(row.Data, &f)
			tf := &treeFile{Size: f.Size, MtimeNS: f.MtimeNS, Format: f.Format, Viewer: f.Viewer, Role: f.Role, ReadOnly: f.ReadOnly}
			if f.Format == "FITS" || f.Format == "Catalog" {
				if fa, ok := facts[absFor(p, row.Path)]; ok && fa.Size == f.Size && fa.MtimeNS == f.MtimeNS {
					id := fa.ID
					tf.DatasetID = &id
				}
			}
			e.File = tf
		}
		out = append(out, e)
	}
	var next *int
	if offset+len(rows) < total {
		n := offset + len(rows)
		next = &n
	}
	writeJSON(w, 200, map[string]any{"group": group, "path": parent, "entries": out, "total": total, "offset": offset, "limit": limit, "nextOffset": next})
}

func hostKey(hostID int64, p string) string { return fmt.Sprintf("h%d:%s", hostID, p) }

func atoiIn(v string, lo, hi int) (int, bool) {
	n := 0
	if v == "" || len(v) > 9 {
		return 0, false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n >= lo && n <= hi
}

func (s *Server) projectNeeds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	ns, err := s.computeNeeds(p)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"needs": ns, "open": openCount(ns)})
}

func (s *Server) answerProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in answerIn
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	ns, err := s.computeNeeds(p)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	var n *need
	for i := range ns {
		if ns[i].ID == in.Need {
			n = &ns[i]
		}
	}
	if n == nil {
		fail(w, 404, "no such question in this project")
		return
	}
	if err := n.validate(in); err != nil {
		fail(w, 422, strings.TrimPrefix(err.Error(), "answer: "))
		return
	}
	written, note, err := s.recordAnswer(r.Context(), p, *n, in) // the file first: a refused write changes nothing
	if err != nil {
		if errors.Is(err, errProjectFile) || strings.Contains(err.Error(), "project.toml") {
			fail(w, 409, err.Error())
			return
		}
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if err := s.applyAnswer(*n, in); err != nil {
		if errors.Is(err, errAnswer) {
			fail(w, 422, strings.TrimPrefix(err.Error(), "answer: "))
			return
		}
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("project question answered", "project", p.ID, n.Kind)
	if n.Kind == "result_folder" && in.Answer == "mark_run" {
		s.startScan(p) // the folder is a run now: the next scan lists it as one
	}
	ns, _ = s.computeNeeds(p)
	var updated *need
	for i := range ns {
		if ns[i].ID == in.Need {
			updated = &ns[i]
		}
	}
	writeJSON(w, 200, map[string]any{"need": updated, "writtenToProject": written, "note": note, "open": openCount(ns)})
}

func (s *Server) writeProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Write bool   `json:"write"`
		Text  string `json:"text"`
	}
	if !body(w, r, 300<<10, &in, true) {
		return
	}
	if !in.Write {
		fail(w, 400, "the project file is written only when you confirm: send write: true")
		return
	}
	if p.HostID != 0 {
		s.writeProjectOnHost(w, r, p, in.Text)
		return
	}
	_, file := projectFilePaths(p.Path)
	_, statErr := os.Lstat(file)
	exists := statErr == nil
	text := in.Text
	if text == "" {
		if exists {
			fail(w, 409, "project.toml already exists; send its new text to replace it")
			return
		}
		text = s.storedSummaryOf(p).Proposed
		if text == "" {
			fail(w, 409, "scan the project first: there is no proposed file yet")
			return
		}
	}
	if _, err := discover.ParseProjectFile([]byte(text)); err != nil {
		fail(w, 422, err.Error())
		return
	}
	if err := writeProjectFile(p.Path, []byte(strings.TrimRight(text, "\n")+"\n")); err != nil {
		if errors.Is(err, errProjectFile) {
			fail(w, 409, err.Error())
			return
		}
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("project file written", "project", p.ID, p.Name)
	s.startScan(p) // read it back: the file is the layout from now on
	writeJSON(w, 200, map[string]any{"written": true, "path": discover.ProjectFilePath, "replaced": exists})
}
