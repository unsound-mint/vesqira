package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The Go-to-worker hand-off of a header-declared sky frame: a psi stated in the Galactic frame on an ICRS grid reaches the relative
// orientation worker with its frame, and the result differs from reading the same angles as east of north in the grid's own frame.
func TestRelativeOrientationCarriesAGalacticAngleStatedByTheFileHeader(t *testing.T) {
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
	localRoot, derivedDir := filepath.Join(base, "local"), filepath.Join(base, "derived")
	for _, d := range []string{localRoot, derivedDir} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), localRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cat, _ := catalog.Open("file:" + filepath.Join(base, "t.db"))
	defer cat.Close()
	ds, _ := derived.Open(cat.DB())
	rs, err := sourceroot.New([]string{localRoot, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: pw, Cache: NewPlaneCache(8 << 20), Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	defer ts.Close()
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(localRoot, name), nil).Body).Decode(&d)
		return d
	}
	gal, ref := reg("vgt_psi_galactic_frame.fits"), reg("vgt_psi_rad.fits")
	url := func(d catalog.Dataset) string { return ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/interpretation" }
	if st, b := callJSON(t, "POST", url(ref), map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "pixel", "quantity": "b_field"}); st != 200 {
		t.Fatalf("declare the reference: %d %s", st, b)
	}
	if st, b := callJSON(t, "POST", url(gal), map[string]any{"kind": "axial_orientation", "fromFile": true}); st != 200 {
		t.Fatalf("adopt the header: %d %s", st, b)
	}
	run := func() (int, string) {
		st, b := callJSON(t, "POST", ts.URL+"/api/analysis/relative-orientation", map[string]any{"datasetId": gal.ID, "otherId": ref.ID, "params": map[string]any{"nShifts": 0}})
		return st, string(b)
	}
	st, fromHeader := run()
	if st != 201 || !strings.Contains(fromHeader, `"angle_stated_in":"galactic"`) {
		t.Fatalf("the Galactic frame must reach the worker: %d %s", st, fromHeader)
	}
	// the same angles read as east of north in the grid's own frame (a person's "sky") are a different comparison
	if st, b := callJSON(t, "POST", url(gal), map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "sky", "quantity": "b_field"}); st != 200 {
		t.Fatalf("declare sky: %d %s", st, b)
	}
	st, own := run()
	if st != 201 || strings.Contains(own, `"angle_stated_in":"galactic"`) || !strings.Contains(own, "grid's own celestial frame") {
		t.Fatalf("a person's sky is the grid's own frame: %d %s", st, own)
	}
	am := func(s string) float64 {
		var o struct {
			Summary struct {
				Am float64 `json:"am"`
			} `json:"summary"`
		}
		json.Unmarshal([]byte(s), &o)
		return o.Summary.Am
	}
	if am(fromHeader) == am(own) {
		t.Errorf("carrying the angle from the Galactic frame must change the comparison: both %v", am(own))
	}
}
