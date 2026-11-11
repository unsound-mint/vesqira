package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// frameOf splits a binary frame into its JSON head and payload.
func frameOf(t *testing.T, b []byte) (head []byte, payload []byte) {
	t.Helper()
	n := binary.LittleEndian.Uint32(b[:4])
	if (4+n)%8 != 0 {
		t.Fatal("the payload is not 8-byte aligned")
	}
	return bytes.TrimRight(b[4:4+n], " "), b[4+n:]
}

// Channel grid, slab and position-velocity routes end to end with the real worker on the generated fixtures: the described
// responses, the declared-axial refusal, the caps, and a repeat served from the cache.
func TestChannelMapRoutesEndToEndWithRealWorker(t *testing.T) {
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
	hi, psi := reg("hi_cube.fits"), reg("vgt_psi_deg.fits")
	url := func(id int, rest string) string { return ts.URL + "/api/datasets/" + itoa(id) + rest }

	// ----- the plan -----
	gridSchema := doc.Components.Schemas["ChannelGridPlan"]
	r, b := get(t, url(hi, "/channel-grid?first=2&last=40&stride=6&average=2"))
	if r.StatusCode != 200 {
		t.Fatalf("grid: %d %s", r.StatusCode, b)
	}
	conforms(t, gridSchema, "channel grid plan", b)
	var plan struct {
		Panels []struct{ First, Last, Channels int } `json:"panels"`
		Scale  struct{ Lo, Hi float64 }              `json:"scale"`
		Step   int                                   `json:"step"`
	}
	json.Unmarshal(b, &plan)
	if len(plan.Panels) != 7 || plan.Panels[1].First != 8 || plan.Panels[1].Last != 9 || plan.Panels[6].Last != 39 || !(plan.Scale.Lo < plan.Scale.Hi) || plan.Step != 1 {
		t.Fatalf("plan: %s", b)
	}
	before := cp.n.Load()
	cp.mu.Lock()
	cp.ops = map[string]int64{}
	cp.mu.Unlock()
	if r, _ := get(t, url(hi, "/channel-grid?first=2&last=40&stride=6&average=2")); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	_ = before
	if cp.ops["channel_grid"] != 0 {
		t.Errorf("a repeated plan reached the worker %d times", cp.ops["channel_grid"])
	}
	// a unit asked for is converted by the worker; a convention is checked here, before any worker call
	r, b = get(t, url(hi, "/channel-grid?first=0&last=9&unit=km/s"))
	conforms(t, gridSchema, "converted plan", b)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"unit":"km/s"`) && !strings.Contains(string(b), `"refused"`) {
		t.Fatalf("unit: %d %s", r.StatusCode, b)
	}
	for name, q := range map[string]string{
		"a backwards range":        "first=9&last=3",
		"no first":                 "last=9",
		"a stride of zero":         "first=0&last=9&stride=0",
		"average above the stride": "first=0&last=9&stride=2&average=3",
		"limits upside down":       "first=0&last=9&limits=5,1",
		"limits that are not":      "first=0&last=9&limits=1",
		"a bad convention":         "first=0&last=9&unit=km/s&convention=galilean",
		"a bad unit":               "first=0&last=9&unit=%3Cb%3E",
	} {
		if r, b := get(t, url(hi, "/channel-grid?"+q)); r.StatusCode < 400 || r.StatusCode >= 500 {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}
	// 64 channels at stride 1 are exactly the cap of 64 panels: allowed (the 65th panel is refused by the worker test)
	if r, b := get(t, url(hi, "/channel-grid?first=0&last=63&stride=1")); r.StatusCode != 200 {
		t.Errorf("64 panels: %d %.200s", r.StatusCode, b)
	}
	if r, b := get(t, url(hi, "/channel-grid?first=0&last=9999")); r.StatusCode >= 200 && r.StatusCode < 400 {
		t.Errorf("a channel beyond the cube: %d %s", r.StatusCode, b)
	}

	// ----- a slab -----
	r, b = get(t, url(hi, "/slab?first=10&last=15"))
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/vnd.vesqira.plane" {
		t.Fatalf("slab: %d %s", r.StatusCode, b)
	}
	head, payload := frameOf(t, b)
	conforms(t, doc.Components.Schemas["SlabHeader"], "slab header", head)
	var sh struct {
		Width, Height int
		Slab          struct{ Channels, ExcludedSamples int }
	}
	json.Unmarshal(head, &sh)
	if len(payload) != 4*sh.Width*sh.Height || sh.Slab.Channels != 6 {
		t.Fatalf("slab frame: %s", head)
	}
	if r, _ := get(t, url(hi, "/slab?first=10&last=15&step=2")); r.StatusCode != 200 {
		t.Errorf("slab preview: %d", r.StatusCode)
	}
	if r, _ := get(t, url(hi, "/slab?first=10&last=15&step=65")); r.StatusCode != 400 {
		t.Errorf("slab step: %d", r.StatusCode)
	}
	if r, _ := get(t, url(hi, "/slab?first=15&last=10")); r.StatusCode != 400 && r.StatusCode != 422 {
		t.Errorf("slab backwards: %d", r.StatusCode)
	}

	// ----- a position-velocity diagram -----
	pvBody := func(m map[string]any) map[string]any {
		base := map[string]any{"points": []any{[]any{3, 12}, []any{28, 12}}, "width": 2, "widthUnit": "pixel", "interpolation": "linear"}
		for k, v := range m {
			base[k] = v
		}
		return base
	}
	reqSchema := requestSchema(t, doc, "/api/datasets/{id}/pv", "POST")
	ask := func(id int, body map[string]any) (int, []byte, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		conforms(t, reqSchema, "pv request", raw)
		r, b := postJSON(t, url(id, "/pv"), body)
		if r.StatusCode != 200 {
			return r.StatusCode, b, nil
		}
		if r.Header.Get("Content-Type") != "application/vnd.vesqira.pv" {
			t.Fatalf("content type %q", r.Header.Get("Content-Type"))
		}
		head, payload := frameOf(t, b)
		conforms(t, doc.Components.Schemas["PvHeader"], "pv header", head)
		return 200, head, payload
	}
	code, head, payload := ask(hi, pvBody(nil))
	if code != 200 {
		t.Fatalf("pv: %d %s", code, head)
	}
	var ph struct {
		Offsets          int `json:"offsets"`
		ChannelsReturned int `json:"channels_returned"`
		NBytes           int `json:"nbytes"`
		Offset           struct{ Unit string }
	}
	json.Unmarshal(head, &ph)
	cells := ph.Offsets * ph.ChannelsReturned
	if ph.Offset.Unit != "arcsec" || ph.ChannelsReturned != 64 || len(payload) != 8*ph.Offsets+8*ph.ChannelsReturned+4*cells+cells {
		t.Fatalf("pv frame: %s (%d payload bytes)", head, len(payload))
	}
	offs := make([]float64, ph.Offsets)
	for i := range offs {
		offs[i] = math.Float64frombits(binary.LittleEndian.Uint64(payload[8*i:]))
	}
	if offs[0] != 0 || !(offs[len(offs)-1] > 0) {
		t.Errorf("offsets %v ... %v", offs[0], offs[len(offs)-1])
	}
	for i := 1; i < len(offs); i++ {
		if !(offs[i] > offs[i-1]) {
			t.Fatalf("offsets are not increasing at %d", i)
		}
	}
	cp.mu.Lock()
	cp.ops = map[string]int64{}
	cp.mu.Unlock()
	if c, _, _ := ask(hi, pvBody(nil)); c != 200 || cp.ops["pv"] != 0 {
		t.Errorf("a repeated pv reached the worker %d times", cp.ops["pv"])
	}
	// a request that names its channel range is cached too, whichever order the keys were read in
	for i := 0; i < 5; i++ {
		ask(hi, pvBody(map[string]any{"first": 2, "last": 20}))
	}
	if cp.ops["pv"] != 1 {
		t.Errorf("a repeated pv with a channel range reached the worker %d times in all, want 1", cp.ops["pv"])
	}
	// the same path with another width is another answer
	if c, _, _ := ask(hi, pvBody(map[string]any{"width": 3})); c != 200 || cp.ops["pv"] != 2 {
		t.Errorf("another width: %d, worker calls %d", c, cp.ops["pv"])
	}
	for name, m := range map[string]map[string]any{
		"an angular width without a scale that is too large": pvBody(map[string]any{"width": 1e9, "widthUnit": "arcsec"}),
		"a path with one vertex":                             pvBody(map[string]any{"points": []any{[]any{1, 1}}}),
		"a channel range backwards":                          pvBody(map[string]any{"first": 9, "last": 3}),
		"no interpolation named":                             pvBody(map[string]any{"interpolation": "cubic"}),
		"a bad convention":                                   pvBody(map[string]any{"unit": "km/s", "convention": "galilean"}),
	} {
		raw, _ := json.Marshal(m)
		r, b := postJSON(t, url(hi, "/pv"), m)
		_ = raw
		if r.StatusCode < 400 || r.StatusCode >= 500 {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}
	if r, _ := postJSON(t, url(hi, "/pv"), map[string]any{"points": []any{[]any{1, 1}, []any{5, 5}}, "width": 1, "widthUnit": "pixel", "interpolation": "linear", "bogus": 1}); r.StatusCode != 400 {
		t.Errorf("an unknown key: %d", r.StatusCode)
	}

	// ----- an orientation map is never averaged across channels or along a path -----
	if r, _ := postJSON(t, url(psi, "/interpretation"), map[string]any{"kind": "axial_orientation", "unit": "deg"}); r.StatusCode != 200 {
		t.Fatalf("declare: %d", r.StatusCode)
	}
	if r, _ := get(t, url(psi, "/slab?first=0&last=0")); r.StatusCode != 409 {
		t.Errorf("slab of an orientation map: %d", r.StatusCode)
	}
	if r, _ := get(t, url(psi, "/channel-grid?first=0&last=0")); r.StatusCode != 409 {
		t.Errorf("grid of an orientation map: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, url(psi, "/pv"), pvBody(nil)); r.StatusCode != 409 {
		t.Errorf("pv of an orientation map: %d", r.StatusCode)
	}
	// a line has no area: no spectrum or histogram is taken of it
	if r, b := get(t, url(hi, "/spectrum?shape=line&x0=0&y0=0&x1=3&y1=3")); r.StatusCode != 400 || !strings.Contains(string(b), "no area") {
		t.Errorf("spectrum of a line: %d %s", r.StatusCode, b)
	}
}

// A channel-grid figure stores its channel-grid/1 description in the figure's description column and is held to the view it
// is saved with; a saved line beside other regions does not break an ordinary figure's scene.
func TestAChannelGridFigureIsValidatedAgainstItsViewAndStoredAsTheServerReadIt(t *testing.T) {
	base := t.TempDir()
	rs, _ := sourceroot.New([]string{base})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs}).Handler())
	defer owner.Close()
	file := filepath.Join(base, "cube.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)

	grid := func(edit func(g map[string]any)) map[string]any {
		g := map[string]any{"schema": "channel-grid/1", "datasetId": d.ID, "first": 0, "last": 6, "stride": 2, "average": 1, "columns": 2, "panels": 4,
			"spectral": map[string]any{"unit": "km/s", "convention": "radio"},
			"display":  map[string]any{"colormap": "viridis", "stretch": "linear", "limits": []any{0.5, 4.5}, "limitsUnit": "K", "source": "1st to 99th percentile of the finite pixels pooled over panels 1, 4 of 4 (evenly spaced, at the preview resolution); one scale for every panel"},
			"preview":  map[string]any{"step": 2, "panelWidth": 16, "panelHeight": 12}}
		if edit != nil {
			edit(g)
		}
		return g
	}
	view := func(edit func(v map[string]any)) map[string]any {
		v := map[string]any{"kind": "channelgrid", "channel": 0, "colormap": "viridis", "stretch": "linear", "range": "custom", "limits": []any{0.5, 4.5}, "limitsUnit": "K",
			"grid": map[string]any{"first": 0, "last": 6, "stride": 2, "average": 1, "panels": 4, "previewStep": 2, "unit": "km/s", "convention": "radio", "scaleSource": "1st to 99th percentile of the finite pixels pooled over panels 1, 4 of 4 (evenly spaced, at the preview resolution); one scale for every panel"}}
		if edit != nil {
			edit(v)
		}
		return v
	}
	fig := func(g, v map[string]any) map[string]any {
		m := map[string]any{"datasetId": d.ID, "title": "Channel map", "view": v, "previewPng": testPNG(t)}
		if g != nil {
			m["channelGrid"] = g
		}
		return m
	}
	for name, body := range map[string]map[string]any{
		"a grid with no channelgrid view":     fig(grid(nil), map[string]any{"kind": "plane", "channel": 0}),
		"a channelgrid view without its grid": fig(nil, view(nil)),
		"a grid of another dataset":           fig(grid(func(g map[string]any) { g["datasetId"] = d.ID + 9 }), view(nil)),
		"a view that disagrees on the range": fig(grid(nil), view(func(v map[string]any) {
			v["grid"].(map[string]any)["last"] = 4
			v["grid"].(map[string]any)["panels"] = 3
		})),
		"limits that are not the grid's": fig(grid(nil), view(func(v map[string]any) { v["limits"] = []any{0.5, 9.0} })),
		"a range that is not custom":     fig(grid(nil), view(func(v map[string]any) { v["range"] = "percentile" })),
		"twilight":                       fig(grid(nil), view(func(v map[string]any) { v["colormap"] = "twilight" })),
		"a channel beyond the cube": fig(grid(func(g map[string]any) { g["last"] = 40; g["panels"] = 21 }), view(func(v map[string]any) {
			v["grid"].(map[string]any)["last"] = 40
			v["grid"].(map[string]any)["panels"] = 21
		})),
		"more than 64 panels":                  fig(grid(func(g map[string]any) { g["stride"] = 1; g["last"] = 99; g["panels"] = 100 }), view(nil)),
		"a scene beside the grid":              func() map[string]any { m := fig(grid(nil), view(nil)); m["scene"] = planeScene(d.ID); return m }(),
		"a limits unit that is not the file's": fig(grid(func(g map[string]any) { g["display"].(map[string]any)["limitsUnit"] = "Jy/beam" }), view(func(v map[string]any) { v["limitsUnit"] = "Jy/beam" })),
		"a working region in a grid": fig(grid(nil), view(func(v map[string]any) {
			v["region"] = map[string]any{"shape": "rect", "x0": 0, "y0": 0, "x1": 1, "y1": 1}
		})),
		"a line as a figure's working region":   fig(nil, map[string]any{"kind": "plane", "channel": 0, "region": map[string]any{"shape": "line"}}),
		"a grid with a different average":       fig(grid(func(g map[string]any) { g["average"] = 2 }), view(nil)),
		"a grid with a different source string": fig(grid(func(g map[string]any) { g["display"].(map[string]any)["source"] = "custom" }), view(nil)),
	} {
		if r, out := postJSON(t, owner.URL+"/api/figures", body); r.StatusCode != 400 {
			t.Errorf("%s: %d %s", name, r.StatusCode, out)
		}
	}
	if fs, _ := bs.Figures(); len(fs) != 0 {
		t.Fatalf("a refused grid saved a figure: %d", len(fs))
	}
	r, out := postJSON(t, owner.URL+"/api/figures", fig(grid(nil), view(nil)))
	if r.StatusCode != 201 {
		t.Fatalf("a matching grid: %d %s", r.StatusCode, out)
	}
	var f boards.Figure
	json.Unmarshal(out, &f)
	if !strings.HasPrefix(string(f.Scene), `{"schema":"channel-grid/1"`) || !strings.Contains(string(f.Scene), `"limits":[0.5,4.5]`) {
		t.Fatalf("the grid is stored as the server read it: %s", f.Scene)
	}
	// what a reviewer may see (provenance) says what the figure is and names no dataset
	var prov struct {
		View       struct{ Kind string }
		Statements []string
	}
	json.Unmarshal(f.Provenance, &prov)
	joined := strings.Join(prov.Statements, "\n")
	if prov.View.Kind != "channelgrid" || strings.Contains(string(f.Provenance), `"datasetId"`) || !strings.Contains(joined, "share one colour scale, 0.5 to 4.5 K") ||
		!strings.Contains(joined, "display previews") || !strings.Contains(joined, "km/s (radio convention)") {
		t.Errorf("provenance: %s", f.Provenance)
	}
	if got, _ := bs.Figure(f.ID); string(got.Scene) != string(f.Scene) {
		t.Errorf("not read back")
	}

	// a saved line beside a saved box in an ordinary figure's scene
	sc := planeScene(d.ID)
	sc["overlays"] = []any{map[string]any{"id": "regions", "kind": "regions", "datasetId": d.ID, "visible": true, "saved": []any{
		map[string]any{"id": 1, "shape": map[string]any{"shape": "rect", "x0": 1, "y0": 1, "x1": 2, "y1": 2}},
		map[string]any{"id": 2, "shape": map[string]any{"shape": "line", "points": []any{[]any{0, 0}, []any{5, 5}}, "width": 2, "widthUnit": "pixel"}}}}}
	r, out = postJSON(t, owner.URL+"/api/figures", map[string]any{"datasetId": d.ID, "title": "t", "view": map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}, "previewPng": testPNG(t), "scene": sc})
	if r.StatusCode != 201 || !strings.Contains(string(out), `"shape":"line","points":[[0,0],[5,5]],"width":2,"widthUnit":"pixel"`) {
		t.Fatalf("a saved line in a scene: %d %s", r.StatusCode, out)
	}
}
