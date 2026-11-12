package server

import (
	"encoding/json"
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

// A map declared as an axial orientation map is never averaged as a scalar: its frame and quantity are validated and
// stored, align resamples the doubled angle and registers the companion reliability map, and moment and slab are refused.
func TestAxialMapsAreDeclaredWithFrameAndQuantityAndResampledAsAngles(t *testing.T) {
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
	psi, rel, coarse := reg("vgt_psi_deg.fits"), reg("vgt_reliability.fits"), reg("hi_cube_coarser.fits")
	declare := func(body map[string]any) (int, catalog.Dataset, string) {
		r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(psi.ID))+"/interpretation", body)
		var d catalog.Dataset
		json.Unmarshal(b, &d)
		return r.StatusCode, d, string(b)
	}
	analyze := func(body map[string]any) (int, string) {
		r, b := postJSON(t, ts.URL+"/api/analysis", body)
		return r.StatusCode, string(b)
	}

	// nothing is declared until someone says it: the file has no header key for either
	if psi.Interpretation != nil {
		t.Fatalf("a fresh dataset has no interpretation: %+v", psi.Interpretation)
	}
	// both fields are checked on the server
	for name, body := range map[string]map[string]any{
		"unknown frame":    {"kind": "axial_orientation", "unit": "deg", "frame": "galactic"},
		"empty frame":      {"kind": "axial_orientation", "unit": "deg", "frame": ""},
		"unknown quantity": {"kind": "axial_orientation", "unit": "deg", "quantity": "polarization"},
	} {
		if code, _, b := declare(body); code != 400 {
			t.Errorf("%s: %d %s, want 400", name, code, b)
		}
	}
	// without a declared frame the angle cannot be carried to another grid: refused, naming the frame
	if code, _, b := declare(map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel.ID}); code != 200 {
		t.Fatalf("declare: %d %s", code, b)
	}
	if code, b := analyze(map[string]any{"op": "align", "datasetId": psi.ID, "referenceId": coarse.ID}); code != 422 || !strings.Contains(b, "frame") {
		t.Errorf("align with an undeclared frame: %d %s", code, b)
	}
	// moment and slab would average the angles
	for _, op := range []string{"moment", "slab"} {
		if code, b := analyze(map[string]any{"op": op, "datasetId": psi.ID}); code != 422 || !strings.Contains(b, "axial") {
			t.Errorf("%s of an axial map: %d %s", op, code, b)
		}
	}

	code, d, b := declare(map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel.ID, "frame": "pixel", "quantity": "gradient"})
	if code != 200 || d.Interpretation == nil || d.Interpretation.Frame == nil || *d.Interpretation.Frame != "pixel" ||
		d.Interpretation.Quantity == nil || *d.Interpretation.Quantity != "gradient" || d.Interpretation.DeclaredBy != "user" {
		t.Fatalf("declare with frame and quantity: %d %s", code, b)
	}
	// 'unspecified' is a declaration too: it says the person looked and does not know
	if code, d, b := declare(map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel.ID, "frame": "pixel", "quantity": "unspecified"}); code != 200 || *d.Interpretation.Quantity != "unspecified" {
		t.Fatalf("unspecified: %d %s", code, b)
	}
	declare(map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel.ID, "frame": "pixel", "quantity": "gradient"})

	// display downsampling: a block mean of angles would average 1 and 179 degrees to 90, so an axial map is always sent whole
	if r, b := get(t, ts.URL+"/api/datasets/"+itoa(int(psi.ID))+"/plane?channel=0&step=2"); r.StatusCode != 400 || !strings.Contains(string(b), "orientation map") {
		t.Errorf("a reduced preview of an axial map: %d %s, want 400", r.StatusCode, b)
	}

	code, ab := analyze(map[string]any{"op": "align", "datasetId": psi.ID, "referenceId": coarse.ID})
	if code != 201 {
		t.Fatalf("axial align: %d %s", code, ab)
	}
	var res struct {
		Dataset catalog.Dataset
		Summary struct {
			Policy          string `json:"policy"`
			Axial           bool   `json:"axial"`
			ReliabilityFile bool   `json:"reliability_file"`
		}
	}
	json.Unmarshal([]byte(ab), &res)
	in := res.Dataset.Interpretation
	if !res.Summary.Axial || !res.Summary.ReliabilityFile || !strings.Contains(res.Summary.Policy, "Doubled-angle") {
		t.Errorf("summary: %s", ab)
	}
	// the result keeps what its input declared, labelled as the operation's proposal, with the companion as its reliability
	if in == nil || in.Unit != "deg" || in.DeclaredBy != analysisAuthor || in.Frame == nil || *in.Frame != "pixel" || in.Quantity == nil || *in.Quantity != "gradient" || in.ReliabilityID == nil {
		t.Fatalf("result interpretation: %+v", in)
	}
	relDS, err := cat.Get(workspace.Local, *in.ReliabilityID)
	if err != nil || !strings.HasPrefix(relDS.Path, derivedDir) {
		t.Fatalf("companion reliability dataset: %v %+v", err, relDS)
	}
	if rec, err := ds.Get(relDS.ID); err != nil || rec.Op != "align" || len(rec.Sources) != 2 {
		t.Errorf("companion record: %v %+v", err, rec)
	}
	if rec, err := ds.Get(res.Dataset.ID); err != nil || !strings.Contains(string(rec.Summary), "Doubled-angle") {
		t.Errorf("the result's record must state the doubled-angle method: %v", err)
	}
}
