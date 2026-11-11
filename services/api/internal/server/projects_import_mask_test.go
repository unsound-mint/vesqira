package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
)

// The author label of a rule is spelled once in discover and once in catalog (which discover may not import); they must agree, or a
// rule's declaration would stop being recognised as the researcher's.
func TestRuleAuthorPrefixIsTheSameInDiscoverAndCatalog(t *testing.T) {
	if discover.RulePrefix != catalog.RuleAuthorPrefix {
		t.Fatalf("%q != %q", discover.RulePrefix, catalog.RuleAuthorPrefix)
	}
}

// A mask rule goes through the mask route's code: the mask is declared (the researcher's rule gave its type and the meaning of nonzero),
// its author names the rule, its flagged count is the file's, and the dataset's own file is untouched. A target that is not registered
// is retried later, a mask on another grid is refused with the reason, and applying twice makes one mask.
func TestImportMaskRuleMakesADeclaredMaskForTheNamedDataset(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, reproject").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and reproject is required")
		}
		t.Skip("python3 with astropy and reproject not available")
	}
	py, _ := exec.LookPath("python3")
	e := newProjEnv(t)
	derivedDir := filepath.Join(e.base, "derived")
	os.MkdirAll(derivedDir, 0o755)
	ds, _ := derived.Open(e.cat.DB())
	ms, err := masks.Open(e.cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Worker, e.srv.Derived, e.srv.Masks = headerWorker(t, py, repo), ds, ms
	e.srv.Analysis = &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}

	root := e.project(t, "masks")
	put(t, root, ".vesqira/project.toml", "[project]\nname = \"masks\"\n")
	if out, err := exec.Command("python3", "-c", `
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
w('psi_flags.fits', m)                                      # target psi.fits by another name: no strip suffix matches
`, filepath.Join(e.base, "masks")).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"psi", "psi_mask", "lonely_mask", "grid", "grid_mask", "psi_flags"} {
		if err := os.Rename(filepath.Join(root, n+".fits"), filepath.Join(root, "data", n+".fits")); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, "data", "psi.fits"))

	maskRule := map[string]any{"name": "sat masks", "kind": "glob", "pattern": "data/*_mask.fits", "role": "mask", "maskType": "saturated",
		"maskNonzero": "flagged", "maskTarget": "{dir}/{stem}.fits", "maskStrip": "_mask", "maskMeaning": "detector saturation"}
	s := e.open(t, root)
	if code, b := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{maskRule}}); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	apply := fmt.Sprintf("/api/projects/%d/import/apply", s.ID)
	var out applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)

	r := out.by("data/psi_mask.fits")
	if r.Outcome != "declared" || len(r.Declared) != 1 || !strings.Contains(r.Declared[0], "mask of data/psi.fits, type saturated, 3 pixels flagged") || !strings.Contains(r.Declared[0], "declared by your rule sat masks") {
		t.Fatalf("%+v", r)
	}
	facts, _ := e.cat.FactsByPath(0)
	psi, _ := e.cat.Get(facts[filepath.Join(root, "data", "psi.fits")].ID)
	list, _ := e.srv.Masks.ForDataset(psi.ID)
	if len(list) != 1 {
		t.Fatalf("%d masks", len(list))
	}
	m := list[0]
	if m.Type != "saturated" || m.Tier != catalog.TierDeclared || m.Author != "rule:sat masks" || m.Method != "import" || m.Flagged != 3 || m.Meaning != "detector saturation" || m.Name != "psi_mask.fits" {
		t.Fatalf("%+v", m)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "data", "psi.fits")); string(after) != string(before) {
		t.Fatal("the dataset's file changed")
	}
	// the dataset the mask names is not registered: tried again by a later apply, not settled
	if r := out.by("data/lonely_mask.fits"); r.Outcome != "failed" || !strings.Contains(r.Detail, "data/lonely.fits") || !strings.Contains(r.Detail, "not registered") {
		t.Fatalf("%+v", r)
	}
	// another grid: refused with the reason, no mask
	if r := out.by("data/grid_mask.fits"); r.Outcome == "declared" || !strings.Contains(r.Detail, "no mask was made") {
		t.Fatalf("%+v", r)
	}
	grid, _ := e.cat.Get(facts[filepath.Join(root, "data", "other.fits")].ID)
	if l, _ := e.srv.Masks.ForDataset(grid.ID); len(l) != 0 {
		t.Fatalf("a mask was made on the wrong grid: %+v", l)
	}
	// once more, asking for the file by name: one mask, said so
	var again applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true, "paths": []string{"data/psi_mask.fits"}}, 200, &again)
	if r := again.by("data/psi_mask.fits"); r.Outcome == "declared" || !strings.Contains(r.Detail, "already imported") {
		t.Fatalf("%+v", r)
	}
	if l, _ := e.srv.Masks.ForDataset(psi.ID); len(l) != 1 {
		t.Fatalf("%d masks after applying twice", len(l))
	}
	// the reviewer's account of a figure from this dataset says the mask is declared, through a rule, and never names the rule
	if got := declaredNote("rule:sat masks", "x"); strings.Contains(got, "sat masks") || !strings.Contains(got, "import rule") {
		t.Fatalf("%q", got)
	}
}
