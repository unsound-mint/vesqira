package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// Profiles from a project folder (ADR 0068, slice 6): a tool that only the project's .vesqira/profiles/ knows is detected in that
// project, imported by the person's own act and not by the scan, labelled as declared by the project's file, and not known anywhere else.

type profEnv struct {
	*projEnv
	fixtures string
}

// newProfEnv is the project server over the real worker, with the generated fixture folders at <base>/fixtures.
func newProfEnv(t *testing.T) *profEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	e := newProjEnv(t)
	pw := headerWorker(t, "python3", repo)
	ds, _ := derived.Open(e.cat.DB())
	py, _ := exec.LookPath("python3")
	derivedDir := filepath.Join(e.base, "derived")
	os.MkdirAll(derivedDir, 0o755)
	rs, _ := sourceroot.New([]string{filepath.Join(e.base, "configured"), derivedDir})
	e.srv.Roots = rs
	e.srv.Worker, e.srv.Planes, e.srv.Cache = pw, pw, NewPlaneCache(8<<20)
	e.srv.CacheDir, e.srv.Derived = filepath.Join(e.base, "cache"), ds
	e.srv.Analysis = &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}
	fx := filepath.Join(e.base, "fixtures")
	os.MkdirAll(fx, 0o755)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), fx)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return &profEnv{projEnv: e, fixtures: fx}
}

// folder copies a generated fixture folder into a project.
func (e *profEnv) folder(t *testing.T, name, project string) {
	t.Helper()
	if out, err := exec.Command("cp", "-r", filepath.Join(e.fixtures, name), filepath.Join(project, name)).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// profileFile writes <project>/.vesqira/profiles/<file> holding one profile of a testdata file, in the project-file form.
func profileFile(t *testing.T, project, file, testdata, name string, edit func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "runs", "testdata", testdata))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range doc.Profiles {
		if p["name"] == name {
			if edit != nil {
				edit(p)
			}
			b, _ := json.MarshalIndent(map[string]any{"format": 1, "about": "written by a test", "profile": p}, "", " ")
			put(t, project, ".vesqira/profiles/"+file, string(b))
			return
		}
	}
	t.Fatalf("no profile %s in %s", name, testdata)
}

type treeRunJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	Run  *struct {
		Profile     string `json:"profile"`
		ProfileFile string `json:"profileFile"`
		Importable  bool   `json:"importable"`
		RunID       *int64 `json:"runId"`
	} `json:"run"`
}

