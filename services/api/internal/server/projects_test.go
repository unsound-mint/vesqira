package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type projEnv struct {
	ts   *httptest.Server
	srv  *Server
	cat  *catalog.Store
	base string
	home string
}

func newProjEnv(t *testing.T) *projEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	configured := filepath.Join(base, "configured")
	for _, d := range []string{home, configured} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	rs, err := sourceroot.New([]string{configured})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	ps, err := projects.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	srv := &Server{Roots: rs, Store: cat, Worker: &fakeWorker{}, Projects: ps, Runs: rn, Audit: au}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &projEnv{ts: ts, srv: srv, cat: cat, base: base, home: home}
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *projEnv) call(t *testing.T, method, p string, body any, hdr ...string) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+p, rd)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func (e *projEnv) json(t *testing.T, method, p string, body any, want int, into any) {
	t.Helper()
	code, b := e.call(t, method, p, body)
	if code != want {
		t.Fatalf("%s %s: %d %s", method, p, code, b)
	}
	if into != nil {
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s: %v in %s", p, err, b)
		}
	}
}

type sumJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Scan struct {
		State      string `json:"state"`
		Error      string `json:"error"`
		Registered int    `json:"registered"`
		ToRegister int    `json:"toRegister"`
	} `json:"scan"`
	Counts struct {
		Runs       int            `json:"runs"`
		ManualRuns int            `json:"manualRuns"`
		Data       int            `json:"data"`
		Configs    int            `json:"configs"`
		Jobs       int            `json:"jobs"`
		ByFormat   map[string]int `json:"byFormat"`
		NoViewer   int            `json:"noViewer"`
		Archives   struct {
			Count int   `json:"count"`
			Bytes int64 `json:"bytes"`
		} `json:"archives"`
		NeverRead int `json:"neverRead"`
	} `json:"counts"`
	Git struct {
		Present bool   `json:"present"`
		Commit  string `json:"commit"`
	} `json:"git"`
	Links struct {
		Outside  int `json:"outside"`
		Dangling int `json:"dangling"`
	} `json:"links"`
	Health []struct {
		ID, State, Detail, Why string
	} `json:"health"`
	NeedsCount  int     `json:"needsCount"`
	Proposed    *string `json:"proposedProjectFile"`
	ProjectFile struct {
		Exists bool `json:"exists"`
	} `json:"projectFile"`
	Changes *struct{ Added, Removed, Changed int } `json:"changes"`
}

func (e *projEnv) open(t *testing.T, dir string) sumJSON {
	t.Helper()
	var s sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"path": dir}, 200, &s)
	return e.settle(t, s.ID)
}

