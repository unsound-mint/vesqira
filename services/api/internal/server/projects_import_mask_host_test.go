package server

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// A mask-role import rule in a project on a host (ADR 0058, ADR 0064): the mask file is registered on the host, the mask is made by the
// host's own copy of the program through the same route as a mask made by hand, and is recorded on that host as the researcher's declared
// mask with the rule as its author. Nothing is read or written here but the summary; the dataset's file and the mask file are untouched.
func TestImportMaskRuleOnAHostProjectMakesTheMaskOnTheHost(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, reproject").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and reproject is required")
		}
		t.Skip("python3 with astropy and reproject not available")
	}
	py, _ := exec.LookPath("python3")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	seed := filepath.Join(base, "seed_root")
	proj := filepath.Join(base, "remote_proj")
	derivedHere := filepath.Join(base, "derived")
	for _, d := range []string{remoteHome, seed, filepath.Join(proj, "data"), derivedHere} {
		os.MkdirAll(d, 0o755)
	}
	t.Setenv("HOME", remoteHome)
	if out, err := exec.Command(py, "-c", `
import sys, numpy as np
from astropy.io import fits
d = sys.argv[1]
def w(name, a): fits.PrimaryHDU(a).writeto(d + '/' + name)
w('psi.fits', np.full((6, 8), 3.0, dtype='<f4'))
m = np.zeros((6, 8), dtype='<i2'); m[0, 0:3] = 1
w('psi_mask.fits', m)
w('lonely_mask.fits', m)                                  # its dataset, lonely.fits, does not exist
w('grid.fits', np.full((6, 8), 3.0, dtype='<f4'))
w('grid_mask.fits', np.zeros((5, 5), dtype='<i2'))         # not the grid of grid.fits
`, filepath.Join(proj, "data")).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	put(t, proj, ".vesqira/project.toml", "[project]\nname = \"theirs\"\n")
	before := mustRead(t, filepath.Join(proj, "data", "psi.fits"))
	maskBefore := mustRead(t, filepath.Join(proj, "data", "psi_mask.fits"))

	mgr := hosts.NewManager(fakeSSH(t, remoteHome), workerFiles(t, repo))
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	ps, _ := projects.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	ds, _ := derived.Open(cat.DB())
	ms, err := masks.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := sourceroot.New([]string{seed, derivedHere})
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Projects: ps, Runs: rn, Audit: au, HostDB: hs, Remote: mgr, Derived: ds, Masks: ms,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedHere, Timeout: 2 * time.Minute}}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &projEnv{ts: ts, srv: srv, cat: cat, base: base, home: remoteHome}
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Python: py, Roots: []string{seed}})
	if err != nil {
		t.Fatal(err)
	}
	var o sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"hostId": h.ID, "path": proj}, 200, &o)
	e.settle(t, o.ID)

	rule := map[string]any{"name": "sat masks", "kind": "glob", "pattern": "data/*_mask.fits", "role": "mask", "maskType": "saturated",
		"maskNonzero": "flagged", "maskTarget": "{dir}/{stem}.fits", "maskStrip": "_mask", "maskMeaning": "detector saturation"}
	if code, b := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", o.ID), map[string]any{"rules": []any{rule}}); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	apply := fmt.Sprintf("/api/projects/%d/import/apply", o.ID)
	var out applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)

	r := out.by("data/psi_mask.fits")
	if r.Outcome == "skipped" || r.DatasetID == nil || len(r.Declared) == 0 || !strings.Contains(strings.Join(r.Declared, ";"), "mask of data/psi.fits, type saturated, 3 pixels flagged") {
		t.Fatalf("the mask rule did not make the mask on the host: %+v", r)
	}
	facts, _ := cat.FactsByPath(workspace.Local, h.ID)
	psi, err := cat.Get(workspace.Local, facts[filepath.Join(proj, "data", "psi.fits")].ID)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := ms.ForDataset(workspace.Local, psi.ID)
	if len(list) != 1 {
		t.Fatalf("%d masks", len(list))
	}
	m := list[0]
	if m.HostID != h.ID || m.Type != "saturated" || m.Tier != catalog.TierDeclared || m.Author != "rule:sat masks" || m.Method != "import" || m.Flagged != 3 ||
		m.Meaning != "detector saturation" || m.Name != "psi_mask.fits" || m.SHA256 == "" {
		t.Fatalf("%+v", m)
	}
	// the new mask file is the host's, in its derived folder (the first root); nothing of it was written here
	hostDerived := filepath.Join(seed, "vesqira_derived")
	if got, _ := filepath.Glob(filepath.Join(hostDerived, fmt.Sprintf("mask-%d-saturated-*.fits", psi.ID))); len(got) != 1 {
		t.Fatalf("host derived folder: %v", derivedFiles(t, hostDerived))
	}
	for _, f := range derivedFiles(t, hostDerived) {
		if strings.HasPrefix(f, ".part-") {
			t.Errorf("a temporary file was left on the host: %s", f)
		}
	}
	if n := len(derivedFiles(t, derivedHere)); n != 0 {
		t.Errorf("%d files were written here for a mask on a host", n)
	}
	if string(mustRead(t, filepath.Join(proj, "data", "psi.fits"))) != string(before) || string(mustRead(t, filepath.Join(proj, "data", "psi_mask.fits"))) != string(maskBefore) {
		t.Fatal("the dataset's file or the mask file changed")
	}
	// the dataset the mask names is not registered: tried again by a later apply, not settled
	if r := out.by("data/lonely_mask.fits"); r.Outcome != "failed" || !strings.Contains(r.Detail, "not registered") {
		t.Fatalf("%+v", r)
	}
	// another grid: refused with the reason, no mask
	if r := out.by("data/grid_mask.fits"); r.Outcome == "declared" || !strings.Contains(r.Detail, "no mask was made") {
		t.Fatalf("%+v", r)
	}
	grid, _ := cat.Get(workspace.Local, facts[filepath.Join(proj, "data", "grid.fits")].ID)
	if l, _ := ms.ForDataset(workspace.Local, grid.ID); len(l) != 0 {
		t.Fatalf("a mask was made on the wrong grid: %+v", l)
	}
	// once more, asking for the file by name: still one mask, and said so
	var again applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true, "paths": []string{"data/psi_mask.fits"}}, 200, &again)
	if r := again.by("data/psi_mask.fits"); r.Outcome == "declared" || !strings.Contains(r.Detail, "already imported") {
		t.Fatalf("%+v", r)
	}
	if l, _ := ms.ForDataset(workspace.Local, psi.ID); len(l) != 1 {
		t.Fatalf("%d masks after applying twice", len(l))
	}
	// the host going away is a failure to retry, with no mask recorded
	os.WriteFile(filepath.Join(remoteHome, "DOWN"), nil, 0o644)
	if err := ms.Delete(workspace.Local, m.ID); err != nil {
		t.Fatal(err)
	}
	var down applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true, "paths": []string{"data/psi_mask.fits"}}, 200, &down)
	if r := down.by("data/psi_mask.fits"); r.Outcome != "failed" {
		t.Fatalf("host unreachable: %+v", r)
	}
	if l, _ := ms.ForDataset(workspace.Local, psi.ID); len(l) != 0 {
		t.Fatalf("a mask was recorded while the host was unreachable: %+v", l)
	}
}
