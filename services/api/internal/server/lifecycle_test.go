package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type lcEnv struct {
	t       *testing.T
	owner   *httptest.Server
	rv      *httptest.Server
	root    string
	managed string
	audit   *audit.Store
	boards  *boards.Store
	base    string
	db      *sql.DB
}

func newLifecycleEnv(t *testing.T) *lcEnv {
	t.Helper()
	base := t.TempDir()
	root, managed := filepath.Join(base, "data"), filepath.Join(base, "managed")
	for _, d := range []string{root, managed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	dv, err := derived.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, err := audit.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Derived: dv, ManagedDir: managed, Audit: au}).Handler())
	t.Cleanup(owner.Close)
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	t.Cleanup(rv.Close)
	return &lcEnv{t: t, owner: owner, rv: rv, root: root, managed: managed, audit: au, boards: bs, base: base, db: cat.DB()}
}

func (e *lcEnv) register(name string) (catalog.Dataset, string) {
	e.t.Helper()
	p := filepath.Join(e.root, name)
	if err := os.WriteFile(p, []byte("not really fits"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	var d catalog.Dataset
	json.NewDecoder(post(e.t, e.owner, p, nil).Body).Decode(&d)
	if d.ID == 0 {
		e.t.Fatalf("register %s failed", name)
	}
	return d, p
}

func (e *lcEnv) figure(datasetID int64, title string) boards.Figure {
	e.t.Helper()
	return e.figureAt(datasetID, title, 3)
}

// figureAt makes a figure of one channel; the same dataset and view is the same evidence, so another channel is another figure.
func (e *lcEnv) figureAt(datasetID int64, title string, channel int) boards.Figure {
	e.t.Helper()
	view := map[string]any{"kind": "plane", "channel": channel, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	r, out := postJSON(e.t, e.owner.URL+"/api/figures", map[string]any{"datasetId": datasetID, "title": title, "view": view, "previewPng": testPNG(e.t)})
	if r.StatusCode != 201 {
		e.t.Fatalf("figure: %d %s", r.StatusCode, out)
	}
	var f boards.Figure
	json.Unmarshal(out, &f)
	return f
}

func (e *lcEnv) board(title string, figs ...int64) boards.Board {
	e.t.Helper()
	_, out := postJSON(e.t, e.owner.URL+"/api/boards", map[string]any{"title": title})
	var b boards.Board
	json.Unmarshal(out, &b)
	for _, f := range figs {
		postJSON(e.t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))+"/items", map[string]any{"figureId": f})
	}
	return b
}

func (e *lcEnv) publish(boardID int64) string {
	e.t.Helper()
	r, out := postJSON(e.t, e.owner.URL+"/api/boards/"+itoa(int(boardID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	if r.StatusCode != 201 {
		e.t.Fatalf("publish: %d %s", r.StatusCode, out)
	}
	var pub struct{ Token string }
	json.Unmarshal(out, &pub)
	return pub.Token
}

type impactDoc struct {
	Digest     string `json:"digest"`
	Blocked    bool   `json:"blocked"`
	FileNote   string `json:"fileNote"`
	OwnedBytes *struct {
		Kind string `json:"kind"`
	} `json:"ownedBytes"`
	Groups []struct {
		Key    string `json:"key"`
		Effect string `json:"effect"`
		Count  int    `json:"count"`
	} `json:"groups"`
}

func (d impactDoc) group(key string) (effect string, count int) {
	for _, g := range d.Groups {
		if g.Key == key {
			return g.Effect, g.Count
		}
	}
	return "", 0
}

func (e *lcEnv) impact(kind string, id int64) impactDoc {
	e.t.Helper()
	r, out := get(e.t, e.owner.URL+"/api/"+kind+"/"+itoa(int(id))+"/impact")
	if r.StatusCode != 200 {
		e.t.Fatalf("impact: %d %s", r.StatusCode, out)
	}
	var im impactDoc
	json.Unmarshal(out, &im)
	return im
}

func (e *lcEnv) del(kind string, id int64, body map[string]any) (int, []byte) {
	e.t.Helper()
	r, out := postJSON(e.t, e.owner.URL+"/api/"+kind+"/"+itoa(int(id))+"/delete", body)
	return r.StatusCode, out
}

func (e *lcEnv) auditActions(kind string) []string {
	es, _ := e.audit.List(workspace.Local, kind, 0, 50)
	var out []string
	for _, x := range es {
		out = append(out, x.Action+": "+x.Detail)
	}
	return out
}

func TestImpactListsTheDependentsOfADataset(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	other, _ := e.register("b.fits")
	f := e.figure(d.ID, "Peak")
	e.figure(other.ID, "Not ours")
	b := e.board("Draft", f.ID)
	e.publish(b.ID)

	im := e.impact("datasets", d.ID)
	if eff, n := im.group("figures"); eff != "stays" || n != 1 {
		t.Errorf("figures: %s %d, want stays 1", eff, n)
	}
	if eff, n := im.group("boards"); eff != "stays" || n != 1 {
		t.Errorf("boards: %s %d", eff, n)
	}
	if eff, n := im.group("snapshots"); eff != "stays" || n != 1 {
		t.Errorf("snapshots: %s %d", eff, n)
	}
	if im.Blocked || im.FileNote != "The file on disk is not deleted." || im.OwnedBytes != nil {
		t.Errorf("a registered file is neither blocked nor owned: %+v", im)
	}
	if r, _ := get(t, e.owner.URL+"/api/datasets/99999/impact"); r.StatusCode != 404 {
		t.Errorf("impact of nothing: %d", r.StatusCode)
	}
}

func TestDeleteForgetsTheEntryNotTheFileAndKeepsPublishedReviewsWorking(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, path := e.register("a.fits")
	f := e.figure(d.ID, "Peak")
	b := e.board("Draft", f.ID)
	tok := e.publish(b.ID)
	im := e.impact("datasets", d.ID)

	if code, _ := e.del("datasets", d.ID, map[string]any{}); code != 400 {
		t.Errorf("delete without a digest: %d, want 400", code)
	}
	if code, _ := e.del("datasets", d.ID, map[string]any{"impactDigest": "0000"}); code != 409 {
		t.Errorf("delete with a wrong digest: %d, want 409", code)
	}
	if code, out := e.del("datasets", d.ID, map[string]any{"impactDigest": im.Digest}); code != 200 {
		t.Fatalf("delete: %d %s", code, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the source file must still exist: %v", err)
	}
	if r, _ := get(t, e.owner.URL+"/api/datasets/"+itoa(int(d.ID))); r.StatusCode != 404 {
		t.Errorf("deleted dataset still resolves: %d", r.StatusCode)
	}
	_, list := get(t, e.owner.URL+"/api/datasets?archived=all")
	if strings.Contains(string(list), `"a.fits"`) {
		t.Errorf("deleted dataset is still listed: %s", list)
	}
	// the figure is pinned evidence: it stays, with its picture, on its board
	if r, _ := get(t, e.owner.URL+"/api/figures/"+itoa(int(f.ID))+"/preview"); r.StatusCode != 200 {
		t.Errorf("figure preview after the dataset was deleted: %d", r.StatusCode)
	}
	// the published review still renders for the reviewer, picture included
	r, body := get(t, e.rv.URL+"/review-api/"+tok)
	if r.StatusCode != 200 || !strings.Contains(string(body), "Peak") {
		t.Fatalf("published review after the dataset was deleted: %d %s", r.StatusCode, body)
	}
	if r, _ := get(t, e.rv.URL+"/review-api/"+tok+"/preview/"+f.PreviewSHA); r.StatusCode != 200 {
		t.Errorf("review preview after the dataset was deleted: %d", r.StatusCode)
	}
	if acts := e.auditActions("dataset"); len(acts) == 0 || !strings.HasPrefix(acts[0], "deleted: ") || !strings.Contains(acts[0], "no file was deleted") {
		t.Errorf("delete not audited: %v", acts)
	}
}

func TestDeleteIsRefusedWhenTheImpactChangedAfterThePreview(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	e.figure(d.ID, "First")
	seen := e.impact("datasets", d.ID)
	e.figureAt(d.ID, "Second, made after the person looked", 4)

	code, out := e.del("datasets", d.ID, map[string]any{"impactDigest": seen.Digest})
	if code != 409 {
		t.Fatalf("delete after the impact changed: %d %s", code, out)
	}
	var got struct{ Impact impactDoc }
	json.Unmarshal(out, &got)
	if _, n := got.Impact.group("figures"); n != 2 || got.Impact.Digest == seen.Digest {
		t.Errorf("the 409 should carry the new impact: %s", out)
	}
	if r, _ := get(t, e.owner.URL+"/api/datasets/"+itoa(int(d.ID))); r.StatusCode != 200 {
		t.Errorf("a refused delete must change nothing: %d", r.StatusCode)
	}
	if code, _ := e.del("datasets", d.ID, map[string]any{"impactDigest": got.Impact.Digest}); code != 200 {
		t.Errorf("delete with the new digest: %d", code)
	}
}

func TestArchiveHidesFromListsButStillResolvesAndRestores(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	f := e.figure(d.ID, "Peak")
	b := e.board("Draft", f.ID)
	for _, c := range []struct{ kind, listKey, itemTitle string }{{"datasets", "datasets", "a.fits"}, {"figures", "figures", "Peak"}, {"boards", "boards", "Draft"}} {
		id := map[string]int64{"datasets": d.ID, "figures": f.ID, "boards": b.ID}[c.kind]
		if r, out := postJSON(t, e.owner.URL+"/api/"+c.kind+"/"+itoa(int(id))+"/archive", map[string]any{}); r.StatusCode != 200 {
			t.Fatalf("archive %s: %d %s", c.kind, r.StatusCode, out)
		}
		_, list := get(t, e.owner.URL+"/api/"+c.kind)
		if strings.Contains(string(list), c.itemTitle) {
			t.Errorf("archived %s still in the default list: %s", c.kind, list)
		}
		_, all := get(t, e.owner.URL+"/api/"+c.kind+"?archived=all")
		_, only := get(t, e.owner.URL+"/api/"+c.kind+"?archived=only")
		if !strings.Contains(string(all), c.itemTitle) || !strings.Contains(string(only), c.itemTitle) || !strings.Contains(string(only), `"archived":true`) {
			t.Errorf("archived=all/only should list the archived %s: %s | %s", c.kind, all, only)
		}
		if r, _ := get(t, e.owner.URL+"/api/"+c.kind+"/"+itoa(int(id))+"/impact"); r.StatusCode != 200 {
			t.Errorf("an archived %s must still resolve", c.kind)
		}
	}
	// a reference to an archived figure still resolves: its preview and its board
	if r, _ := get(t, e.owner.URL+"/api/figures/"+itoa(int(f.ID))+"/preview"); r.StatusCode != 200 {
		t.Errorf("archived figure preview: %d", r.StatusCode)
	}
	if r, _ := get(t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))); r.StatusCode != 200 {
		t.Errorf("archived board: %d", r.StatusCode)
	}
	if r, _ := get(t, e.owner.URL+"/api/datasets/"+itoa(int(d.ID))); r.StatusCode != 200 {
		t.Errorf("archived dataset by id: %d", r.StatusCode)
	}
	for _, c := range []struct {
		kind string
		id   int64
	}{{"datasets", d.ID}, {"figures", f.ID}, {"boards", b.ID}} {
		postJSON(t, e.owner.URL+"/api/"+c.kind+"/"+itoa(int(c.id))+"/restore", map[string]any{})
		_, list := get(t, e.owner.URL+"/api/"+c.kind)
		if strings.Contains(string(list), `"archived":true`) || !strings.Contains(string(list), `"archived":false`) {
			t.Errorf("restored %s should be in the default list: %s", c.kind, list)
		}
	}
	if acts := e.auditActions("dataset"); len(acts) != 2 || !strings.HasPrefix(acts[0], "restored: ") || !strings.HasPrefix(acts[1], "archived: ") {
		t.Errorf("archive and restore not audited: %v", acts)
	}
	// archiving twice records once
	postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))+"/archive", map[string]any{})
	postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))+"/archive", map[string]any{})
	if acts := e.auditActions("board"); len(acts) != 3 {
		t.Errorf("a repeated archive should not be audited again: %v", acts)
	}
}

