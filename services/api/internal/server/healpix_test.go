package server

import (
	"encoding/json"
	"io"
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

// healpixServer is a server with the real worker over freshly generated fixtures (fixtures/generate.py).
func healpixServer(t *testing.T) (*httptest.Server, string, *derived.Store) {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, astropy_healpix").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and astropy_healpix is required")
		}
		t.Skip("python3 with astropy and astropy_healpix not available")
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
	t.Cleanup(func() { cat.Close() })
	ds, _ := derived.Open(cat.DB())
	rs, err := sourceroot.New([]string{localRoot, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: pw, Cache: NewPlaneCache(8 << 20), Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	t.Cleanup(ts.Close)
	return ts, localRoot, ds
}

// A Planck HEALPix map registers as its own kind, a map that does not state its convention or coordinate system is refused with a
// sentence, and every route that needs a pixel grid refuses it.
func TestHealpixMapRegistersAsItsOwnKindAndIsRefusedWhereAGridIsNeeded(t *testing.T) {
	t.Parallel()
	ts, root, _ := healpixServer(t)
	resp := post(t, ts, filepath.Join(root, "planck_353_cosmo.fits"), nil)
	var d catalog.Dataset
	json.NewDecoder(resp.Body).Decode(&d)
	if resp.StatusCode != 201 || d.Kind != catalog.KindHealpix {
		t.Fatalf("registered as %d %q", resp.StatusCode, d.Kind)
	}
	h, ok := catalog.ParseHealpixHeader(d.Header)
	if !ok || h.Nside != 16 || h.Ordering != "RING" || h.Polcconv != "COSMO" || h.Frame != "galactic" || !h.HasQUCov {
		t.Fatalf("header %+v", h)
	}
	for name, word := range map[string]string{"planck_353_no_polcconv.fits": "POLCCONV", "planck_353_no_coordsys.fits": "COORDSYS"} {
		resp := post(t, ts, filepath.Join(root, name), nil)
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 422 || !strings.Contains(string(b), word) || !strings.Contains(string(b), "not registered") || strings.Contains(string(b), "cannot read") {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	id := itoa(int(d.ID))
	// the dataset reply with a HEALPix header is what the spec says (the header's oneOf gained HealpixHeader)
	doc := loadSpec(t)
	if st, b := callJSON(t, "GET", ts.URL+"/api/datasets/"+id, nil); st != 200 {
		t.Fatalf("get: %d %s", st, b)
	} else {
		conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}", "GET", 200, "application/json"), "healpix dataset", b)
	}
	img := post(t, ts, filepath.Join(root, "vgt_psi_rad.fits"), nil)
	var im catalog.Dataset
	json.NewDecoder(img.Body).Decode(&im)
	imID := itoa(int(im.ID))
	contours := map[string]any{"overlay": map[string]any{"id": "co", "kind": "contours", "datasetId": d.ID, "visible": true, "channel": 0, "levels": map[string]any{"mode": "sigma", "multiples": []float64{3}}, "unit": "K", "color": "amber"}}
	for _, c := range []struct {
		method, url string
		body        any
	}{
		{"GET", "/api/datasets/" + id + "/view", nil},
		{"POST", "/api/datasets/" + id + "/interpretation", map[string]any{"kind": "axial_orientation", "unit": "deg"}},
		{"POST", "/api/analysis", map[string]any{"op": "stats", "datasetId": d.ID}},
		{"POST", "/api/analysis/relative-orientation", map[string]any{"datasetId": im.ID, "otherId": d.ID}},
		{"POST", "/api/analysis/healpix-sample", map[string]any{"datasetId": d.ID, "targetId": d.ID, "product": "b_field"}},
		{"POST", "/api/analysis/healpix-sample", map[string]any{"datasetId": im.ID, "targetId": im.ID, "product": "b_field"}},
		{"POST", "/api/datasets/" + id + "/unit", map[string]any{"unit": "K"}},
		{"GET", "/api/datasets/" + id + "/corrections", nil},
		{"GET", "/api/datasets/" + id + "/conversions", nil},
		{"POST", "/api/datasets/" + imID + "/link", map[string]any{"otherId": d.ID, "region": map[string]any{"x0": -0.5, "y0": -0.5, "x1": 3.5, "y1": 3.5}, "points": [][]float64{{0, 0}}}},
		{"POST", "/api/datasets/" + imID + "/overlay-geometry", contours},
		{"POST", "/api/datasets/" + id + "/overlay-geometry", contours},
	} {
		st, b := callJSON(t, c.method, ts.URL+c.url, c.body)
		if st != 409 || !strings.Contains(string(b), "HEALPix") {
			t.Errorf("%s %s: want 409 naming HEALPix, got %d %s", c.method, c.url, st, b)
		}
	}
}

// The full path: sample the Planck field onto a VGT map's grid as the field orientation (and as dust polarization), get an orientation
// map declared by the operation with P and sigma_P companions, and compare it with the VGT map. The VGT map holds the same field, stated
// in the Galactic frame by its header, so both sides agree only if the flip and both frame turns are right.
func TestHealpixSampleThenRelativeOrientationWithAVGTMap(t *testing.T) {
	t.Parallel()
	ts, root, ds := healpixServer(t)
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(root, name), nil).Body).Decode(&d)
		return d
	}
	planck, vgt := reg("planck_353_cosmo.fits"), reg("vgt_psi_planck_field.fits")
	if st, b := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(vgt.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "fromFile": true}); st != 200 {
		t.Fatalf("adopt the VGT header: %d %s", st, b)
	}
	doc := loadSpec(t)
	const route = "/api/analysis/healpix-sample"
	sample := func(body map[string]any) (int, []byte) {
		st, b := callJSON(t, "POST", ts.URL+route, body)
		if st == 201 {
			conforms(t, responseSchemaAt(t, doc, route, "POST", 201, "application/json"), "healpix sample", b)
			conforms(t, requestSchema(t, doc, route, "POST"), "healpix sample request", mustJSON(t, body))
		} else if st >= 400 {
			conforms(t, responseSchemaAt(t, doc, route, "POST", st, "application/json"), "healpix sample error", b)
		}
		return st, b
	}
	for _, bad := range []map[string]any{
		{"datasetId": planck.ID, "targetId": vgt.ID},
		{"datasetId": planck.ID, "targetId": vgt.ID, "product": "b_field", "method": "gaussian"},
		{"datasetId": planck.ID, "targetId": vgt.ID, "product": "b_field", "fwhmArcmin": 30},
		{"datasetId": planck.ID, "targetId": vgt.ID, "product": "b_field", "snrMin": -1},
	} {
		if st, b := sample(bad); st != 400 {
			t.Errorf("%v: want 400, got %d %s", bad, st, b)
		}
	}
	// a kernel smaller than the map's pixels is the worker's refusal, in its words
	if st, b := sample(map[string]any{"datasetId": planck.ID, "targetId": vgt.ID, "product": "b_field", "method": "gaussian", "fwhmArcmin": 30}); st != 422 || !strings.Contains(string(b), "smaller than this map's pixels") {
		t.Errorf("small kernel: %d %s", st, b)
	}
	for _, product := range []string{"b_field", "dust_polarization"} {
		st, b := sample(map[string]any{"datasetId": planck.ID, "targetId": vgt.ID, "product": product, "method": "bilinear", "snrMin": 3})
		if st != 201 {
			t.Fatalf("%s: %d %s", product, st, b)
		}
		var out struct {
			Dataset catalog.Dataset  `json:"dataset"`
			P       *catalog.Dataset `json:"p"`
			SigmaP  *catalog.Dataset `json:"sigmaP"`
			Summary struct {
				Policy     string `json:"policy"`
				Derivation struct {
					Convention struct {
						Flipped bool `json:"flipped"`
					} `json:"convention"`
					Noise string `json:"noise"`
				} `json:"derivation"`
			} `json:"summary"`
		}
		json.Unmarshal(b, &out)
		in := out.Dataset.Interpretation
		if in == nil || in.Kind != "axial_orientation" || in.Unit != "deg" || deref(in.Frame) != "sky" || in.SkyFrame != nil || deref(in.Quantity) != product || in.DeclaredBy != analysisAuthor {
			t.Fatalf("%s: the result's declaration %+v", product, in)
		}
		if out.P == nil || out.SigmaP == nil {
			t.Fatalf("%s: companions missing: %s", product, b)
		}
		if !out.Summary.Derivation.Convention.Flipped || !strings.Contains(out.Summary.Derivation.Noise, "not modelled") || !strings.Contains(out.Summary.Policy, "COSMO to IAU") {
			t.Errorf("%s: the derivation must name the flip and the unmodelled noise: %s", product, b)
		}
		rec, err := ds.Get(out.Dataset.ID)
		if err != nil || rec.Op != "healpix_sample" || len(rec.Sources) != 2 || rec.Sources[0] != planck.ID || rec.Sources[1] != vgt.ID {
			t.Fatalf("%s: derived record %+v %v", product, rec, err)
		}
		var pr struct {
			Companions map[string]int64 `json:"companions"`
		}
		json.Unmarshal(rec.Params, &pr)
		if pr.Companions["p"] != out.P.ID || pr.Companions["sigmaP"] != out.SigmaP.ID {
			t.Errorf("%s: the angle map's record must name its companions: %s", product, rec.Params)
		}
		if crec, err := ds.Get(out.SigmaP.ID); err != nil || !strings.Contains(string(crec.Summary), `"companionOf"`) {
			t.Errorf("%s: sigma_P record %v %v", product, crec, err)
		}
		st, rb := callJSON(t, "POST", ts.URL+"/api/analysis/relative-orientation", map[string]any{"datasetId": vgt.ID, "otherId": out.Dataset.ID, "params": map[string]any{"nShifts": 0}})
		if st != 201 {
			t.Fatalf("%s: relative orientation %d %s", product, st, rb)
		}
		var ro struct {
			Summary struct {
				Am     float64 `json:"am"`
				Counts struct {
					Used int `json:"used"`
				} `json:"counts"`
			} `json:"summary"`
		}
		json.Unmarshal(rb, &ro)
		if ro.Summary.Counts.Used != 16 || ro.Summary.Am < 0.9999 {
			t.Errorf("%s: the VGT map and the Planck field must agree: AM %v over %d pixels", product, ro.Summary.Am, ro.Summary.Counts.Used)
		}
	}
}
