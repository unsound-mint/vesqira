package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type fakePlanes struct {
	calls []map[string]any
	reply worker.Reply
	err   error
}

func (f *fakePlanes) Do(_ context.Context, req map[string]any) (worker.Reply, error) {
	f.calls = append(f.calls, req)
	return f.reply, f.err
}

func planeServer(t *testing.T, fp PlaneService) (base string, root string, sibling string, file string, id int) {
	t.Helper()
	ts, root, sibling := newPlaneServer(t, fp)
	file = filepath.Join(root, "a.fits")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var d catalog.Dataset
	resp := post(t, ts, file, nil)
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || resp.StatusCode != 201 {
		t.Fatalf("register: %d %v", resp.StatusCode, err)
	}
	return ts.URL, root, sibling, file, int(d.ID)
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestPlaneFrameAndValidation(t *testing.T) {
	head := `{"ok":true,"nbytes":8}`
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(head), Payload: []byte{1, 2, 3, 4, 5, 6, 7, 8}}}
	base, _, _, _, id := planeServer(t, fp)
	url := func(q string) string { return base + "/api/datasets/" + itoa(id) + "/plane" + q }

	resp, body := get(t, url("?channel=3"))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/vnd.vesqira.plane" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	n := binary.LittleEndian.Uint32(body[:4])
	if strings.TrimRight(string(body[4:4+n]), " ") != head || (4+n)%8 != 0 || len(body) != 4+int(n)+8 {
		t.Fatalf("frame layout wrong: %q", body)
	}
	if fp.calls[0]["channel"] != 3 || fp.calls[0]["op"] != "plane" {
		t.Fatalf("worker got %v", fp.calls[0])
	}
	for _, q := range []string{"", "?channel=-1", "?channel=x", "?channel=1.5", "?channel=99999999999999"} {
		if r, _ := get(t, url(q)); r.StatusCode != 400 {
			t.Errorf("%q: %d, want 400", q, r.StatusCode)
		}
	}
	if r, _ := get(t, base+"/api/datasets/zz/plane?channel=0"); r.StatusCode != 400 {
		t.Errorf("bad id: %d", r.StatusCode)
	}
	if r, _ := get(t, base+"/api/datasets/9999/plane?channel=0"); r.StatusCode != 404 {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
}

func TestPlaneRequestsAreReauthorizedAndTamperEvident(t *testing.T) {
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(`{"ok":true,"nbytes":0}`)}}
	base, root, sibling, file, id := planeServer(t, fp)
	u := base + "/api/datasets/" + itoa(id) + "/plane?channel=0"

	// file replaced after registration: refuse rather than serve pixels the catalog never described
	if err := os.WriteFile(file, []byte("changed content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(t, u); r.StatusCode != 409 {
		t.Errorf("changed file: %d, want 409", r.StatusCode)
	}
	if len(fp.calls) != 0 {
		t.Fatal("worker must not run for a changed file")
	}
	// a stored path that is now a symlink out of the roots is refused with the same generic 403
	outside := filepath.Join(sibling, "o.fits")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	_ = os.Remove(file)
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if r, b := get(t, u); r.StatusCode != 403 || len(fp.calls) != 0 {
		t.Errorf("escaped symlink: %d %s calls=%d", r.StatusCode, b, len(fp.calls))
	}
	_ = root
}

func TestPlaneCacheServesRepeatsAndForgetsChangedFiles(t *testing.T) {
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(`{"ok":true,"nbytes":4}`), Payload: []byte("abcd")}}
	ts, root, _ := newPlaneServer(t, fp)
	file := filepath.Join(root, "a.fits")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, ts, file, nil).Body).Decode(&d)
	u := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/plane?channel="
	get(t, u+"1")
	get(t, u+"1")
	get(t, u+"2")
	if len(fp.calls) != 2 {
		t.Fatalf("worker calls=%d, want 2 (channel 1 cached)", len(fp.calls))
	}
}

func TestCacheEvictsLeastRecentlyUsedWithinItsByteBound(t *testing.T) {
	c := NewPlaneCache(3 * (100 + 64))
	rep := worker.Reply{Payload: make([]byte, 100)}
	for i := 0; i < 4; i++ {
		c.put(cacheKey{channel: i}, rep)
		if i == 2 {
			c.get(cacheKey{channel: 0}) // touch 0 so 1 is the oldest
		}
	}
	if _, ok := c.get(cacheKey{channel: 1}); ok {
		t.Error("channel 1 should have been evicted")
	}
	for _, k := range []int{0, 2, 3} {
		if _, ok := c.get(cacheKey{channel: k}); !ok {
			t.Errorf("channel %d evicted too early", k)
		}
	}
	c.put(cacheKey{channel: 9}, worker.Reply{Payload: make([]byte, 10_000)}) // bigger than the whole cache: not stored
	if _, ok := c.get(cacheKey{channel: 9}); ok {
		t.Error("an entry larger than the cache must not be stored")
	}
}

