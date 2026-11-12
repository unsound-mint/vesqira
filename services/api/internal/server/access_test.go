package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Access labels (ADR 0069) on the group server, through HTTP: who may set and lower a label, who may change the publishing policy,
// what publishing a board does with a label, and that a blocked label cannot be published or pushed by any route.

type accessRig struct {
	g                                   *group
	admin, researcher, maintainer, view string
	cube, derivedCube                   int64 // dataset ids; derivedCube was made from cube
	board                               int64
	figure                              int64
}

const secretCube = "SECRETCUBE-91c.fits"

func newAccessRig(t *testing.T) *accessRig {
	t.Helper()
	g, _, _ := pushGroup(t)
	var err error
	if g.s.Boards, err = boards.Open(g.cat.DB(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if g.s.Access, err = access.Open(g.cat.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err = derived.Open(g.cat.DB()); err != nil {
		t.Fatal(err)
	}
	g.h = g.s.Handler()
	r := &accessRig{g: g}
	r.admin, _ = g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	r.researcher = g.invite(workspace.Local, "researcher", "rex")
	r.maintainer = g.invite(workspace.Local, "maintainer", "mia")
	r.view = g.invite(workspace.Local, "viewer", "vic")
	rex := g.mustAccount("rex")
	db := g.cat.DB()
	ins := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		id, _ := res.LastInsertId()
		return id
	}
	dataset := func(name, by string) int64 {
		return ins(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,workspace_id,created_by) VALUES(?,?,1,1,1,'{}',1,?)`, "/d/"+name, name, by)
	}
	r.cube = dataset(secretCube, rex.ID)
	r.derivedCube = dataset("moment0.fits", rex.ID)
	ins(`INSERT INTO derived(dataset_id,op,params,sources,summary,software,created_at) VALUES(?,'moment','{}',?,'{}','x',1)`, r.derivedCube, fmt.Sprintf("[%d]", r.cube))
	r.figure = ins(`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,workspace_id,created_by) VALUES(?,'Moment map','',1,'rev-1','abc',1,1,'{}',1,?)`, r.derivedCube, ada.ID)
	r.board = ins(`INSERT INTO boards(title,created_at,updated_at,workspace_id,created_by) VALUES('Draft',1,1,1,?)`, ada.ID)
	ins(`INSERT INTO board_items(board_id,position,figure_id,note) VALUES(?,0,?,'')`, r.board, r.figure)
	return r
}

func (r *accessRig) setLabel(cookie string, id int64, label string) int {
	r.g.t.Helper()
	return r.g.do(req{method: "POST", path: fmt.Sprintf("/api/datasets/%d/access", id), body: `{"label":"` + label + `","reason":"PI has not approved"}`, cookie: cookie}).Code
}

func (r *accessRig) publish(cookie string, confirm bool) *httptest.ResponseRecorder {
	r.g.t.Helper()
	body := `{"label":"supervisor","hours":24}`
	if confirm {
		body = `{"label":"supervisor","hours":24,"confirmAccess":true}`
	}
	return r.g.do(req{method: "POST", path: fmt.Sprintf("/api/boards/%d/publish", r.board), body: body, cookie: cookie})
}

func (r *accessRig) snapshots(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.g.cat.DB().QueryRow(`SELECT COUNT(*) FROM snapshots WHERE board_id=?`, r.board).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The label of the source reaches the figure drawn from a dataset made from it, from the recorded edges, and shows on the board.
func TestFigureCarriesTheMostRestrictiveLabelOfItsSources(t *testing.T) {
	r := newAccessRig(t)
	if c := r.setLabel(r.researcher, r.cube, "collaboration"); c != 200 {
		t.Fatalf("set: %d", c)
	}
	var fig struct {
		Items []struct {
			Figure struct {
				Access *access.Effective `json:"access"`
			} `json:"figure"`
		} `json:"items"`
	}
	w := r.g.do(req{path: fmt.Sprintf("/api/boards/%d", r.board), cookie: r.admin})
	fig = decode[struct {
		Items []struct {
			Figure struct {
				Access *access.Effective `json:"access"`
			} `json:"figure"`
		} `json:"items"`
	}](t, w)
	if len(fig.Items) != 1 || fig.Items[0].Figure.Access == nil || fig.Items[0].Figure.Access.Label != access.Collaboration {
		t.Fatalf("the figure of a derived map should inherit collaboration: %s", w.Body)
	}
	if v := fig.Items[0].Figure.Access.Via; len(v) != 1 || v[0].Name != secretCube {
		t.Fatalf("the label should name the dataset it comes from: %+v", v)
	}
	// a second, stricter source wins; it is computed when asked, so relabelling shows at once
	if c := r.setLabel(r.researcher, r.derivedCube, "unreleased"); c != 200 {
		t.Fatalf("set: %d", c)
	}
	w = r.g.do(req{path: "/api/figures", cookie: r.admin})
	got := decode[struct {
		Figures []struct {
			Access *access.Effective `json:"access"`
		} `json:"figures"`
	}](t, w)
	if len(got.Figures) != 1 || got.Figures[0].Access == nil || got.Figures[0].Access.Label != access.Unreleased || len(got.Figures[0].Access.Via) != 2 {
		t.Fatalf("the most restrictive source should win and both be listed: %s", w.Body)
	}
	// the overview feeds the badges of the lists
	ov := decode[struct {
		Datasets map[string]struct {
			Own       access.Label     `json:"own"`
			Effective access.Effective `json:"effective"`
		} `json:"datasets"`
	}](t, r.g.do(req{path: "/api/access", cookie: r.view}))
	if ov.Datasets[fmt.Sprint(r.cube)].Own != access.Collaboration || ov.Datasets[fmt.Sprint(r.derivedCube)].Effective.Label != access.Unreleased {
		t.Fatalf("overview: %+v", ov)
	}
}

// Raising a label needs the right to write the dataset; lowering needs Maintainer, so a Researcher cannot undo a block by
// relabelling; a Viewer sets nothing. Every change is in the history and the audit log.
func TestOnlyAMaintainerCanReleaseWhatAResearcherLabelled(t *testing.T) {
	r := newAccessRig(t)
	if c := r.setLabel(r.view, r.cube, "unreleased"); c != 403 {
		t.Errorf("a Viewer set a label: %d", c)
	}
	if c := r.setLabel(r.researcher, r.cube, "unreleased"); c != 200 {
		t.Fatalf("a Researcher could not raise a label: %d", c)
	}
	w := r.g.do(req{method: "POST", path: fmt.Sprintf("/api/datasets/%d/access", r.cube), body: `{"label":"none"}`, cookie: r.researcher})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "Maintainer") {
		t.Errorf("a Researcher lowered a label: %d %s", w.Code, w.Body)
	}
	if c := r.setLabel(r.researcher, r.cube, "collaboration"); c != 403 {
		t.Errorf("a Researcher lowered unreleased to collaboration: %d", c)
	}
	if c := r.setLabel(r.maintainer, r.cube, "collaboration"); c != 200 {
		t.Errorf("a Maintainer could not lower a label: %d", c)
	}
	if c := r.setLabel(r.g.admin(r), r.cube, "bogus"); c != 422 {
		t.Errorf("an unknown label: %d", c)
	}
	h := decode[struct {
		Label   access.Label    `json:"label"`
		History []access.Change `json:"history"`
	}](t, r.g.do(req{path: fmt.Sprintf("/api/datasets/%d/access", r.cube), cookie: r.view}))
	if h.Label != access.Collaboration || len(h.History) != 2 || h.History[0].Previous != access.Unreleased || h.History[1].Author != r.g.mustAccount("rex").ID {
		t.Fatalf("history: %+v", h)
	}
	if _, err := r.g.cat.DB().Exec(`UPDATE dataset_access SET label='none'`); err == nil {
		t.Error("the history can be rewritten")
	}
	var n int
	if err := r.g.cat.DB().QueryRow(`SELECT COUNT(*) FROM audit WHERE action='access label set' AND kind='dataset'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("audit rows %d %v", n, err)
	}
}

func (g *group) admin(r *accessRig) string { return r.admin }

// Publishing a labelled board is refused until the author confirms, the confirmation names the datasets, the snapshot states the
// label, and the reviewer's view of it holds the label's name and no list of the labelled sources.
func TestPublishingALabelledBoardNeedsConfirmationAndStatesTheLabel(t *testing.T) {
	r := newAccessRig(t)
	if w := r.publish(r.admin, false); w.Code != 201 || r.snapshots(t) != 1 {
		t.Fatalf("an unlabelled board publishes as before: %d %s", w.Code, w.Body)
	}
	r.setLabel(r.researcher, r.cube, "unreleased")
	pre := decode[struct {
		Restricted []restrictedPanel `json:"restricted"`
		Blocked    bool              `json:"blocked"`
		Frozen     struct {
			Access string `json:"access"`
		} `json:"frozen"`
	}](t, r.g.do(req{path: fmt.Sprintf("/api/boards/%d/preflight", r.board), cookie: r.admin}))
	if len(pre.Restricted) != 1 || pre.Frozen.Access != "unreleased" || pre.Blocked {
		t.Fatalf("preflight: %+v", pre)
	}
	w := r.publish(r.admin, false)
	var refusal struct{ Error, Code string }
	json.Unmarshal(w.Body.Bytes(), &refusal)
	if w.Code != 409 || refusal.Code != "access_confirmation_required" || !strings.Contains(refusal.Error, secretCube) || !strings.Contains(refusal.Error, "Moment map") {
		t.Fatalf("publishing without confirmation: %d %s", w.Code, w.Body)
	}
	if r.snapshots(t) != 1 {
		t.Fatal("a refused publication created a snapshot")
	}
	w = r.publish(r.admin, true)
	if w.Code != 201 {
		t.Fatalf("confirmed publish: %d %s", w.Code, w.Body)
	}
	pub := decode[publishResponse](t, w)
	if pub.Snapshot.Access != "unreleased" {
		t.Fatalf("the snapshot must state the label: %+v", pub.Snapshot)
	}
	rv, err := r.g.s.Boards.Review(pub.Token)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rv)
	if !strings.Contains(string(raw), `"access":"unreleased"`) {
		t.Errorf("the reviewer's copy lacks the label: %s", raw)
	}
	// the label brings no source list: its names and ids are the owner's alone (the figures' own provenance is as before)
	for _, banned := range []string{`"via"`, `"restricted"`, "datasetId", "/d/"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the reviewer's copy holds %q: %s", banned, raw)
		}
	}
	// the label of a published review is frozen: relabelling later does not rewrite what was stated
	r.setLabel(r.maintainer, r.cube, "none")
	r.setLabel(r.maintainer, r.derivedCube, "none")
	again, _ := r.g.s.Boards.Review(pub.Token)
	if raw2, _ := json.Marshal(again); !strings.Contains(string(raw2), `"access":"unreleased"`) {
		t.Errorf("a published review lost its label: %s", raw2)
	}
	if w := r.publish(r.admin, false); w.Code != 201 {
		t.Errorf("once the data is released the board publishes without a question: %d %s", w.Code, w.Body)
	}
}