func (e *projEnv) settle(t *testing.T, id int64) sumJSON {
	t.Helper()
	var s sumJSON
	for i := 0; i < 400; i++ {
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d", id), nil, 200, &s)
		if s.Scan.State == "done" || s.Scan.State == "error" || s.Scan.State == "canceled" {
			if s.Scan.State != "done" {
				t.Fatalf("scan ended as %s (%s)", s.Scan.State, s.Scan.Error)
			}
			return s
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("scan did not finish: %+v", s.Scan)
	return s
}

func vgtProject(t *testing.T, root string) {
	put(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	put(t, root, ".git/refs/heads/main", "0123456789abcdef0123456789abcdef01234567\n")
	meta := func(lambda string) string {
		return "generated_at_utc = \"2026-08-01T10:00:00Z\"\ngitcommit = \"abc1234\"\n[config]\nlambda = " + lambda + "\n[input]\npath = \"/x/cube.fits\"\nsize_bytes = 100\n[outputs]\npsi_path = \"/x/psi.fits\"\n"
	}
	put(t, root, "validation_output/a/run_metadata.toml", meta("0.5"))
	put(t, root, "validation_output/a/psi.fits", "x")
	put(t, root, "validation_output/b/run_metadata.toml", meta("0.9"))
	put(t, root, "validation_output/b/psi.fits", "x")
	put(t, root, "data/exp_raw/c1.fits", "x")
	put(t, root, "data/exp_raw/c2.fits", "x")
	put(t, root, "data/sims/s.fits", "x")
	put(t, root, "configs/rohsa/base.toml", "a=1")
	put(t, root, "hpc/cluster/run.slurm", "#!/bin/bash")
	put(t, root, "old/stale.fits", "x")
	put(t, root, "node_modules/p/x.fits", "x")
}

func (e *projEnv) project(t *testing.T, name string) string {
	d := filepath.Join(e.base, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOpeningARefusesTheWholeDiskTheHomeAndCredentials(t *testing.T) {
	e := newProjEnv(t)
	sub := filepath.Join(e.home, "sub")
	os.MkdirAll(filepath.Join(sub, ".ssh"), 0o755)
	os.MkdirAll(filepath.Join(e.home, ".config", "gh"), 0o755)
	for name, p := range map[string]string{
		"disk": "/", "home": e.home, "ancestor of home": e.base, "ssh": filepath.Join(sub, ".ssh"),
		"inside .config": filepath.Join(e.home, ".config", "gh"),
	} {
		code, b := e.call(t, "POST", "/api/projects/open", map[string]any{"path": p})
		if code != 422 {
			t.Fatalf("%s: %d %s", name, code, b)
		}
		if name != "ssh" && name != "inside .config" && !strings.Contains(string(b), "pick a project folder, not the whole disk/home") {
			t.Fatalf("%s: %s", name, b)
		}
	}
	put(t, e.base, "afile.txt", "x")
	for name, p := range map[string]string{"file": filepath.Join(e.base, "afile.txt"), "missing": filepath.Join(e.base, "nope"), "relative": "x/y"} {
		if code, b := e.call(t, "POST", "/api/projects/open", map[string]any{"path": p}); code != 422 {
			t.Fatalf("%s: %d %s", name, code, b)
		}
	}
	if code, _ := e.call(t, "POST", "/api/projects/open", map[string]any{"path": filepath.Join(sub, "x"), "extra": 1}); code != 400 {
		t.Fatalf("unknown fields are refused: %d", code)
	}
	var list struct{ Projects []any }
	e.json(t, "GET", "/api/projects", nil, 200, &list)
	if len(list.Projects) != 0 {
		t.Fatalf("nothing was opened: %v", list.Projects)
	}
	// the refused folders were not made readable
	if code, _ := e.call(t, "POST", "/api/datasets", map[string]any{"path": filepath.Join(e.home, "afile")}); code == 201 {
		t.Fatal("home became readable")
	}
}

func TestOpenScanRegisterAndBrowseAVGTLikeProject(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "vgt")
	vgtProject(t, root)
	put(t, root, ".vesqira/project.toml", "[layout]\nskip = [\"old/**\"]\n")
	s := e.open(t, root)

	if s.Name != "vgt" || s.Path != root || !s.Git.Present || s.Git.Commit != "0123456" {
		t.Fatalf("%+v", s)
	}
	if s.Counts.Runs != 2 || s.Counts.Data != 3 || s.Counts.Configs != 1 || s.Counts.Jobs != 1 || s.Counts.ByFormat["FITS"] != 3 {
		t.Fatalf("%+v", s.Counts)
	}
	if !s.ProjectFile.Exists || s.Proposed != nil {
		t.Fatalf("a project file exists, so none is proposed: %+v", s.ProjectFile)
	}
	// the folder is readable now, through the same check as any root
	var ds struct{ Datasets []catalog.Dataset }
	e.json(t, "GET", "/api/datasets", nil, 200, &ds)
	got := map[string]bool{}
	for _, d := range ds.Datasets {
		got[d.Path] = true
	}
	for _, rel := range []string{"data/exp_raw/c1.fits", "data/sims/s.fits", "validation_output/a/psi.fits"} {
		if !got[filepath.Join(root, rel)] {
			t.Fatalf("%s was not registered: %v", rel, got)
		}
	}
	if got[filepath.Join(root, "old/stale.fits")] || got[filepath.Join(root, "node_modules/p/x.fits")] {
		t.Fatalf("a skipped folder was registered: %v", got)
	}
	var runList struct{ Runs []struct{ ID int64 } }
	e.json(t, "GET", "/api/runs", nil, 200, &runList)
	if len(runList.Runs) != 2 {
		t.Fatalf("the VGT runs were imported through the existing importer: %+v", runList)
	}

	// the tree is lazy and paged; a run carries the settings that differ from its siblings
	var top struct {
		Entries []struct{ Name, Path, Kind string }
		Total   int
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs", s.ID), nil, 200, &top)
	if top.Total != 1 || top.Entries[0].Kind != "dir" || top.Entries[0].Name != "validation_output" {
		t.Fatalf("%+v", top)
	}
	var page struct {
		Entries []struct {
			Name, Path, Kind string
			Run              *struct {
				Record, RecordKind string
				Differs            []struct{ Key, Value string }
				Fields             []struct{ Key, Value, Status, Source string }
				RunID              *int64
			}
		}
		Total      int
		NextOffset *int
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs&path=validation_output&limit=1", s.ID), nil, 200, &page)
	if page.Total != 2 || len(page.Entries) != 1 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("%+v", page)
	}
	a := page.Entries[0]
	if a.Path != "validation_output/a" || a.Run.Record != "run_metadata.toml" || a.Run.RecordKind != "vgt" || a.Run.RunID == nil || len(a.Run.Differs) != 1 || a.Run.Differs[0].Key != "lambda" {
		t.Fatalf("%+v", a)
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs&path=validation_output&limit=1&offset=1", s.ID), nil, 200, &page)
	if len(page.Entries) != 1 || page.Entries[0].Name != "b" || page.NextOffset != nil {
		t.Fatalf("%+v", page)
	}
	var data struct {
		Entries []struct {
			Name string
			File *struct {
				Format    string
				Viewer    bool
				DatasetID *int64
			}
		}
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=data&path=data/exp_raw", s.ID), nil, 200, &data)
	if len(data.Entries) != 2 || data.Entries[0].File.Format != "FITS" || !data.Entries[0].File.Viewer || data.Entries[0].File.DatasetID == nil {
		t.Fatalf("%+v", data)
	}
	for _, q := range []string{"group=nope", "group=runs&path=../x", "group=runs&limit=0", "group=runs&offset=-1"} {
		if code, _ := e.call(t, "GET", fmt.Sprintf("/api/projects/%d/tree?%s", s.ID, q), nil); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}

	// recent projects, and a rescan that finds nothing changed
	var list struct {
		Projects []struct {
			ID   int64
			Name string
			Runs int
		}
	}
	e.json(t, "GET", "/api/projects", nil, 200, &list)
	if len(list.Projects) != 1 || list.Projects[0].Runs != 2 {
		t.Fatalf("%+v", list)
	}
	var started struct{ Started bool }
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), map[string]any{}, 202, &started)
	again := e.settle(t, s.ID)
	if again.Changes == nil || again.Changes.Added+again.Changes.Removed+again.Changes.Changed != 0 {
		t.Fatalf("size and mtime are unchanged: %+v", again.Changes)
	}
	put(t, root, "data/sims/new.fits", "x")
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), nil, 202, &started)
	if again = e.settle(t, s.ID); again.Changes == nil || again.Changes.Added != 1 {
		t.Fatalf("%+v", again.Changes)
	}
}

func TestARunImportedByHandIsTheSameRunAfterOpening(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "vgt")
	vgtProject(t, root)
	// opening the folder is what makes it readable, so the hand import goes through after the first open
	first := e.open(t, root)
	var before struct{ Runs []struct{ ID int64 } }
	e.json(t, "GET", "/api/runs", nil, 200, &before)
	var imported struct{ ID int64 }
	e.json(t, "POST", "/api/runs/import", map[string]any{"path": filepath.Join(root, "validation_output/a")}, 201, &imported)
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan", first.ID), nil, 202, nil)
	e.settle(t, first.ID)
	var after struct{ Runs []struct{ ID int64 } }
	e.json(t, "GET", "/api/runs", nil, 200, &after)
	if len(after.Runs) != len(before.Runs) || len(after.Runs) != 2 {
		t.Fatalf("a run found again is not a second run: %d then %d", len(before.Runs), len(after.Runs))
	}
	var page struct {
		Entries []struct {
			Name string
			Run  *struct{ RunID *int64 }
		}
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs&path=validation_output", first.ID), nil, 200, &page)
	if page.Entries[0].Run.RunID == nil || *page.Entries[0].Run.RunID != imported.ID {
		t.Fatalf("%+v vs %d", page.Entries[0].Run, imported.ID)
	}
}

func TestUntidyProjectAsksInsteadOfGuessing(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "messy")
	put(t, root, "results_v3/README.md", "notes")
	put(t, root, "H1299 images/a.tif", "x")
	put(t, root, "backup.zip", strings.Repeat("z", 1<<20))
	put(t, root, "blind_set_key/key_DO_NOT_OPEN.csv", "secret")
	os.Symlink("/etc", filepath.Join(root, "out"))
	os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling"))
	s := e.open(t, root)
	if s.Counts.Runs != 0 || s.Counts.NoViewer != 1 || s.Counts.Archives.Count != 1 || s.Counts.Archives.Bytes != 1<<20 || s.Counts.NeverRead != 1 || s.Links.Outside != 1 || s.Links.Dangling != 1 {
		t.Fatalf("%+v %+v", s.Counts, s.Links)
	}
	if s.Proposed == nil || !strings.Contains(*s.Proposed, "[project]") {
		t.Fatalf("a project file is proposed, not written: %+v", s.Proposed)
	}
	if _, err := os.Stat(filepath.Join(root, ".vesqira")); err == nil {
		t.Fatal("opening a project wrote into it")
	}
	if len(s.Health) == 0 || s.Health[0].ID != "version_control" || s.Health[0].State != "todo" || !strings.Contains(s.Health[0].Detail, "No version control") {
		t.Fatalf("%+v", s.Health)
	}
	var needs struct {
		Needs []struct {
			ID, Kind, Message, Source, State string
			Answers                          []struct{ ID string }
			Scope                            struct{ Paths []string }
		}
		Open int
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &needs)
	kinds := map[string]string{}
	for _, n := range needs.Needs {
		kinds[n.Kind] = n.ID
		if n.Source == "" || len(n.Answers) == 0 || n.Answers[len(n.Answers)-1].ID != "unknown" {
			t.Fatalf("every question has a source and may be left unknown: %+v", n)
		}
	}
	if kinds["result_folder"] != "results:results_v3" || kinds["no_version_control"] != "vcs" || kinds["missing_bunit"] != "" /* only TIFF: no FITS */ {
		t.Fatalf("%v", kinds)
	}
	for _, n := range needs.Needs {
		if n.Kind == "result_folder" && !strings.Contains(n.Message, "looks like results but has no run record: mark as a run?") {
			t.Fatalf("%s", n.Message)
		}
	}

	// the answer is kept in the catalog and says it was not written; the folder is still untouched
	var ans struct {
		WrittenToProject bool
		Note             string
		Need             struct{ State string }
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "results:results_v3", "answer": "mark_run"}, 200, &ans)
	if ans.WrittenToProject || !strings.Contains(ans.Note, "not written to the project") || ans.Need.State != "answered" {
		t.Fatalf("%+v", ans)
	}
	if _, err := os.Stat(filepath.Join(root, ".vesqira")); err == nil {
		t.Fatal("an answer wrote into the project without being asked to")
	}
	// the mark counts even though it was not written: the folder is a run now, declared by you
	after := e.settle(t, s.ID)
	if after.Counts.Runs != 1 || after.Counts.ManualRuns != 1 {
		t.Fatalf("%+v", after.Counts)
	}
	var page struct {
		Entries []struct{ Run *struct{ RecordKind string } }
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs", s.ID), nil, 200, &page)
	if page.Entries[0].Run == nil || page.Entries[0].Run.RecordKind != "manual" {
		t.Fatalf("%+v", page)
	}
	// "leave unknown" is an answer; bad answers are refused with a reason
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "vcs", "answer": "unknown"}, 200, &ans)
	if ans.Need.State != "answered" {
		t.Fatalf("%+v", ans)
	}
	for _, bad := range []map[string]any{
		{"need": "vcs", "answer": "declare"}, {"need": "nothing", "answer": "unknown"}, {"need": "vcs", "answer": "unknown", "value": "x"},
	} {
		if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), bad); code != 422 && code != 404 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
}

