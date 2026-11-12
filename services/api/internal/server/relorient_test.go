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
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Relative orientation (ADR 0047) through the API with the real worker: every side must be declared before it is compared, a
// catalog needs a fresh declaration with an angle, two maps need a common grid, and a result is a registered file with a derived
// record and a table to download.
func TestRelativeOrientationNeedsDeclarationsAndKeepsAResult(t *testing.T) {
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
	// the preset is recorded in the derived record: its identity, every value it supplied and the explicit parameters that replaced one
	var presetRec struct {
		Preset struct {
			Name       string             `json:"name"`
			Version    int                `json:"version"`
			Values     map[string]float64 `json:"values"`
			Overridden map[string]struct {
				Preset, Used float64
			} `json:"overridden"`
		} `json:"preset"`
	}
	json.Unmarshal(rec.Params, &presetRec)
	if pr := presetRec.Preset; pr.Name != "standard" || pr.Version != 1 || pr.Values["boot_samples"] != 2000 || pr.Values["sigma_floor_deg"] != 1 ||
		pr.Values["radius_arcmin"] != 10 || pr.Overridden["n_shifts"].Preset != 3000 || pr.Overridden["n_shifts"].Used != 200 || pr.Overridden["seed"].Used != 5 {
		t.Errorf("the preset in the derived record: %s", rec.Params)
	}
	// the presets are listed for the form, as the file the worker reads; a named preset is used, an unknown one refused
	const presetsRoute = "/api/analysis/relative-orientation/presets"
	st, pb := callJSON(t, "GET", ts.URL+presetsRoute, nil)
	if st != 200 {
		t.Fatalf("presets: %d %s", st, pb)
	}
	conforms(t, responseSchemaAt(t, doc, presetsRoute, "GET", 200, "application/json"), "presets", pb)
	var listed struct {
		Default string `json:"default"`
		Presets []struct {
			Name   string                    `json:"name"`
			Values map[string]map[string]any `json:"values"`
		} `json:"presets"`
	}
	json.Unmarshal(pb, &listed)
	if listed.Default != "standard" || len(listed.Presets) < 2 || listed.Presets[0].Name != "standard" || listed.Presets[1].Name != "lmc-star-comparison" ||
		listed.Presets[0].Values["radius_arcmin"]["value"] != 10.0 || listed.Presets[0].Values["radius_arcmin"]["reason"] == "" {
		t.Errorf("listed presets: %s", pb)
	}
	if st, o, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": map[string]any{"radiusArcmin": 1.5, "minShiftDeg": 0.05, "nShifts": 20, "preset": "lmc-star-comparison"}}); st != 201 ||
		o["summary"].(map[string]any)["preset"].(map[string]any)["name"] != "lmc-star-comparison" {
		t.Errorf("a named preset: %d %s", st, b)
	}
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": map[string]any{"preset": "nope"}}); st != 422 || !strings.Contains(b, "standard") {
		t.Errorf("an unknown preset: %d %s", st, b)
	}
	if st, _, b := run(map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "params": map[string]any{"preset": "../x"}}); st != 400 {
		t.Errorf("a malformed preset name: %d %s", st, b)
	}
	// the null Vesqira ran is recorded as how this control was built, verified, with the parameters the worker used
	var recParams struct {
		Controls *runs.Controls `json:"controls"`
	}
	json.Unmarshal(rec.Params, &recParams)
	if c := recParams.Controls; c == nil || c.Construction == nil || c.Smoothing != nil || c.Noise != nil {
		t.Fatalf("controls of a result whose null ran: %s", rec.Params)
	} else {
		k := c.Construction
		got := map[string]string{}
		for _, p := range k.Params {
			got[p.Key] = p.Value
		}
		if k.Basis != "verified" || k.By != "vesqira" || k.Kind != "other" || !strings.Contains(k.Note, "shifted source pattern") || got["seed"] != "5" || got["n_shifts"] != "200" || got["keep_fraction"] != "0.9" || got["shifts_kept"] == "" {
			t.Errorf("construction: %+v params %v", k, got)
		}
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
	// no shifts were asked for: the null did not run, so nothing is claimed about how it was built
	if rec, err := ds.Get(res.ID); err != nil || strings.Contains(string(rec.Params), "controls") {
		t.Fatalf("a skipped null must not be recorded as a construction: %+v %v", rec, err)
	}
	if st, b := callJSON(t, "GET", ts.URL+"/api/datasets/"+itoa(int(res.ID))+"/relative-orientation.csv", nil); st != 200 || !strings.HasPrefix(string(b), "x_fits,y_fits") {
		t.Fatalf("map table: %d %s", st, b)
	}

	// map against map, binned by a third map (ADR 0047, amendment): a scalar map on the same grid with a unit; the comparison without it is unchanged
	mk := exec.Command("python3", "-c", `
import sys, numpy as np
from astropy.io import fits
root = sys.argv[1]
h = fits.open(root + "/vgt_psi_deg.fits")[0].header
col = (np.arange(16, dtype=float).reshape(4, 4) + 1.0)
for name, unit in (("column.fits", "cm-2"), ("column_nounit.fits", None)):
    hh = h.copy()
    if unit:
        hh["BUNIT"] = unit
    fits.PrimaryHDU(col.astype("<f4"), header=hh).writeto(root + "/" + name, overwrite=True)
other = h.copy()
other["NAXIS1"] = other["NAXIS2"] = 7
other["CRPIX1"] = other["CRPIX2"] = 4.0
other["BUNIT"] = "cm-2"
fits.PrimaryHDU(np.ones((7, 7), dtype="<f4"), header=other).writeto(root + "/column_other_grid.fits", overwrite=True)
`, localRoot)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	column, noUnit, otherGrid := reg("column.fits"), reg("column_nounit.fits"), reg("column_other_grid.fits")
	pair := map[string]any{"datasetId": psiDeg.ID, "otherId": psiRad.ID}
	with := func(extra map[string]any, params map[string]any) map[string]any {
		b := map[string]any{"datasetId": pair["datasetId"], "otherId": pair["otherId"], "params": params}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for name, c := range map[string]struct {
		body map[string]any
		code int
		want string
	}{
		"no unit":         {with(map[string]any{"stratifyId": noUnit.ID}, map[string]any{"nShifts": 0}), 422, "no brightness unit"},
		"another grid":    {with(map[string]any{"stratifyId": otherGrid.ID}, map[string]any{"nShifts": 0}), 409, "align"},
		"orientation map": {with(map[string]any{"stratifyId": tang.ID}, map[string]any{"nShifts": 0}), 422, "orientation map"},
		"one of the two":  {with(map[string]any{"stratifyId": psiRad.ID}, map[string]any{"nShifts": 0}), 422, "another dataset"},
		"edges and bins":  {with(map[string]any{"stratifyId": column.ID}, map[string]any{"strataBins": 3, "strataEdges": []float64{0, 5}}), 400, "not both"},
		"edges order":     {with(map[string]any{"stratifyId": column.ID}, map[string]any{"strataEdges": []float64{5, 1}}), 400, "strictly increasing"},
		"bins range":      {with(map[string]any{"stratifyId": column.ID}, map[string]any{"strataBins": 51}), 400, "strataBins"},
		"no third map":    {with(nil, map[string]any{"strataBins": 3}), 400, "stratifyId"},
		"catalog":         {map[string]any{"datasetId": tang.ID, "otherId": stars.ID, "stratifyId": column.ID, "params": small}, 422, "two maps"},
	} {
		if st, _, b := run(c.body); st != c.code || !strings.Contains(b, c.want) {
			t.Errorf("stratified, %s: %d %s, want %d with %q", name, st, b, c.code, c.want)
		}
	}
	st, out, b = run(with(map[string]any{"stratifyId": column.ID}, map[string]any{"nShifts": 0, "strataBins": 3}))
	if st != 201 {
		t.Fatalf("stratified: %d %s", st, b)
	}
	sum = out["summary"].(map[string]any)
	strata := sum["stratified"].(map[string]any)
	bins := strata["bins"].([]any)
	if len(bins) != 3 || strata["third_map"].(map[string]any)["unit"] != "cm-2" || strata["counts"].(map[string]any)["in_bins"].(float64) != 15 {
		t.Fatalf("stratified summary: %s", b)
	}
	for _, x := range bins {
		if am := x.(map[string]any)["am"].(float64); math.Abs(am-1) > 1e-9 { // the same angles in degrees and radians agree in every bin
			t.Errorf("a bin of identical angles has AM %v", am)
		}
	}
	rb, _ = json.Marshal(out["dataset"])
	json.Unmarshal(rb, &res)
	if rec, err := ds.Get(res.ID); err != nil || len(rec.Sources) != 3 || rec.Sources[0] != psiDeg.ID || rec.Sources[1] != psiRad.ID || rec.Sources[2] != column.ID || !strings.Contains(string(rec.Params), "stratifiedBy") {
		t.Fatalf("the third map is a source after the two maps: %+v %v", rec, err)
	}
	// the same request without the third map has no `stratified` and the same AM
	st, out, b = run(with(nil, map[string]any{"nShifts": 0}))
	if st != 201 || out["summary"].(map[string]any)["stratified"] != nil {
		t.Fatalf("unchanged comparison: %d %s", st, b)
	}
	// user-set edges, with a unit declared by the author for a file that has none
	if st, b := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(noUnit.ID))+"/unit", map[string]any{"unit": "1e20 cm-2"}); st != 200 {
		t.Fatalf("declare the unit: %d %s", st, b)
	}
	st, out, b = run(with(map[string]any{"stratifyId": noUnit.ID}, map[string]any{"nShifts": 0, "strataEdges": []float64{0, 8, 20}}))
	if st != 201 {
		t.Fatalf("stratified by edges with a declared unit: %d %s", st, b)
	}
	strata = out["summary"].(map[string]any)["stratified"].(map[string]any)
	if u := strata["third_map"].(map[string]any); u["unit"] != "1e20 cm-2" || u["unit_source"] != "user" || strata["counts"].(map[string]any)["outside_edges"].(float64) != 0 {
		t.Fatalf("a declared unit is labelled as the author's: %s", b)
	}
}

