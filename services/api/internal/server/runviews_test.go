package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func f64(v float64) *float64 { return &v }

// The runs table is built in the browser from GET /api/runs, so that listing has to stay usable for a researcher with
// thousands of sweep runs: every run with its parameters and typed results, in one reply, quickly and in bounded size.
func TestRunListingWithParametersAndMetricsAtScale(t *testing.T) {
	t.Parallel()
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	const n = 2000
	for i := 0; i < n; i++ {
		p := runs.Parsed{Run: runs.Run{
			Name: fmt.Sprintf("lmc_b%d_s%d", 8<<(i%4), i), Kind: "vgt", GeneratedAt: "2026-08-01T10:00:00Z", GitCommit: "abc1234",
			Params: []runs.Param{{Key: "block_size", Value: fmt.Sprint(8 << (i % 4)), Source: "config"}, {Key: "gradient_method", Value: []string{"central", "sobel", "scharr"}[i%3], Source: "config"}, {Key: "smoothing", Value: fmt.Sprint(i % 5), Source: "config"}, {Key: "seed", Value: fmt.Sprint(i), Source: "run_parameters"}},
			Metrics: []runs.Metric{
				{Key: "am_stars", Value: f64(0.2 + float64(i%50)/100), Unit: "", Uncertainty: f64(0.02), UncertaintyKind: "standard_error", Definition: "Axial agreement with the stars", Status: "declared"},
				{Key: "am_planck", Value: f64(0.1 + float64(i%70)/100), Uncertainty: f64(0.03), UncertaintyKind: "confidence_68", Definition: "Axial agreement with Planck 353 GHz", Status: "declared"},
				{Key: "residual", Value: f64(float64(i) / 7), Unit: "K", Status: "declared"},
			},
		}}
		if i%10 == 0 {
			p.Metrics = p.Metrics[:1] // some runs never recorded the others
		}
		if _, err := rn.Save(workspace.Local, "local-owner", fmt.Sprintf("/runs/%d", i), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer((&Server{Store: cat, Runs: rn}).Handler())
	defer ts.Close()

	start := time.Now()
	r, b := get(t, ts.URL+"/api/runs")
	took := time.Since(start)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	// a generous bound for a slow CI machine: the measured time is a small fraction of it
	if took > 5*time.Second {
		t.Errorf("listing %d runs took %v", n, took)
	}
	// about 2.2 KB a run today, most of it the constant reproducibility text; over 3 KB a run the listing needs a leaner projection
	if len(b) > n*3<<10 {
		t.Errorf("listing %d runs is %d bytes: over 3 KB a run, the table would need a leaner projection", n, len(b))
	}
	var out struct {
		Runs []struct {
			Params  []runs.Param  `json:"params"`
			Metrics []runs.Metric `json:"metrics"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != n {
		t.Fatalf("%d runs listed, want %d", len(out.Runs), n)
	}
	missing := 0
	for _, x := range out.Runs {
		if len(x.Params) != 4 {
			t.Fatalf("a run lost parameters: %v", x.Params)
		}
		if len(x.Metrics) == 1 {
			missing++
		}
	}
	if missing != n/10 {
		t.Errorf("%d runs with a single result, want %d: a run that did not record a result still lists, without it", missing, n/10)
	}
	t.Logf("%d runs: %d bytes in %v", n, len(b), took)
}

func TestSavedRunViewsRoutesMatchTheSpec(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, _ := runs.Open(cat.DB())
	ts := httptest.NewServer((&Server{Store: cat, Runs: rn}).Handler())
	defer ts.Close()
	check := func(path, method string, status int, label string, body []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, path, method, status, "application/json"), label, body)
	}
	_, b := get(t, ts.URL+"/api/run-views")
	check("/api/run-views", "GET", 200, "GET empty", b)
	spec := map[string]any{"query": "block_size:16..64 am>0.3", "columns": []string{"status", "p:block_size", "m:am"}, "sort": map[string]any{"column": "m:am", "dir": -1}, "plot": map[string]any{"y": "am", "x": "block_size", "xKind": "param", "by": "gradient_method"}}
	r, b := postJSON(t, ts.URL+"/api/run-views", map[string]any{"name": "LMC sweep", "spec": spec})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/run-views", "POST", 200, "POST view", b)
	var v struct{ ID int64 }
	_ = json.Unmarshal(b, &v)
	_, b = get(t, ts.URL+"/api/run-views")
	check("/api/run-views", "GET", 200, "GET one", b)
	// a name that cannot be kept, and an unknown field, are refused with a reason
	if r, _ := postJSON(t, ts.URL+"/api/run-views", map[string]any{"name": "", "spec": spec}); r.StatusCode != 422 {
		t.Errorf("empty name: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/run-views", map[string]any{"name": "x", "spec": spec, "extra": 1}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: %d", r.StatusCode)
	}
	r, b = postJSON(t, ts.URL+"/api/run-views/"+itoa(int(v.ID))+"/delete", map[string]any{})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/run-views/{id}/delete", "POST", 200, "POST delete", b)
	if r, _ := postJSON(t, ts.URL+"/api/run-views/"+itoa(int(v.ID))+"/delete", map[string]any{}); r.StatusCode != 404 {
		t.Errorf("deleting twice: %d", r.StatusCode)
	}
}
