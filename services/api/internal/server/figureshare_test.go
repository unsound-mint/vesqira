package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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

// A figure shared on its own (ADR 0071) through the owner API and the reviewer listener: the reviewer gets that figure and nothing
// else, the link is listed with the owner's shares (not on any board), and revocation and expiry end it like a board link.
func TestAFigureSharedOnItsOwnShowsTheReviewerOnlyThatFigure(t *testing.T) {
	t.Parallel()
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
	api := func(p string) string { return owner.URL + p }

	file := filepath.Join(root, "secret-cube-name-not-a-secret.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)
	mk := func(title string, channel int) boards.Figure {
		t.Helper()
		view := map[string]any{"kind": "plane", "channel": channel, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
		r, out := postJSON(t, api("/api/figures"), map[string]any{"datasetId": d.ID, "title": title, "view": view, "previewPng": pngOfWidth(t, 16+channel)})
		if r.StatusCode != 201 {
			t.Fatalf("create figure: %d %s", r.StatusCode, out)
		}
		var f boards.Figure
		json.Unmarshal(out, &f)
		return f
	}
	shared, other := mk("Shared view", 1), mk("Not shared", 2)
	share := func(id int64, body map[string]any) (int, []byte) {
		r, out := postJSON(t, api("/api/figures/"+itoa(int(id))+"/share"), body)
		return r.StatusCode, out
	}

	if c, _ := share(shared.ID, map[string]any{"hours": 99999}); c != 422 {
		t.Errorf("an expiry beyond 90 days: %d", c)
	}
	if c, _ := share(99999, map[string]any{"hours": 24}); c != 404 {
		t.Errorf("an unknown figure: %d", c)
	}
	if c, _ := share(shared.ID, map[string]any{"hours": 24, "supersedes": 3}); c != 400 {
		t.Errorf("an unknown field is refused, as for a board: %d", c)
	}
	code, out := share(shared.ID, map[string]any{"label": "call", "hours": 24, "question": "Is the stretch fair?"})
	if code != 201 {
		t.Fatalf("share: %d %s", code, out)
	}
	var pub struct {
		Token      string
		ReviewPath string
		Snapshot   boards.Snapshot
	}
	json.Unmarshal(out, &pub)
	if !strings.HasPrefix(pub.ReviewPath, "/#/review/") || pub.Snapshot.BoardID != 0 || pub.Snapshot.Items != 1 {
		t.Fatalf("a figure share has no board: %+v", pub)
	}

	// what the reviewer receives: the one figure, its provenance, and none of the owner's facts
	resp, rbody := get(t, rv.URL+"/review-api/"+pub.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("review: %d", resp.StatusCode)
	}
	for _, leak := range []string{root, base, "private-research-root", "datasetId", `"path"`, "scene/1", `"overlays"`, "Not shared"} {
		if strings.Contains(string(rbody), leak) {
			t.Errorf("reviewer response leaks %q: %s", leak, rbody)
		}
	}
	var rev struct {
		Snapshot struct {
			BoardTitle, Label, Question string
			Items                       []struct{ Revision, PreviewSHA, Title string }
		}
	}
	json.Unmarshal(rbody, &rev)
	if len(rev.Snapshot.Items) != 1 || rev.Snapshot.Items[0].Title != "Shared view" || rev.Snapshot.Label != "call" || rev.Snapshot.Question == "" || !strings.Contains(string(rbody), "secret-cube-name-not-a-secret.fits") {
		t.Fatalf("the reviewer should see the one figure and its provenance: %s", rbody)
	}
	// the token opens its own picture and no other; the other figure's revision cannot be commented on through it
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token+"/preview/"+rev.Snapshot.Items[0].PreviewSHA); r.StatusCode != 200 {
		t.Errorf("the shared picture: %d", r.StatusCode)
	}
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token+"/preview/"+other.PreviewSHA); r.StatusCode != 404 {
		t.Errorf("another figure's picture through the link: %d, want 404", r.StatusCode)
	}
	if r, _ := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/comments", map[string]any{"figureRevision": other.Revision, "author": "Prof", "body": "x"}); r.StatusCode == 201 {
		t.Error("a comment on a figure that was not shared was accepted")
	}
	if r, _ := postJSON(t, rv.URL+"/review-api/"+pub.Token+"/comments", map[string]any{"figureRevision": rev.Snapshot.Items[0].Revision, "author": "Prof", "body": "Looks fair."}); r.StatusCode != 201 {
		t.Errorf("a comment on the shared figure: %d", r.StatusCode)
	}
	for _, p := range []string{"/api/datasets", "/api/datasets/" + itoa(int(d.ID)) + "/plane?channel=0", "/api/figures", "/api/figures/" + itoa(int(shared.ID)) + "/preview", "/api/figure-shares", "/api/boards"} {
		if r, _ := get(t, rv.URL+p); r.StatusCode == 200 {
			t.Errorf("owner route %s reachable on the reviewer surface", p)
		}
	}

	// the owner finds it: under the figure and among all figure shares, with the comment counted; no board lists it
	var mine, all struct{ Snapshots []boards.Snapshot }
	_, b1 := get(t, api("/api/figures/"+itoa(int(shared.ID))+"/shares"))
	_, b2 := get(t, api("/api/figure-shares"))
	_, b3 := get(t, api("/api/figures/"+itoa(int(other.ID))+"/shares"))
	json.Unmarshal(b1, &mine)
	json.Unmarshal(b2, &all)
	if len(mine.Snapshots) != 1 || mine.Snapshots[0].ID != pub.Snapshot.ID || mine.Snapshots[0].Comments != 1 || mine.Snapshots[0].Title != "Shared view" || len(all.Snapshots) != 1 || strings.Contains(string(b3), `"id"`) {
		t.Errorf("figure shares: mine %s all %s other %s", b1, b2, b3)
	}
	if _, bb := get(t, api("/api/boards")); strings.Contains(string(bb), "Shared view") || strings.Contains(string(bb), `"snapshots"`) {
		t.Errorf("a figure share must not make a board: %s", bb)
	}
	// the author reads the comments and replies through the snapshot routes boards already have
	if r, cb := get(t, api("/api/snapshots/"+itoa(int(pub.Snapshot.ID))+"/comments")); r.StatusCode != 200 || !strings.Contains(string(cb), "Looks fair.") {
		t.Errorf("owner comments: %d %s", r.StatusCode, cb)
	}

	// expiry is enforced on every request, exactly as for a board
	if _, err := cat.DB().Exec(`UPDATE snapshots SET expires_at=1 WHERE id=?`, pub.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token); r.StatusCode != 404 {
		t.Errorf("an expired figure link: %d, want 404", r.StatusCode)
	}
	if _, err := cat.DB().Exec(`UPDATE snapshots SET expires_at=?`, pub.Snapshot.ExpiresAt.Unix()); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token); r.StatusCode != 200 {
		t.Fatalf("link before revocation: %d", r.StatusCode)
	}

	// revoke through the same route as a board link; the figure stays
	if r, _ := postJSON(t, api("/api/snapshots/"+itoa(int(pub.Snapshot.ID))+"/revoke"), map[string]any{}); r.StatusCode != 200 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token); r.StatusCode != 404 {
		t.Errorf("revoked figure link: %d, want 404", r.StatusCode)
	}
	if r, _ := get(t, rv.URL+"/review-api/"+pub.Token+"/preview/"+rev.Snapshot.Items[0].PreviewSHA); r.StatusCode != 404 {
		t.Errorf("the picture through a revoked link: %d, want 404", r.StatusCode)
	}
	_, b4 := get(t, api("/api/figures/"+itoa(int(shared.ID))+"/shares"))
	json.Unmarshal(b4, &mine)
	if len(mine.Snapshots) != 1 || !mine.Snapshots[0].Revoked {
		t.Errorf("a revoked share stays listed as revoked: %s", b4)
	}
	if r, _ := get(t, api("/api/figures/"+itoa(int(shared.ID))+"/preview")); r.StatusCode != 200 {
		t.Errorf("the owner's figure after revoking: %d", r.StatusCode)
	}
}