func TestRelorientConstructionIsVerifiedOnlyWhenTheNullRan(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	ran := json.RawMessage(`{"parameters":{"n_shifts":1000,"min_shift_deg":1.0,"keep_fraction":0.9,"seed":20261020,"bins":18},"null":{"method":"shifted map: the second map moved by whole pixels with wrap, values kept","skipped":null,"kept":987}}`)
	k := relorientConstruction(false, ran, now)
	if k == nil || k.Kind != "shifted_map_null" || k.Basis != runs.BasisVerified || k.By != runs.ByVesqira || k.At != "2026-11-11T12:00:00Z" {
		t.Fatalf("map null: %+v", k)
	}
	if fmt.Sprint(k.Params) != "[{n_shifts 1000} {min_shift_deg 1.0} {keep_fraction 0.9} {seed 20261020} {shifts_kept 987}]" {
		t.Errorf("params: %v", k.Params)
	}
	if c := relorientConstruction(true, ran, now); c == nil || c.Kind != "other" {
		t.Errorf("a catalog null moves the pattern, not a map: %+v", c)
	}
	for name, raw := range map[string]string{
		"skipped": `{"parameters":{"n_shifts":0},"null":{"method":"shifted map","skipped":"no shifts were asked for"}}`,
		"no null": `{"parameters":{"n_shifts":10}}`,
		"no text": `{"null":{"skipped":null}}`,
		"garbage": `[1,2`,
	} {
		if c := relorientConstruction(false, json.RawMessage(raw), now); c != nil {
			t.Errorf("%s: a construction was claimed: %+v", name, c)
		}
	}
}
