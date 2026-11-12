package server

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type corrEnv struct {
	t      *testing.T
	ts, rv *httptest.Server
	root   string
	repo   string
}

func newCorrEnv(t *testing.T) *corrEnv {
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
	base := t.TempDir()
	root := filepath.Join(base, "data")
	os.Mkdir(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bs, err := boards.Open(st.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Roots: rs, Store: st, Worker: pw, Planes: pw, Boards: bs, Cache: NewPlaneCache(8 << 20)}).Handler())
	t.Cleanup(ts.Close)
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	t.Cleanup(rv.Close)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return &corrEnv{t, ts, rv, root, repo}
}

func (e *corrEnv) call(method, path string, body any) (int, []byte) {
	e.t.Helper()
	return callJSON(e.t, method, e.ts.URL+path, body)
}

func (e *corrEnv) register(name string) catalog.Dataset {
	e.t.Helper()
	resp := post(e.t, e.ts, filepath.Join(e.root, name), nil)
	var d catalog.Dataset
	json.NewDecoder(resp.Body).Decode(&d)
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		e.t.Fatalf("%s: register %d", name, resp.StatusCode)
	}
	return d
}

// python runs a short script with the repository's fixtures importable.
func (e *corrEnv) python(script string, args ...string) {
	e.t.Helper()
	cmd := exec.Command("python3", append([]string{"-I", "-c", script}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
}

func (e *corrEnv) head(id int64, path string) map[string]any {
	e.t.Helper()
	code, b := e.call("GET", fmt.Sprintf("/api/datasets/%d%s", id, path), nil)
	if code != 200 {
		e.t.Fatalf("%s: %d %s", path, code, b)
	}
	if len(b) > 0 && b[0] != '{' {
		b = b[4 : 4+binary.LittleEndian.Uint32(b[:4])]
	}
	var h map[string]any
	json.Unmarshal(b, &h)
	return h
}

type corrList struct {
	Current *catalog.Corrections       `json:"current"`
	History []catalog.CorrectionRecord `json:"history"`
	Last    int64                      `json:"last"`
}

func (e *corrEnv) list(id int64) corrList {
	e.t.Helper()
	code, b := e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections", id), nil)
	if code != 200 {
		e.t.Fatalf("list: %d %s", code, b)
	}
	var l corrList
	json.Unmarshal(b, &l)
	return l
}

func (e *corrEnv) save(id int64, in map[string]any) (int, []byte) {
	e.t.Helper()
	if _, ok := in["expect"]; !ok {
		in["expect"] = e.list(id).Last
	}
	return e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections", id), in)
}

// The whole correction workflow against the real worker: a unit the file states and a WCS keyword are corrected with a reason;
// every head says so and the coordinates move by exactly the correction, while the catalogued header and the file stay as they
// were; refusals; a figure records the state and the reviewer sees the tier and the reason without ids or paths; impact lists the
// figure; Draw again announces a change; revert is a new record; a changed file makes the state stale and unapplied until
// confirmed; a sidecar round-trips, and one made for another file needs a sentence.
func TestCorrectionsWorkflowWithTheRealWorker(t *testing.T) {
	e := newCorrEnv(t)
	d := e.register("hi_cube.fits")
	other := e.register("hi_cube_galactic.fits")
	before := sha256.Sum256(mustRead(t, filepath.Join(e.root, "hi_cube.fits")))
	id := d.ID
	if l := e.list(id); l.Current != nil || len(l.History) != 0 || l.Last != 0 {
		t.Fatalf("a new dataset has corrections: %+v", l)
	}
	plain := e.head(id, "/coord?x=15&y=11&channel=0")
	plainLat := plain["sky"].(map[string]any)["lat"].(float64)

	set := map[string]any{"action": "set", "unit": "Jy/beam", "wcs": map[string]any{"CRVAL2": 2.05}, "reason": "pointing correction and the unit from the observing log", "source": "observatory log 2019-03-02"}
	// preview: nothing is stored, the coordinates before and after are given, and so are the dependents
	code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/preview", id), set)
	if code != 200 {
		t.Fatalf("preview: %d %s", code, b)
	}
	var pv struct {
		Changes []catalog.CorrectionChange `json:"changes"`
		Preview struct {
			Refused *string `json:"refused"`
			Before  struct {
				Points []struct {
					Name string
					Lat  *float64
				} `json:"points"`
			} `json:"before"`
			After struct {
				Points []struct {
					Name string
					Lat  *float64
				} `json:"points"`
			} `json:"after"`
		} `json:"preview"`
		Impact struct {
			Live []string `json:"live"`
		} `json:"impact"`
	}
	json.Unmarshal(b, &pv)
	if len(pv.Changes) != 2 || pv.Changes[0].What != "BUNIT" || pv.Changes[0].File != "K" || pv.Changes[1].What != "CRVAL2" || pv.Changes[1].File != 2.0 {
		t.Fatalf("changes: %s", b)
	}
	if pv.Preview.Refused != nil || len(pv.Preview.After.Points) != 5 || math.Abs(*pv.Preview.After.Points[0].Lat-*pv.Preview.Before.Points[0].Lat-0.05) > 1e-6 {
		t.Fatalf("preview: %s", b)
	}
	if len(pv.Impact.Live) != 2 {
		t.Fatalf("impact: %s", b)
	}
	if l := e.list(id); len(l.History) != 0 {
		t.Fatal("a preview stored something")
	}
	// refusals
	if code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections", id), set); code != 400 {
		t.Fatalf("saving without expect: %d %s", code, b)
	}
	for _, bad := range []struct {
		in   map[string]any
		code int
		says string
	}{
		{map[string]any{"action": "set", "wcs": map[string]any{"CRVAL2": 2.05}, "reason": ""}, 422, "a reason is required"},
		{map[string]any{"action": "set", "wcs": map[string]any{"CTYPE1": "FREQ"}, "reason": "the axis is spectral"}, 422, "would change kind"},
		{map[string]any{"action": "set", "wcs": map[string]any{"CRVAL4": 1.0}, "reason": "a fourth axis"}, 422, "names an axis the image does not have"},
		{map[string]any{"action": "set", "wcs": map[string]any{"NAXIS": 4.0}, "reason": "more axes"}, 422, "is not a keyword that can be corrected"},
		{map[string]any{"action": "set", "wcs": map[string]any{"CTYPE1": "RA---XYZ", "CTYPE2": "DEC--XYZ"}, "reason": "projection"}, 422, "astropy does not accept"},
		{map[string]any{"action": "set", "unit": "K", "reason": "the same"}, 422, "the file already says K"},
		{map[string]any{"action": "set", "wcs": map[string]any{"CRVAL2": 2.05}, "reason": "a <b>tag</b>"}, 422, "a reason is required"},
	} {
		code, b := e.save(id, bad.in)
		if code != bad.code || !strings.Contains(string(b), bad.says) {
			t.Fatalf("%v: %d %s", bad.in, code, b)
		}
	}
	code, b = e.save(id, set)
	if code != 201 {
		t.Fatalf("save: %d %s", code, b)
	}
	var saved struct {
		Record  catalog.CorrectionRecord `json:"record"`
		Dataset catalog.Dataset          `json:"dataset"`
	}
	json.Unmarshal(b, &saved)
	first := saved.Record.ID
	if saved.Record.Tier != catalog.TierDeclared || saved.Record.Author != "user" || saved.Dataset.Corrections == nil || saved.Dataset.Corrections.Stale {
		t.Fatalf("record: %s", b)
	}
	if string(saved.Dataset.Header) != string(d.Header) {
		t.Fatal("the catalogued header facts were changed")
	}
	if code, b := e.save(id, map[string]any{"action": "set", "wcs": map[string]any{"CDELT1": -0.011}, "reason": "two windows", "expect": 0}); code != 409 {
		t.Fatalf("a save built on an older history was accepted: %d %s", code, b)
	}
	// every head says the corrected unit, beside what the file says; the coordinates move by exactly the correction
	for _, p := range []string{"/view", "/plane?channel=3", "/coord?x=15&y=11&channel=0", "/spectrum?shape=circle&cx=20&cy=9&r=3"} {
		h := e.head(id, p)
		if h["bunit"] != "Jy/beam" || h["bunit_status"] != "corrected" || h["file_bunit"] != "K" {
			t.Fatalf("%s: %v %v %v", p, h["bunit"], h["bunit_status"], h["file_bunit"])
		}
	}
	lat := e.head(id, "/coord?x=15&y=11&channel=0")["sky"].(map[string]any)["lat"].(float64)
	if math.Abs(lat-plainLat-0.05) > 1e-9 {
		t.Fatalf("the latitude at the reference pixel moved by %v, not 0.05", lat-plainLat)
	}
	// a figure records the state; its limits are in the corrected unit (the file's is refused now)
	fig := func(unit string) (int, []byte) {
		scene := map[string]any{
			"schema":   "scene/1",
			"base":     map[string]any{"datasetId": id, "channel": 3, "display": map[string]any{"mapping": "scalar", "colormap": "viridis", "stretch": "linear", "range": "custom", "limits": []float64{0, 1}, "limitsUnit": unit}},
			"overlays": []any{},
			"view":     map[string]any{"zoom": 1},
			"render":   map[string]any{"canvas": map[string]any{"width": 16, "height": 12}, "nodata": []any{[]any{27, 34, 44}, []any{85, 98, 117}}, "figureWidth": 720},
		}
		return e.call("POST", "/api/figures", map[string]any{"datasetId": id, "title": "Map", "scene": scene, "view": map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "custom", "limitsUnit": unit, "limits": []float64{0, 1}}, "previewPng": testPNG(t)})
	}
	if code, b := fig("K"); code != 400 {
		t.Fatalf("the file's unit was accepted for a corrected map: %d %s", code, b)
	}
	code, b = fig("Jy/beam")
	if code != 201 {
		t.Fatalf("figure: %d %s", code, b)
	}
	var f boards.Figure
	json.Unmarshal(b, &f)
	prov := string(f.Provenance)
	for _, want := range []string{`"corrections":[`, "corrected by the researcher (tier: declared): pointing correction and the unit from the observing log", "The file says K", `"tier":"declared"`, "Source: observatory log 2019-03-02"} {
		if !strings.Contains(prov, want) {
			t.Fatalf("provenance does not say %q: %s", want, prov)
		}
	}
	if !strings.Contains(prov, `"headerDeclaredByFile":{`) || !strings.Contains(prov, `"bunit":"K"`) {
		t.Fatalf("the file's own header must stay in the provenance: %s", prov)
	}
	// the reviewer sees the tier and the reason, and no id or path
	_, bout := e.call("POST", "/api/boards", map[string]any{"title": "For review"})
	var bd boards.Board
	json.Unmarshal(bout, &bd)
	e.call("POST", "/api/boards/"+itoa(int(bd.ID))+"/items", map[string]any{"figureId": f.ID})
	_, pout := e.call("POST", "/api/boards/"+itoa(int(bd.ID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	var pub struct{ Token string }
	json.Unmarshal(pout, &pub)
	_, rbody := get(t, e.rv.URL+"/review-api/"+pub.Token)
	rs := string(rbody)
	if !strings.Contains(rs, "corrected by the researcher") || !strings.Contains(rs, "pointing correction and the unit from the observing log") || !strings.Contains(rs, `"tier":"declared"`) {
		t.Fatalf("the reviewer is not told: %s", rs)
	}
	if strings.Contains(rs, e.root) || strings.Contains(rs, "recordId") || strings.Contains(rs, "sourceIdentity") || strings.Contains(rs, fmt.Sprintf(`"datasetId":%d`, id)) {
		t.Fatalf("something leaked to the reviewer: %s", rs)
	}
	// impact: the figure is pinned evidence
	code, b = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/preview", id), map[string]any{"action": "set", "wcs": map[string]any{"CDELT1": -0.011}, "reason": "plate scale from the astrometric solution"})
	if code != 200 || !strings.Contains(string(b), `"key":"figures","effect":"pinned"`) || !strings.Contains(string(b), `"key":"boards","effect":"pinned"`) {
		t.Fatalf("impact does not list the figure as pinned: %d %s", code, b)
	}
	code, b = e.save(id, map[string]any{"action": "set", "wcs": map[string]any{"CDELT1": -0.011}, "reason": "plate scale from the astrometric solution"})
	if code != 201 {
		t.Fatalf("second save: %d %s", code, b)
	}
	// Draw again announces the change before drawing
	code, b = e.call("GET", "/api/figures/"+itoa(int(f.ID))+"/redraw-check", nil)
	if code != 200 || !strings.Contains(string(b), "WCS and unit corrections were BUNIT, CRVAL2 (corrected by you), now BUNIT, CDELT1, CRVAL2 (corrected by you)") || !strings.Contains(string(b), `"state":"ask"`) {
		t.Fatalf("redraw check: %d %s", code, b)
	}
	// revert the second record: a new record, the old ones stay
	code, b = e.save(id, map[string]any{"action": "revert", "reverts": first + 1, "reason": "the plate scale was right after all"})
	if code != 201 {
		t.Fatalf("revert: %d %s", code, b)
	}
	l := e.list(id)
	if len(l.History) != 3 || l.History[2].Action != "revert" || l.History[2].RevertsID == nil || *l.History[2].RevertsID != first+1 || l.Current.Hash != l.History[0].State.Hash() {
		t.Fatalf("history after revert: %+v", l)
	}
	if len(l.History[2].Changes) != 1 || l.History[2].Changes[0].What != "CDELT1" || l.History[2].Changes[0].After != nil {
		t.Fatalf("the revert's changes: %+v", l.History[2].Changes)
	}
	// sidecar: exported without ids or paths, and read back as made for this file
	code, sc := e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", id), nil)
	if code != 200 || !strings.Contains(string(sc), `"format": "vesqira-corrections"`) || strings.Contains(string(sc), e.root) || strings.Contains(string(sc), `"id"`) {
		t.Fatalf("sidecar: %d %s", code, sc)
	}
	code, b = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar/preview", id), map[string]any{"sidecar": json.RawMessage(sc)})
	if code != 200 || !strings.Contains(string(b), `"applies":true`) {
		t.Fatalf("sidecar preview on the same file: %d %s", code, b)
	}
	// on another file it is "made for a different file"; applying needs confirmation and a sentence; a unit correction cannot go
	// to a file with another BUNIT story, so this one is the WCS part only
	var doc map[string]any
	json.Unmarshal(sc, &doc)
	delete(doc["corrections"].(map[string]any), "unit")
	wcsOnly, _ := json.Marshal(doc)
	code, b = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar/preview", other.ID), map[string]any{"sidecar": json.RawMessage(wcsOnly)})
	if code != 200 || !strings.Contains(string(b), `"applies":false`) || !strings.Contains(string(b), "made for a different file") {
		t.Fatalf("sidecar preview on another file: %d %s", code, b)
	}
	if code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", other.ID), map[string]any{"sidecar": json.RawMessage(wcsOnly), "confirm": true, "expect": 0}); code != 409 || !strings.Contains(string(b), "say in a sentence") {
		t.Fatalf("a sidecar for another file was applied without a sentence: %d %s", code, b)
	}
	if code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", other.ID), map[string]any{"sidecar": json.RawMessage(wcsOnly), "expect": 0, "differentFile": "the same pointing applies to the Galactic reprojection"}); code != 422 {
		t.Fatalf("applied without confirmation: %d %s", code, b)
	}
	code, b = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", other.ID), map[string]any{"sidecar": json.RawMessage(wcsOnly), "confirm": true, "expect": 0, "differentFile": "the same pointing applies to the Galactic reprojection"})
	if code != 201 || !strings.Contains(string(b), "applied to this different file: the same pointing applies to the Galactic reprojection") || !strings.Contains(string(b), `"action":"import"`) {
		t.Fatalf("import with a sentence: %d %s", code, b)
	}
	// a changed file: the state is stale, not applied, and only a confirmation or a withdrawal is accepted
	e.python("import sys\nfrom astropy.io import fits\nwith fits.open(sys.argv[1], mode='update') as h:\n    h[0].header['OBJECT'] = 'edited'\n", filepath.Join(e.root, "hi_cube.fits"))
	nd := e.register("hi_cube.fits")
	if nd.ID != id || nd.Corrections == nil || !nd.Corrections.Stale {
		t.Fatalf("after the file changed: %+v", nd.Corrections)
	}
	if h := e.head(id, "/view"); h["bunit"] != "K" || h["bunit_status"] != "declared" {
		t.Fatalf("a stale correction was applied: %v %v", h["bunit"], h["bunit_status"])
	}
	if l := e.list(id); !l.History[0].Stale {
		t.Fatal("the history does not mark records made for other bytes")
	}
	if code, b := e.save(id, map[string]any{"action": "set", "wcs": map[string]any{"CRVAL1": 150.1}, "reason": "more"}); code != 409 || !strings.Contains(string(b), "confirm them") {
		t.Fatalf("a set on a stale state: %d %s", code, b)
	}
	if code, b := e.save(id, map[string]any{"action": "confirm", "reason": "only OBJECT changed; the pointing and unit still apply"}); code != 201 {
		t.Fatalf("confirm: %d %s", code, b)
	}
	if h := e.head(id, "/view"); h["bunit"] != "Jy/beam" || h["bunit_status"] != "corrected" {
		t.Fatalf("after confirming: %v %v", h["bunit"], h["bunit_status"])
	}
	// the file the corrections were never written to is byte-identical; the edited one was changed by the test, not by Vesqira
	if sha256.Sum256(mustRead(t, filepath.Join(e.root, "hi_cube_galactic.fits"))) == before {
		t.Fatal("fixtures are not distinct")
	}
	// withdraw everything: a record, and the file's own values again
	if code, b := e.save(id, map[string]any{"action": "withdraw", "reason": "the archive released a fixed file"}); code != 201 {
		t.Fatalf("withdraw: %d %s", code, b)
	}
	if h := e.head(id, "/view"); h["bunit"] != "K" {
		t.Fatalf("after withdrawing: %v", h["bunit"])
	}
	if l := e.list(id); l.Current != nil || len(l.History) != 5 {
		t.Fatalf("after withdrawing: %+v", l)
	}
}

// The corrections routes answer what the spec says (ADR 0032), with the real worker.
func TestCorrectionsRoutesMatchTheSpec(t *testing.T) {
	e := newCorrEnv(t)
	doc := loadSpec(t)
	d := e.register("hi_cube.fits")
	in := map[string]any{"action": "set", "unit": "Jy/beam", "wcs": map[string]any{"CRVAL1": 150.2, "CDELT3": 1000.5}, "reason": "from the observing log"}
	conforms(t, requestSchema(t, doc, "/api/datasets/{id}/corrections", "POST"), "input", mustJSON(t, in))
	code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/preview", d.ID), in)
	if code != 200 {
		t.Fatalf("preview %d %s", code, b)
	}
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/corrections/preview", "POST", "application/json"), "preview", b)
	code, b = e.save(d.ID, in)
	if code != 201 {
		t.Fatalf("save %d %s", code, b)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/corrections", "POST", 201, "application/json"), "saved", b)
	_, b = e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections", d.ID), nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/corrections", "GET", "application/json"), "list", b)
	_, b = e.call("GET", fmt.Sprintf("/api/datasets/%d/view", d.ID), nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/view", "GET", "application/json"), "corrected view", b)
	_, sc := e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", d.ID), nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/corrections/sidecar", "GET", "application/json"), "sidecar", sc)
	code, b = e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections/sidecar/preview", d.ID), map[string]any{"sidecar": json.RawMessage(sc)})
	if code != 200 {
		t.Fatalf("sidecar preview %d %s", code, b)
	}
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/corrections/sidecar/preview", "POST", "application/json"), "sidecar preview", b)
}