// A workspace admin can make a label block publishing; confirmation does not override it, a Researcher cannot change the policy
// or get round it by relabelling, and a refused publication leaves nothing behind.
func TestAnAdminCanBlockPublishingAndNobodyGoesRoundIt(t *testing.T) {
	r := newAccessRig(t)
	r.setLabel(r.researcher, r.cube, "unreleased")
	policy := func(cookie, labels string) int {
		return r.g.do(req{method: "POST", path: "/api/workspaces/1/blocked-labels", body: `{"labels":` + labels + `}`, cookie: cookie}).Code
	}
	for name, cookie := range map[string]string{"researcher": r.researcher, "maintainer": r.maintainer, "viewer": r.view} {
		if c := policy(cookie, `["unreleased"]`); c != 403 {
			t.Errorf("a %s changed the publishing policy: %d", name, c)
		}
	}
	if c := policy(r.admin, `["none"]`); c != 422 {
		t.Errorf("none cannot block: %d", c)
	}
	if c := policy(r.admin, `["unreleased"]`); c != 200 {
		t.Fatalf("the admin could not set the policy: %d", c)
	}
	for _, confirm := range []bool{false, true} {
		w := r.publish(r.admin, confirm)
		var body struct{ Code string }
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 403 || body.Code != "publishing_blocked" {
			t.Fatalf("confirm=%v: %d %s", confirm, w.Code, w.Body)
		}
	}
	if r.snapshots(t) != 0 {
		t.Fatal("a blocked publication created a snapshot")
	}
	pre := decode[struct {
		Blocked bool `json:"blocked"`
	}](t, r.g.do(req{path: fmt.Sprintf("/api/boards/%d/preflight", r.board), cookie: r.admin}))
	if !pre.Blocked {
		t.Error("the preflight should say publishing is blocked")
	}
	// relabelling by a Researcher is refused, so the block cannot be undone from below
	if c := r.setLabel(r.researcher, r.cube, "none"); c != 403 {
		t.Errorf("a Researcher released blocked data: %d", c)
	}
	if w := r.publish(r.admin, true); w.Code != 403 {
		t.Errorf("still blocked: %d", w.Code)
	}
	// collaboration-only data is not blocked by a policy on unreleased: it asks for confirmation instead
	r.setLabel(r.maintainer, r.cube, "collaboration")
	r.setLabel(r.maintainer, r.derivedCube, "none")
	if w := r.publish(r.admin, false); w.Code != 409 {
		t.Errorf("collaboration is not blocked by a policy on unreleased: %d %s", w.Code, w.Body)
	}
	// the policy is visible to members, and clearing it opens publishing again
	ov := decode[struct {
		Blocked []string `json:"blockedLabels"`
	}](t, r.g.do(req{path: "/api/access", cookie: r.researcher}))
	if len(ov.Blocked) != 1 || ov.Blocked[0] != "unreleased" {
		t.Errorf("policy as members see it: %+v", ov)
	}
	if c := policy(r.admin, `[]`); c != 200 {
		t.Fatal(c)
	}
	if w := r.publish(r.admin, true); w.Code != 201 {
		t.Errorf("policy cleared: %d %s", w.Code, w.Body)
	}
}

