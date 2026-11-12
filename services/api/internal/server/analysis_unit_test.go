package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// A unit the person declared for a file without BUNIT follows the file into the analyses run on it, labelled as theirs: the
// result says whose it is in its header, in its summary and in the record that travels with figures and reviews. The file's own
// BUNIT wins, a withdrawn or stale declaration is not sent, and the declared text is only ever a value of the worker's option.
func TestAnalysisCarriesADeclaredUnitAsTheAuthorsAndNeverAStaleOne(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, reproject").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and reproject is required")
		}
		t.Skip("python3 with astropy and reproject not available")
	}
	py, _ := exec.LookPath("python3")
	base := t.TempDir()
	root, derivedDir := filepath.Join(base, "data"), filepath.Join(base, "derived")
	for _, d := range []string{root, derivedDir} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// copies of the cube without BUNIT; "extra" makes the bytes of the same file differ, for the stale case
	script := "import sys\nfrom astropy.io import fits\nwith fits.open(sys.argv[1]) as h:\n    del h[0].header['BUNIT']\n    if len(sys.argv) > 3:\n        h[0].header['HISTORY'] = sys.argv[3]\n    h.writeto(sys.argv[2], overwrite=True)"
	strip := func(name string, extra ...string) {
		t.Helper()
		out, err := exec.Command("python3", append([]string{"-I", "-c", script, filepath.Join(root, "hi_cube.fits"), filepath.Join(root, name)}, extra...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	strip("nounit.fits")
	strip("nounit_b.fits")

	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	ds, _ := derived.Open(cat.DB())
	rs, err := sourceroot.New([]string{root, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: pw, Planes: pw, Cache: NewPlaneCache(8 << 20), Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	defer ts.Close()

	register := func(name string) catalog.Dataset {
		t.Helper()
		resp := post(t, ts, filepath.Join(root, name), nil)
		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			t.Fatalf("%s: register %d", name, resp.StatusCode)
		}
		var d catalog.Dataset
		json.NewDecoder(resp.Body).Decode(&d)
		return d
	}
	noUnit, other, withUnit := register("nounit.fits"), register("nounit_b.fits"), register("hi_cube.fits")
	declare := func(d catalog.Dataset, unit string) {
		t.Helper()
		body := map[string]any{"unit": unit}
		if unit == "" {
			body = map[string]any{"clear": true}
		}
		if st, b := callJSON(t, "POST", fmt.Sprintf("%s/api/datasets/%d/unit", ts.URL, d.ID), body); st != 200 {
			t.Fatalf("declare %q: %d %s", unit, st, b)
		}
	}
	type result struct {
		Dataset catalog.Dataset `json:"dataset"`
		Summary struct {
			UnitSource string `json:"unit_source"`
			Unit       string `json:"unit"`
			Policy     string `json:"policy"`
		} `json:"summary"`
	}
	analyse := func(in map[string]any) (int, result, []byte) {
		t.Helper()
		st, b := callJSON(t, "POST", ts.URL+"/api/analysis", in)
		var r result
		json.Unmarshal(b, &r)
		return st, r, b
	}
	headerOf := func(r result) (bunit, status string) {
		var h struct {
			Bunit       *string `json:"bunit"`
			BunitStatus string  `json:"bunit_status"`
		}
		json.Unmarshal(r.Dataset.Header, &h)
		if h.Bunit != nil {
			bunit = *h.Bunit
		}
		return bunit, h.BunitStatus
	}
	moment0 := func(d catalog.Dataset) (int, result, []byte) {
		return analyse(map[string]any{"op": "moment", "datasetId": d.ID, "params": map[string]string{"order": "0"}})
	}

	// nothing declared: the result is unresolved, as before
	st, r, b := moment0(noUnit)
	if st != 201 || r.Summary.UnitSource != "unresolved" {
		t.Fatalf("%d %s", st, b)
	}
	if bu, _ := headerOf(r); !strings.HasPrefix(bu, "(brightness unit unresolved)") {
		t.Fatalf("an undeclared unit must stay unresolved: %q", bu)
	}

	// declared: the result carries it, says whose it is, and the record that goes with figures says it too
	declare(noUnit, "K")
	st, r, b = moment0(noUnit)
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	if bu, _ := headerOf(r); bu != "K m/s" || r.Summary.UnitSource != "user" || !strings.Contains(r.Summary.Policy, "declared by the author") {
		t.Fatalf("a declared unit must reach the result as the author's: %q %+v", bu, r.Summary)
	}
	_, rec := get(t, fmt.Sprintf("%s/api/datasets/%d/derived", ts.URL, r.Dataset.ID))
	if !strings.Contains(string(rec), `"declaredUnits":{"input":"K"}`) {
		t.Errorf("the record must say what was offered to the worker: %s", rec)
	}
	if sum := (&Server{Derived: ds, Store: cat}).derivedSummary(workspace.Local, r.Dataset.ID); sum == nil || !strings.Contains(sum.Policy, "declared by the author") {
		t.Errorf("the reviewer-safe summary must carry the attribution: %+v", sum)
	}

	// a unit that starts with a dash is a value of the option, not an option of the worker
	declare(noUnit, "-K")
	if st, r, b = moment0(noUnit); st != 201 {
		t.Fatalf("%d %s", st, b)
	} else if bu, _ := headerOf(r); bu != "-K m/s" {
		t.Fatalf("%q", bu)
	}

	// withdrawn: the next result is unresolved again
	declare(noUnit, "")
	if st, r, b = moment0(noUnit); st != 201 || r.Summary.UnitSource != "unresolved" {
		t.Fatalf("a withdrawn unit was still sent: %d %s", st, b)
	}

	// stale: the file was rewritten after the declaration, so it describes other bytes and is not sent
	declare(noUnit, "K")
	strip("nounit.fits", "rewritten after the unit was declared")
	if d := register("nounit.fits"); d.UnitDeclaration == nil || !d.UnitDeclaration.Stale {
		t.Fatalf("a rewritten file must mark the declaration stale: %+v", d.UnitDeclaration)
	}
	if st, r, b = moment0(noUnit); st != 201 || r.Summary.UnitSource != "unresolved" {
		t.Fatalf("a stale declaration was applied: %d %s", st, b)
	}

	// the file's own BUNIT wins: nothing can be declared for it, and the result says the unit is the file's
	if st, _ := callJSON(t, "POST", fmt.Sprintf("%s/api/datasets/%d/unit", ts.URL, withUnit.ID), map[string]any{"unit": "Jy"}); st != 409 {
		t.Fatalf("a file with a BUNIT must refuse a declared one: %d", st)
	}
	if st, r, b = moment0(withUnit); st != 201 || r.Summary.UnitSource != "file" {
		t.Fatalf("%d %s", st, b)
	} else if bu, _ := headerOf(r); bu != "K m/s" {
		t.Fatalf("%q", bu)
	}

	// a difference of two maps with no BUNIT: refused until both have a unit, then it carries the declared one as the author's
	cut := func(d catalog.Dataset) catalog.Dataset {
		t.Helper()
		st, r, b := analyse(map[string]any{"op": "slab", "datasetId": d.ID, "params": map[string]string{"chanStart": "3", "chanEnd": "3"}})
		if st != 201 {
			t.Fatalf("%d %s", st, b)
		}
		return r.Dataset
	}
	declare(noUnit, "K")
	a, c := cut(noUnit), cut(other) // single planes of the two files: a is the author's K, c has no unit
	diff := func() (int, result, []byte) {
		return analyse(map[string]any{"op": "scalar_diff", "datasetId": a.ID, "referenceId": c.ID})
	}
	if st, _, b := diff(); st != 422 || !strings.Contains(string(b), "brightness unit") {
		t.Fatalf("a difference against a map with no unit must be refused: %d %s", st, b)
	}
	declare(c, "K")
	if st, r, b := diff(); st != 201 || r.Summary.Unit != "K" || r.Summary.UnitSource != "user" || !strings.Contains(r.Summary.Policy, "declared by the author for the input and the reference") {
		t.Fatalf("the declared unit of the reference must be sent and attributed: %d %s", st, b)
	}
	declare(c, "Jy")
	if st, _, b := diff(); st != 422 || !strings.Contains(string(b), "different brightness units") {
		t.Fatalf("different declared units are refused, not converted: %d %s", st, b)
	}
}