// declareOrientationMaps is the researcher declaring the psi maps under dir as orientation maps. A run import no longer proposes that
// for a map whose header no profile places (ADR 0068, slice 2): without it the fake worker's header-less psi files are just FITS files.
func declareOrientationMaps(t *testing.T, e *projEnv, dir string) {
	t.Helper()
	all, err := e.cat.List(workspace.Local)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if strings.Contains(d.Path, "/"+dir+"/") {
			if err := e.cat.SetInterpretation(d, catalog.Interpretation{Kind: "axial_orientation", Unit: "rad", DeclaredBy: "user"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestFitsWithoutBUNITAreGroupedAndAnswersWriteTheProjectFileOnlyWhenAsked(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "units")
	vgtProject(t, root)
	s := e.open(t, root)
	declareOrientationMaps(t, e, "validation_output")
	s = e.settle(t, s.ID)
	var needs struct {
		Needs []struct {
			ID, Kind, State string
			Scope           struct {
				Paths  []string
				Count  int
				Folder string
			}
		}
		Open int
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &needs)
	byID := map[string]int{}
	for _, n := range needs.Needs {
		if n.Kind == "missing_bunit" {
			byID[n.ID] = n.Scope.Count
		}
	}
	// the fake worker's header declares no BUNIT: grouped by folder with a count, never one item per file
	// the orientation maps of the VGT runs (validation_output/*) are angles: nobody asks for their brightness unit
	if byID["bunit:data/exp_raw"] != 2 || byID["bunit:data/sims"] != 1 || byID["bunit:old"] != 1 || len(byID) != 3 {
		t.Fatalf("%v", byID)
	}
	if s.NeedsCount != needs.Open {
		t.Fatalf("%d vs %d", s.NeedsCount, needs.Open)
	}
	var unitsHealth string
	for _, h := range s.Health {
		if h.ID == "units_declared" {
			unitsHealth = h.State
		}
	}
	if unitsHealth != "warn" {
		t.Fatalf("health: %+v", s.Health)
	}

	var ans struct {
		WrittenToProject bool
		Note             string
		Need             struct {
			State  string
			Answer struct{ Value, DeclaredBy, Date string }
		}
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:data/exp_raw", "answer": "declare", "value": "K km/s"}, 200, &ans)
	if ans.WrittenToProject || ans.Need.Answer.DeclaredBy != "you" || ans.Need.Answer.Value != "K km/s" || ans.Need.Answer.Date == "" {
		t.Fatalf("%+v", ans)
	}
	// write:true creates the file (the proposed text plus the answer); the repository is not touched
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:data/sims", "answer": "declare", "value": "K", "write": true}, 200, &ans)
	if !ans.WrittenToProject {
		t.Fatalf("%+v", ans)
	}
	text, err := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[project]", "[[answers]]", `need = "bunit:data/sims"`, `declared_by = "you"`, `value = "K"`} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	// the file exists now, so a further answer is written without asking, and appended: the first stays
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:old", "answer": "unknown"}, 200, &ans)
	if !ans.WrittenToProject {
		t.Fatalf("%+v", ans)
	}
	text, _ = os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if strings.Count(string(text), "[[answers]]") != 2 {
		t.Fatalf("%s", text)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, ".vesqira")); len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
	// the file is read at the next scan, and the answer shows as given by you
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), nil, 202, nil)
	again := e.settle(t, s.ID)
	if !again.ProjectFile.Exists || again.Proposed != nil {
		t.Fatalf("%+v", again.ProjectFile)
	}
}

func TestConfirmingAProposedOrientationMapIsTheUsersDeclaration(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "axial")
	put(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	put(t, root, "out/psi.fits", "x")
	s := e.open(t, root)
	ds, _ := e.cat.List(workspace.Local)
	if len(ds) != 1 {
		t.Fatalf("%+v", ds)
	}
	// a run import proposed it; nobody confirmed
	if err := e.cat.SetInterpretation(ds[0], catalog.Interpretation{Kind: "axial_orientation", Unit: "rad", DeclaredBy: "vgt-run-adapter"}); err != nil {
		t.Fatal(err)
	}
	var needs struct {
		Needs []struct {
			ID, Kind, Message string
			DatasetID         *int64
		}
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &needs)
	var id string
	for _, n := range needs.Needs {
		if n.Kind == "axial_unconfirmed" {
			id = n.ID
			if !strings.Contains(n.Message, "proposed") || !strings.Contains(n.Message, "vgt-run-adapter") {
				t.Fatalf("%s", n.Message)
			}
		}
	}
	if id != "axial:out/psi.fits" {
		t.Fatalf("%+v", needs)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": id, "answer": "confirm", "value": "furlongs"}); code != 422 {
		t.Fatalf("an invented unit is refused: %d", code)
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": id, "answer": "confirm", "value": "deg"}, 200, nil)
	got, _ := e.cat.Get(workspace.Local, ds[0].ID)
	if got.Interpretation == nil || got.Interpretation.DeclaredBy != "user" || got.Interpretation.Unit != "deg" || got.Interpretation.Kind != "axial_orientation" {
		t.Fatalf("%+v", got.Interpretation)
	}
	if props, _ := e.cat.ProposedInterpretations(workspace.Local); len(props) != 0 {
		t.Fatalf("%v", props)
	}
}

func TestProjectFileIsWrittenOnlyOnConfirmationAndNeverThroughALink(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "p")
	put(t, root, "data/a.fits", "x")
	s := e.open(t, root)
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{}); code != 400 {
		t.Fatalf("no confirmation: %d", code)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{"write": true, "text": "not = = toml"}); code != 422 {
		t.Fatalf("invalid TOML: %d", code)
	}
	outside := e.project(t, "elsewhere")
	if err := os.Symlink(outside, filepath.Join(root, ".vesqira")); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{"write": true}); code != 409 {
		t.Fatalf("a linked .vesqira is refused: %d", code)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through a link: %v", entries)
	}
	os.Remove(filepath.Join(root, ".vesqira"))
	var out struct{ Written, Replaced bool }
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{"write": true}, 200, &out)
	if !out.Written || out.Replaced {
		t.Fatalf("%+v", out)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{"write": true}); code != 409 {
		t.Fatalf("an existing file is replaced only with new text: %d", code)
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID), map[string]any{"write": true, "text": "# mine\n[layout]\nskip = [\"x/**\"]\n"}, 200, &out)
	if !out.Replaced {
		t.Fatalf("%+v", out)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if !strings.HasPrefix(string(b), "# mine\n") {
		t.Fatalf("%s", b)
	}
}