func TestWorkerFailuresMapToStatuses(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&worker.RequestError{Code: "bad_request", Message: "channel out of range"}, 400},
		{&worker.RequestError{Code: "unsupported", Message: "extra axis"}, 422},
		{&worker.RequestError{Code: "unreadable", Message: "cannot read file"}, 422},
		{worker.ErrTimeout, 504},
		{worker.ErrUnavailable, 502},
		{errors.New("boom"), 502},
	}
	for _, c := range cases {
		base, _, _, _, id := planeServer(t, &fakePlanes{err: c.err})
		if r, _ := get(t, base+"/api/datasets/"+itoa(id)+"/plane?channel=0"); r.StatusCode != c.want {
			t.Errorf("%v: %d, want %d", c.err, r.StatusCode, c.want)
		}
	}
	base, _, _, _, id := planeServer(t, nil)
	if r, _ := get(t, base+"/api/datasets/"+itoa(id)+"/view"); r.StatusCode != 503 {
		t.Errorf("no worker: %d", r.StatusCode)
	}
}

// End to end through the HTTP API with the real persistent worker on generated fixtures.
// Expected sky values are from the independent TAN formulas in fixtures/test_fixtures.py (tan_world)
// for pixel (x=20, y=9), channel 40, recomputed inline below.
func TestPlaneViewerEndToEndWithRealWorker(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	hdr := worker.Runner{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/fits_header.py")}, Timeout: 20 * time.Second}
	ts, root, _ := newPlaneServerWith(t, hdr, pw)
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
	id := reg("hi_cube.fits")
	api := ts.URL + "/api/datasets/" + itoa(id)

	_, vb := get(t, api+"/view")
	var view struct {
		Channels int `json:"channels"`
		Width    int `json:"width"`
		Height   int `json:"height"`
		Flags    struct {
			Declared bool  `json:"declared"`
			Channels []int `json:"channels"`
		} `json:"flags"`
	}
	json.Unmarshal(vb, &view)
	if view.Channels != 64 || view.Width != 32 || view.Height != 24 || !view.Flags.Declared || len(view.Flags.Channels) != 2 {
		t.Fatalf("view: %s", vb)
	}

	resp, body := get(t, api+"/plane?channel=40")
	if resp.StatusCode != 200 {
		t.Fatalf("plane: %d %s", resp.StatusCode, body)
	}
	hl := int(binary.LittleEndian.Uint32(body[:4]))
	var ph struct {
		Width, Height int
		Stats         struct {
			NonFinite int `json:"nonfinite_count"`
		}
	}
	json.Unmarshal(body[4:4+hl], &ph)
	px := body[4+hl:]
	if len(px) != 32*24*4 || ph.Stats.NonFinite != 9 {
		t.Fatalf("pixels=%d nonfinite=%d", len(px), ph.Stats.NonFinite)
	}
	val := func(x, y int) float32 {
		return math.Float32frombits(binary.LittleEndian.Uint32(px[4*(y*32+x):]))
	}
	if !math.IsNaN(float64(val(3, 3))) || math.IsNaN(float64(val(20, 9))) || val(20, 9) < 10 {
		t.Fatalf("NaN box must be NaN and the source peak finite and bright: nan=%v peak=%v", val(3, 3), val(20, 9))
	}

	_, cb := get(t, api+"/coord?x=20&y=9&channel=40")
	var c struct {
		Sky      struct{ Lon, Lat float64 }
		Spectral struct{ Value float64 }
	}
	json.Unmarshal(cb, &c)
	// Independent gnomonic formulas (not astropy): crpix (16,12) 1-based, crval (150, 2), cdelt 0.01 deg.
	xi, eta := math.Pi/180*(21-16)*-0.01, math.Pi/180*(10-12)*0.01
	a0, d0 := 150*math.Pi/180, 2*math.Pi/180
	den := math.Cos(d0) - eta*math.Sin(d0)
	wantRA := math.Mod(180/math.Pi*(a0+math.Atan2(xi, den))+360, 360)
	wantDec := 180 / math.Pi * math.Atan2(math.Sin(d0)+eta*math.Cos(d0), math.Hypot(xi, den))
	if math.Abs(c.Sky.Lon-wantRA) > 1e-8 || math.Abs(c.Sky.Lat-wantDec) > 1e-8 {
		t.Fatalf("sky (%v,%v), independent TAN says (%v,%v)", c.Sky.Lon, c.Sky.Lat, wantRA, wantDec)
	}
	if math.Abs(c.Spectral.Value-(-32000+40*1000)) > 1e-6 {
		t.Fatalf("spectral %v", c.Spectral.Value)
	}

	if r, _ := get(t, api+"/plane?channel=64"); r.StatusCode != 400 {
		t.Errorf("channel past the end: %d", r.StatusCode)
	}
	// A degenerate STOKES axis in front of FREQ: channel axis is FITS axis 4
	s4 := reg("cube_stokes_freq.fits")
	_, sb := get(t, ts.URL+"/api/datasets/"+itoa(s4)+"/view")
	var sv struct {
		FitsAxes struct{ Channel int } `json:"fits_axes"`
		Spectral struct{ Velocity string }
	}
	json.Unmarshal(sb, &sv)
	if sv.FitsAxes.Channel != 4 || sv.Spectral.Velocity != "unresolved" {
		t.Fatalf("stokes cube view: %s", sb)
	}
	// Region spectrum: the synthetic source peaks in channel 40 (0-based) inside this circle.
	sresp, sbody := get(t, api+"/spectrum?shape=circle&cx=20&cy=9&r=3")
	if sresp.StatusCode != 200 {
		t.Fatalf("spectrum: %d %s", sresp.StatusCode, sbody)
	}
	sh := int(binary.LittleEndian.Uint32(sbody[:4]))
	sp := sbody[4+sh:]
	best, bestV := -1, math.Inf(-1)
	for c := 0; c < 64; c++ {
		v := math.Float64frombits(binary.LittleEndian.Uint64(sp[8*64+8*c:]))
		if v > bestV {
			best, bestV = c, v
		}
	}
	if best != 40 {
		t.Fatalf("spectrum peak at channel %d, want 40", best)
	}
	if pw.Starts.Load() != 1 {
		t.Errorf("worker started %d times, want 1 for the whole session", pw.Starts.Load())
	}
}

func TestSameSecondRewriteAndCrossSiteGetAreRefused(t *testing.T) {
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(`{"ok":true,"nbytes":0}`)}}
	base, _, _, file, id := planeServer(t, fp)
	u := base + "/api/datasets/" + itoa(id) + "/plane?channel=0"
	info, _ := os.Stat(file)
	if err := os.Chtimes(file, time.Now(), info.ModTime().Add(7*time.Millisecond)); err != nil { // same size, same second
		t.Fatal(err)
	}
	if r, _ := get(t, u); r.StatusCode != 409 {
		t.Errorf("sub-second change: %d, want 409", r.StatusCode)
	}
	_ = os.Chtimes(file, time.Now(), info.ModTime())
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 403 {
		t.Errorf("cross-site GET: %v %v", r, err)
	}
	if len(fp.calls) != 0 {
		t.Error("worker ran for a refused request")
	}
}

