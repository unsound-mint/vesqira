package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
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
	if r, _ := postJSON(t, ts.URL+"/api/regions/"+itoa(int(reg.ID))+"/delete", map[string]any{}); r.StatusCode != 200 {
		t.Errorf("delete: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/regions/"+itoa(int(reg.ID))+"/delete", map[string]any{}); r.StatusCode != 404 {
		t.Errorf("deleting twice: %d", r.StatusCode)
	}
}
