package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// A tool that has only a profile (ADR 0068, slice 3): the synthetic HOG-style folder is imported as a run with generic output roles,
// its own product names, and no interpretation, because the profile declares none. Without that profile the same folder is refused.
// A VGT run's JSON carries both orientationHeader and the deprecated vgtHeader during the compatibility period.
func TestRunImportOfAToolThatHasOnlyAProfile(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, _ := audit.Open(cat.DB())
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), Runs: rn, Audit: au}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	hog := filepath.Join(root, "hog_run_demo")

	// shipped profiles only: the record is not a known tool's, so the folder is refused
	if r, out := postJSON(t, ts.URL+"/api/runs/import", map[string]any{"path": hog}); r.StatusCode < 400 {
		t.Fatalf("an unprofiled tool was imported: %d %s", r.StatusCode, out)
	}

	rn.Profiles = runstest.WithSynthetic(t)
	r, out := postJSON(t, ts.URL+"/api/runs/import", map[string]any{"path": hog})
	if r.StatusCode != 201 {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	var run struct {
		Kind, Profile string
		Outputs       []struct {
			Role, Product string
			DatasetID     int64
		}
	}
	json.Unmarshal(out, &run)
	if run.Kind != "hog-synthetic" || run.Profile != "HOG-synthetic@1" {
		t.Fatalf("kind %q profile %q", run.Kind, run.Profile)
	}
	want := map[string]string{"orientation_map": "orientation", "coherence": "reliability", "mask": "support"}
	got := map[string]int64{}
	for _, o := range run.Outputs {
		if want[o.Product] != o.Role {
			t.Errorf("product %s: role %s, want %s", o.Product, o.Role, want[o.Product])
		}
		got[o.Product] = o.DatasetID
	}
	if len(got) != len(want) {
		t.Fatalf("outputs: %s", out)
	}
	// nothing is declared that the profile does not declare
	_, pb := get(t, ts.URL+"/api/datasets/"+itoa(int(got["orientation_map"])))
	var d catalog.Dataset
	json.Unmarshal(pb, &d)
	if d.Interpretation != nil || d.VGT != nil || d.OrientationHeader != nil {
		t.Errorf("a profile with no orientation declares nothing: %+v", d.Interpretation)
	}
	// generic roles drive the brightness rule: orientation and reliability outputs cannot be given a brightness unit
	for _, product := range []string{"orientation_map", "coherence"} {
		if r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(got[product]))+"/unit", map[string]any{"unit": "Jy/beam"}); r.StatusCode != 409 {
			t.Errorf("%s: a unit was accepted: %d %s", product, r.StatusCode, b)
		}
	}

	// a VGT run: both header names, the same value
	rr, vb := postJSON(t, ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(root, "vgt_run_current")})
	if rr.StatusCode != 201 {
		t.Fatalf("vgt import: %d %s", rr.StatusCode, vb)
	}
	var vrun struct {
		Outputs []struct {
			Role      string
			DatasetID int64
		}
	}
	json.Unmarshal(vb, &vrun)
	for _, o := range vrun.Outputs {
		if o.Role != "orientation" {
			continue
		}
		_, db := get(t, ts.URL+"/api/datasets/"+itoa(int(o.DatasetID)))
		var raw map[string]json.RawMessage
		json.Unmarshal(db, &raw)
		if len(raw["orientationHeader"]) == 0 || string(raw["orientationHeader"]) == "null" || string(raw["orientationHeader"]) != string(raw["vgtHeader"]) {
			t.Errorf("orientationHeader and vgtHeader differ: %s vs %s", raw["orientationHeader"], raw["vgtHeader"])
		}
		return
	}
	t.Fatalf("no orientation output: %s", vb)
}
