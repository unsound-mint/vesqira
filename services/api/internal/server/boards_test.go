package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type hdrWorker struct{}

func (hdrWorker) Header(_ context.Context, _ string) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true,"naxis":3,"bitpix":-32,"axes":[{"axis":1,"length":8,"ctype":"RA---TAN","cunit":"deg","crval":150,"cdelt":-0.01}],"bunit":"K","radesys":"ICRS","specsys":null}`), nil
}

func testPNG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 16, 12))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// The whole sharing path: figure -> board -> frozen snapshot -> reviewer. What the reviewer can read is checked on
// the wire, not just in the types: no source path, no dataset id, and no owner route on the reviewer's surface.
func TestFigureBoardReviewPathExposesNoSourceToTheReviewer(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "private-research-root")
	os.MkdirAll(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
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
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()

	file := filepath.Join(root, "secret-cube-name-not-a-secret.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)

	api := func(p string) string { return owner.URL + p }
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	fig := map[string]any{"datasetId": d.ID, "title": "Peak channel", "caption": "H I at 8 km/s", "view": view, "previewPng": testPNG(t)}

	// refusals: unknown keys in the view (nothing else may ride along), a bad preview, a path smuggled as text
	badView := map[string]any{"datasetId": d.ID, "title": "x", "view": map[string]any{"kind": "plane", "path": file}, "previewPng": testPNG(t)}
	if r, _ := postJSON(t, api("/api/figures"), badView); r.StatusCode != 400 {
		t.Errorf("unknown view key: %d, want 400", r.StatusCode)
	}
	if r, _ := postJSON(t, api("/api/figures"), map[string]any{"datasetId": d.ID, "title": "x", "view": view, "previewPng": base64.StdEncoding.EncodeToString([]byte("not a png"))}); r.StatusCode != 422 {
		t.Errorf("bad preview: %d, want 422", r.StatusCode)
	}
	for name, v := range map[string]map[string]any{
		"channel past the data": {"kind": "plane", "channel": 99},
		"unit not in the list":  {"kind": "plane", "spectralUnit": "<script>alert(1)</script>"},
		"unit the header lacks": {"kind": "plane", "limitsUnit": "Jy/beam"},
	} {
		if r, _ := postJSON(t, api("/api/figures"), map[string]any{"datasetId": d.ID, "title": "x", "view": v, "previewPng": testPNG(t)}); r.StatusCode != 400 {
			t.Errorf("%s: %d, want 400", name, r.StatusCode)
		}
	}
	r, out := postJSON(t, api("/api/figures"), fig)
	if r.StatusCode != 201 {
		t.Fatalf("create figure: %d %s", r.StatusCode, out)
	}
	var f boards.Figure
	json.Unmarshal(out, &f)
	if !strings.Contains(string(f.Provenance), "secret-cube-name-not-a-secret.fits") || strings.Contains(string(f.Provenance), root) {
		t.Fatalf("provenance should name the file but never its path: %s", f.Provenance)
	}

	_, bout := postJSON(t, api("/api/boards"), map[string]any{"title": "For review"})
	var b boards.Board
	json.Unmarshal(bout, &b)
	postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items"), map[string]any{"figureId": f.ID})
	if r, _ := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/publish"), map[string]any{"hours": 99999}); r.StatusCode != 422 {
		t.Errorf("an expiry beyond 90 days: %d", r.StatusCode)
	}
	r, pout := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/publish"), map[string]any{"label": "v1", "hours": 24})
	if r.StatusCode != 201 {
		t.Fatalf("publish: %d %s", r.StatusCode, pout)
	}
	var pub struct {
		Token    string
		Snapshot boards.Snapshot
	}
	json.Unmarshal(pout, &pub)

	// the reviewer's surface
	resp, rbody := get(t, rv.URL+"/review-api/"+pub.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("review: %d", resp.StatusCode)
	}
	for _, leak := range []string{root, base, "private-research-root", "datasetId", `"path"`} {
		if strings.Contains(string(rbody), leak) {
			t.Errorf("reviewer response leaks %q: %s", leak, rbody)
		}
	}
	if !strings.Contains(string(rbody), "Peak channel") {
		t.Errorf("reviewer cannot see the figure: %s", rbody)
	}
	for _, p := range []string{"/api/datasets", "/api/datasets/" + itoa(int(d.ID)) + "/plane?channel=0", "/api/boards", "/api/figures", "/api/roots"} {
		if r, _ := get(t, rv.URL+p); r.StatusCode == 200 {
			t.Errorf("owner route %s reachable on the reviewer surface", p)
		}
	}
	// a reviewer token does nothing on the owner API, and the owner API refuses a cross-site page
	req, _ := http.NewRequest("GET", owner.URL+"/api/boards", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("cross-site read of the owner API: %d", r.StatusCode)
	}

	// a thread: the reviewer marks a place, the author replies and closes it; the reviewer cannot close it, and the
	// owner-only routes are not on the review surface
	var fr struct {
		Snapshot struct{ Items []struct{ Revision string } }
	}
	json.Unmarshal(rbody, &fr)
	cr, cb := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/comments", map[string]any{"figureRevision": fr.Snapshot.Items[0].Revision, "author": "Prof", "body": "What is this?", "anchor": map[string]any{"kind": "point", "x": 0.3, "y": 0.4}})
	if cr.StatusCode != 201 {
		t.Fatalf("anchored comment: %d %s", cr.StatusCode, cb)
	}
	var thread boards.Comment
	json.Unmarshal(cb, &thread)
	if r, _ := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/comments", map[string]any{"figureRevision": fr.Snapshot.Items[0].Revision, "body": "x", "anchor": map[string]any{"kind": "point", "x": 3, "y": 0.4}}); r.StatusCode != 422 {
		t.Errorf("a place outside the picture: %d, want 422", r.StatusCode)
	}
	sid := itoa(int(pub.Snapshot.ID))
	if r, _ := postJSON(t, owner.URL+"/api/snapshots/"+sid+"/comments", map[string]any{"parentId": thread.ID, "body": "A sidelobe."}); r.StatusCode != 201 {
		t.Errorf("owner reply: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, owner.URL+"/api/comments/"+itoa(int(thread.ID))+"/resolve", map[string]any{"resolved": true}); r.StatusCode != 200 {
		t.Errorf("resolve: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, owner.URL+"/api/comments/99999/resolve", map[string]any{"resolved": true}); r.StatusCode != 404 {
		t.Errorf("resolving an unknown comment: %d", r.StatusCode)
	}
	for _, p := range []string{"/api/comments/" + itoa(int(thread.ID)) + "/resolve", "/api/snapshots/" + sid + "/comments"} {
		if r, _ := postJSON(t, rv.URL+p, map[string]any{"resolved": true, "parentId": thread.ID, "body": "x"}); r.StatusCode == 200 || r.StatusCode == 201 {
			t.Errorf("owner route %s reachable on the review surface: %d", p, r.StatusCode)
		}
	}
	if _, rb2 := get(t, rv.URL+"/review-api/"+pub.Token); !strings.Contains(string(rb2), "A sidelobe.") || !strings.Contains(string(rb2), "resolvedAt") {
		t.Errorf("the reviewer sees the reply and that the thread is closed: %s", rb2)
	}

	// the reviewer's outcome is accepted and validated; preflight is the author's and not on the review surface
	if r, _ := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/outcome", map[string]any{"outcome": "changes_requested", "note": "rescale", "author": "Prof"}); r.StatusCode != 201 {
		t.Errorf("outcome: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/outcome", map[string]any{"outcome": "valid"}); r.StatusCode != 422 {
		t.Errorf("an outcome that is not one of the three: %d, want 422", r.StatusCode)
	}
	if r, pb := get(t, owner.URL+"/api/boards/"+itoa(int(b.ID))+"/preflight"); r.StatusCode != 200 || !strings.Contains(string(pb), "warnings") {
		t.Errorf("preflight: %d %s", r.StatusCode, pb)
	}
	if r, _ := get(t, rv.URL+"/api/boards/"+itoa(int(b.ID))+"/preflight"); r.StatusCode == 200 {
		t.Errorf("preflight reachable on the review surface")
	}

	// revoke: the link stops working, the evidence stays
	postJSON(t, api("/api/snapshots/"+itoa(int(pub.Snapshot.ID))+"/revoke"), map[string]any{})
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token); r.StatusCode != 404 {
		t.Errorf("revoked link: %d, want 404", r.StatusCode)
	}
	// the downloaded picture carries its own provenance, and the reviewer surface has no download route
	if r, db := get(t, api("/api/figures/"+itoa(int(f.ID))+"/download")); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(string(db), "iTXt") || !strings.Contains(string(db), "Provenance") || strings.Contains(string(db), root) {
		t.Errorf("figure download: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := get(t, rv.URL+"/api/figures/"+itoa(int(f.ID))+"/download"); r.StatusCode == 200 {
		t.Error("the download route is reachable on the review surface")
	}
	if r, _ := get(t, api("/api/figures/"+itoa(int(f.ID))+"/preview")); r.StatusCode != 200 {
		t.Errorf("owner preview after revoke: %d", r.StatusCode)
	}
}
