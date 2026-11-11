package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The conversion routes end to end with the real worker: a frequency cube asked for a radio velocity and Jy/beam as K matches
// closed forms; a file without a unit offers and converts nothing until the person declares one, and then the conversion keeps
// the "declared" label; an orientation map is refused a brightness conversion; nothing malformed reaches the worker.
func TestConversionRoutesAnswerWithNativeValuesBasisAndRefusals(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts, root, _ := newPlaneServerWith(t, pw, pw)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	strip := exec.Command("python3", "-I", "-c", "import sys\nfrom astropy.io import fits\nwith fits.open(sys.argv[1]) as h:\n    del h[0].header['BUNIT']\n    h.writeto(sys.argv[2])", filepath.Join(root, "hi_cube_freq.fits"), filepath.Join(root, "freq_nounit.fits"))
	if out, err := strip.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	register := func(name string) catalog.Dataset {
		resp := post(t, ts, filepath.Join(root, name), nil)
		if resp.StatusCode != 201 {
			t.Fatalf("%s: register %d", name, resp.StatusCode)
		}
		var d catalog.Dataset
		json.NewDecoder(resp.Body).Decode(&d)
		return d
	}
	freq, noUnit, psi := register("hi_cube_freq.fits"), register("freq_nounit.fits"), register("vgt_psi_deg.fits")
	call := func(method, path string, body any) (int, map[string]any) {
		st, b := callJSON(t, method, ts.URL+path, body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m == nil {
			m = map[string]any{"error": string(b)}
		}
		return st, m
	}
	ds := func(d catalog.Dataset, path string) string { return fmt.Sprintf("/api/datasets/%d%s", d.ID, path) }
	num := func(m map[string]any, keys ...string) float64 {
		var cur any = m
		for _, k := range keys {
			cur = cur.(map[string]any)[k]
		}
		return cur.(float64)
	}

	// velocity of the 1420.0 MHz channel, radio convention chosen by the user: c (1 - f / f0) against the closed form
	st, m := call("GET", ds(freq, "/convert?x=3&y=3&channel=0&spectral=km/s&convention=radio"), nil)
	if st != 200 {
		t.Fatalf("convert: %d %v", st, m)
	}
	const c, f0 = 299792458.0, 1.420405751768e9
	if got, want := num(m, "spectral", "converted", "value"), c*(1-1.42e9/f0)/1000; math.Abs(got-want) > 1e-6 {
		t.Fatalf("radio velocity %v, closed form %v", got, want)
	}
	if num(m, "spectral", "native", "value") != 1.42e9 || m["spectral"].(map[string]any)["convention_source"] != "user" {
		t.Fatalf("native value and the user's choice must be reported: %v", m["spectral"])
	}
	// no convention for a frequency axis: refused with the reason, not defaulted
	_, m = call("GET", ds(freq, "/convert?x=3&y=3&channel=0&spectral=km/s"), nil)
	if sp := m["spectral"].(map[string]any); sp["converted"] != nil || sp["refused"] == "" {
		t.Fatalf("a velocity without a convention must be refused: %v", sp)
	}
	// Jy/beam to K for a 60 arcsec beam at 1.42 GHz: T = S c^2 / (2 k nu^2 omega), omega = pi / (4 ln 2) theta^2
	_, m = call("GET", ds(freq, "/convert?x=3&y=3&channel=0&to=K&value=1"), nil)
	omega := math.Pi / (4 * math.Log(2)) * math.Pow(60*math.Pi/180/3600, 2)
	want := 1e-26 * c * c / (2 * 1.380649e-23 * 1.42e9 * 1.42e9 * omega)
	if got := num(m, "value", "converted", "value"); math.Abs(got/want-1) > 1e-9 {
		t.Fatalf("T = %v, closed form %v", got, want)
	}
	// frame: an FK5 file's position in Galactic
	if _, m = call("GET", ds(freq, "/convert?x=20&y=9&channel=0&frame=galactic"), nil); m["sky"].(map[string]any)["converted"] == nil {
		t.Fatalf("%v", m)
	}
	// offers
	st, m = call("GET", ds(freq, "/conversions"), nil)
	if st != 200 || m["spectral"] == nil || m["sky"] == nil || m["value"] == nil {
		t.Fatalf("conversions: %d %v", st, m)
	}

	// a file with no unit: nothing offered, nothing converted; declaring a unit enables it and it stays the person's
	_, m = call("GET", ds(noUnit, "/conversions"), nil)
	if len(m["value"].(map[string]any)["targets"].([]any)) != 0 {
		t.Fatalf("no unit must offer no conversion: %v", m["value"])
	}
	_, m = call("GET", ds(noUnit, "/convert?x=3&y=3&channel=0&to=mJy/beam&value=1"), nil)
	if v := m["value"].(map[string]any); v["converted"] != nil || v["refused"] == "" {
		t.Fatalf("%v", v)
	}
	if st, b := call("POST", ds(noUnit, "/unit"), map[string]any{"unit": "Jy/beam"}); st != 200 {
		t.Fatalf("declare: %d %v", st, b)
	}
	_, m = call("GET", ds(noUnit, "/convert?x=3&y=3&channel=0&to=mJy/beam&value=1"), nil)
	if got := num(m, "value", "converted", "value"); got != 1000 {
		t.Fatalf("declared Jy/beam to mJy/beam: %v", got)
	}
	if m["value"].(map[string]any)["converted"].(map[string]any)["unit_status"] != "user" || m["value"].(map[string]any)["native"].(map[string]any)["unit_status"] != "user" {
		t.Fatalf("a declared unit must stay declared after conversion: %v", m["value"])
	}

	// an orientation map has angles, not a brightness
	if st, b := call("POST", ds(psi, "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg"}); st != 200 {
		t.Fatalf("declare axial: %d %v", st, b)
	}
	if st, _ := call("GET", ds(psi, "/convert?x=3&y=3&channel=0&to=rad&value=1"), nil); st != 409 {
		t.Fatalf("a brightness conversion of an orientation map: %d, want 409", st)
	}

	// malformed requests are 400 and never reach the worker
	for _, bad := range []string{"spectral=a%3Cb", "convention=sideways", "frame=ecliptic", "to=%27", "value=NaN", "value=abc", "value=1e999", "spectral=parsec"} {
		st, _ := call("GET", ds(freq, "/convert?x=3&y=3&channel=0&"+bad), nil)
		if st != 400 { // spectral=parsec is valid unit text that the worker does not offer: a bad request as well
			t.Errorf("%s: %d, want 400", url.QueryEscape(bad), st)
		}
	}
	for _, bad := range []string{"x=-1&y=0&channel=0", "x=0&y=0", "x=99&y=0&channel=0", "x=0&y=0&channel=99"} {
		if st, _ := call("GET", ds(freq, "/convert?"+bad+"&frame=icrs"), nil); st != 400 && st != 422 {
			t.Errorf("%s: %d", bad, st)
		}
	}
}

// Only what was asked for is sent, the declared unit travels with it, and the cache key separates requests.
func TestConvertSendsOnlyValidatedFieldsAndCachesPerRequest(t *testing.T) {
	ep := &echoPlanes{}
	ts, root, _ := newPlaneServer(t, ep)
	file := filepath.Join(root, "a.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, ts, file, nil).Body).Decode(&d)
	base := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/convert?x=1&y=2&channel=3"
	if r, _ := get(t, base+"&to=mK&value=0.5"); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	get(t, base+"&to=mK&value=0.5")
	if len(ep.calls) != 1 {
		t.Fatalf("an identical request must be cached: %d worker calls", len(ep.calls))
	}
	get(t, base+"&to=mK&value=0.6")
	get(t, base+"&to=K&value=0.5")
	if len(ep.calls) != 3 {
		t.Fatalf("a different value or unit is a different request: %d calls", len(ep.calls))
	}
	req := ep.calls[0]
	if req["op"] != "convert" || req["to"] != "mK" || req["value"] != 0.5 || req["x"] != 1 || req["y"] != 2 || req["channel"] != 3 {
		t.Fatalf("%v", req)
	}
	if _, has := req["spectral"]; has {
		t.Fatalf("a part that was not asked for must not be sent: %v", req)
	}
	n := len(ep.calls)
	get(t, base+"&frame=nope")
	get(t, base+"&to=%3Cb%3E")
	if len(ep.calls) != n {
		t.Fatal("a malformed request reached the worker")
	}
	if cb, _ := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/unit", map[string]any{"unit": "Jy/beam"}); cb != 200 {
		t.Fatal(cb)
	}
	get(t, base+"&to=mK&value=0.5")
	if len(ep.calls) != n+1 || ep.calls[n]["declared_bunit"] != "Jy/beam" {
		t.Fatalf("the declared unit must travel with the request and key the cache: %v", ep.calls[len(ep.calls)-1])
	}
}
