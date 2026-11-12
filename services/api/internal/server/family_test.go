package server

import (
	"encoding/json"
	"math"
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
)

// Spectral smoothing is a derived operation with its kernel in the record, a declared orientation map is refused it, and a family of
// maps is compared only when they share a grid and are all scalar or all axial (ADR 0069).
func TestSpectralSmoothingAndTheFamilyAgreement(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, scipy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and scipy is required")
		}
		t.Skip("python3 with astropy and scipy not available")
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
	local := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: local, Cache: NewPlaneCache(8 << 20), Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	defer ts.Close()
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(localRoot, name), nil).Body).Decode(&d)
		return d
	}
	cube, coarse, psiDeg, psiRad := reg("hi_cube.fits"), reg("hi_cube_coarser.fits"), reg("vgt_psi_deg.fits"), reg("vgt_psi_rad.fits")
	analyze := func(body map[string]any) (int, string) {
		r, b := postJSON(t, ts.URL+"/api/analysis", body)
		return r.StatusCode, string(b)
	}
	family := func(body map[string]any) (int, string) {
		r, b := postJSON(t, ts.URL+"/api/analysis/family", body)
		return r.StatusCode, string(b)
	}

	// smoothing: the kernel and the policy travel with the result's record
	smooth := func(params map[string]string) (catalog.Dataset, string) {
		code, b := analyze(map[string]any{"op": "smooth_spectral", "datasetId": cube.ID, "params": params})
		if code != 201 {
			t.Fatalf("smooth %v: %d %s", params, code, b)
		}
		var res struct {
			Dataset catalog.Dataset
			Summary struct {
				Kernel struct {
					Kind    string    `json:"kind"`
					Weights []float64 `json:"weights"`
				} `json:"kernel"`
				Spectral struct {
					Calibrated bool `json:"calibrated"`
				} `json:"spectral"`
			}
		}
		json.Unmarshal([]byte(b), &res)
		sum := 0.0
		for _, w := range res.Summary.Kernel.Weights {
			sum += w
		}
		if math.Abs(sum-1) > 1e-9 || !res.Summary.Spectral.Calibrated {
			t.Errorf("kernel weights %v or calibration: %s", res.Summary.Kernel.Weights, b)
		}
		return res.Dataset, b
	}
	s1, b1 := smooth(map[string]string{"kernel": "boxcar", "width": "5"})
	s2, _ := smooth(map[string]string{"kernel": "gaussian", "width": "6", "nativeFwhm": "2"})
	if !strings.Contains(b1, "boxcar") || s1.ID == cube.ID {
		t.Errorf("a smoothing is a new dataset, never in place: %s", b1)
	}
	rec, err := ds.Get(s1.ID)
	if err != nil || rec.Op != "smooth_spectral" || len(rec.Sources) != 1 || rec.Sources[0] != cube.ID || !strings.Contains(string(rec.Params), `"width":"5"`) {
		t.Errorf("record: %v %+v %s", err, rec, rec.Params)
	}
	if code, b := analyze(map[string]any{"op": "smooth_spectral", "datasetId": cube.ID, "params": map[string]string{"kernel": "boxcar", "width": "4"}}); code != 422 || !strings.Contains(b, "odd") {
		t.Errorf("an even boxcar: %d %s", code, b)
	}
	if code, b := analyze(map[string]any{"op": "smooth_spectral", "datasetId": cube.ID, "params": map[string]string{"kernel": "lorentz", "width": "3"}}); code != 422 {
		t.Errorf("an unknown kernel: %d %s", code, b)
	}
	// a map declared axial would have its angles averaged
	if r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(psiDeg.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "deg"}); r.StatusCode != 200 {
		t.Fatalf("declare: %d %s", r.StatusCode, b)
	}
	if code, b := analyze(map[string]any{"op": "smooth_spectral", "datasetId": psiDeg.ID, "params": map[string]string{"kernel": "boxcar", "width": "3"}}); code != 422 || !strings.Contains(b, "axial") {
		t.Errorf("smoothing an orientation map: %d %s", code, b)
	}

	// a family of cubes that differ in one smoothing parameter, at one channel, on one grid
	code, fb := family(map[string]any{"datasetIds": []int64{cube.ID, s1.ID, s2.ID}, "referenceId": cube.ID, "channel": 40, "spread": "mad"})
	if code != 201 {
		t.Fatalf("family: %d %s", code, fb)
	}
	var fr struct {
		Maps []struct {
			Role    string
			Member  int
			Dataset catalog.Dataset
		}
		Summary struct {
			Axial   bool `json:"axial"`
			Spread  string
			Members []struct {
				Reference bool `json:"reference"`
				Compared  int  `json:"pixels_compared"`
			}
		}
	}
	json.Unmarshal([]byte(fb), &fr)
	if len(fr.Maps) != 3 || fr.Maps[0].Role != "spread" || fr.Summary.Spread != "mad" || fr.Summary.Axial || len(fr.Summary.Members) != 3 || !fr.Summary.Members[0].Reference || fr.Summary.Members[1].Compared == 0 {
		t.Errorf("family result: %s", fb)
	}
	if r, err := ds.Get(fr.Maps[1].Dataset.ID); err != nil || r.Op != "family_agreement" || len(r.Sources) != 3 {
		t.Errorf("family record: %v %+v", err, r)
	}
	// a cube without a channel is refused by the worker's sentence, not computed
	if code, b := family(map[string]any{"datasetIds": []int64{cube.ID, s1.ID}}); code != 422 || !strings.Contains(b, "channel") {
		t.Errorf("a cube with no channel: %d %s", code, b)
	}
	// maps that do not share a grid are refused with reasons; nothing is resampled
	if code, b := family(map[string]any{"datasetIds": []int64{cube.ID, coarse.ID}, "channel": 3}); code != 409 || !strings.Contains(b, "share a grid") {
		t.Errorf("different grids: %d %s", code, b)
	}
	if code, b := family(map[string]any{"datasetIds": []int64{cube.ID}}); code != 422 {
		t.Errorf("one map: %d %s", code, b)
	}
	if code, b := family(map[string]any{"datasetIds": []int64{cube.ID, cube.ID}}); code != 422 {
		t.Errorf("a map twice: %d %s", code, b)
	}
	// scalar and axial maps do not mix
	if code, b := family(map[string]any{"datasetIds": []int64{cube.ID, psiDeg.ID}, "channel": 0}); code != 422 || !strings.Contains(b, "mix") {
		t.Errorf("mixed kinds: %d %s", code, b)
	}

	// axial maps: the same angles in degrees and radians agree exactly (AM 1, no plain difference)
	if r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(psiRad.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "rad"}); r.StatusCode != 200 {
		t.Fatalf("declare rad: %d %s", r.StatusCode, b)
	}
	code, ab := family(map[string]any{"datasetIds": []int64{psiDeg.ID, psiRad.ID}})
	if code != 201 {
		t.Fatalf("axial family: %d %s", code, ab)
	}
	var ar struct {
		Summary struct {
			Axial   bool `json:"axial"`
			Unit    string
			Policy  string
			Members []struct {
				AM   *float64 `json:"am"`
				Mean *float64 `json:"mean_abs_deg"`
			}
		}
	}
	json.Unmarshal([]byte(ab), &ar)
	m := ar.Summary.Members
	if !ar.Summary.Axial || ar.Summary.Unit != "deg" || len(m) != 2 || m[1].AM == nil || math.Abs(*m[1].AM-1) > 1e-4 || m[1].Mean == nil || *m[1].Mean > 0.01 || !strings.Contains(ar.Summary.Policy, "no plain difference") {
		t.Errorf("axial family: %s", ab)
	}
}