func TestSpectrumQueryValidationAndFrame(t *testing.T) {
	head := `{"ok":true,"nbytes":8}`
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(head), Payload: make([]byte, 8)}}
	base, _, _, _, id := planeServer(t, fp)
	u := func(q string) string { return base + "/api/datasets/" + itoa(id) + "/spectrum" + q }
	resp, body := get(t, u("?shape=rect&x0=-3&y0=0&x1=5&y1=6"))
	if resp.StatusCode != 200 || (4+binary.LittleEndian.Uint32(body[:4]))%8 != 0 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if fp.calls[0]["x0"] != -3 || fp.calls[0]["shape"] != "rect" {
		t.Fatalf("worker got %v", fp.calls[0])
	}
	get(t, u("?shape=rect&x0=-3&y0=0&x1=5&y1=6"))
	if len(fp.calls) != 1 {
		t.Errorf("identical region must come from the cache, worker calls=%d", len(fp.calls))
	}
	get(t, u("?shape=circle&cx=4&cy=4&r=2"))
	get(t, u("?shape=rect&x0=-3&y0=0&x1=5&y1=7"))
	if len(fp.calls) != 3 {
		t.Errorf("distinct regions must not share a cache entry, calls=%d", len(fp.calls))
	}
	for _, q := range []string{"", "?shape=blob", "?shape=rect&x0=1", "?shape=circle&cx=1&cy=1&r=x", "?shape=rect&x0=99999999&y0=0&x1=1&y1=1"} {
		if r, _ := get(t, u(q)); r.StatusCode != 400 {
			t.Errorf("%q: %d, want 400", q, r.StatusCode)
		}
	}
}

