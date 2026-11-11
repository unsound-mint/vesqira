package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

// Relative orientation (ADR 0047) through the API with the real worker: every side must be declared before it is compared, a
// catalog needs a fresh declaration with an angle, two maps need a common grid, and a result is a registered file with a derived
// record and a table to download.
func TestRelativeOrientationNeedsDeclarationsAndKeepsAResult(t *testing.T) {
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
	// Four stars around the centre of the tangential field (33 x 33 pixels of 0.01 deg at RA 150, Dec 2, north up, east left):
	// right and left of the centre the field runs north-south (position angle 0), above and below it runs east-west (90). Each
	// star's angle is that field direction, so they agree with the map by construction.
	dra := 0.08 / math.Cos(2*math.Pi/180)
	rows := "ra,dec,pa,spa\n" + fmt.Sprintf("%.8f,2.0,0,3\n%.8f,2.0,0,3\n150.0,2.08,90,3\n150.0,1.92,90,3\n,,,\n", 150-dra, 150+dra)
	if err := os.WriteFile(filepath.Join(localRoot, "stars.csv"), []byte(rows), 0o644); err != nil {
		t.Fatal(err)
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
	tang, psiDeg, psiRad := reg("vgt_tangential_deg.fits"), reg("vgt_psi_deg.fits"), reg("vgt_psi_rad.fits")
	stars := reg("stars.csv")
	if stars.Kind != catalog.KindCatalog {
		t.Fatalf("stars.csv registered as %q", stars.Kind)
	}
	declare := func(d catalog.Dataset, body map[string]any) {
		t.Helper()
		if st, b := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/interpretation", body); st != 200 {
			t.Fatalf("declare %s: %d %s", d.Name, st, b)
		}
	}
	doc := loadSpec(t)
	const route = "/api/analysis/relative-orientation"
	run := func(body map[string]any) (int, map[string]any, string) {
		st, b := callJSON(t, "POST", ts.URL+route, body)
		var out map[string]any
		json.Unmarshal(b, &out)
		if st == 201 { // the reply, and a request that worked, are what the spec says
			conforms(t, responseSchemaAt(t, doc, route, "POST", 201, "application/json"), "relative orientation", b)
			conforms(t, requestSchema(t, doc, route, "POST"), "relative orientation request", mustJSON(t, body))
		} else if st >= 400 {
			conforms(t, responseSchemaAt(t, doc, route, "POST", st, "application/json"), "relative orientation error", b)
		}
		return st, out, string(b)
	}
	small := map[string]any{"radiusArcmin": 1.5, "minShiftDeg": 0.05, "nShifts": 200, "seed": 5}

	// nothing is compared before both sides say what their angles are
	declare(tang, map[string]any{"kind": "axial_orientation", "unit": "deg"})
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": small}); st != 422 || !strings.Contains(b, "frame") || !strings.Contains(b, "what the angle is") {
		t.Fatalf("an undeclared frame and quantity: %d %s", st, b)
	}
	declare(tang, map[string]any{"kind": "axial_orientation", "unit": "deg", "frame": "pixel", "quantity": "unspecified"})
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": small}); st != 422 || !strings.Contains(b, "unspecified") {
		t.Fatalf("an unspecified quantity: %d %s", st, b)
	}
	declare(tang, map[string]any{"kind": "axial_orientation", "unit": "deg", "frame": "pixel", "quantity": "b_field"})
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": small}); st != 409 || !strings.Contains(b, "no columns are declared") {
		t.Fatalf("a catalog without a declaration: %d %s", st, b)
	}
	decl := map[string]any{"lon": "ra", "lat": "dec", "format": "degrees", "frame": "icrs", "equinox": nil, "values": []string{}, "angle": nil}
	if st, b := callJSON(t, "PUT", ts.URL+"/api/datasets/"+itoa(int(stars.ID))+"/catalog/declaration", decl); st != 200 {
		t.Fatalf("declare the catalog: %d %s", st, b)
	}
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": small}); st != 422 || !strings.Contains(b, "angle") {
		t.Fatalf("a catalog without an angle: %d %s", st, b)
	}
	decl["angle"] = map[string]any{"column": "pa", "unit": "deg", "frame": "sky", "quantity": "starlight_polarization", "sigmaColumn": "spa"}
	if st, b := callJSON(t, "PUT", ts.URL+"/api/datasets/"+itoa(int(stars.ID))+"/catalog/declaration", decl); st != 200 {
		t.Fatalf("declare the catalog angle: %d %s", st, b)
	}
	for name, p := range map[string]map[string]any{"keep fraction": {"keepFraction": 0.0}, "bins": {"bins": 1}, "unknown": {"radius": 3}} {
		if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": p}); st != 400 {
			t.Errorf("%s: %d %s, want 400", name, st, b)
		}
	}
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": map[string]any{"radiusArcmin": 1.5}}); st != 422 || !strings.Contains(b, "minimum shift") {
		t.Fatalf("the default 1 degree shift on a 0.33 degree map: %d %s", st, b)
	}

	// map against catalog
	st, out, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": small})
	if st != 201 {
		t.Fatalf("relative orientation: %d %s", st, b)
	}
	sum := out["summary"].(map[string]any)
	if sum["label"] != "agreement statistic" || sum["am"].(float64) < 0.99 {
		t.Fatalf("summary: %s", b)
	}
	counts := sum["counts"].(map[string]any)
	if counts["used"].(float64) != 4 || counts["rows"].(float64) != 5 {
		t.Fatalf("counts: %v", counts)
	}
	if p := sum["parameters"].(map[string]any); p["seed"].(float64) != 5 || p["n_shifts"].(float64) != 200 || p["keep_fraction"].(float64) != 0.9 {
		t.Fatalf("the parameters as used, with defaults stated: %v", p)
	}
	var res catalog.Dataset
	rb, _ := json.Marshal(out["dataset"])
	json.Unmarshal(rb, &res)
	if res.Kind != catalog.KindCatalog || !strings.HasPrefix(res.Name, "relorient-") {
		t.Fatalf("the result table is registered as a catalog: %+v", res)
	}
	rec, err := ds.Get(res.ID)
	if err != nil || rec.Op != "relorient_catalog" || len(rec.Sources) != 2 || rec.Sources[0] != tang.ID || rec.Sources[1] != stars.ID {
		t.Fatalf("derived record: %+v %v", rec, err)
	}
	resp, err := http.Get(ts.URL + "/api/datasets/" + itoa(int(res.ID)) + "/relative-orientation.csv")
	if err != nil {
		t.Fatal(err)
	}
	csv, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("table: %d %v", resp.StatusCode, resp.Header)
	}
	if lines := strings.Split(strings.TrimSpace(string(csv)), "\n"); len(lines) != 6 || !strings.HasPrefix(lines[0], "row,lon_deg,lat_deg") || !strings.Contains(lines[5], "position missing") {
		t.Fatalf("table rows: %q", csv)
	}
	if st, _ := callJSON(t, "GET", ts.URL+"/api/datasets/"+itoa(int(tang.ID))+"/relative-orientation.csv", nil); st != 404 {
		t.Fatalf("a dataset that is not a result has no table: %d", st)
	}

	// map against map: a common grid is required
	declare(psiDeg, map[string]any{"kind": "axial_orientation", "unit": "deg", "frame": "pixel", "quantity": "b_field"})
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": psiDeg.ID, "params": map[string]any{"nShifts": 0}}); st != 409 || !strings.Contains(b, "align") {
		t.Fatalf("different grids: %d %s", st, b)
	}
	if st, _, b := run(map[string]any{"datasetId": psiDeg.ID, "otherId": psiRad.ID, "params": map[string]any{"nShifts": 0}}); st != 422 || !strings.Contains(b, "second map") {
		t.Fatalf("an undeclared second map: %d %s", st, b)
	}
	declare(psiRad, map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "pixel", "quantity": "b_field"})
	if st, _, b := run(map[string]any{"datasetId": psiDeg.ID, "otherId": psiRad.ID, "params": map[string]any{"radiusArcmin": 2}}); st != 400 {
		t.Fatalf("a radius for two maps: %d %s", st, b)
	}
	st, out, b = run(map[string]any{"datasetId": psiDeg.ID, "otherId": psiRad.ID, "params": map[string]any{"nShifts": 0}})
	if st != 201 {
		t.Fatalf("maps: %d %s", st, b)
	}
	sum = out["summary"].(map[string]any)
	if math.Abs(sum["am"].(float64)-1) > 1e-9 || sum["prs"].(map[string]any)["V"] == nil || sum["counts"].(map[string]any)["used"].(float64) != 15 {
		t.Fatalf("the same angles in degrees and radians agree exactly: %s", b)
	}
	rb, _ = json.Marshal(out["dataset"])
	json.Unmarshal(rb, &res)
	if res.Kind == catalog.KindCatalog || res.Interpretation != nil {
		t.Fatalf("the |Δψ| map is an image with no orientation declaration: %+v", res)
	}
	if st, b := callJSON(t, "GET", ts.URL+"/api/datasets/"+itoa(int(res.ID))+"/relative-orientation.csv", nil); st != 200 || !strings.HasPrefix(string(b), "x_fits,y_fits") {
		t.Fatalf("map table: %d %s", st, b)
	}
}