func TestFigureOnABoardCannotBeDeletedAndAPublishedReviewKeepsItsPicture(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	f := e.figure(d.ID, "Peak")
	b := e.board("Draft", f.ID)
	tok := e.publish(b.ID)

	im := e.impact("figures", f.ID)
	if eff, _ := im.group("boards"); eff != "blocks" || !im.Blocked {
		t.Fatalf("a figure on a board should block its deletion: %+v", im)
	}
	if code, _ := e.del("figures", f.ID, map[string]any{"impactDigest": im.Digest}); code != 409 {
		t.Errorf("deleting a figure on a board: %d, want 409", code)
	}
	postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))+"/items/0/remove", map[string]any{})
	im = e.impact("figures", f.ID)
	if im.Blocked {
		t.Fatalf("off the board it should not be blocked: %+v", im)
	}
	if eff, n := im.group("snapshots"); eff != "stays" || n != 1 {
		t.Errorf("the published review should be listed as staying: %s %d", eff, n)
	}
	if code, out := e.del("figures", f.ID, map[string]any{"impactDigest": im.Digest}); code != 200 {
		t.Fatalf("delete figure: %d %s", code, out)
	}
	if r, _ := get(t, e.owner.URL+"/api/figures/"+itoa(int(f.ID))+"/preview"); r.StatusCode != 404 {
		t.Errorf("deleted figure still served: %d", r.StatusCode)
	}
	if r, _ := get(t, e.rv.URL+"/review-api/"+tok+"/preview/"+f.PreviewSHA); r.StatusCode != 200 {
		t.Errorf("the published review lost its picture when the figure was deleted: %d", r.StatusCode)
	}
	if r, body := get(t, e.rv.URL+"/review-api/"+tok); r.StatusCode != 200 || !strings.Contains(string(body), "Peak") {
		t.Errorf("published review after the figure was deleted: %d %s", r.StatusCode, body)
	}
}