// The policy route is guarded by the admin permission of the workspace the request acts in, so a path that names another
// workspace is refused: an admin of one workspace cannot change the policy of another.
func TestAnAdminCannotSetThePolicyOfAnotherWorkspace(t *testing.T) {
	r := newAccessRig(t)
	other, err := r.g.acc.CreateWorkspace("Other lab", r.g.mustAccount("ada").ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	w := r.g.do(req{method: "POST", path: fmt.Sprintf("/api/workspaces/%d/blocked-labels", other.ID), body: `{"labels":["unreleased"]}`, cookie: r.admin})
	if w.Code != 404 {
		t.Fatalf("a path naming another workspace: %d %s", w.Code, w.Body)
	}
	got, err := r.g.acc.Workspace(other.ID)
	if err != nil || len(got.BlockedLabels) != 0 {
		t.Fatalf("the other workspace's policy changed: %+v %v", got.BlockedLabels, err)
	}
}

// Pushed work carries the label its instance declared, and the server applies the workspace's policy to it: a blocked label is
// refused with nothing stored, a label that is not canonical is refused, and an allowed one is stored in the document.
func TestPushedWorkCarriesItsLabelAndTheServerEnforcesItsPolicy(t *testing.T) {
	g, _, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	png, sha := cleanPNG(t, 16, 12)
	if w := putAsset(g, token, sha, png); w.Code/100 != 2 {
		t.Fatalf("asset: %d %s", w.Code, w.Body)
	}
	item := func(label string) string {
		doc, _ := json.Marshal(pushedFigure{Title: "Peak", Revision: "r1", Asset: sha, Width: 16, Height: 12, Provenance: json.RawMessage(`{"source":{"name":"cube.fits"}}`), Access: label})
		b, _ := json.Marshal(map[string]any{"instance": testInstance, "item": map[string]any{"kind": "figure", "origin": strings.Repeat("a", 32), "title": "Peak", "doc": json.RawMessage(doc), "assets": []string{sha}, "references": []any{}}, "identity": []any{}})
		return string(b)
	}
	push := func(label string) int {
		return g.bearer(token, req{method: "POST", path: "/api/push/items", body: item(label)}).Code
	}
	count := func() int {
		var n int
		if err := g.cat.DB().QueryRow(`SELECT COUNT(*) FROM pushed_items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	_ = ps
	if err := g.acc.SetBlockedLabels(workspace.Local, []string{"unreleased"}); err != nil {
		t.Fatal(err)
	}
	if c := push("unreleased"); c != 403 || count() != 0 {
		t.Fatalf("a blocked label was accepted: %d, %d items", c, count())
	}
	if c := push("none"); c != 422 || count() != 0 {
		t.Fatalf("a non-canonical label: %d", c)
	}
	if c := push("secret"); c != 422 {
		t.Fatalf("an unknown label: %d", c)
	}
	if c := push("collaboration"); c != 201 && c != 200 {
		t.Fatalf("a label the policy does not block was refused: %d", c)
	}
	var doc string
	if err := g.cat.DB().QueryRow(`SELECT doc FROM pushed_items`).Scan(&doc); err != nil || !strings.Contains(doc, `"access":"collaboration"`) {
		t.Fatalf("the stored document should carry the label: %s %v", doc, err)
	}
	var refused int
	if err := g.cat.DB().QueryRow(`SELECT COUNT(*) FROM audit WHERE action='push.refused'`).Scan(&refused); err != nil || refused < 1 {
		t.Errorf("a refusal is audited: %d %v", refused, err)
	}
}

// The local side: labelling a dataset puts the label into the figure, board and report it pushes, so the server can apply its policy.
func TestLocalPushCarriesTheLabelOfItsDatasets(t *testing.T) {
	g, ts, _ := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	l := newPushLocal(t, ts)
	l.link(t, g.origin, g.mintPush(full))
	var err error
	if l.s.Access, err = access.Open(l.s.Store.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err = derived.Open(l.s.Store.DB()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.s.Access.Set(workspace.Local, l.dataset, "cube.fits", access.Unreleased, "not validated", "local-owner"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"figure", "board", "report"} {
		id := map[string]int64{"figure": l.figure, "board": l.board, "report": l.report}[kind]
		_, out := l.preflight(t, kind, id)
		if !strings.Contains(out, "labelled unreleased") {
			t.Errorf("the %s preflight does not warn about the label: %s", kind, out)
		}
		e := l.enqueue(t, kind, id)
		var raw string
		if err := l.s.Store.DB().QueryRow(`SELECT body FROM push_queue WHERE id=?`, e.ID).Scan(&raw); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		// the queued body is the frozen document itself or an item wrapping it; the label is in the document either way
		if !strings.Contains(raw, `"access":"unreleased"`) {
			t.Errorf("the pushed %s does not carry the label: %s", kind, raw)
		}
	}
}
