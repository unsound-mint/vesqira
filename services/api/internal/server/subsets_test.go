package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type subEnv struct {
	t       *testing.T
	ts      *httptest.Server
	root    string
	derived string
	regions *regions.Store
	audit   *audit.Store
}

// stripWindow is a plane worker that ignores the window, as an older worker on a host would.
type stripWindow struct{ inner PlaneService }

func (s stripWindow) Do(ctx context.Context, req map[string]any) (worker.Reply, error) {
	delete(req, "window")
	return s.inner.Do(ctx, req)
}

// newSubEnv builds a server over the real worker and the generated fixtures. A test that needs more of the server (the access store,
// say) passes options, which see the server before it starts.
func newSubEnv(t *testing.T, wrap func(PlaneService) PlaneService, opts ...func(*Server, *catalog.Store)) *subEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	var planes PlaneService = pw
	if wrap != nil {
		planes = wrap(pw)
	}
	base := t.TempDir()
	root, derivedDir := filepath.Join(base, "data"), filepath.Join(base, "derived")
	os.Mkdir(root, 0o755)
	os.Mkdir(derivedDir, 0o755)
	rs, _ := sourceroot.New([]string{root, derivedDir})
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bs, _ := boards.Open(st.DB(), base)
	ds, _ := derived.Open(st.DB())
	rg, _ := regions.Open(st.DB())
	sb, err := subsets.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, _ := audit.Open(st.DB())
	srv := &Server{Roots: rs, Store: st, Worker: pw, Planes: planes, Boards: bs, Derived: ds, Regions: rg, Subsets: sb, Audit: au, Cache: NewPlaneCache(8 << 20),
		Analysis: &AnalysisConfig{Python: "python3", Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}
	for _, o := range opts {
		o(srv, st)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return &subEnv{t, ts, root, derivedDir, rg, au}
}

func (e *subEnv) call(method, path string, body any) (int, []byte) {
	e.t.Helper()
	return callJSON(e.t, method, e.ts.URL+path, body)
}

func (e *subEnv) register(name string) catalog.Dataset {
	e.t.Helper()
	resp := post(e.t, e.ts, filepath.Join(e.root, name), nil)
	var d catalog.Dataset
	json.NewDecoder(resp.Body).Decode(&d)
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		e.t.Fatalf("%s: register %d", name, resp.StatusCode)
	}
	return d
}

type subDoc struct {
	Subset       subsetView        `json:"subset"`
	Geometry     map[string]any    `json:"geometry"`
	Dataset      catalog.Dataset   `json:"dataset"`
	Reused       bool              `json:"reused"`
	Summary      map[string]any    `json:"summary"`
	Versions     []subsets.Version `json:"versions"`
	Materialised []map[string]any  `json:"materialised"`
}

func (e *subEnv) create(source int64, in map[string]any) (int, subDoc, []byte) {
	e.t.Helper()
	code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/subsets", source), in)
	var d subDoc
	json.Unmarshal(b, &d)
	return code, d, b
}

func (e *subEnv) mustCreate(source int64, in map[string]any) subsetView {
	e.t.Helper()
	code, d, b := e.create(source, in)
	if code != 201 {
		e.t.Fatalf("create: %d %s", code, b)
	}
	return d.Subset
}

// frame splits a plane or spectrum frame into its JSON head and payload.
func subFrame(t *testing.T, b []byte) (map[string]any, []byte) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("short frame: %q", b)
	}
	n := int(binary.LittleEndian.Uint32(b[:4]))
	var h map[string]any
	if err := json.Unmarshal(b[4:4+n], &h); err != nil {
		t.Fatalf("frame head: %v %q", err, b[4:4+n])
	}
	return h, b[4+n:]
}

func (e *subEnv) get(path string) (int, []byte) {
	e.t.Helper()
	r, b := get(e.t, e.ts.URL+path)
	return r.StatusCode, b
}