func TestUnpublishedFigureDeleteRemovesItsPreviewBytes(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	f := e.figure(d.ID, "Alone")
	file := filepath.Join(e.base, "figures", f.PreviewSHA+".png")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	im := e.impact("figures", f.ID)
	if code, _ := e.del("figures", f.ID, map[string]any{"impactDigest": im.Digest}); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("an unreferenced preview should be removed: %v", err)
	}
}

func TestBoardWithAPublishedReviewCanOnlyBeArchived(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	f := e.figure(d.ID, "Peak")
	b := e.board("Pinned", f.ID)
	tok := e.publish(b.ID)

	im := e.impact("boards", b.ID)
	if eff, _ := im.group("snapshots"); eff != "blocks" {
		t.Fatalf("published reviews should block deleting the board: %+v", im)
	}
	if code, _ := e.del("boards", b.ID, map[string]any{"impactDigest": im.Digest}); code != 409 {
		t.Errorf("delete a published board: %d, want 409", code)
	}
	if r, _ := postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(b.ID))+"/archive", map[string]any{}); r.StatusCode != 200 {
		t.Errorf("archive: %d", r.StatusCode)
	}
	if r, _ := get(t, e.rv.URL+"/review-api/"+tok); r.StatusCode != 200 {
		t.Errorf("review of an archived board: %d", r.StatusCode)
	}
	free := e.board("Scratch")
	fim := e.impact("boards", free.ID)
	if code, _ := e.del("boards", free.ID, map[string]any{"impactDigest": fim.Digest}); code != 200 {
		t.Errorf("an unpublished board should delete: %d", code)
	}
	if r, _ := get(t, e.owner.URL+"/api/boards/"+itoa(int(free.ID))); r.StatusCode != 404 {
		t.Errorf("deleted board still opens: %d", r.StatusCode)
	}
}

