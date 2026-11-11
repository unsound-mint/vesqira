package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Linked views end to end with the real worker on the generated fixtures (ADR 0042, section 4): the reply against the spec,
// a cursor across ICRS and Galactic with the cache answering a repeat, a channel link and its refusal when the spectral
// frames differ, and the request's own refusals.
func TestLinkEndToEndWithRealWorker(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
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
	hi, galOverlap, topo, coarser, freq := reg("hi_cube.fits"), reg("hi_cube_galactic_overlap.fits"), reg("hi_cube_topocentric.fits"), reg("hi_cube_coarser.fits"), reg("hi_cube_freq.fits")
	reqSchema := requestSchema(t, doc, "/api/datasets/{id}/link", "POST")
	replySchema := responseSchema(t, doc, "/api/datasets/{id}/link", "POST", "application/json")
	askRaw := func(id int, body map[string]any) (int, map[string]any, []byte) {
		t.Helper()
		r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(id)+"/link", body)
		var out map[string]any
		json.Unmarshal(b, &out)
		return r.StatusCode, out, b
	}
	ask := func(id int, body map[string]any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		conforms(t, reqSchema, "link request", raw)
		code, out, b := askRaw(id, body)
		if code == 200 {
			conforms(t, replySchema, "link reply", b)
		}
		return code, out
	}
	region := map[string]any{"x0": -0.5, "y0": -0.5, "x1": 31.5, "y1": 23.5}

	// the cursor across frames: ICRS to Galactic, the exact position of a pixel and the affine over the region
	code, h := ask(hi, map[string]any{"otherId": galOverlap, "region": region, "points": [][]float64{{16, 12}, {0, 0}}})
	if code != 200 {
		t.Fatalf("link: %d %v", code, h)
	}
	sky := h["sky"].(map[string]any)
	frames := sky["frames"].(map[string]any)
	if sky["refused"] != nil || frames["from"] != "ICRS" || frames["to"] != "Galactic" || h["spectral"] != nil {
		t.Fatalf("sky %v", sky)
	}
	ex := sky["exact"].([]any)[0].(map[string]any)
	if !ex["inside"].(bool) || ex["bx"].(float64) < 10 || ex["bx"].(float64) > 22 {
		t.Fatalf("the reference pixel of A should land near the middle of B: %v", ex)
	}
	if sky["max_error_px"].(float64) > 0.05 {
		t.Fatalf("a 0.3 degree field should be linear to well under a pixel: %v", sky["max_error_px"])
	}
	counted := func(f func()) int64 {
		cp.mu.Lock()
		cp.ops = map[string]int64{}
		cp.mu.Unlock()
		f()
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return cp.ops["link"]
	}
	// a small pan widens to the same half-pixel grid and is answered from the cache
	if n := counted(func() {
		ask(hi, map[string]any{"otherId": galOverlap, "region": region, "points": [][]float64{{16, 12}, {0, 0}}})
		ask(hi, map[string]any{"otherId": galOverlap, "region": map[string]any{"x0": -0.4, "y0": -0.4, "x1": 31.4, "y1": 23.4}, "points": [][]float64{{16, 12}, {0, 0}}})
	}); n != 0 {
		t.Fatalf("a repeat reached the worker %d times", n)
	}

	// the channel link: the same velocity axis maps channel to channel; a topocentric cube is refused with the reason
	code, h = ask(hi, map[string]any{"otherId": coarser, "channels": true})
	if code != 200 || h["sky"] != nil {
		t.Fatalf("channels: %d %v", code, h)
	}
	spec := h["spectral"].(map[string]any)
	idx := spec["index"].([]any)
	if spec["refused"] != nil || len(idx) != 64 || idx[40].(float64) != 40 || spec["residual"].([]any)[40].(float64) != 0 {
		t.Fatalf("spectral %v", spec)
	}
	code, h = ask(hi, map[string]any{"otherId": topo, "channels": true, "region": region})
	spec = h["spectral"].(map[string]any)
	if code != 200 || spec["refused"] != "spectral frames differ (LSRK vs TOPOCENT); changing frame is not offered" || h["sky"].(map[string]any)["refused"] != nil {
		t.Fatalf("topocentric: %d %v", code, h)
	}
	// a frequency axis (hi_cube_freq): the same rest frequency, so a velocity axis maps onto it
	code, h = ask(hi, map[string]any{"otherId": freq, "channels": true})
	if spec = h["spectral"].(map[string]any); code != 200 || spec["refused"] != nil || spec["unit"] != "m/s" || !strings.Contains(spec["basis"].(string), "doppler_radio") {
		t.Fatalf("freq: %d %v", code, h)
	}

	// the request's own refusals
	for name, c := range map[string]struct {
		id   int
		body map[string]any
		want int
	}{
		"itself":         {hi, map[string]any{"otherId": hi, "channels": true}, 400},
		"nothing asked":  {hi, map[string]any{"otherId": coarser}, 400},
		"points only":    {hi, map[string]any{"otherId": coarser, "points": [][]float64{{1, 1}}}, 400},
		"too many":       {hi, map[string]any{"otherId": coarser, "region": region, "points": make([][]float64, 65)}, 400},
		"empty region":   {hi, map[string]any{"otherId": coarser, "region": map[string]any{"x0": 5, "y0": 5, "x1": 5, "y1": 9}}, 400},
		"unknown other":  {hi, map[string]any{"otherId": 9999, "channels": true}, 404},
		"unknown field":  {hi, map[string]any{"otherId": coarser, "channels": true, "bogus": 1}, 400},
		"unknown source": {9999, map[string]any{"otherId": hi, "channels": true}, 404},
	} {
		if name == "too many" {
			for i := range c.body["points"].([][]float64) {
				c.body["points"].([][]float64)[i] = []float64{1, 1}
			}
		}
		if code, h, _ := askRaw(c.id, c.body); code != c.want {
			t.Fatalf("%s: %d %v", name, code, h)
		}
	}
}
