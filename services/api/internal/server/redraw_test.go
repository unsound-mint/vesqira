package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
)

func identityOf(size int64, mod time.Time, sampled, full string) figureDatasetIdentity {
	e := figureDatasetIdentity{Role: "source", DatasetID: 1, Name: "map.fits", SizeBytes: size, ModifiedAt: mod}
	if sampled != "" {
		e.Sampled = &identityValue{Value: sampled}
	}
	if full != "" {
		e.Full = &identityValue{Value: full}
	}
	return e
}

func datasetWith(size int64, mod time.Time, sampled, full string) catalog.Dataset {
	l := catalog.Location{Primary: true, SizeBytes: size, ModifiedAt: mod}
	if sampled != "" {
		l.Sampled = &catalog.Stamp{Value: sampled}
	}
	if full != "" {
		l.Full = &catalog.Stamp{Value: full}
	}
	return catalog.Dataset{ID: 1, Name: "map.fits", SizeBytes: size, ModifiedAt: mod, Locations: []catalog.Location{l}}
}

// What the owner is told, per source, in the words the web client shows. The statuses never claim more than the evidence:
// only equal full SHA-256 values are "unchanged"; an equal sampled fingerprint is "probable" (its blind spot is documented in
// ADR 0053); a difference in anything computed is "changed".
func TestRedrawComparisonSaysExactlyWhatTheEvidenceProves(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0).UTC()
	t1 := t0.Add(time.Hour)
	cases := []struct {
		name    string
		src     *figureDatasetIdentity
		d       catalog.Dataset
		status  string
		differs string
		text    string
	}{
		{"unchanged: equal full hashes", ptr(identityOf(10, t0, "s", "f")), datasetWith(10, t0, "s", "f"), srcUnchanged, "",
			"map.fits: unchanged (its SHA-256 equals the one recorded when the figure was drawn)."},
		{"unchanged: touched, same bytes", ptr(identityOf(10, t0, "s", "f")), datasetWith(10, t1, "s", "f"), srcUnchanged, "time",
			"map.fits: unchanged (its SHA-256 equals the recorded one; only its modification time differs)."},
		{"changed: another size", ptr(identityOf(10, t0, "s", "f")), datasetWith(12, t0, "s2", "f2"), srcChanged, "size,fingerprint",
			"map.fits: changed since the figure was drawn (size and fingerprint differ)."},
		{"changed: only the fingerprint", ptr(identityOf(10, t0, "s", "f")), datasetWith(10, t0, "s", "g"), srcChanged, "fingerprint",
			"map.fits: changed since the figure was drawn (fingerprint differs)."},
		{"changed: sampled values differ, no full hash on one side", ptr(identityOf(10, t0, "s", "")), datasetWith(10, t0, "other", ""), srcChanged, "fingerprint",
			"map.fits: changed since the figure was drawn (fingerprint differs)."},
		{"changed: only the time, bytes unknown", ptr(identityOf(10, t0, "", "")), datasetWith(10, t1, "", ""), srcChanged, "time",
			"map.fits: changed since the figure was drawn (time differs)."},
		{"probable: sampled fingerprints equal, no full hash", ptr(identityOf(10, t0, "s", "")), datasetWith(10, t0, "s", ""), srcProbable, "",
			"map.fits: probably unchanged, not verified (size and time match and the sampled fingerprint is equal, which does not prove the whole file is the same)."},
		{"probable: nothing recorded to compare", ptr(identityOf(10, t0, "", "")), datasetWith(10, t0, "", ""), srcProbable, "",
			"map.fits: probably unchanged, not verified (size and time match, but no fingerprint was recorded to compare)."},
		{"probable: the figure recorded no identity", nil, datasetWith(10, t0, "s", "f"), srcProbable, "",
			"map.fits: probably unchanged, not verified (no identity was recorded for this file when the figure was drawn)."},
	}
	for _, c := range cases {
		status, differs, text := classifySource(c.src, c.d)
		if status != c.status || strings.Join(differs, ",") != c.differs || text != c.text {
			t.Errorf("%s:\n got %s [%s] %q\nwant %s [%s] %q", c.name, status, strings.Join(differs, ","), text, c.status, c.differs, c.text)
		}
	}
	if s, _, text := classifyUnreadable("map.fits", 409, []string{"time"}); s != srcChangedOnDisk || text != "map.fits: the file on disk changed after it was registered (time differs); register it again, then draw again." {
		t.Errorf("changed on disk: %s %q", s, text)
	}
	if s, _, text := classifyUnreadable("map.fits", 404, nil); s != srcMissing || text != "map.fits: the file was not found." {
		t.Errorf("missing: %s %q", s, text)
	}
	if s, _, text := classifyUnreadable("map.fits", 403, nil); s != srcMissing || text != "map.fits: not available." {
		t.Errorf("not allowed: %s %q", s, text)
	}
}