func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// Axial interpretation, companion compatibility, server-side refusal to average angles, and the doubled-angle
// statistics, end to end with the real workers on generated fixtures.
func TestAxialOrientationEndToEndWithRealWorker(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	hdr := worker.Runner{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/fits_header.py")}, Timeout: 20 * time.Second}
	ts, root, _ := newPlaneServerWith(t, hdr, pw)
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
	psi, rel, other := reg("vgt_psi_deg.fits"), reg("vgt_reliability.fits"), reg("vgt_tangential_deg.fits")
	api := func(id int, p string) string { return ts.URL + "/api/datasets/" + itoa(id) + p }

	// a cube cannot be declared an orientation map, even by posting the declaration directly
	if r, _ := postJSON(t, api(reg("hi_cube.fits"), "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg"}); r.StatusCode != 422 {
		t.Errorf("declaring a cube axial: %d, want 422", r.StatusCode)
	}
	// not declared yet: no orientation statistics, and the plain mean is still available as a scalar
	if r, _ := get(t, api(psi, "/axial")); r.StatusCode != 409 {
		t.Errorf("undeclared map: %d, want 409", r.StatusCode)
	}
	// the unit is required and is never guessed
	if r, _ := postJSON(t, api(psi, "/interpretation"), map[string]any{"kind": "axial_orientation"}); r.StatusCode != 400 {
		t.Errorf("missing unit: %d, want 400", r.StatusCode)
	}
	// a companion on another grid is refused with reasons
	r, body := postJSON(t, api(psi, "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": other})
	if r.StatusCode != 422 || !strings.Contains(string(body), "reasons") {
		t.Errorf("incompatible companion: %d %s", r.StatusCode, body)
	}
	r, body = postJSON(t, api(psi, "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel})
	if r.StatusCode != 200 {
		t.Fatalf("declare: %d %s", r.StatusCode, body)
	}
	var d catalog.Dataset
	json.Unmarshal(body, &d)
	if d.Interpretation == nil || d.Interpretation.DeclaredBy != "user" || d.Interpretation.Unit != "deg" {
		t.Fatalf("interpretation not stored as a user declaration: %+v", d.Interpretation)
	}

	// invariant 4, enforced on the server: no arithmetic region mean of angles
	if r, _ := get(t, api(psi, "/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1")); r.StatusCode != 409 {
		t.Errorf("spectrum of an axial map: %d, want 409", r.StatusCode)
	}

	stats := func(q string) (mean *float64, defined bool) {
		resp, b := get(t, api(psi, "/axial"+q))
		if resp.StatusCode != 200 {
			t.Fatalf("axial%s: %d %s", q, resp.StatusCode, b)
		}
		var out struct {
			Stats struct {
				Unweighted struct {
					Mean    *float64 `json:"mean_deg"`
					Defined bool     `json:"defined"`
				} `json:"unweighted"`
				Weighted *struct{ N int } `json:"weighted"`
			} `json:"stats"`
		}
		json.Unmarshal(b, &out)
		if out.Stats.Weighted == nil {
			t.Errorf("declared reliability must produce weighted statistics: %s", b)
		}
		return out.Stats.Unweighted.Mean, out.Stats.Unweighted.Defined
	}
	m, ok := stats("?shape=rect&x0=0&y0=0&x1=1&y1=0") // 1 deg and 179 deg
	if !ok || m == nil || math.Min(*m, 180-*m) > 1e-9 {
		t.Errorf("1 and 179 degrees must average to 0, got %v", m)
	}
	if m, ok := stats("?shape=rect&x0=0&y0=3&x1=1&y1=3"); ok || m != nil { // 0 and 90 degrees cancel
		t.Errorf("perpendicular orientations must be undefined, got %v defined=%v", m, ok)
	}
	// the file changes after the declaration: it is flagged stale until the user declares again
	pf := filepath.Join(root, "vgt_psi_deg.fits")
	st, _ := os.Stat(pf)
	if err := os.Chtimes(pf, time.Now(), st.ModTime().Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	reg("vgt_psi_deg.fits") // re-registering refreshes size and mtime
	if r, b := get(t, api(psi, "/axial")); r.StatusCode != 409 || !strings.Contains(string(b), "declare it again") {
		t.Errorf("stale declaration: %d %s", r.StatusCode, b)
	}
	if r, _ := postJSON(t, api(psi, "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg", "reliabilityId": rel}); r.StatusCode != 200 {
		t.Fatalf("redeclare: %d", r.StatusCode)
	}
	if r, _ := get(t, api(psi, "/axial")); r.StatusCode != 200 {
		t.Errorf("after redeclaring: %d", r.StatusCode)
	}
	// clearing the declaration returns the dataset to an ordinary scalar map
	if r, _ := postJSON(t, api(psi, "/interpretation"), map[string]any{"kind": "none"}); r.StatusCode != 200 {
		t.Errorf("clear: %d", r.StatusCode)
	}
	if r, _ := get(t, api(psi, "/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1")); r.StatusCode != 200 {
		t.Errorf("scalar map spectrum after clearing: %d", r.StatusCode)
	}
}