// pngOfWidth is a valid picture whose bytes differ with w, so two figures never share a content-addressed preview.
func pngOfWidth(t *testing.T, w int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, 12))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func (r *accessRig) shareFigure(cookie string, confirm bool) *httptest.ResponseRecorder {
	r.g.t.Helper()
	body := `{"label":"call","hours":24}`
	if confirm {
		body = `{"label":"call","hours":24,"confirmAccess":true}`
	}
	return r.g.do(req{method: "POST", path: fmt.Sprintf("/api/figures/%d/share", r.figure), body: body, cookie: cookie})
}

func (r *accessRig) figureShares(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.g.cat.DB().QueryRow(`SELECT COUNT(*) FROM snapshots WHERE board_id=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Sharing a figure goes through publishGate itself: a labelled figure needs the author's yes naming the data, a label the workspace
// blocks is refused whatever the request says and leaves nothing behind, and the label is frozen into the share.
func TestSharingALabelledFigureAsksAndAnAdminBlockStands(t *testing.T) {
	r := newAccessRig(t)
	if w := r.shareFigure(r.admin, false); w.Code != 201 || r.figureShares(t) != 1 {
		t.Fatalf("an unlabelled figure is shared without a question: %d %s", w.Code, w.Body)
	}
	r.setLabel(r.researcher, r.cube, "unreleased")
	w := r.shareFigure(r.admin, false)
	var refusal struct{ Error, Code string }
	json.Unmarshal(w.Body.Bytes(), &refusal)
	if w.Code != 409 || refusal.Code != "access_confirmation_required" || !strings.Contains(refusal.Error, secretCube) || !strings.Contains(refusal.Error, "Moment map") || !strings.Contains(refusal.Error, "This figure") {
		t.Fatalf("sharing without confirmation: %d %s", w.Code, w.Body)
	}
	if r.figureShares(t) != 1 {
		t.Fatal("a refused share created a snapshot")
	}
	w = r.shareFigure(r.admin, true)
	if w.Code != 201 {
		t.Fatalf("confirmed share: %d %s", w.Code, w.Body)
	}
	pub := decode[publishResponse](t, w)
	if pub.Snapshot.Access != "unreleased" {
		t.Fatalf("the share must state the label: %+v", pub.Snapshot)
	}
	rv, err := r.g.s.Boards.Review(pub.Token)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(rv); !strings.Contains(string(raw), `"access":"unreleased"`) || strings.Contains(string(raw), `"via"`) || strings.Contains(string(raw), secretCube) {
		t.Errorf("the reviewer's copy: %s", raw)
	}

	// the admin's block is final: no confirmation, no relabelling from below, nothing stored
	if c := r.g.do(req{method: "POST", path: "/api/workspaces/1/blocked-labels", body: `{"labels":["unreleased"]}`, cookie: r.admin}).Code; c != 200 {
		t.Fatalf("policy: %d", c)
	}
	n := r.figureShares(t)
	for _, confirm := range []bool{false, true} {
		w := r.shareFigure(r.admin, confirm)
		var body struct{ Code string }
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 403 || body.Code != "publishing_blocked" {
			t.Fatalf("confirm=%v: %d %s", confirm, w.Code, w.Body)
		}
	}
	if c := r.setLabel(r.researcher, r.cube, "none"); c != 403 {
		t.Errorf("a Researcher released blocked data: %d", c)
	}
	if r.figureShares(t) != n {
		t.Fatal("a blocked share created a snapshot")
	}
}

// Who may share: the roles of a board link apply. A Viewer cannot; a Researcher shares only their own figure; revoking follows the
// same rule, with no board to name the owner, so the snapshot's own author counts.
func TestWhoMayShareAFigureAndWhoMayRevokeIt(t *testing.T) {
	r := newAccessRig(t) // the figure is Ada's (the admin's)
	if c := r.shareFigure(r.view, false).Code; c != 403 {
		t.Errorf("a viewer shared a figure: %d", c)
	}
	if c := r.shareFigure(r.researcher, false).Code; c != 403 && c != 404 {
		t.Errorf("a researcher shared someone else's figure: %d", c)
	}
	w := r.shareFigure(r.admin, false)
	if w.Code != 201 {
		t.Fatalf("the author shares: %d %s", w.Code, w.Body)
	}
	pub := decode[publishResponse](t, w)
	revoke := func(cookie string) int {
		return r.g.do(req{method: "POST", path: fmt.Sprintf("/api/snapshots/%d/revoke", pub.Snapshot.ID), body: `{}`, cookie: cookie}).Code
	}
	if c := revoke(r.researcher); c != 403 && c != 404 {
		t.Errorf("a researcher revoked someone else's link: %d", c)
	}
	if _, err := r.g.s.Boards.Review(pub.Token); err != nil {
		t.Fatal("the link died without a revocation")
	}
	if c := revoke(r.admin); c != 200 {
		t.Errorf("the author could not revoke: %d", c)
	}
	if _, err := r.g.s.Boards.Review(pub.Token); err == nil {
		t.Error("the link survived its revocation")
	}
	// a Researcher's own figure: they share it and, with no board to name the owner, revoke it by being the snapshot's author
	if _, err := r.g.cat.DB().Exec(`UPDATE figures SET created_by=? WHERE id=?`, r.g.mustAccount("rex").ID, r.figure); err != nil {
		t.Fatal(err)
	}
	w = r.shareFigure(r.researcher, false)
	if w.Code != 201 {
		t.Fatalf("a researcher shares their own figure: %d %s", w.Code, w.Body)
	}
	mine := decode[publishResponse](t, w)
	if c := r.g.do(req{method: "POST", path: fmt.Sprintf("/api/snapshots/%d/revoke", mine.Snapshot.ID), body: `{}`, cookie: r.researcher}).Code; c != 200 {
		t.Errorf("a researcher could not revoke their own figure link: %d", c)
	}
	if c := r.g.do(req{method: "POST", path: fmt.Sprintf("/api/snapshots/%d/revoke", pub.Snapshot.ID), body: `{}`, cookie: r.maintainer}).Code; c != 200 {
		t.Errorf("a maintainer could not revoke a link in the workspace: %d", c)
	}
}
