package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// countingPlanes counts the requests that reach the worker, so a test can see the cache answer a repeat.
type countingPlanes struct {
	inner PlaneService
	n     atomic.Int64
}

func (c *countingPlanes) Do(ctx context.Context, req map[string]any) (worker.Reply, error) {
	if req["op"] == "overlay_geometry" {
		c.n.Add(1)
	}
	return c.inner.Do(ctx, req)
}

// Overlay geometry end to end with the real worker on the generated fixtures: authorization of both files, declarations
// from the catalog (never the request), refusals with their reasons, the frame and its header against the spec, the cache.
func TestOverlayGeometryEndToEndWithRealWorker(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, contourpy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and contourpy is required")
		}
		t.Skip("python3 with astropy and contourpy not available")
	}
	doc := loadSpec(t)
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	cp := &countingPlanes{inner: pw}
	ts, root, _ := newPlaneServerWith(t, headerWorker(t, "python3", repo), cp)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	reg := func(name string) int {
		var d catalog.Dataset
		resp := post(t, ts, filepath.Join(root, name), nil)
		if resp.StatusCode != 201 {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
		json.NewDecoder(resp.Body).Decode(&d)
		return int(d.ID)
	}
	hi, rot, gal, psi, tan := reg("hi_cube.fits"), reg("hi_cube_rotated.fits"), reg("hi_cube_galactic.fits"), reg("vgt_psi_deg.fits"), reg("vgt_tangential_deg.fits")
	reqSchema := requestSchema(t, doc, "/api/datasets/{id}/overlay-geometry", "POST")
	headSchema := doc.Components.Schemas["OverlayGeometryHeader"]
	ask := func(base int, body map[string]any) (int, map[string]any, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), `"bogus"`) {
			conforms(t, reqSchema, "overlay geometry request", raw)
		}
		r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(base)+"/overlay-geometry", body)
		if r.StatusCode != 200 {
			var e map[string]any
			json.Unmarshal(b, &e)
			return r.StatusCode, e, nil
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/vnd.vesqira.geometry" {
			t.Fatalf("content type %q", ct)
		}
		n := binary.LittleEndian.Uint32(b[:4])
		head := bytes.TrimRight(b[4:4+n], " ")
		conforms(t, headSchema, "overlay geometry header", head)
		var h map[string]any
		json.Unmarshal(head, &h)
		if (4+n)%8 != 0 {
			t.Fatal("the payload is not 8-byte aligned")
		}
		return 200, h, b[4+n:]
	}
	contours := func(ds int, levels map[string]any) map[string]any {
		return map[string]any{"overlay": map[string]any{"id": "co", "kind": "contours", "datasetId": ds, "visible": true, "channel": 40, "levels": levels, "unit": "K", "color": "amber"}}
	}

	// contours of a rotated cube on the base, at k x sigma
	code, h, payload := ask(hi, contours(rot, map[string]any{"mode": "sigma", "multiples": []float64{3, 5, 10}}))
	if code != 200 {
		t.Fatalf("contours: %d %v", code, h)
	}
	if h["kind"] != "contours" || h["sigma"] == nil || h["unit"] != "K" || h["paths"].(float64) < 1 {
		t.Fatalf("contours head %v", h)
	}
	if len(payload) != int(h["nbytes"].(float64)) || len(payload) != 4*(2*int(h["paths"].(float64))+2*int(h["vertices"].(float64))) {
		t.Fatalf("payload %d bytes for %v", len(payload), h)
	}
	before := cp.n.Load()
	// the colour and visibility do not change the geometry: a repeat with another colour is served from the cache
	again := contours(rot, map[string]any{"mode": "sigma", "multiples": []float64{3, 5, 10}})
	again["overlay"].(map[string]any)["color"] = "cyan"
	if code, _, _ := ask(hi, again); code != 200 || cp.n.Load() != before {
		t.Fatalf("repeat: %d, worker calls %d -> %d", code, before, cp.n.Load())
	}

	// a Galactic-frame cube elsewhere on the sky: refused, saying the footprints do not overlap
	if code, e, _ := ask(hi, contours(gal, map[string]any{"mode": "list", "values": []float64{1}})); code != 422 || !strings.Contains(e["error"].(string), "does not overlap") {
		t.Fatalf("galactic: %d %v", code, e)
	}
	// levels written in another unit than the file's
	wrong := contours(rot, map[string]any{"mode": "list", "values": []float64{1}})
	wrong["overlay"].(map[string]any)["unit"] = "Jy/beam"
	if code, e, _ := ask(hi, wrong); code != 422 || !strings.Contains(e["error"].(string), "unit") {
		t.Fatalf("unit: %d %v", code, e)
	}

	// beam: from the base header (BMAJ 0.03, BMIN 0.02, BPA 45 degrees), refused for a map without one
	code, h, _ = ask(hi, map[string]any{"overlay": map[string]any{"id": "b", "kind": "beam", "datasetId": hi, "visible": true}})
	if code != 200 || h["beam"].(map[string]any)["bmaj_arcsec"].(float64) != 108 {
		t.Fatalf("beam: %d %v", code, h)
	}
	if code, e, _ := ask(hi, map[string]any{"overlay": map[string]any{"id": "b", "kind": "beam", "datasetId": psi, "visible": true}}); code != 422 || !strings.Contains(e["error"].(string), "BMAJ") {
		t.Fatalf("no beam: %d %v", code, e)
	}

	// grid of the base, in another frame; refused from another dataset
	if code, h, _ = ask(hi, map[string]any{"overlay": map[string]any{"id": "g", "kind": "grid", "datasetId": hi, "visible": true, "frame": "galactic"}}); code != 200 || len(h["labels"].([]any)) == 0 {
		t.Fatalf("grid: %d %v", code, h)
	}
	if code, _, _ := ask(hi, map[string]any{"overlay": map[string]any{"id": "g", "kind": "grid", "datasetId": rot, "visible": true, "frame": "icrs"}}); code != 400 {
		t.Fatalf("grid from another dataset: %d", code)
	}

	// orientation from another dataset: refused until declared axial, then by name until its frame is declared
	orient := map[string]any{"overlay": map[string]any{"id": "o", "kind": "orientation", "datasetId": tan, "visible": true, "sampling": map[string]any{"mode": "auto", "gapPx": 16}, "length": "fixed", "opacity": "uniform"}, "spacing": 4}
	if code, _, _ := ask(hi, orient); code != 409 {
		t.Fatalf("undeclared orientation: %d", code)
	}
	if r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(tan)+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "deg"}); r.StatusCode != 200 {
		t.Fatalf("declare: %d %s", r.StatusCode, b)
	}
	if code, e, _ := ask(hi, orient); code != 422 || !strings.Contains(e["error"].(string), "frame") {
		t.Fatalf("no frame: %d %v", code, e)
	}
	frame, quantity := "pixel", "b_field"
	if r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(tan)+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "deg", "frame": frame, "quantity": quantity}); r.StatusCode != 200 {
		t.Fatalf("declare: %d %s", r.StatusCode, b)
	}
	code, h, payload = ask(hi, orient)
	if code != 200 || h["segments"].(float64) < 1 || len(payload) != 16*int(h["segments"].(float64)) {
		t.Fatalf("orientation: %d %v", code, h)
	}
	noSpacing := map[string]any{"overlay": orient["overlay"]}
	if code, _, _ := ask(hi, noSpacing); code != 400 {
		t.Fatalf("auto sampling without spacing: %d", code)
	}

	// what the request may not carry: an unknown key, a kind with no geometry, a missing dataset
	if code, _, _ := ask(hi, map[string]any{"overlay": map[string]any{"id": "b", "kind": "beam", "datasetId": hi, "visible": true}, "bogus": 1}); code != 400 {
		t.Fatalf("unknown key: %d", code)
	}
	if code, _, _ := ask(hi, map[string]any{"overlay": map[string]any{"id": "r", "kind": "regions", "datasetId": hi, "visible": true, "saved": []any{}}}); code != 400 {
		t.Fatalf("regions: %d", code)
	}
	if code, _, _ := ask(hi, map[string]any{"overlay": map[string]any{"id": "b", "kind": "beam", "datasetId": 999, "visible": true}}); code != 404 {
		t.Fatalf("missing overlay dataset: %d", code)
	}

	// a changed overlay file is refused, as a plane read is
	if err := os.Chtimes(filepath.Join(root, "hi_cube_rotated.fits"), time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := ask(hi, contours(rot, map[string]any{"mode": "sigma", "multiples": []float64{3}})); code != 409 {
		t.Fatalf("changed overlay file: %d", code)
	}
}