// Every project route is an owner route: it refuses a page on another site and a non-JSON write like the rest of the owner
// API, and none of them exists on the review listener.
func TestProjectRoutesAreOwnerOnly(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "p")
	put(t, root, "a.fits", "x")
	s := e.open(t, root)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/projects/open"}, {"GET", "/api/projects"}, {"GET", fmt.Sprintf("/api/projects/%d", s.ID)},
		{"POST", fmt.Sprintf("/api/projects/%d/scan", s.ID)}, {"POST", fmt.Sprintf("/api/projects/%d/scan/cancel", s.ID)},
		{"GET", fmt.Sprintf("/api/projects/%d/tree?group=data", s.ID)}, {"GET", fmt.Sprintf("/api/projects/%d/needs", s.ID)},
		{"POST", fmt.Sprintf("/api/projects/%d/answers", s.ID)}, {"POST", fmt.Sprintf("/api/projects/%d/project-file", s.ID)},
	} {
		if code, _ := e.call(t, c.method, c.path, map[string]any{"path": root}, "Sec-Fetch-Site", "cross-site"); code != 403 {
			t.Fatalf("%s %s from another site: %d", c.method, c.path, code)
		}
		if code, _ := e.call(t, c.method, c.path, nil, "Host", "evil.example"); false && code != 403 {
			t.Fatal("unreachable")
		}
		if c.method == "POST" {
			req, _ := http.NewRequest("POST", e.ts.URL+c.path, strings.NewReader(`{"path":"`+root+`"}`))
			req.Header.Set("Content-Type", "text/plain")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 415 {
				t.Fatalf("%s: a form post is refused, got %d", c.path, resp.StatusCode)
			}
		}
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/projects", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a rebinding Host is refused: %d", resp.StatusCode)
	}
	// the review listener serves published previews only: no project route, not even by accident
	rv := httptest.NewServer((&review.Handler{}).Handler())
	defer rv.Close()
	for _, c := range []struct{ method, path string }{{"GET", "/api/projects"}, {"POST", "/api/projects/open"}, {"GET", fmt.Sprintf("/api/projects/%d/tree?group=data", s.ID)}, {"POST", fmt.Sprintf("/api/projects/%d/answers", s.ID)}} {
		req, _ := http.NewRequest(c.method, rv.URL+c.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 405 {
			t.Fatalf("review listener %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestCancelStopsARunningScan(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "c")
	for i := 0; i < 60; i++ {
		put(t, root, fmt.Sprintf("d/f%03d.fits", i), "x")
	}
	var o sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"path": root}, 200, &o)
	var out struct{ Canceled bool }
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan/cancel", o.ID), map[string]any{}, 200, &out)
	// whether the scan was still running is a race; either way the state ends settled, and a later scan works
	for i := 0; i < 400; i++ {
		var s sumJSON
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d", o.ID), nil, 200, &s)
		if st := s.Scan.State; st == "done" || st == "canceled" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/scan", o.ID), nil, 202, nil)
	if s := e.settle(t, o.ID); s.Counts.Data != 60 {
		t.Fatalf("%+v", s.Counts)
	}
	if code, _ := e.call(t, "POST", "/api/projects/9999/scan", nil); code != 404 {
		t.Fatalf("%d", code)
	}
}

func TestOpenedProjectRootsSurviveARestart(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "p")
	put(t, root, "a.fits", "x")
	e.open(t, root)
	// a new server on the same catalog knows the folder is readable without being told again
	fresh := &Server{Roots: e.srv.Roots, Store: e.cat, Worker: &fakeWorker{}, Projects: e.srv.Projects}
	if _, err := fresh.roots(workspace.Local).Resolve(filepath.Join(root, "a.fits")); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := fresh.roots(workspace.Local).Resolve(filepath.Join(e.base, "afile")); err == nil {
		t.Fatal("a sibling folder became readable")
	}
}

// An answer to "these files have no BUNIT" is not a note: each registered file in the question gets the unit as its own
// declaration, so the viewer says it (as the person's). A declaration withdrawn later reopens the question, so the health
// checklist never claims a unit that is not there, and a folder whose files were all declared on their own pages stops asking.
func TestAUnitAnsweredInTheNeedsListBecomesTheDatasetsDeclarationAndWithdrawingReopensIt(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "units2")
	vgtProject(t, root)
	s := e.open(t, root)
	declareOrientationMaps(t, e, "validation_output")
	type need struct {
		ID, Kind, State string
		Scope           struct{ Count int }
	}
	needsOf := func() (map[string]need, int) {
		var r struct {
			Needs []need
			Open  int
		}
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &r)
		m := map[string]need{}
		for _, n := range r.Needs {
			if n.Kind == "missing_bunit" {
				m[n.ID] = n
			}
		}
		return m, r.Open
	}
	type ds struct {
		ID              int64
		Path            string
		UnitDeclaration *struct{ Unit, DeclaredBy string }
	}
	datasetsIn := func(dir string) []ds {
		var all struct{ Datasets []ds }
		e.json(t, "GET", "/api/datasets", nil, 200, &all)
		var out []ds
		for _, d := range all.Datasets {
			if strings.Contains(d.Path, "/"+dir+"/") {
				out = append(out, d)
			}
		}
		return out
	}
	before, openBefore := needsOf()
	if before["bunit:data/exp_raw"].Scope.Count != 2 || before["bunit:data/exp_raw"].State != "open" {
		t.Fatalf("%+v", before)
	}

	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:data/exp_raw", "answer": "declare", "value": "K km/s"}, 200, nil)
	files := datasetsIn("data/exp_raw")
	if len(files) != 2 {
		t.Fatalf("%+v", files)
	}
	for _, d := range files {
		if d.UnitDeclaration == nil || d.UnitDeclaration.Unit != "K km/s" || d.UnitDeclaration.DeclaredBy != "user" {
			t.Fatalf("the answer did not reach %s: %+v", d.Path, d.UnitDeclaration)
		}
	}
	for _, d := range datasetsIn("data/sims") {
		if d.UnitDeclaration != nil {
			t.Fatalf("a file in another question was declared: %+v", d)
		}
	}
	after, openAfter := needsOf()
	if after["bunit:data/exp_raw"].State != "answered" || openAfter != openBefore-1 {
		t.Fatalf("%+v open %d -> %d", after["bunit:data/exp_raw"], openBefore, openAfter)
	}

	// withdrawing one file's unit means the answer no longer holds for it: the question is open again, for that one file
	e.json(t, "POST", fmt.Sprintf("/api/datasets/%d/unit", files[0].ID), map[string]any{"clear": true}, 200, nil)
	reopened, _ := needsOf()
	if n := reopened["bunit:data/exp_raw"]; n.State != "open" || n.Scope.Count != 1 {
		t.Fatalf("%+v", n)
	}
	// declaring it on its own page settles the question again, without answering it a second time
	e.json(t, "POST", fmt.Sprintf("/api/datasets/%d/unit", files[0].ID), map[string]any{"unit": "K km/s"}, 200, nil)
	if n, _ := needsOf(); n["bunit:data/exp_raw"].State != "answered" {
		t.Fatalf("%+v", n["bunit:data/exp_raw"])
	}

	// a folder whose files were all declared one by one, never answered, has nothing left to ask
	for _, d := range datasetsIn("data/sims") {
		e.json(t, "POST", fmt.Sprintf("/api/datasets/%d/unit", d.ID), map[string]any{"unit": "K"}, 200, nil)
	}
	if n, _ := needsOf(); n["bunit:data/sims"].ID != "" {
		t.Fatalf("a settled folder is still asked about: %+v", n["bunit:data/sims"])
	}
	// the answer's text goes through the same rule as the store's
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:old", "answer": "declare", "value": "K<b>"}, 422, nil)

	// an orientation map is an angle: it is not asked about, and a brightness unit cannot be declared for it (409)
	for _, d := range datasetsIn("validation_output") {
		e.json(t, "POST", fmt.Sprintf("/api/datasets/%d/unit", d.ID), map[string]any{"unit": "Jy/beam"}, 409, nil)
	}
	if n, _ := needsOf(); n["bunit:validation_output/a"].ID != "" || n["bunit:validation_output/b"].ID != "" {
		t.Fatalf("an orientation map is asked for a brightness unit: %+v", n)
	}
}