func ptr[T any](v T) *T { return &v }

type checked struct {
	State   string         `json:"state"`
	Kind    string         `json:"kind"`
	Prompt  string         `json:"prompt"`
	Sources []redrawSource `json:"sources"`
}

func (e *idEnv) redrawCheck(id int64) checked {
	e.t.Helper()
	code, b := e.call("GET", "/api/figures/"+itoa(int(id))+"/redraw-check", nil)
	if code != 200 {
		e.t.Fatalf("redraw-check: %d %s", code, b)
	}
	var c checked
	if err := json.Unmarshal(b, &c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func renderedScene(id int64, withRender bool) map[string]any {
	s := planeScene(id)
	if withRender {
		s["render"] = map[string]any{"canvas": map[string]any{"width": 16, "height": 12}, "nodata": []any{[]any{27, 34, 44}, []any{85, 98, 117}}, "figureWidth": 720}
	}
	return s
}

var planeView = map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}

func (e *idEnv) saveFigure(d catalog.Dataset, scene any, extra map[string]any) (int, boards.Figure, []byte) {
	e.t.Helper()
	body := map[string]any{"datasetId": d.ID, "title": "Map", "view": planeView, "previewPng": testPNG(e.t)}
	if scene != nil {
		body["scene"] = scene
	}
	for k, v := range extra {
		body[k] = v
	}
	code, b := e.call("POST", "/api/figures", body)
	var f boards.Figure
	if code == 201 {
		_ = json.Unmarshal(b, &f)
	}
	return code, f, b
}

// The route compares the figure's recorded identity with the files as they are: unchanged, changed (the registered file is not
// what was drawn), changed on disk and not read again (it cannot be read), probable only (a large file has no full hash) and
// missing. A figure without a description, or without the render record, is shown as its PNG.
func TestRedrawCheckComparesTheRecordedIdentityWithTheFilesNow(t *testing.T) {
	e := newIDEnv(t)
	p := e.write("map.fits", []byte("0123456789abcdef"))
	d := e.mustRegister(p)
	code, fig, b := e.saveFigure(d, renderedScene(d.ID, true), nil)
	if code != 201 {
		t.Fatalf("figure: %d %s", code, b)
	}
	if !strings.Contains(string(fig.Scene), `"render":{"canvas":{"width":16,"height":12},"nodata":[[27,34,44],[85,98,117]],"figureWidth":720}`) {
		t.Fatalf("the render record is stored as the server read it: %s", fig.Scene)
	}

	c := e.redrawCheck(fig.ID)
	if c.State != "ready" || c.Kind != "scene" || c.Prompt != "" || len(c.Sources) != 1 || c.Sources[0].Status != srcUnchanged || !c.Sources[0].Readable || c.Sources[0].Role != "source" {
		t.Fatalf("unchanged: %+v", c)
	}

	// touched, not read again: it cannot be read, and it says what differs
	later := time.Now().Add(time.Hour)
	os.Chtimes(p, later, later)
	c = e.redrawCheck(fig.ID)
	if c.State != "blocked" || c.Prompt != promptBlocked || c.Sources[0].Status != srcChangedOnDisk || c.Sources[0].Readable ||
		c.Sources[0].Text != "map.fits: the file on disk changed after it was registered (time differs); register it again, then draw again." {
		t.Fatalf("changed on disk: %+v", c)
	}

	// read again with other bytes: readable, and the owner is asked
	os.WriteFile(p, []byte("0123456789abcdeX"), 0o644)
	os.Chtimes(p, later, later)
	e.mustRegister(p)
	c = e.redrawCheck(fig.ID)
	if c.State != "ask" || c.Prompt != "Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?" ||
		c.Sources[0].Status != srcChanged || !c.Sources[0].Readable || c.Sources[0].Text != "map.fits: changed since the figure was drawn (time and fingerprint differ)." {
		t.Fatalf("changed: %+v", c)
	}

	// the file is gone
	os.Remove(p)
	c = e.redrawCheck(fig.ID)
	if c.State != "blocked" || c.Sources[0].Status != srcMissing || c.Sources[0].Text != "map.fits: the file was not found." {
		t.Fatalf("missing: %+v", c)
	}

	// a large file has only a sampled fingerprint: equal evidence is "probable", and the owner is asked
	big := e.sparse("big.fits", 20<<20, 1)
	bd := e.mustRegister(big)
	code, bf, b := e.saveFigure(bd, renderedScene(bd.ID, true), nil)
	if code != 201 {
		t.Fatalf("figure of the large file: %d %s", code, b)
	}
	c = e.redrawCheck(bf.ID)
	if c.State != "ask" || c.Prompt != "Some data cannot be proven unchanged. Drawing from different bytes makes a different figure; the original PNG is kept. Draw it again anyway?" ||
		c.Sources[0].Status != srcProbable || !c.Sources[0].Readable || !strings.Contains(c.Sources[0].Text, "which does not prove the whole file is the same") {
		t.Fatalf("probable: %+v", c)
	}

	// a figure of a scene that does not record how it was drawn, or with no scene at all, is shown as its PNG
	q := e.write("q.fits", []byte("another file"))
	qd := e.mustRegister(q)
	_, old, _ := e.saveFigure(qd, renderedScene(qd.ID, false), nil)
	if c := e.redrawCheck(old.ID); c.State != "none" || c.Kind != "scene" || c.Prompt != promptNoRender || len(c.Sources) != 0 {
		t.Fatalf("a scene without a render record: %+v", c)
	}
	_, plain, _ := e.saveFigure(qd, nil, nil)
	if c := e.redrawCheck(plain.ID); c.State != "none" || c.Kind != "none" || c.Prompt != promptNone {
		t.Fatalf("a figure without a scene: %+v", c)
	}
	if code, _ := e.call("GET", "/api/figures/9999/redraw-check", nil); code != 404 {
		t.Errorf("unknown figure: %d", code)
	}
}

// A redrawing is a figure of its own, linked to the original, which stays exactly as it was; boards keep pointing at the figure
// they pinned. The link is part of the new revision, so a redrawing with equal pixels does not find (and cannot replace) the original.
func TestARedrawingIsANewRevisionLinkedToTheOriginalWhichIsUntouched(t *testing.T) {
	e := newIDEnv(t)
	p := e.write("map.fits", []byte("0123456789abcdef"))
	d := e.mustRegister(p)
	_, orig, b := e.saveFigure(d, renderedScene(d.ID, true), nil)
	if orig.ID == 0 {
		t.Fatalf("%s", b)
	}
	code, bb := e.call("POST", "/api/boards", map[string]any{"title": "Board"})
	if code != 201 && code != 200 {
		t.Fatalf("board: %d %s", code, bb)
	}
	var board boards.Board
	json.Unmarshal(bb, &board)
	if code, out := e.call("POST", "/api/boards/"+itoa(int(board.ID))+"/items", map[string]any{"figureId": orig.ID}); code/100 != 2 {
		t.Fatalf("add: %d %s", code, out)
	}
	before, _ := e.srv.Boards.Figure(orig.ID)

	code, re, b := e.saveFigure(d, renderedScene(d.ID, true), map[string]any{"redrawnFrom": orig.ID})
	if code != 201 {
		t.Fatalf("redrawing: %d %s", code, b)
	}
	if re.ID == orig.ID || re.Revision == orig.Revision {
		t.Fatalf("a redrawing with equal pixels must be a new figure: %d/%d %s/%s", re.ID, orig.ID, re.Revision, orig.Revision)
	}
	var id figureIdentity
	if err := json.Unmarshal(re.Identity, &id); err != nil || id.RedrawnFrom == nil || id.RedrawnFrom.FigureID != orig.ID || id.RedrawnFrom.Revision != orig.Revision {
		t.Fatalf("the link: %s", re.Identity)
	}
	// the same redrawing again is the same evidence: it finds itself, not a third figure
	_, again, _ := e.saveFigure(d, renderedScene(d.ID, true), map[string]any{"redrawnFrom": orig.ID})
	if again.ID != re.ID {
		t.Errorf("the same redrawing twice: %d and %d", re.ID, again.ID)
	}

	after, err := e.srv.Boards.Figure(orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(before)
	jb, _ := json.Marshal(after)
	if string(ja) != string(jb) || strings.Contains(string(after.Identity), "redrawnFrom") {
		t.Fatalf("the original changed:\n%s\n%s", ja, jb)
	}
	bd, err := e.srv.Boards.Board(board.ID)
	if err != nil || len(bd.Items) != 1 || bd.Items[0].Figure == nil || bd.Items[0].Figure.ID != orig.ID || bd.Items[0].Figure.Revision != orig.Revision {
		t.Fatalf("the board keeps what it pinned: %+v %v", bd.Items, err)
	}
	if strings.Contains(string(re.Provenance), "redrawnFrom") || strings.Contains(string(re.Provenance), orig.Revision) {
		t.Errorf("the link is owner-side and never part of the reviewer-safe provenance: %s", re.Provenance)
	}

	// a link must name a figure of the same dataset and kind
	other := e.mustRegister(e.write("other.fits", []byte("zzz")))
	for name, body := range map[string]map[string]any{"another dataset": {"redrawnFrom": orig.ID}, "an unknown figure": {"redrawnFrom": 9999}} {
		d2 := d
		if name == "another dataset" {
			d2 = other
		}
		if code, _, out := e.saveFigure(d2, renderedScene(d2.ID, true), body); code != 400 {
			t.Errorf("%s: %d %s, want 400", name, code, out)
		}
	}
	if code, _, out := e.saveFigure(d, renderedScene(d.ID, true), map[string]any{"redrawnFrom": 0}); code != 400 {
		t.Errorf("a link to figure 0: %d %s, want 400", code, out)
	}
	_ = filepath.Join
}

// SVG export is the owner's: it is built in the owner's browser from the stored scene, which names dataset ids. A reviewer holds
// the frozen PNG and the reviewer-safe provenance only, and has no download route for the picture in any format (ADR 0011), so
// nothing of the scene, the render record, the redraw link or an SVG can reach them.
func TestAReviewerGetsNoSceneRedrawLinkOrSVGOfAPublishedFigure(t *testing.T) {
	e := newIDEnv(t)
	d := e.mustRegister(e.write("a.fits", []byte("figure source bytes")))
	_, orig, _ := e.saveFigure(d, renderedScene(d.ID, true), nil)
	_, re, b := e.saveFigure(d, renderedScene(d.ID, true), map[string]any{"redrawnFrom": orig.ID})
	if re.ID == 0 {
		t.Fatalf("%s", b)
	}
	_, bb := e.call("POST", "/api/boards", map[string]any{"title": "Board"})
	var board boards.Board
	json.Unmarshal(bb, &board)
	e.call("POST", "/api/boards/"+itoa(int(board.ID))+"/items", map[string]any{"figureId": re.ID})
	code, pb := e.call("POST", "/api/boards/"+itoa(int(board.ID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	if code != 201 {
		t.Fatalf("publish: %d %s", code, pb)
	}
	var pub struct{ Token string }
	json.Unmarshal(pb, &pub)
	rv := httptest.NewServer((&review.Handler{Store: e.srv.Boards}).Handler())
	defer rv.Close()
	_, snap := get(t, rv.URL+"/review-api/"+pub.Token)
	for _, secret := range []string{"redrawnFrom", orig.Revision, "figureWidth", "nodata", `"canvas"`, "svg", "<metadata"} {
		if strings.Contains(string(snap), secret) {
			t.Errorf("%q reached the reviewer: %s", secret, snap)
		}
	}
	for _, path := range []string{"/review-api/" + pub.Token + "/download.svg", "/review-api/" + pub.Token + "/preview/" + re.PreviewSHA + ".svg", "/review-api/" + pub.Token + "/figures/" + itoa(int(re.ID)) + "/svg"} {
		if r, _ := get(t, rv.URL+path); r.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", path, r.StatusCode)
		}
	}
	// the owner's SVG is made in the browser: the server has no route that renders one
	if code, _ := e.call("GET", "/api/figures/"+itoa(int(re.ID))+"/download.svg", nil); code != 404 && code != 405 {
		t.Errorf("owner svg route: %d", code)
	}
}