func (e *profEnv) runRows(t *testing.T, id int64) []treeRunJSON {
	t.Helper()
	var page struct {
		Entries []treeRunJSON `json:"entries"`
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/tree?group=runs", id), nil, 200, &page)
	return page.Entries
}

// runEntry finds the run folder's row in the project's run tree.
func (e *profEnv) runEntry(t *testing.T, id int64, dir string) *treeRunJSON {
	t.Helper()
	rows := e.runRows(t, id)
	for i := range rows {
		if rows[i].Path == dir && rows[i].Run != nil {
			return &rows[i]
		}
	}
	t.Fatalf("no run %s in the tree: %+v", dir, rows)
	return nil
}

func TestProjectProfileDetectsImportsAndLabelsARunOfItsOwnTool(t *testing.T) {
	e := newProfEnv(t)
	proj := e.project(t, "withprofile")
	e.folder(t, "hog_run_demo", proj)
	profileFile(t, proj, "hog.json", "synthetic_hog_profile.json", "HOG-synthetic", nil)
	// refused files are listed with a sentence and cost the others nothing
	profileFile(t, proj, "shipped.json", "synthetic_hog_profile.json", "HOG-synthetic", func(p map[string]any) { p["name"], p["kind"] = "ROHSA", "hog2" })
	outside := filepath.Join(e.base, "outside.json")
	os.WriteFile(outside, []byte(`{"format":1}`), 0o644)
	os.Symlink(outside, filepath.Join(proj, ".vesqira/profiles/leak.json"))

	s := e.open(t, proj)
	var sum struct {
		Profiles []struct{ File, Profile, Why string } `json:"profiles"`
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d", s.ID), nil, 200, &sum)
	loaded, refused := 0, map[string]string{}
	for _, p := range sum.Profiles {
		if p.Why == "" {
			loaded++
			if p.Profile != "HOG-synthetic@1" {
				t.Errorf("loaded profile %+v", p)
			}
		} else {
			refused[filepath.Base(p.File)] = p.Why
		}
	}
	if loaded != 1 || !strings.Contains(refused["shipped.json"], "cannot redefine") || !strings.Contains(refused["leak.json"], "leaves the project") {
		t.Fatalf("profiles: %+v", sum.Profiles)
	}

	// the scan detects the run and says whose profile did; it does not import it or register its files
	row := e.runEntry(t, s.ID, "hog_run_demo")
	if row.Run.Profile != "HOG-synthetic@1" || row.Run.ProfileFile != ".vesqira/profiles/hog.json" || !row.Run.Importable || row.Run.RunID != nil {
		t.Fatalf("tree row %+v", row.Run)
	}
	if s.Scan.Registered != 0 {
		t.Errorf("a scan registered %d files for a project profile's run", s.Scan.Registered)
	}

	// the person's Import applies it
	r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(proj, "hog_run_demo")})
	if r.StatusCode != 201 {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	var run struct {
		Profile       string
		ProfileOrigin *struct{ File, SHA256 string }
		Outputs       []struct{ Role, Product string }
	}
	json.Unmarshal(out, &run)
	if run.Profile != "HOG-synthetic@1" || run.ProfileOrigin == nil || run.ProfileOrigin.File != ".vesqira/profiles/hog.json" || len(run.ProfileOrigin.SHA256) != 64 || len(run.Outputs) != 3 {
		t.Fatalf("run %s", out)
	}
	if row := e.runEntry(t, s.ID, "hog_run_demo"); row.Run.RunID == nil {
		t.Errorf("the tree does not show the run as imported")
	}

	// another project does not know the profile
	other := e.project(t, "withoutprofile")
	e.folder(t, "hog_run_demo", other)
	o := e.open(t, other)
	for _, en := range e.runRows(t, o.ID) {
		if en.Run != nil && (en.Run.Profile != "" || en.Run.Importable) {
			t.Errorf("the profile leaked into another project: %+v", en.Run)
		}
	}
	if r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(other, "hog_run_demo")}); r.StatusCode < 400 {
		t.Errorf("a run of a tool only another project profiles was imported: %d %s", r.StatusCode, out)
	}
}

// A decomposition declared by a project's profile is read after its import with the same sources, and a profile file edited since
// the import is refused rather than applied to a record made under the old declaration.
func TestProjectProfileDecompositionIsPinnedToTheFileItWasImportedUnder(t *testing.T) {
	e := newProfEnv(t)
	proj := e.project(t, "decomp")
	e.folder(t, "synth_decomp_demo", proj)
	profileFile(t, proj, "synth.json", "synthetic_maps_profile.json", "SynthDecomp-cubes", nil)
	e.open(t, proj)

	r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(proj, "synth_decomp_demo")})
	if r.StatusCode != 201 {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	var run rohsaRunJSON
	json.Unmarshal(out, &run)
	if run.Kind != "synthdecomp" || run.Rohsa == nil || run.Rohsa.CropDatasetID == nil || run.Rohsa.NGauss != dnComp {
		t.Fatalf("run %s", out)
	}
	base := e.ts.URL + "/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa"
	if r, ib := postJSON(t, base+"/index", map[string]any{"runId": run.ID}); r.StatusCode != 200 {
		t.Fatalf("index %d %s", r.StatusCode, ib)
	}
	if resp, b := get(t, base+"/components?run="+itoa(int(run.ID))+"&x=3&y=2"); resp.StatusCode != 200 {
		t.Fatalf("components %d %s", resp.StatusCode, b)
	}

	// the person (or a collaborator, or a git checkout) edits the profile
	profileFile(t, proj, "synth.json", "synthetic_maps_profile.json", "SynthDecomp-cubes", func(p map[string]any) { p["source"] = "edited later" })
	e.open(t, proj)
	resp, b := get(t, base+"/components?run="+itoa(int(run.ID))+"&x=3&y=2")
	if resp.StatusCode != 409 || !strings.Contains(string(b), "changed") {
		t.Fatalf("components after the profile changed: %d %s", resp.StatusCode, b)
	}
}