var boxDef = map[string]any{"name": "Core, every second pixel", "reason": "the source and its surroundings for the comparison with the Planck map",
	"box": map[string]any{"x0": 3, "x1": 20, "y0": 2, "y1": 15}, "step": 2, "channels": map[string]any{"c0": 10, "c1": 40}}

func def(over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range boxDef {
		out[k] = v
	}
	for k, v := range over {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

// The whole workflow against the real worker: a subset is created pinned to its source and read through its window by the view,
// plane, spectrum, coordinate and header routes; writing it as a file gives a dataset whose readouts are the same ones, byte for byte
// where they are numbers.
func TestSubsetReadsEqualAMaterialisedFile(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hi_cube.fits", "hi_cube_rotated.fits", "hi_cube_reverse_spectral.fits"} {
		t.Run(name, func(t *testing.T) {
			e := newSubEnv(t, nil)
			src := e.register(name)
			sub := e.mustCreate(src.ID, boxDef)
			if !sub.Virtual || sub.Stale || sub.Version != 1 || sub.Definition.Step != 2 || sub.Pinned.Size != src.SizeBytes {
				t.Fatalf("subset: %+v", sub)
			}
			sp := fmt.Sprintf("/api/subsets/%d", sub.ID)
			code, b := e.get(sp + "/view")
			if code != 200 {
				t.Fatalf("view: %d %s", code, b)
			}
			var v struct {
				Width, Height, Channels int
				Window                  struct{ X0, Y0, Step, C0, C1 int }
			}
			json.Unmarshal(b, &v)
			if v.Width != 9 || v.Height != 7 || v.Channels != 31 || v.Window.X0 != 3 || v.Window.Step != 2 || v.Window.C0 != 10 {
				t.Fatalf("view: %s", b)
			}
			code, b = e.call("POST", sp+"/materialise", map[string]any{})
			if code != 201 {
				t.Fatalf("materialise: %d %s", code, b)
			}
			var m subDoc
			json.Unmarshal(b, &m)
			if m.Reused || m.Dataset.ID == 0 || !strings.HasPrefix(filepath.Base(m.Dataset.Path), "subset-") {
				t.Fatalf("materialised: %s", b)
			}
			mp := fmt.Sprintf("/api/datasets/%d", m.Dataset.ID)

			// planes: the same pixels, to the bit
			for _, ch := range []int{0, 15, 30} {
				c1, pa := e.get(fmt.Sprintf("%s/plane?channel=%d", sp, ch))
				c2, pb := e.get(fmt.Sprintf("%s/plane?channel=%d", mp, ch))
				if c1 != 200 || c2 != 200 {
					t.Fatalf("plane %d: %d %d", ch, c1, c2)
				}
				ha, da := subFrame(t, pa)
				hb, db := subFrame(t, pb)
				if !bytes.Equal(da, db) || ha["width"] != hb["width"] || ha["spectral_value"] != hb["spectral_value"] || ha["channel"] != hb["channel"] {
					t.Fatalf("plane %d differs: %v %v", ch, ha, hb)
				}
			}
			// coordinates in the subset's own pixel frame
			for _, q := range []string{"x=0&y=0&channel=0", "x=8&y=6&channel=30", "x=4&y=3&channel=12"} {
				_, ca := e.get(sp + "/coord?" + q)
				_, cb := e.get(mp + "/coord?" + q)
				var a, bb map[string]any
				json.Unmarshal(ca, &a)
				json.Unmarshal(cb, &bb)
				if fmt.Sprint(a["sky"]) != fmt.Sprint(bb["sky"]) || fmt.Sprint(a["spectral"]) != fmt.Sprint(bb["spectral"]) || a["sky"] == nil {
					t.Fatalf("coord %s: %s vs %s", q, ca, cb)
				}
			}
			// spectrum of a region of the subset
			rq := "/spectrum?shape=rect&x0=1&y0=1&x1=4&y1=3"
			c1, sa := e.get(sp + rq)
			c2, sb := e.get(mp + rq)
			if c1 != 200 || c2 != 200 {
				t.Fatalf("spectrum: %d %d %s", c1, c2, sa)
			}
			_, pa := subFrame(t, sa)
			_, pb := subFrame(t, sb)
			if !bytes.Equal(pa, pb) {
				t.Fatal("the spectrum of the subset differs from the spectrum of the file written from it")
			}
			// header: the subset shows the shifted WCS, the file has it
			_, ha := e.get(sp + "/header-cards")
			_, hb := e.get(mp + "/header-cards")
			cards := func(raw []byte) map[string]string {
				var h struct{ Cards []string }
				json.Unmarshal(raw, &h)
				out := map[string]string{}
				for _, c := range h.Cards {
					if len(c) > 10 && c[8] == '=' {
						out[strings.TrimSpace(c[:8])] = strings.TrimSpace(strings.SplitN(c[9:], "/", 2)[0])
					}
				}
				return out
			}
			ca, cb := cards(ha), cards(hb)
			for _, k := range []string{"NAXIS1", "NAXIS2", "NAXIS3", "CRPIX1", "CRPIX2", "CRPIX3", "CRVAL1", "CRVAL3", "BUNIT"} {
				if ca[k] == "" || ca[k] != cb[k] {
					t.Errorf("%s: subset says %q, file says %q", k, ca[k], cb[k])
				}
			}
			if !strings.Contains(string(ha), "virtual subset") {
				t.Error("the subset's header does not say it is a window")
			}
			// the file says what it was made from; a reviewer-visible record carries the name, reason and definition but no ids or paths
			_, rb := e.get(mp + "/derived")
			var rec struct {
				Op      string
				Params  map[string]any
				Sources []int64
			}
			json.Unmarshal(rb, &rec)
			if rec.Op != "subset" || len(rec.Sources) != 1 || rec.Sources[0] != src.ID {
				t.Fatalf("derived record: %s", rb)
			}
			ps, _ := rec.Params["subset"].(map[string]any)
			if ps["name"] != boxDef["name"] || ps["version"] != 1.0 || ps["id"] != nil {
				t.Fatalf("params: %v", rec.Params)
			}
			if src, _ := rec.Params["source"].(map[string]any); src["identity"] == "" {
				t.Errorf("the record does not name the source identity: %v", rec.Params)
			}
		})
	}
}

func TestSubsetDefinitionIsCheckedAgainstTheFile(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	cat := e.register("hi_cube_no_spectral_ref.fits")
	for _, c := range []struct {
		name string
		id   int64
		in   map[string]any
		code int
		says string
	}{
		{"outside", src.ID, def(map[string]any{"box": map[string]any{"x0": 0, "x1": 99, "y0": 0, "y1": 3}}), 422, "not inside"},
		{"channels outside", src.ID, def(map[string]any{"channels": map[string]any{"c0": 0, "c1": 99}}), 422, "not inside"},
		{"no reason", src.ID, def(map[string]any{"reason": ""}), 422, "reason is required"},
		{"markup in the name", src.ID, def(map[string]any{"name": "<b>x</b>"}), 422, "name is"},
		{"step", src.ID, def(map[string]any{"step": 65}), 422, "step"},
		{"both spectral", src.ID, def(map[string]any{"world": map[string]any{"lo": -20, "hi": -10, "unit": "km/s"}}), 422, "not both"},
		{"frame", src.ID, def(map[string]any{"channels": nil, "world": map[string]any{"lo": -20, "hi": -10, "unit": "km/s", "frame": "BARY"}}), 422, "declares LSRK"},
		{"unit", src.ID, def(map[string]any{"channels": nil, "world": map[string]any{"lo": -20, "hi": -10, "unit": "Hz"}}), 422, "cannot be converted"},
		{"empty", src.ID, def(map[string]any{"channels": nil, "world": map[string]any{"lo": 500, "hi": 600, "unit": "km/s"}}), 422, "no channel centre"},
		{"undeclared axis", cat.ID, def(map[string]any{"channels": nil, "world": map[string]any{"lo": -20, "hi": -10, "unit": "km/s"}}), 422, "not fully declared"},
	} {
		code, _, b := e.create(c.id, c.in)
		if code != c.code || !strings.Contains(string(b), c.says) {
			t.Errorf("%s: %d %s", c.name, code, b)
		}
	}
	if code, b := e.get("/api/subsets"); code != 200 || strings.Contains(string(b), `"id"`) {
		t.Fatalf("a refused subset was stored: %s", b)
	}
	// a world interval is resolved to channels by the worker and the rule is stated
	sub := e.mustCreate(src.ID, def(map[string]any{"channels": nil, "world": map[string]any{"lo": -20, "hi": -10, "unit": "km/s", "frame": "LSRK"}, "step": 1, "name": "Velocity window"}))
	_, b := e.get(fmt.Sprintf("/api/subsets/%d/view", sub.ID))
	var v struct {
		Channels int
		Window   struct {
			C0, C1      int
			ChannelRule string `json:"channel_rule"`
		}
	}
	json.Unmarshal(b, &v)
	if v.Window.C0 != 12 || v.Window.C1 != 22 || v.Channels != 11 || !strings.Contains(v.Window.ChannelRule, "closed interval") {
		t.Fatalf("world interval: %s", b)
	}
	// a box from a saved region: a circle's square, clipped to the image
	rg, err := e.regions.Create(workspace.Local, "local-owner", src.ID, "Peak", regions.Shape{Shape: "circle", CX: 20, CY: 9, R: 12})
	if err != nil {
		t.Fatal(err)
	}
	sub = e.mustCreate(src.ID, map[string]any{"name": "Peak square", "reason": "everything within 12 pixels of the peak", "regionId": rg.ID})
	if b := sub.Definition.Box; b == nil || *b != (subsets.Box{X0: 8, X1: 31, Y0: 0, Y1: 21}) || sub.Definition.Region == nil || sub.Definition.Region.Name != "Peak" {
		t.Fatalf("region box: %+v", sub.Definition)
	}
	line, _ := e.regions.Create(workspace.Local, "local-owner", src.ID, "Cut", regions.Shape{Shape: "line", Points: [][]int{{1, 1}, {9, 9}}, Width: 1, WidthUnit: "pixel"})
	if code, _, b := e.create(src.ID, map[string]any{"name": "x", "reason": "a line", "regionId": line.ID}); code != 422 || !strings.Contains(string(b), "no area") {
		t.Errorf("a line: %d %s", code, b)
	}
	// what a reviewer may see of the file written from it names the region, not its id
	code, mb := e.call("POST", fmt.Sprintf("/api/subsets/%d/materialise", sub.ID), map[string]any{})
	var made subDoc
	json.Unmarshal(mb, &made)
	if code != 201 {
		t.Fatalf("materialise: %d %s", code, mb)
	}
	_, rb := e.get(fmt.Sprintf("/api/datasets/%d/derived", made.Dataset.ID))
	if !strings.Contains(string(rb), `"region":{"name":"Peak"}`) || strings.Contains(string(rb), fmt.Sprintf(`"id":%d`, rg.ID)) {
		t.Errorf("the record of the written file: %s", rb)
	}
}

// A source that changed makes the subset stale: it is read nowhere and not offered as current; re-pinning records a new version;
// a file written from the old version keeps the version it was written from.
func TestSubsetGoesStaleWhenTheSourceChangesAndIsRepinned(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	sub := e.mustCreate(src.ID, boxDef)
	sp := fmt.Sprintf("/api/subsets/%d", sub.ID)
	code, b := e.call("POST", sp+"/materialise", map[string]any{})
	if code != 201 {
		t.Fatalf("materialise: %d %s", code, b)
	}
	var first subDoc
	json.Unmarshal(b, &first)

	path := filepath.Join(e.root, "hi_cube.fits")
	later := time.Now().Add(2 * time.Hour)
	os.Chtimes(path, later, later)
	e.register("hi_cube.fits") // "Read it again": the registered file is now the changed one
	_, b = e.get("/api/subsets")
	var list struct{ Subsets []subsetView }
	json.Unmarshal(b, &list)
	if len(list.Subsets) != 1 || !list.Subsets[0].Stale || !strings.Contains(list.Subsets[0].StaleReason, "Re-pin") || list.Subsets[0].Materialised != nil {
		t.Fatalf("list: %s", b)
	}
	for _, route := range []string{"/view", "/plane?channel=0", "/coord?x=0&y=0&channel=0", "/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1", "/header-cards"} {
		code, b := e.get(sp + route)
		if code != 409 || !strings.Contains(string(b), `"subset_stale"`) {
			t.Errorf("%s of a stale subset: %d %s", route, code, b)
		}
	}
	if code, b := e.call("POST", sp+"/materialise", map[string]any{}); code != 409 || !strings.Contains(string(b), "subset_stale") {
		t.Errorf("materialise of a stale subset: %d %s", code, b)
	}
	// re-pin needs a reason and the version the person saw
	if code, b := e.call("POST", sp+"/repin", map[string]any{"reason": ""}); code != 422 {
		t.Errorf("re-pin without a reason: %d %s", code, b)
	}
	if code, b := e.call("POST", sp+"/repin", map[string]any{"reason": "the file was only touched; same bytes", "expectVersion": 7}); code != 409 {
		t.Errorf("re-pin of another version: %d %s", code, b)
	}
	code, b = e.call("POST", sp+"/repin", map[string]any{"reason": "the file was only touched; same bytes"})
	if code != 200 {
		t.Fatalf("repin: %d %s", code, b)
	}
	var rp subDoc
	json.Unmarshal(b, &rp)
	if rp.Subset.Version != 2 || rp.Subset.Kind != "repin" || rp.Subset.Stale || rp.Subset.Name != sub.Name || rp.Geometry == nil {
		t.Fatalf("repin: %s", b)
	}
	if code, _ := e.get(sp + "/plane?channel=0"); code != 200 {
		t.Fatalf("a re-pinned subset reads: %d", code)
	}
	if code, b := e.call("POST", sp+"/repin", map[string]any{"reason": "again"}); code != 409 {
		t.Errorf("re-pin of a current subset: %d %s", code, b)
	}
	// the history keeps both versions; the file made from version 1 says so, the new one is made from version 2
	_, b = e.get(sp)
	var det subDoc
	json.Unmarshal(b, &det)
	if len(det.Versions) != 2 || det.Versions[0].Kind != "create" || det.Versions[1].Kind != "repin" || det.Versions[1].Reason == det.Versions[0].Reason {
		t.Fatalf("versions: %s", b)
	}
	if len(det.Materialised) != 1 || det.Materialised[0]["version"] != 1.0 || int64(det.Materialised[0]["datasetId"].(float64)) != first.Dataset.ID {
		t.Fatalf("materialised: %v", det.Materialised)
	}
	code, b = e.call("POST", sp+"/materialise", map[string]any{})
	var second subDoc
	json.Unmarshal(b, &second)
	if code != 201 || second.Reused || second.Dataset.ID == first.Dataset.ID {
		t.Fatalf("a new version is a new file: %d %s", code, b)
	}
	_, rb := e.get(fmt.Sprintf("/api/datasets/%d/derived", first.Dataset.ID))
	if !strings.Contains(string(rb), `"version":1`) {
		t.Errorf("the first file lost its version: %s", rb)
	}
	// an edit is a new version too, with another window
	code, b = e.call("POST", sp, def(map[string]any{"name": "Narrower", "reason": "only the inner part is needed", "box": map[string]any{"x0": 5, "x1": 10, "y0": 5, "y1": 10}, "expectVersion": 2}))
	var ed subDoc
	json.Unmarshal(b, &ed)
	if code != 200 || ed.Subset.Version != 3 || ed.Subset.Kind != "edit" || ed.Subset.Definition.Box.X0 != 5 {
		t.Fatalf("edit: %d %s", code, b)
	}
	if code, b := e.call("POST", sp, def(map[string]any{"expectVersion": 2})); code != 409 {
		t.Errorf("an edit from an old version: %d %s", code, b)
	}
}

// Writing a subset as a file twice for the same definition, version and source bytes gives the one file; a changed file or a
// correction that changes what it would hold gives another.
func TestSubsetMaterialisationIsReusedAndCarriesTheCorrections(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	sub := e.mustCreate(src.ID, def(map[string]any{"step": 1}))
	sp := fmt.Sprintf("/api/subsets/%d", sub.ID)
	mat := func() (int, subDoc) {
		code, b := e.call("POST", sp+"/materialise", map[string]any{})
		var d subDoc
		json.Unmarshal(b, &d)
		if code != 200 && code != 201 {
			t.Fatalf("materialise: %d %s", code, b)
		}
		return code, d
	}
	c1, a := mat()
	c2, b := mat()
	if c1 != 201 || c2 != 200 || !b.Reused || a.Dataset.ID != b.Dataset.ID {
		t.Fatalf("reuse: %d %d %+v", c1, c2, b)
	}
	if n := len(globFits(t, e.derived)); n != 1 {
		t.Fatalf("%d files written", n)
	}
	// a file that was changed on disk is not reused
	if err := os.WriteFile(a.Dataset.Path, append(mustRead(t, a.Dataset.Path), make([]byte, 2880)...), 0o644); err != nil {
		t.Fatal(err)
	}
	c3, c := mat()
	if c3 != 201 || c.Dataset.ID == a.Dataset.ID {
		t.Fatalf("a changed file was reused: %d %+v", c3, c)
	}
	// a correction in force changes what the file would hold: a new file that carries it as HISTORY, and the subset's own readouts
	code, rb := e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections", src.ID), nil)
	var l struct{ Last int64 }
	json.Unmarshal(rb, &l)
	code, rb = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections", src.ID), map[string]any{"action": "set", "wcs": map[string]any{"CRVAL3": -30000.0}, "reason": "the velocity reference was off by two km/s", "expect": l.Last})
	if code != 201 {
		t.Fatalf("correction: %d %s", code, rb)
	}
	_, ca := e.get(sp + "/coord?x=0&y=0&channel=0")
	var coordV map[string]any
	json.Unmarshal(ca, &coordV)
	if sv := coordV["spectral"].(map[string]any)["value"].(float64); sv != -20000.0 { // source channel 10 under the corrected CRVAL3
		t.Fatalf("the virtual view does not carry the correction: %s", ca)
	}
	c4, d := mat()
	if c4 != 201 || d.Dataset.ID == c.Dataset.ID {
		t.Fatalf("a correction did not make a new file: %d %+v", c4, d)
	}
	_, hb := e.get(fmt.Sprintf("/api/datasets/%d/header-cards", d.Dataset.ID))
	for _, want := range []string{"VESQWCSC", "corrected keywords: CRVAL3", "virtual subset", "BUNIT"} {
		if !strings.Contains(string(hb), want) {
			t.Errorf("the written file's header lacks %q:\n%s", want, hb)
		}
	}
	_, cb := e.get(fmt.Sprintf("/api/datasets/%d/coord?x=0&y=0&channel=0", d.Dataset.ID))
	json.Unmarshal(cb, &coordV)
	if sv := coordV["spectral"].(map[string]any)["value"].(float64); sv != -20000.0 {
		t.Fatalf("the file does not read as the corrected subset: %s", cb)
	}
	_, rec := e.get(fmt.Sprintf("/api/datasets/%d/derived", d.Dataset.ID))
	if !strings.Contains(string(rec), "corrections") {
		t.Errorf("the record does not say which corrections were applied: %s", rec)
	}
	// the correction's impact preview names the subset as live
	code, ib := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/preview", src.ID), map[string]any{"action": "set", "wcs": map[string]any{"CRVAL3": -29000.0}, "reason": "again"})
	if code != 200 || !strings.Contains(string(ib), `"key":"subsets"`) {
		t.Errorf("correction impact: %d %s", code, ib)
	}
}

func globFits(t *testing.T, dir string) []string {
	m, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeletingASourceListsItsSubsetsAndRemovesThem(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	other := e.register("hi_cube_rotated.fits")
	a := e.mustCreate(src.ID, boxDef)
	e.mustCreate(src.ID, def(map[string]any{"name": "Second"}))
	keep := e.mustCreate(other.ID, boxDef)
	code, b := e.call("POST", fmt.Sprintf("/api/subsets/%d/materialise", a.ID), map[string]any{})
	if code != 201 {
		t.Fatalf("materialise: %d %s", code, b)
	}
	var m subDoc
	json.Unmarshal(b, &m)
	_, ib := e.get(fmt.Sprintf("/api/datasets/%d/impact", src.ID))
	var im struct {
		Digest string
		Groups []struct {
			Key, Effect, Text string
			Count             int
		}
	}
	json.Unmarshal(ib, &im)
	var gotSubsets, gotDerived bool
	for _, g := range im.Groups {
		if g.Key == "subsets" && g.Effect == "removed" && g.Count == 2 && strings.Contains(g.Text, "stay") {
			gotSubsets = true
		}
		if g.Key == "derived" && g.Count == 1 {
			gotDerived = true
		}
	}
	if !gotSubsets || !gotDerived {
		t.Fatalf("impact: %s", ib)
	}
	if code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/delete", src.ID), map[string]any{"impactDigest": im.Digest}); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	_, lb := e.get("/api/subsets")
	var list struct{ Subsets []subsetView }
	json.Unmarshal(lb, &list)
	if len(list.Subsets) != 1 || list.Subsets[0].ID != keep.ID {
		t.Fatalf("subsets after the delete: %s", lb)
	}
	if code, _ := e.get(fmt.Sprintf("/api/datasets/%d/header-cards", m.Dataset.ID)); code != 200 {
		t.Fatalf("the file written from the subset must stay a dataset: %d", code)
	}
	if code, _ := e.get(fmt.Sprintf("/api/subsets/%d/view", a.ID)); code != 404 {
		t.Fatalf("a deleted source's subset: %d", code)
	}
	// deleting a subset keeps the source and the files made from it
	if code, _ := e.call("POST", fmt.Sprintf("/api/subsets/%d/delete", keep.ID), map[string]any{}); code != 200 {
		t.Fatal("delete subset")
	}
	if code, _ := e.get(fmt.Sprintf("/api/datasets/%d/view", other.ID)); code != 200 {
		t.Fatal("the source went with its subset")
	}
}

// A subset is read only through the routes that apply its window; every other route of a dataset is not offered for it, and the
// ones that cannot be windowed yet say so instead of answering for the source's pixels.
func TestSubsetRefusesWhatItCannotAnswerForTheWindow(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, nil)
	src := e.register("hi_cube.fits")
	sub := e.mustCreate(src.ID, boxDef)
	sp := fmt.Sprintf("/api/subsets/%d", sub.ID)
	for _, route := range []string{"/convert?x=0&y=0&channel=0", "/channel-grid", "/slab?chan_start=0&chan_end=2", "/region-histogram?shape=rect", "/conversions"} {
		if code, _ := e.get(sp + route); code != 404 && code != 405 {
			t.Errorf("%s answered for a subset: %d", route, code)
		}
	}
	if code, b := e.get(sp + "/spectrum?shape=rect&x0=0&y0=0&x1=2&y1=2&mask=1:exclude"); code != 409 || !strings.Contains(string(b), "not applied to a virtual subset") {
		t.Errorf("masks on a subset: %d %s", code, b)
	}
	// a subset of an orientation map is never averaged: the dataset's declaration governs its subsets too
	vgt := e.register("vgt_psi_deg.fits")
	if code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/interpretation", vgt.ID), map[string]any{"kind": "axial_orientation", "unit": "deg"}); code != 200 && code != 201 {
		t.Fatalf("declare: %d %s", code, b)
	}
	vs := e.mustCreate(vgt.ID, map[string]any{"name": "Corner", "reason": "a look at one corner", "box": map[string]any{"x0": 0, "x1": 3, "y0": 0, "y1": 3}})
	if code, b := e.get(fmt.Sprintf("/api/subsets/%d/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1", vs.ID)); code != 409 || !strings.Contains(string(b), "axial") {
		t.Errorf("a spectrum of an orientation subset: %d %s", code, b)
	}
	if code, b := e.get(fmt.Sprintf("/api/subsets/%d/plane?channel=0&step=2", vs.ID)); code != 400 || !strings.Contains(string(b), "orientation") {
		t.Errorf("a preview of an orientation subset: %d %s", code, b)
	}
	if code, _ := e.get(fmt.Sprintf("/api/subsets/%d/plane?channel=0", vs.ID)); code != 200 {
		t.Errorf("a plane of an orientation subset: %d", code)
	}
	// the file written from it is the same kind of map, said by the operation, so it is not averaged either
	code, mb := e.call("POST", fmt.Sprintf("/api/subsets/%d/materialise", vs.ID), map[string]any{})
	var made subDoc
	json.Unmarshal(mb, &made)
	if code != 201 || made.Dataset.Interpretation == nil || made.Dataset.Interpretation.Kind != "axial_orientation" || made.Dataset.Interpretation.Unit != "deg" || made.Dataset.Interpretation.DeclaredBy == "user" {
		t.Errorf("a file written from an orientation subset: %d %s", code, mb)
	}
	if code, b := e.get(fmt.Sprintf("/api/datasets/%d/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1", made.Dataset.ID)); code != 409 {
		t.Errorf("the written orientation file was averaged: %d %s", code, b)
	}
	if code, b := e.get("/api/subsets/999/view"); code != 404 {
		t.Errorf("unknown subset: %d %s", code, b)
	}
}

