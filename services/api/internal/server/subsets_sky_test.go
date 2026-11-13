package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// A virtual subset has the same two sky routes as a dataset (ADR 0072). The worker applies the window to the parent's WCS; the
// browser never does. The astropy comparison is in the worker's tests (test_subset.py, SubsetSky); here the route is shown to
// read through the window, to answer for the subset's own pixels, and to refuse a stale subset and a worker that ignored the window.
func TestSubsetSkyRoutesReadThroughTheWindow(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	sub := e.mustCreate(src.ID, boxDef)
	read := func(path string) map[string]any {
		code, b := e.get(path)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, b)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	whole := func(route string) map[string]any { return read(fmt.Sprintf("/api/datasets/%d/%s", src.ID, route)) }
	part := func(route string) map[string]any { return read(fmt.Sprintf("/api/subsets/%d/%s", sub.ID, route)) }
	ext := func(m map[string]any) float64 { return m["footprint"].(map[string]any)["extent_deg"].(float64) }
	a, b := whole("sky-footprint?frame=icrs"), part("sky-footprint?frame=icrs")
	if ext(b) >= ext(a) || b["window"] == nil {
		t.Fatalf("the subset's outline is not the window's: whole %v part %v", ext(a), ext(b))
	}
	// pixel (0,0) of the subset is source pixel (3,2): the same sky position as the source's own pixel there
	pp, sp := part("sky-point?frame=icrs&x=0&y=0")["position"].(map[string]any), whole("sky-point?frame=icrs&x=3&y=2")["position"].(map[string]any)
	if pp["lon"] != sp["lon"] || pp["lat"] != sp["lat"] {
		t.Fatalf("subset pixel (0,0) is %v, source pixel (3,2) is %v", pp, sp)
	}
	if code, _ := e.get(fmt.Sprintf("/api/subsets/%d/sky-point?frame=icrs&x=a&y=0", sub.ID)); code != 400 {
		t.Errorf("a bad pixel: %d", code)
	}
	if code, _ := e.get("/api/subsets/999/sky-footprint?frame=icrs"); code != 404 {
		t.Errorf("unknown subset: %d", code)
	}

	// a source that changed makes the subset stale: its outline is not offered as current
	later := time.Now().Add(2 * time.Hour)
	os.Chtimes(filepath.Join(e.root, "hi_cube.fits"), later, later)
	e.register("hi_cube.fits")
	code, body := e.get(fmt.Sprintf("/api/subsets/%d/sky-footprint?frame=icrs", sub.ID))
	if code != 409 || !strings.Contains(string(body), "subset_stale") {
		t.Errorf("a stale subset: %d %s", code, body)
	}
}

// dropWindowOnSky is a worker that applies windows to everything except the sky operations, as an older worker on a host would.
type dropWindowOnSky struct{ inner PlaneService }

func (d dropWindowOnSky) Do(ctx context.Context, req map[string]any) (worker.Reply, error) {
	if op, _ := req["op"].(string); strings.HasPrefix(op, "sky_") {
		delete(req, "window")
	}
	return d.inner.Do(ctx, req)
}

func TestSubsetSkyNeverShowsAnOutlineFromAWorkerThatIgnoredTheWindow(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, func(p PlaneService) PlaneService { return dropWindowOnSky{p} })
	src := e.register("hi_cube.fits")
	sub := e.mustCreate(src.ID, boxDef)
	code, b := e.get(fmt.Sprintf("/api/subsets/%d/sky-footprint?frame=icrs", sub.ID))
	if code != 502 || !strings.Contains(string(b), "did not apply the window") {
		t.Fatalf("%d %s", code, b)
	}
}