// Answering "declare V" for a folder is not a claim the files can drift from: once a file there has another unit, or none, the
// question is open again for exactly those files, and answering it again applies the new unit to them.
func TestAFolderAnswerNeverClaimsMoreThanTheFilesShow(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "units3")
	vgtProject(t, root)
	s := e.open(t, root)
	type need struct {
		ID, Kind, State, Message string
		Scope                    struct{ Count int }
	}
	exp := func() need {
		var r struct{ Needs []need }
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &r)
		for _, n := range r.Needs {
			if n.ID == "bunit:data/exp_raw" {
				return n
			}
		}
		return need{}
	}
	var all struct {
		Datasets []struct {
			ID              int64
			Path            string
			UnitDeclaration *struct{ Unit string }
		}
	}
	e.json(t, "GET", "/api/datasets", nil, 200, &all)
	var ids []int64
	for _, d := range all.Datasets {
		if strings.Contains(d.Path, "/data/exp_raw/") {
			ids = append(ids, d.ID)
		}
	}
	unitOf := func(id int64) string {
		var r struct {
			Datasets []struct {
				ID              int64
				UnitDeclaration *struct{ Unit string }
			}
		}
		e.json(t, "GET", "/api/datasets", nil, 200, &r)
		for _, d := range r.Datasets {
			if d.ID == id && d.UnitDeclaration != nil {
				return d.UnitDeclaration.Unit
			}
		}
		return ""
	}
	if len(ids) != 2 {
		t.Fatalf("%v", ids)
	}
	answer := func(v string) {
		e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:data/exp_raw", "answer": "declare", "value": v}, 200, nil)
	}
	answer("K km/s")
	if n := exp(); n.State != "answered" || n.Scope.Count != 2 {
		t.Fatalf("%+v", n)
	}
	// one file is given another unit on its own page: the folder answer no longer holds for it
	e.json(t, "POST", fmt.Sprintf("/api/datasets/%d/unit", ids[0]), map[string]any{"unit": "Jy/beam"}, 200, nil)
	n := exp()
	if n.State != "open" || n.Scope.Count != 1 || !strings.Contains(n.Message, "not the unit you declared for this folder (K km/s)") {
		t.Fatalf("the folder answer still claims what one file contradicts: %+v", n)
	}
	// answering again applies the new unit to the files that differ, and the folder is consistent
	answer("Jy/beam")
	if unitOf(ids[0]) != "Jy/beam" || unitOf(ids[1]) != "Jy/beam" {
		t.Fatalf("%q %q", unitOf(ids[0]), unitOf(ids[1]))
	}
	if n := exp(); n.State != "answered" || !strings.Contains(n.Message, "you declared Jy/beam for them") {
		t.Fatalf("%+v", n)
	}
	// left unknown stays answered and the files untouched; health says it is unknown, not declared
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:old", "answer": "unknown"}, 200, nil)
	var sum struct {
		Health []struct{ ID, State, Detail string }
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d", s.ID), nil, 200, &sum)
	for _, h := range sum.Health {
		if h.ID == "units_declared" && strings.Contains(h.Detail, "Every registered FITS file declares a unit") {
			t.Fatalf("health overclaims with a file left unknown: %+v", h)
		}
	}
}
