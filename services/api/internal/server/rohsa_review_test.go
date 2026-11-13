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
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// reviewEnv is the ROHSA test server with the analysis programs and the derived records, over the generated fixture fit.
func reviewEnv(t *testing.T) (rohsaEnv, *derived.Store, string) {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := headerWorker(t, "python3", repo)
	base := t.TempDir()
	root, derivedDir := filepath.Join(base, "root"), filepath.Join(base, "derived")
	for _, d := range []string{root, derivedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs, _ := sourceroot.New([]string{root, derivedDir})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := derived.Open(cat.DB())
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	py, _ := exec.LookPath("python3")
	srv := &Server{Roots: rs, Store: cat, Worker: pw, Planes: pw, Cache: NewPlaneCache(8 << 20), Runs: rn, CacheDir: filepath.Join(base, "cache"), Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return rohsaEnv{ts, root, srv.CacheDir, srv}, ds, derivedDir
}

// The generated fit (fixtures/generate.py): 6 x 4 pixels, three Gaussians, amp = 1 + 0.5 k + 0.1 x + 0.01 y (0 for the last component of
// pixel (0, 0)), sigma = 1.2 + 0.7 k + 0.05 x channels, 0.8 km/s per channel.
func fixtureWeights() (total float64, narrow, rest int) {
	const dv = 0.8
	for y := 0; y < 4; y++ {
		for x := 0; x < 6; x++ {
			for k := 0; k < 3; k++ {
				amp := 1 + 0.5*float64(k) + 0.1*float64(x) + 0.01*float64(y)
				if x == 0 && y == 0 && k == 2 {
					amp = 0
				}
				sig := 1.2 + 0.7*float64(k) + 0.05*float64(x)
				total += amp * sig * math.Sqrt(2*math.Pi) * dv
				if sig*dv < 2.0 {
					narrow++
				} else {
					rest++
				}
			}
		}
	}
	return
}

func TestROHSAReviewDiagramMaskAndClassMaps(t *testing.T) {
	t.Parallel()
	e, ds, derivedDir := reviewEnv(t)
	run, _ := e.importRun(t, filepath.Join(e.root, "rohsa_run_demo", "region_a"), 201)
	crop := *run.Rohsa.CropDatasetID
	base := e.ts.URL + "/api/datasets/" + itoa(int(crop)) + "/rohsa"
	wantTotal, wantNarrow, wantRest := fixtureWeights()
	doc := loadSpec(t)
	conform := func(route string, status int, req map[string]any, raw []byte) {
		t.Helper()
		pattern := "/api/datasets/{id}/rohsa" + route
		conforms(t, requestSchema(t, doc, pattern, "POST"), "review request "+route, mustJSON(t, req))
		conforms(t, responseSchemaAt(t, doc, pattern, "POST", status, "application/json"), "review reply "+route, raw)
	}

	// the diagram: the bins are the person's; the weighting and its unit are stated; nothing in it names a folder
	req := map[string]any{"runId": run.ID, "weight": "integrated", "velocityBins": map[string]any{"min": -14.0, "max": 10.0, "n": 12}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
	resp, raw := postJSON(t, base+"/sigmav", req)
	if resp.StatusCode != 200 {
		t.Fatalf("sigmav %d %s", resp.StatusCode, raw)
	}
	var d struct {
		WeightUnit string  `json:"weight_unit"`
		WeightHow  string  `json:"weight_how"`
		Components int     `json:"components"`
		InDiagram  int     `json:"in_diagram"`
		Outside    int     `json:"outside"`
		Total      float64 `json:"weight_total"`
		Cells      [][]any `json:"cells"`
		Axes       struct {
			Centre struct {
				Unit string `json:"unit"`
			} `json:"centre"`
		} `json:"axes"`
		UnitSource string `json:"unit_source"`
		RunID      int64  `json:"runId"`
		PixelIndex *struct {
			Bytes int `json:"bytes"`
			Rows  int `json:"rows"`
		} `json:"pixel_index"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.WeightUnit != "K km/s" || d.Axes.Centre.Unit != "km/s" || d.Components != 72 || d.InDiagram+d.Outside != 72 || d.RunID != run.ID {
		t.Fatalf("diagram: %s", raw)
	}
	if d.PixelIndex == nil || d.PixelIndex.Rows != 72 || d.PixelIndex.Bytes != 2*72 {
		t.Errorf("the diagram does not state its pixel index (2 bytes per row): %s", raw)
	}
	if math.Abs(d.Total/wantTotal-1) > 1e-12 {
		t.Errorf("weight total %v, want %v", d.Total, wantTotal)
	}
	if strings.Contains(string(raw), e.root) {
		t.Errorf("the reply names a folder: %s", raw)
	}
	conform("/sigmav", 200, req, raw)
	if len(d.Cells) == 0 {
		t.Fatal("no cells")
	}

	// the pixels behind a cell: the mask holds as many components as the cell says
	cell := d.Cells[0]
	s, v, count := int(cell[0].(float64)), int(cell[1].(float64)), int(cell[2].(float64))
	creq := map[string]any{"runId": run.ID, "weight": "integrated", "velocityBins": req["velocityBins"], "sigmaBins": req["sigmaBins"], "cell": map[string]any{"s": s, "v": v}}
	resp, raw = postJSON(t, base+"/sigmav/cell", creq)
	if resp.StatusCode != 200 {
		t.Fatalf("cell %d %s", resp.StatusCode, raw)
	}
	conform("/sigmav/cell", 200, creq, raw)
	var m struct {
		Components int `json:"components"`
		Pixels     int `json:"pixels"`
		Mask       struct {
			Width, Height int
			Encoding      string
			Data          string
		} `json:"mask"`
	}
	json.Unmarshal(raw, &m)
	if m.Components != count || m.Pixels < 1 || m.Pixels > count || m.Mask.Width != 6 || m.Mask.Height != 4 || !strings.Contains(m.Mask.Encoding, "row-major") || m.Mask.Data == "" {
		t.Errorf("cell mask: %s (cell count %d)", raw, count)
	}

	// class maps: one dataset per class on the crop's sky grid, each with a record of what made it
	mreq := map[string]any{"runId": run.ID, "weight": "integrated", "edges": []float64{2.0}}
	resp, raw = postJSON(t, base+"/class-maps", mreq)
	if resp.StatusCode != 201 {
		t.Fatalf("class maps %d %s", resp.StatusCode, raw)
	}
	conform("/class-maps", 201, mreq, raw)
	var cm struct {
		Maps []struct {
			Dataset catalog.Dataset
			Class   struct {
				Label       string
				Components  int
				WeightTotal float64 `json:"weight_total"`
			}
		} `json:"maps"`
		Policy string `json:"policy"`
	}
	json.Unmarshal(raw, &cm)
	if len(cm.Maps) != 2 || cm.Maps[0].Class.Components != wantNarrow || cm.Maps[1].Class.Components != wantRest {
		t.Fatalf("classes: %s", raw)
	}
	if math.Abs((cm.Maps[0].Class.WeightTotal+cm.Maps[1].Class.WeightTotal)/wantTotal-1) > 1e-12 {
		t.Errorf("the classes do not add up to the whole decomposition")
	}
	for i, mp := range cm.Maps {
		rec, err := ds.Get(mp.Dataset.ID)
		if err != nil || rec.Op != "rohsa_class_map" || len(rec.Sources) != 1 || rec.Sources[0] != crop {
			t.Fatalf("record of class %d: %+v %v", i, rec, err)
		}
		all := string(rec.Params) + string(rec.Summary)
		for _, want := range []string{`"weight":"integrated"`, `"edges":[2]`, `rohsa_n3.dat`, `"runName":"rohsa_n3"`, "K km/s"} {
			if !strings.Contains(all, want) {
				t.Errorf("record of class %d lacks %s: %s", i, want, all)
			}
		}
		if strings.Contains(all, e.root) || strings.Contains(all, derivedDir) {
			t.Errorf("the record names a folder: %s", all)
		}
		if mp.Dataset.Header == nil || !strings.Contains(string(mp.Dataset.Header), "K km/s") {
			t.Errorf("class %d header does not say K km/s: %s", i, mp.Dataset.Header)
		}
	}
}

func TestROHSAReviewRefusesWhatItCannotDoAndAnswersInSentences(t *testing.T) {
	t.Parallel()
	e, _, _ := reviewEnv(t)
	run, _ := e.importRun(t, filepath.Join(e.root, "rohsa_run_demo", "region_a"), 201)
	crop := *run.Rohsa.CropDatasetID
	base := e.ts.URL + "/api/datasets/" + itoa(int(crop)) + "/rohsa"
	for name, c := range map[string]struct {
		path string
		body map[string]any
		want int
	}{
		"weight":        {"/sigmav", map[string]any{"runId": run.ID, "weight": "mass"}, 400},
		"bins n":        {"/sigmav", map[string]any{"runId": run.ID, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 129}}, 400},
		"bins order":    {"/sigmav", map[string]any{"runId": run.ID, "velocityBins": map[string]any{"min": 4.0, "max": 4.0, "n": 4}}, 400},
		"cell missing":  {"/sigmav/cell", map[string]any{"runId": run.ID}, 400},
		"edges missing": {"/class-maps", map[string]any{"runId": run.ID}, 400},
		"edges order":   {"/class-maps", map[string]any{"runId": run.ID, "edges": []float64{3, 2}}, 400},
		"edges sign":    {"/class-maps", map[string]any{"runId": run.ID, "edges": []float64{-1}}, 400},
		"no such run":   {"/sigmav", map[string]any{"runId": 99999}, 404},
	} {
		resp, raw := postJSON(t, base+c.path, c.body)
		if resp.StatusCode != c.want || !strings.Contains(string(raw), "error") {
			t.Errorf("%s: %d %s", name, resp.StatusCode, raw)
		}
	}
	// a cell outside the diagram is the worker's refusal, in a sentence
	resp, raw := postJSON(t, base+"/sigmav/cell", map[string]any{"runId": run.ID, "velocityBins": map[string]any{"min": -14.0, "max": 10.0, "n": 12}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}, "cell": map[string]any{"s": 8, "v": 0}})
	if resp.StatusCode != 422 || !strings.Contains(string(raw), "outside the diagram") {
		t.Errorf("outside cell: %d %s", resp.StatusCode, raw)
	}
	// a cell of bins that were never drawn has no pixel index: the worker says to draw the diagram again, and the .dat is not parsed
	resp, raw = postJSON(t, base+"/sigmav/cell", map[string]any{"runId": run.ID, "velocityBins": map[string]any{"min": -14.0, "max": 10.0, "n": 12}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}, "cell": map[string]any{"s": 1, "v": 1}})
	if resp.StatusCode != 422 || !strings.Contains(string(raw), "draw the diagram again") {
		t.Errorf("undrawn bins: %d %s", resp.StatusCode, raw)
	}
	// a run whose fit is on a host but whose crop is not on that host is never reviewed here: the program reads both on one machine
	info := run.Rohsa
	info.HostID = 7
	if err := e.srv.Runs.SetROHSA(run.ID, "/somewhere/rohsa_n3.dat", *info); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/sigmav", "/sigmav/cell", "/class-maps"} {
		resp, raw := postJSON(t, base+p, map[string]any{"runId": run.ID, "edges": []float64{2}, "cell": map[string]any{"s": 0, "v": 0}})
		if resp.StatusCode != 409 || !strings.Contains(string(raw), "not on the same machine") {
			t.Errorf("host %s: %d %s", p, resp.StatusCode, raw)
		}
	}
}
