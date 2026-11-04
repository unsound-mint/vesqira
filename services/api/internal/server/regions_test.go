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
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

func TestSavedRegionsCreateEditDeleteAndExportAsDS9(t *testing.T) {
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rg, _ := regions.Open(cat.DB())
	d, err := cat.Upsert(catalog.Dataset{Path: "/data/hi cube.fits", Name: "hi cube.fits", SizeBytes: 1, ModifiedAt: time.Now(), Header: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: cat, Regions: rg}).Handler())
	defer ts.Close()
	base := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/regions"

	r, b := postJSON(t, base, map[string]any{"name": "Core", "shape": map[string]any{"shape": "rect", "x0": 2, "y0": 3, "x1": 9, "y1": 7}})
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %s", r.StatusCode, b)
	}
	var reg regions.Region
	json.Unmarshal(b, &reg)
	if r, _ := postJSON(t, base, map[string]any{"name": "Bad", "shape": map[string]any{"shape": "rect", "x0": 9, "y0": 0, "x1": 2, "y1": 0}}); r.StatusCode != 422 {
		t.Errorf("an inverted rectangle: %d, want 422", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/datasets/999/regions", map[string]any{"name": "x", "shape": map[string]any{"shape": "circle", "cx": 1, "cy": 1, "r": 1}}); r.StatusCode != 404 {
		t.Errorf("a region on an unknown dataset: %d, want 404", r.StatusCode)
	}
	// rename, reshape, duplicate (a create with the same shape) and delete
	if r, rb := postJSON(t, ts.URL+"/api/regions/"+itoa(int(reg.ID)), map[string]any{"name": "Core (wide)", "shape": map[string]any{"shape": "rect", "x0": 0, "y0": 0, "x1": 20, "y1": 20}}); r.StatusCode != 200 || !strings.Contains(string(rb), "Core (wide)") || !strings.Contains(string(rb), `"x1":20`) {
		t.Errorf("edit: %d %s", r.StatusCode, rb)
	}
	if r, _ := postJSON(t, ts.URL+"/api/regions/9999", map[string]any{"name": "x"}); r.StatusCode != 404 {
		t.Errorf("editing an unknown region: %d", r.StatusCode)
	}
	postJSON(t, base, map[string]any{"name": "Peak", "shape": map[string]any{"shape": "circle", "cx": 10, "cy": 11, "r": 4}})
	if _, lb := get(t, base); strings.Count(string(lb), `"datasetId"`) != 2 {
		t.Errorf("list: %s", lb)
	}
	resp, ds9 := get(t, ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/regions.reg")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="hi_cube.reg"`) ||
		!strings.Contains(string(ds9), "box(11,11,21,21,0) # text={Core (wide)}") || !strings.Contains(string(ds9), "circle(11,12,4)") {
		t.Errorf("DS9: %d %v\n%s", resp.StatusCode, resp.Header, ds9)
	}
	if _, one := get(t, ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/regions.reg?id="+itoa(int(reg.ID))); strings.Contains(string(one), "circle(") {
		t.Errorf("one region only: %s", one)
	}
	// import: a preview changes nothing; applying saves the exact regions and leaves the approximated ones unless asked
	file := "image\nbox(5,5,3,3,0) # text={Imp}\ncircle(8.4,8,2.5)\nellipse(1,1,2,2,0)\n"
	imp := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/regions/import"
	before, _ := get(t, base)
	_ = before
	if r, pb := postJSON(t, imp, map[string]any{"text": file}); r.StatusCode != 200 || !strings.Contains(string(pb), `"status":"approximated"`) || !strings.Contains(string(pb), `"status":"skipped"`) {
		t.Errorf("preview: %d %s", r.StatusCode, pb)
	}
	_, l1 := get(t, base)
	if strings.Count(string(l1), `"datasetId"`) != 2 {
		t.Errorf("a preview must not save anything: %s", l1)
	}
	if r, ab := postJSON(t, imp, map[string]any{"text": file, "apply": true}); r.StatusCode != 201 || strings.Count(string(ab), `"datasetId"`) != 1 {
		t.Errorf("apply: %d %s", r.StatusCode, ab)
	}
	if r, ab := postJSON(t, imp, map[string]any{"text": file, "apply": true, "includeApproximated": true}); r.StatusCode != 201 || strings.Count(string(ab), `"datasetId"`) != 2 {
		t.Errorf("apply with approximated: %d %s", r.StatusCode, ab)
	}
	if r, _ := postJSON(t, ts.URL+"/api/datasets/999/regions/import", map[string]any{"text": file}); r.StatusCode != 404 {
		t.Errorf("import into an unknown dataset: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/regions/"+itoa(int(reg.ID))+"/delete", map[string]any{}); r.StatusCode != 200 {
		t.Errorf("delete: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/regions/"+itoa(int(reg.ID))+"/delete", map[string]any{}); r.StatusCode != 404 {
		t.Errorf("deleting twice: %d", r.StatusCode)
	}
}

func TestDS9SkyRegionsAreWorkedOutWithTheDatasetsOwnWCS(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	hdr := worker.Runner{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/fits_header.py")}, Timeout: 20 * time.Second}
	base := t.TempDir()
	root := filepath.Join(base, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rg, _ := regions.Open(st.DB())
	ts := httptest.NewServer((&Server{Roots: rs, Store: st, Worker: hdr, Planes: pw, Regions: rg}).Handler())
	t.Cleanup(ts.Close)
	var d catalog.Dataset
	resp := post(t, ts, filepath.Join(root, "hi_cube.fits"), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&d)
	imp := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/regions/import"
	// the reference position is pixel (16, 12) in FITS counting, which is (15, 11) here; pixels are 36 arcsec
	file := "fk5\ncircle(150.0,2.0,72\")\nbox(150.0,2.0,360\",180\")\ngalactic\ncircle(150.0,2.0,72\")\n"
	r, pb := postJSON(t, imp, map[string]any{"text": file})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, pb)
	}
	var out struct {
		Items []regions.ImportItem `json:"items"`
	}
	json.Unmarshal(pb, &out)
	if len(out.Items) != 3 {
		t.Fatalf("%s", pb)
	}
	c, b, g := out.Items[0], out.Items[1], out.Items[2]
	if c.Status != regions.Approximate || c.Shape == nil || *c.Shape != (regions.Shape{Shape: "circle", CX: 15, CY: 11, R: 2}) {
		t.Errorf("sky circle: %+v", c)
	}
	if b.Status != regions.Approximate || b.Shape == nil || *b.Shape != (regions.Shape{Shape: "rect", X0: 10, Y0: 9, X1: 20, Y1: 13}) { // 10 x 5 pixels around (15, 11)
		t.Errorf("sky box: %+v", b)
	}
	if g.Status != regions.Skipped || !strings.Contains(g.Note, "no frame transformation") {
		t.Errorf("a galactic region on an equatorial map: %+v", g)
	}
	r, ab := postJSON(t, imp, map[string]any{"text": file, "apply": true, "includeApproximated": true})
	if r.StatusCode != 201 || strings.Count(string(ab), `"datasetId"`) != 2 {
		t.Errorf("apply: %d %s", r.StatusCode, ab)
	}
}