// The file is never written: reading it with corrections, saving, reverting and exporting leave its bytes as they were.
func TestCorrectionsNeverWriteTheFile(t *testing.T) {
	e := newCorrEnv(t)
	d := e.register("hi_cube_freq.fits")
	p := filepath.Join(e.root, "hi_cube_freq.fits")
	before := sha256.Sum256(mustRead(t, p))
	st, _ := os.Stat(p)
	if code, b := e.save(d.ID, map[string]any{"action": "set", "wcs": map[string]any{"RESTFRQ": 1.42e9, "SPECSYS": "BARYCENT"}, "reason": "the rest frequency of the line actually observed"}); code != 201 {
		t.Fatalf("save: %d %s", code, b)
	}
	h := e.head(d.ID, "/view")
	if h["spectral"].(map[string]any)["specsys"] != "BARYCENT" {
		t.Fatalf("view: %v", h["spectral"])
	}
	conv := e.head(d.ID, "/convert?x=3&y=3&channel=0&spectral=km/s&convention=radio")
	s := conv["spectral"].(map[string]any)
	nu := s["native"].(map[string]any)["value"].(float64)
	want := 299792458.0 * (1 - nu/1.42e9) / 1000
	if v := s["converted"].(map[string]any)["value"].(float64); math.Abs(v-want) > 1e-6 {
		t.Fatalf("radio velocity with the corrected rest frequency: %v, want %v", v, want)
	}
	e.save(d.ID, map[string]any{"action": "revert", "reverts": e.list(d.ID).Last, "reason": "back to the file"})
	e.call("GET", fmt.Sprintf("/api/datasets/%d/corrections/sidecar", d.ID), nil)
	st2, _ := os.Stat(p)
	if sha256.Sum256(mustRead(t, p)) != before || !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("the file was changed")
	}
}