// A worker that does not apply the window (older, on a host) must never pass its answer off as the subset's.
func TestSubsetNeverShowsAWorkerAnswerThatIgnoredTheWindow(t *testing.T) {
	t.Parallel()
	e := newSubEnv(t, func(p PlaneService) PlaneService { return stripWindow{p} })
	src := e.register("hi_cube.fits")
	code, _, b := e.create(src.ID, boxDef)
	if code != 502 || !strings.Contains(string(b), "did not apply the window") {
		t.Fatalf("create through a worker that ignores windows: %d %s", code, b)
	}
}

func TestWindowEchoMustMatchTheWindowSent(t *testing.T) {
	t.Parallel()
	win := `{"c0":10,"c1":40,"step":2,"x0":3,"x1":20,"y0":2,"y1":15}`
	ok := `{"window":{"x0":3,"y0":2,"step":2,"c0":10,"c1":40,"width":9,"height":7}}`
	for head, want := range map[string]bool{
		ok:            true,
		`{"width":9}`: false,
		`{"window":{"x0":0,"y0":2,"step":2,"c0":10,"c1":40,"width":9,"height":7}}`:   false,
		`{"window":{"x0":3,"y0":2,"step":1,"c0":10,"c1":40,"width":9,"height":7}}`:   false,
		`{"window":{"x0":3,"y0":2,"step":2,"c0":11,"c1":40,"width":9,"height":7}}`:   false,
		`{"window":{"x0":3,"y0":2,"step":2,"c0":10,"c1":40,"width":18,"height":14}}`: false,
	} {
		if got := windowEchoed([]byte(head), win); got != want {
			t.Errorf("%s: %v", head, got)
		}
	}
	if !windowEchoed([]byte(`{"window":{"x0":0,"y0":0,"step":1,"c0":0,"c1":63,"width":32,"height":24}}`), `{}`) {
		t.Error("an empty window is the whole source")
	}
}