func TestManagedCopyBytesAreRemovedOnlyOnTheSeparateChoiceAndAudited(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	orig, origPath := e.register("orig.fits")
	copyOf := func() (catalog.Dataset, string) {
		r, out := postJSON(t, e.owner.URL+"/api/datasets/"+itoa(int(orig.ID))+"/copy", map[string]any{})
		if r.StatusCode != 201 {
			t.Fatalf("managed copy: %d %s", r.StatusCode, out)
		}
		var c struct{ Dataset catalog.Dataset }
		json.Unmarshal(out, &c)
		return c.Dataset, c.Dataset.Path
	}
	c1, p1 := copyOf()
	im := e.impact("datasets", c1.ID)
	if im.OwnedBytes == nil || im.OwnedBytes.Kind != "managed copy" || !strings.Contains(im.FileNote, "original") {
		t.Fatalf("a managed copy should say Vesqira owns its bytes: %+v", im)
	}
	// the choice is required, and is refused for a file Vesqira did not write
	if code, _ := e.del("datasets", c1.ID, map[string]any{"impactDigest": im.Digest}); code != 400 {
		t.Errorf("deleting a managed copy without choosing about its bytes: %d, want 400", code)
	}
	oim := e.impact("datasets", orig.ID)
	if oim.OwnedBytes != nil {
		t.Errorf("a registered original is never owned bytes: %+v", oim)
	}
	if code, _ := e.del("datasets", orig.ID, map[string]any{"impactDigest": oim.Digest, "ownedBytes": "remove"}); code != 400 {
		t.Errorf("asking to remove a registered file: %d, want 400", code)
	}
	if _, err := os.Stat(origPath); err != nil {
		t.Fatalf("the original must be untouched: %v", err)
	}

	// keep: the entry goes, the bytes stay
	if code, out := e.del("datasets", c1.ID, map[string]any{"impactDigest": im.Digest, "ownedBytes": "keep"}); code != 200 || !strings.Contains(string(out), `"kept"`) {
		t.Fatalf("delete keeping bytes: %d %s", code, out)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Errorf("kept bytes are gone: %v", err)
	}

	// remove: the bytes go too, the original does not
	c2, p2 := copyOf()
	im2 := e.impact("datasets", c2.ID)
	if code, out := e.del("datasets", c2.ID, map[string]any{"impactDigest": im2.Digest, "ownedBytes": "remove"}); code != 200 || !strings.Contains(string(out), `"removed"`) {
		t.Fatalf("delete removing bytes: %d %s", code, out)
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Errorf("the managed copy's bytes should be removed: %v", err)
	}
	if _, err := os.Stat(origPath); err != nil {
		t.Errorf("the original must still exist: %v", err)
	}
	acts := e.auditActions("dataset")
	if joined := strings.Join(acts, "\n"); !strings.Contains(joined, "removed its managed copy bytes") || !strings.Contains(joined, "bytes were kept") {
		t.Errorf("managed-copy deletes not audited as such: %v", acts)
	}
}

func TestDeleteRefusedWhileAnotherDatasetUsesItAsItsReliabilityMap(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	rel, _ := e.register("rel.fits")
	psi, _ := e.register("psi.fits")
	if _, err := e.db.Exec(`INSERT INTO interpretations(dataset_id,kind,unit,reliability_id,declared_at,declared_by) VALUES(?,?,?,?,0,'user')`, psi.ID, "axial_orientation", "deg", rel.ID); err != nil {
		t.Fatal(err)
	}
	im := e.impact("datasets", rel.ID)
	if eff, _ := im.group("reliability"); eff != "blocks" || !im.Blocked {
		t.Fatalf("a reliability map in use should block: %+v", im)
	}
	if code, _ := e.del("datasets", rel.ID, map[string]any{"impactDigest": im.Digest}); code != 409 {
		t.Errorf("delete: %d, want 409", code)
	}
}

func TestOwnerMutationsAreRefusedCrossSite(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t)
	d, _ := e.register("a.fits")
	req, _ := http.NewRequest("POST", e.owner.URL+"/api/datasets/"+itoa(int(d.ID))+"/archive", strings.NewReader("{}"))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Content-Type", "application/json")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("cross-site archive: %d, want 403", r.StatusCode)
	}
}
