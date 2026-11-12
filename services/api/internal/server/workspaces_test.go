package server

import (
	"encoding/base32"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/discussion"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const leak = "LEAKMARK-7f3a" // written into every fixture row of workspace A; never in an answer to workspace B

// scopedGroup is a group server with the real stores of every workspace kind on one catalog and every optional route
// mounted. It returns the routes the group server mounts.
func scopedGroup(t *testing.T) (*group, map[string]authz.Permission) {
	t.Helper()
	g := newGroup(t)
	db, dir := g.cat.DB(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: g.cat, Audit: g.audit, WebDist: g.s.WebDist, MultiUser: g.s.MultiUser, Analysis: &AnalysisConfig{}, ManagedDir: filepath.Join(dir, "managed")}
	var err error
	s.Boards, err = boards.Open(db, dir)
	must(err)
	s.Runs, err = runs.Open(db)
	must(err)
	s.Reports, err = reports.Open(db, dir)
	must(err)
	s.Regions, err = regions.Open(db)
	must(err)
	s.Masks, err = masks.Open(db)
	must(err)
	s.Subsets, err = subsets.Open(db)
	must(err)
	s.History, err = history.Open(db)
	must(err)
	s.Projects, err = projects.Open(db)
	must(err)
	s.Derived, err = derived.Open(db)
	must(err)
	s.Notes, err = notify.Open(db)
	must(err)
	s.Discussion = discussion.Open(db)
	s.Jobs, err = jobs.Open(db) // local-only, but the runs list asks it for lineages
	must(err)
	v := reflect.ValueOf(s).Elem() // the stores of features the group server does not mount, so their routes are declared too
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.CanSet() && f.Kind() == reflect.Pointer && f.IsNil() {
			f.Set(reflect.New(f.Type().Elem()))
		}
	}
	declared := map[string]authz.Permission{}
	s.onRoute = func(p string, perm authz.Permission) { declared[p] = perm }
	g.s, g.h = s, s.Handler()
	return g, declared
}

// join accepts an invitation over HTTP as a new account and enrols its second factor; it returns the full session.
func (g *group) join(secret, username string) string {
	g.t.Helper()
	w := g.do(req{method: "POST", path: "/api/auth/invitation/accept", body: `{"token":"` + secret + `","name":"` + username + `","username":"` + username + `","password":"` + adminPass + `"}`})
	if w.Code != 201 {
		g.t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	pending := sessionFrom(g.t, w)
	en := decode[enrolmentBody](g.t, g.do(req{method: "POST", path: "/api/auth/enrolment", body: `{}`, cookie: pending}))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	if err != nil {
		g.t.Fatal(err)
	}
	w = g.do(req{method: "POST", path: "/api/auth/enrolment/confirm", body: `{"code":"` + accounts.CodeAt(key, g.clock.now()) + `"}`, cookie: pending})
	if w.Code != 200 {
		g.t.Fatalf("enrol: %d %s", w.Code, w.Body)
	}
	return sessionFrom(g.t, w)
}

// invite makes an invitation into ws with a role and has a new account accept it.
func (g *group) invite(ws workspace.ID, role, username string) string {
	g.t.Helper()
	secret, _, err := g.acc.CreateInvitation(ws, role, "", "test")
	if err != nil {
		g.t.Fatal(err)
	}
	return g.join(secret, username)
}

// fixtures writes one row of every kind route can resolve into workspace a, each carrying the leak marker where it has text,
// and returns the value each kind's path parameter takes.
func fixtures(t *testing.T, g *group, a workspace.ID, by string) map[string]string {
	t.Helper()
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
	ds := ins(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,workspace_id,created_by) VALUES(?,?,1,1,1,'{}',?,?)`, "/a/"+leak+".fits", leak, a, by)
	ins(`INSERT INTO dataset_locations(dataset_id,host_id,path,is_primary,size_bytes,modified_at,added_at,workspace_id) VALUES(?,0,?,1,1,1,1,?)`, ds, "/a/"+leak+".fits", a)
	fig := ins(`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,workspace_id,created_by) VALUES(?,?,?,1,?,'abc',1,1,'{}',?,?)`, ds, leak, leak, "rev-"+leak, a, by)
	board := ins(`INSERT INTO boards(title,created_at,updated_at,workspace_id,created_by) VALUES(?,1,1,?,?)`, leak, a, by)
	ins(`INSERT INTO board_items(board_id,position,figure_id,note) VALUES(?,0,?,?)`, board, fig, leak)
	snap := ins(`INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen,workspace_id,created_by) VALUES(?,?,1,9999999999,?,'{}',?,?)`, board, leak, "th-"+leak, a, by)
	comment := ins(`INSERT INTO comments(snapshot_id,figure_revision,author,body,created_at) VALUES(?,?,?,?,1)`, snap, "rev-"+leak, leak, leak)
	rep := ins(`INSERT INTO reports(key,title,body,revision,created_at,updated_at,workspace_id,created_by) VALUES(?,?,?,1,1,1,?,?)`, "k-"+leak, leak, leak, a, by)
	ins(`INSERT INTO report_history(report_id,revision,author,at,title,body) VALUES(?,1,?,1,?,?)`, rep, by, leak, leak)
	run := ins(`INSERT INTO runs(dir,name,kind,imported_at,generated_at,git_commit,input_name,input_size,params,diagnostics,workspace_id,created_by) VALUES(?,?,'vgt',1,'','',?,1,'{}','{}',?,?)`, "/a/run-"+leak, leak, leak, a, by)
	view := ins(`INSERT INTO run_views(name,spec,updated_at,workspace_id,created_by) VALUES(?,'{}',1,?,?)`, leak, a, by)
	region := ins(`INSERT INTO saved_regions(dataset_id,name,shape,created_at,updated_at,workspace_id,created_by) VALUES(?,?,'{}',1,1,?,?)`, ds, leak, a, by)
	mask := ins(`INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,method,rule,author,created_at,source_size,source_mtime,file_size,file_mtime,sha256,workspace_id,created_by)
		VALUES(?,'flag',?,?,1,1,1,0,1,'declared','x','r',?,1,1,1,1,1,'s',?,?)`, ds, leak, "/m/"+leak, by, a, by)
	sub := ins(`INSERT INTO subsets(source_id,created_at,workspace_id,created_by) VALUES(?,1,?,?)`, ds, a, by)
	ins(`INSERT INTO subset_versions(subset_id,version,kind,name,reason,definition,source_size,source_mtime_ns,author,created_at) VALUES(?,1,'window',?,'r','{}',1,1,?,1)`, sub, leak, by)
	step := ins(`INSERT INTO exploration_steps(project_id,dataset_id,kind,label,scene,at,workspace_id,created_by) VALUES(0,?,'view',?,'{}',1,?,?)`, ds, leak, a, by)
	proj := ins(`INSERT INTO projects(host_id,root,name,opened_at,workspace_id,created_by) VALUES(0,?,?,1,?,?)`, "/a/"+leak, leak, a, by)
	ins(`INSERT INTO attachments(sha,mime,size,created_at,workspace_id,created_by) VALUES(?,'image/png',1,1,?,?)`, "sha"+strings.ToLower(leak), a, by)
	disc := ins(`INSERT INTO member_comments(workspace_id,created_by,kind,target_id,body,created_at) VALUES(?,?,'figure',?,?,1)`, a, by, fig, leak)
	_, inv, err := g.acc.CreateInvitation(a, "viewer", leak, by)
	if err != nil {
		t.Fatal(err)
	}
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	return map[string]string{
		"datasets/id": id(ds), "figures/id": id(fig), "boards/id": id(board), "snapshots/id": id(snap), "comments/id": id(comment),
		"reports/id": id(rep), "runs/id": id(run), "run-views/id": id(view), "regions/id": id(region), "masks/id": id(mask),
		"subsets/sid": id(sub), "history/id": id(step), "projects/id": id(proj), "attachments/sha": "sha" + strings.ToLower(leak),
		"discussion/id": id(disc), "invitations/id": id(inv.ID),
	}
}

// dumpWorkspace is every row of every table that has workspace_id, in workspace a, as text: what B's requests must not change.
func dumpWorkspace(t *testing.T, g *group, a workspace.ID) string {
	t.Helper()
	db := g.cat.DB()
	rows, err := db.Query(`SELECT m.name FROM sqlite_master m WHERE m.type='table' AND EXISTS(SELECT 1 FROM pragma_table_info(m.name) WHERE name='workspace_id') ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tbl := range tables {
		r, err := db.Query(`SELECT * FROM `+tbl+` WHERE workspace_id=? ORDER BY rowid`, a)
		if err != nil {
			continue // a WITHOUT ROWID table has no rowid; none of ours is
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			fmt.Fprintf(&b, "%s %v\n", tbl, vals)
		}
		r.Close()
	}
	return b.String()
}

// Guard test 3 (ADR 0050 section 4): for every route the group server mounts whose path names a workspace object, a member of
// another workspace, Owner of their own (so a refusal can only come from scoping, never from the role), gets 404 for workspace
// A's object, the answer holds nothing of it, and nothing in A changes. Built from the route declarations: a new route is
// covered without editing this test, and a new kind of object without a fixture fails it.
func TestNoRouteLeaksAcrossWorkspaces(t *testing.T) {
	g, declared := scopedGroup(t)
	admin, _ := g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	a := workspace.Local
	bws, err := g.acc.CreateWorkspace("B", ada.ID, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	bo := g.invite(bws.ID, "owner", "bo")
	if _, err := g.acc.RemoveMember(bws.ID, ada.ID); err != nil { // ada keeps A only; bo is B's only Owner
		t.Fatal(err)
	}
	ids := fixtures(t, g, a, ada.ID)
	for k := range scopeKinds {
		if ids[k] == "" {
			t.Errorf("scope kind %s has no fixture here: add one so its routes are requested across workspaces", k)
		}
	}
	before := dumpWorkspace(t, g, a)
	var patterns []string
	for p := range declared {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	tried := 0
	for _, pattern := range patterns {
		params := patternParams(pattern)
		if len(params) == 0 {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		skip := false
		for _, k := range params {
			_, name, _ := strings.Cut(k, "/")
			val := ids[k]
			switch {
			case val != "":
			case childParams[k] != "":
				val = map[string]string{"lid": "1", "pos": "0", "rev": "1"}[name]
			case k == "members/id":
				val = ada.ID // A's member, seen from B
			default:
				skip = true // an account, a workspace (admin routes) or an in-memory bundle: not workspace data
			}
			path = strings.ReplaceAll(path, "{"+name+"}", val)
		}
		if skip {
			continue
		}
		tried++
		w := g.do(req{method: method, path: path + "?path=/a&dataset=" + ids["datasets/id"], body: `{}`, cookie: bo, hdr: map[string]string{WorkspaceHeader: bws.ID.String()}})
		if w.Code != 404 {
			t.Errorf("%s as B's Owner on A's object: %d %s, want 404", pattern, w.Code, strings.TrimSpace(w.Body.String()))
		}
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("%s answered B with A's data: %s", pattern, w.Body)
		}
	}
	if tried < 100 {
		t.Fatalf("only %d routes with a workspace object were requested", tried)
	}
	if after := dumpWorkspace(t, g, a); after != before {
		t.Errorf("B's requests changed workspace A:\n--- before\n%s\n--- after\n%s", before, after)
	}
	// and A's own Owner still reaches the same objects
	if w := g.do(req{path: "/api/boards/" + ids["boards/id"], cookie: admin}); w.Code != 200 {
		t.Errorf("A's Owner cannot read A's board: %d %s", w.Code, w.Body)
	}
	// lists hold only the actor's workspace
	for _, list := range []string{"/api/boards", "/api/figures", "/api/datasets", "/api/reports", "/api/runs", "/api/run-views", "/api/subsets", "/api/projects", "/api/history"} {
		w := g.do(req{path: list, cookie: bo, hdr: map[string]string{WorkspaceHeader: bws.ID.String()}})
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("%s lists A's rows to B: %s", list, w.Body)
		}
	}
	// naming a workspace one is not a member of reads nothing
	if w := g.do(req{path: "/api/boards", cookie: bo, hdr: map[string]string{WorkspaceHeader: "1"}}); w.Code != 403 || strings.Contains(w.Body.String(), leak) {
		t.Errorf("B naming A in the header: %d %s, want 403", w.Code, w.Body)
	}
}

// The matrix and the ownership rule over HTTP: a Viewer reads and comments but writes nothing; a Researcher creates boards and
// edits their own, not someone else's; a Maintainer edits anyone's and invites only Viewers and Researchers.
func TestRolesOverHTTP(t *testing.T) {
	g, _ := scopedGroup(t)
	admin, _ := g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	ids := fixtures(t, g, workspace.Local, ada.ID)
	viewer := g.invite(workspace.Local, "viewer", "vi")
	res := g.invite(workspace.Local, "researcher", "re")
	maint := g.invite(workspace.Local, "maintainer", "ma")
	adaBoard := "/api/boards/" + ids["boards/id"]

	if w := g.do(req{path: adaBoard, cookie: viewer}); w.Code != 200 {
		t.Errorf("a Viewer cannot read a board: %d", w.Code)
	}
	for _, r := range []req{
		{method: "POST", path: "/api/boards", body: `{"title":"mine"}`},
		{method: "POST", path: adaBoard + "/layout", body: `{"items":[]}`},
		{method: "POST", path: "/api/invitations", body: `{"role":"viewer"}`},
	} {
		r.cookie = viewer
		if w := g.do(r); w.Code != 403 {
			t.Errorf("a Viewer: %s %s answered %d, want 403", r.method, r.path, w.Code)
		}
	}
	if w := g.do(req{method: "POST", path: "/api/figures/" + ids["figures/id"] + "/discussion", body: `{"body":"looks right"}`, cookie: viewer}); w.Code != 201 {
		t.Errorf("a Viewer cannot comment: %d %s", w.Code, w.Body)
	}

	if w := g.do(req{method: "POST", path: adaBoard + "/layout", body: `{"items":[]}`, cookie: res}); w.Code != 403 {
		t.Errorf("a Researcher edited someone else's board: %d %s", w.Code, w.Body)
	}
	w := g.do(req{method: "POST", path: "/api/boards", body: `{"title":"Researcher's board"}`, cookie: res})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("a Researcher cannot create a board: %d %s", w.Code, w.Body)
	}
	own := decode[struct{ ID int64 }](t, w)
	if w := g.do(req{method: "POST", path: fmt.Sprintf("/api/boards/%d/archive", own.ID), body: `{}`, cookie: res}); w.Code == 403 || w.Code == 404 {
		t.Errorf("a Researcher cannot archive their own board: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{method: "POST", path: fmt.Sprintf("/api/boards/%d/restore", own.ID), body: `{}`, cookie: maint}); w.Code == 403 || w.Code == 404 {
		t.Errorf("a Maintainer cannot act on someone else's board: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{method: "POST", path: "/api/invitations", body: `{"role":"owner"}`, cookie: maint}); w.Code != 403 {
		t.Errorf("a Maintainer invited an Owner: %d", w.Code)
	}
	if w := g.do(req{method: "POST", path: "/api/invitations", body: `{"role":"researcher"}`, cookie: maint}); w.Code != 201 {
		t.Errorf("a Maintainer cannot invite a Researcher: %d %s", w.Code, w.Body)
	}
	// only an Owner changes roles, and never so that no Owner is left
	reAcct := g.mustAccount("re")
	if w := g.do(req{method: "POST", path: "/api/members/" + reAcct.ID + "/role", body: `{"role":"owner"}`, cookie: maint}); w.Code != 403 {
		t.Errorf("a Maintainer changed a role: %d", w.Code)
	}
	if w := g.do(req{method: "POST", path: "/api/members/" + ada.ID + "/role", body: `{"role":"viewer"}`, cookie: admin}); w.Code != 422 {
		t.Errorf("the last Owner demoted herself: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{method: "POST", path: "/api/members/" + reAcct.ID + "/role", body: `{"role":"viewer"}`, cookie: admin}); w.Code != 204 {
		t.Errorf("an Owner cannot change a role: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{method: "POST", path: "/api/boards", body: `{"title":"after"}`, cookie: res}); w.Code != 403 {
		t.Errorf("a role change did not take effect at once: %d", w.Code)
	}
	// effective permissions say the same
	caps := decode[struct {
		Workspace *WorkspaceContext `json:"workspace"`
	}](t, g.do(req{path: "/api/capabilities", cookie: viewer}))
	if caps.Workspace == nil || caps.Workspace.Role != authz.RoleViewer {
		t.Fatalf("capabilities for a Viewer: %+v", caps.Workspace)
	}
	has := map[authz.Permission]bool{}
	for _, p := range caps.Workspace.Permissions {
		has[p] = true
	}
	if !has[authz.BoardRead] || !has[authz.CommentWrite] || has[authz.BoardWrite] || has[authz.LifecycleDelete] {
		t.Errorf("a Viewer's effective permissions: %v", caps.Workspace.Permissions)
	}
	// an invitation link works once
	secret, _, _ := g.acc.CreateInvitation(workspace.Local, "viewer", "", ada.ID)
	g.join(secret, "once")
	if w := g.do(req{method: "POST", path: "/api/auth/invitation/accept", body: `{"token":"` + secret + `","name":"Twice","username":"twice","password":"` + adminPass + `"}`}); w.Code != 400 {
		t.Errorf("a used invitation was accepted again: %d %s", w.Code, w.Body)
	}
}

// Path routes on the group server reach only the workspace's declared data roots: the file tree, path completion and opening
// a folder as a project; a root cannot be declared through a link or over the server's own data.
func TestDataRootsConfinePathRoutes(t *testing.T) {
	g, _ := scopedGroup(t)
	admin, _ := g.setupAdmin("ada")
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	inside, outside := filepath.Join(base, "lab"), filepath.Join(base, "private")
	for _, d := range []string{filepath.Join(inside, "cubes"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(outside, "secret.fits"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(inside, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(base, "lablink")); err != nil {
		t.Fatal(err)
	}
	if w := g.do(req{method: "POST", path: "/api/workspaces/1/roots", body: `{"path":"` + filepath.Join(base, "lablink") + `"}`, cookie: admin}); w.Code != 422 {
		t.Errorf("a root declared through a link: %d %s", w.Code, w.Body)
	}
	g.s.MultiUser.DataDir = filepath.Join(inside, "cubes")
	if w := g.do(req{method: "POST", path: "/api/workspaces/1/roots", body: `{"path":"` + inside + `"}`, cookie: admin}); w.Code != 422 {
		t.Errorf("a root holding the server's data: %d %s", w.Code, w.Body)
	}
	g.s.MultiUser.DataDir = ""
	if w := g.do(req{method: "POST", path: "/api/workspaces/1/roots", body: `{"path":"` + inside + `"}`, cookie: admin}); w.Code != 204 {
		t.Fatalf("declaring a root: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{path: "/api/files/tree?path=" + inside, cookie: admin}); w.Code != 200 || !strings.Contains(w.Body.String(), "cubes") {
		t.Errorf("the tree of a root: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{outside, filepath.Join(inside, "escape"), base, "/"} {
		if w := g.do(req{path: "/api/files/tree?path=" + p, cookie: admin}); w.Code == 200 {
			t.Errorf("the tree outside the roots (%s): %d %s", p, w.Code, w.Body)
		}
		if w := g.do(req{path: "/api/fs/complete?prefix=" + p + "/", cookie: admin}); !strings.Contains(w.Body.String(), `"entries":[]`) {
			t.Errorf("completion outside the roots (%s): %s", p, w.Body)
		}
		if w := g.do(req{method: "POST", path: "/api/projects/open", body: `{"path":"` + p + `"}`, cookie: admin}); w.Code != 422 {
			t.Errorf("opening a folder outside the roots (%s): %d %s", p, w.Code, w.Body)
		}
	}
	if w := g.do(req{path: "/api/fs/complete?prefix=" + inside + "/", cookie: admin}); !strings.Contains(w.Body.String(), "cubes") {
		t.Errorf("completion inside a root: %s", w.Body)
	}
	if w := g.do(req{path: "/api/fs/complete?prefix=~/", cookie: admin}); !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Errorf("completion reached the server account's home: %s", w.Body)
	}
	if w := g.do(req{method: "POST", path: "/api/projects/open", body: `{"path":"` + filepath.Join(inside, "cubes") + `"}`, cookie: admin}); w.Code != 200 && w.Code != 201 {
		t.Errorf("opening a folder inside a root: %d %s", w.Code, w.Body)
	}
	// another workspace does not see this one's roots
	ada := g.mustAccount("ada")
	other, _ := g.acc.CreateWorkspace("Other", ada.ID, ada.ID)
	if w := g.do(req{path: "/api/files/tree?path=" + inside, cookie: admin, hdr: map[string]string{WorkspaceHeader: other.ID.String()}}); w.Code == 200 {
		t.Errorf("a root of workspace 1 is readable from another workspace: %d %s", w.Code, w.Body)
	}
	// each workspace reads its own managed copies, never another's
	managed := g.s.managedDirOf(workspace.Local)
	_ = os.MkdirAll(managed, 0o700)
	_ = os.WriteFile(filepath.Join(managed, leak+".fits"), []byte("x"), 0o444)
	managed, _ = filepath.EvalSymlinks(managed)
	if w := g.do(req{path: "/api/files/tree?path=" + managed, cookie: admin}); w.Code != 200 || !strings.Contains(w.Body.String(), leak) {
		t.Errorf("workspace 1 cannot read its managed copies: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{path: "/api/files/tree?path=" + managed, cookie: admin, hdr: map[string]string{WorkspaceHeader: other.ID.String()}}); w.Code == 200 || strings.Contains(w.Body.String(), leak) {
		t.Errorf("another workspace reads workspace 1's managed copies: %d %s", w.Code, w.Body)
	}
	_ = http.StatusOK
}

// One file registered in two workspaces is two datasets with their own corrections: the path-to-dataset memory the correcting
// service uses keeps them apart, so one workspace's corrections are never attached to the other's reads.
func TestCorrectionPathsArePerWorkspace(t *testing.T) {
	var s Server
	s.notePath(1, 0, "/data/cube.fits", 10)
	s.notePath(2, 0, "/data/cube.fits", 20)
	for ws, want := range map[workspace.ID]int64{1: 10, 2: 20} {
		v, ok := s.corrPaths.Load(corrPathRef{ws, 0, "/data/cube.fits"})
		if !ok || v.(int64) != want {
			t.Errorf("workspace %d: dataset %v, want %d", ws, v, want)
		}
	}
}

// The tables internal/arch treats as local-only (hosts, jobs, sweeps, transfers, offline copies, the publish server's side) are
// not scoped by workspace; that is safe only while the group server mounts none of their routes.
func TestLocalOnlyFeaturesStayUnmounted(t *testing.T) {
	for _, p := range []authz.Permission{authz.LocalOnly, authz.ClusterRead, authz.ClusterUse, authz.ClusterAdmin, authz.JobRead, authz.JobSubmit,
		authz.JobAccess, authz.SweepRead, authz.SweepSubmit, authz.PublishManage} {
		if !notOnGroupServer[p] {
			t.Errorf("%s is mounted on the group server: scope its tables by workspace first (internal/arch scope_test.go)", p)
		}
	}
	for _, p := range []string{"GET /api/report-comments", "POST /api/reply-drafts/{id}/send", "GET /api/publish"} {
		if !groupServerOff(p) {
			t.Errorf("%s is mounted on the group server", p)
		}
	}
}

// Jobs are a local-only feature with an unscoped table: an adopted catalog brings workspace 1's jobs onto the group server. A
// member of another workspace must not be able to make a board panel follow one (which would show its name and state, tell
// existing ids from missing ones, and freeze it into a review link).
func TestBoardsCannotFollowJobsOnTheGroupServer(t *testing.T) {
	g, _ := scopedGroup(t)
	g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	if _, err := g.cat.DB().Exec(`INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at) VALUES(1,?,'vgt','','','finished','','','','',1)`, leak); err != nil {
		t.Fatal(err)
	}
	bws, _ := g.acc.CreateWorkspace("B", ada.ID, ada.ID)
	bo := g.invite(bws.ID, "researcher", "bo")
	hdr := map[string]string{WorkspaceHeader: bws.ID.String()}
	w := g.do(req{method: "POST", path: "/api/boards", body: `{"title":"mine"}`, cookie: bo, hdr: hdr})
	board := decode[struct{ ID int64 }](t, w)
	add := func(job int) *httptest.ResponseRecorder {
		return g.do(req{method: "POST", path: fmt.Sprintf("/api/boards/%d/items", board.ID), body: fmt.Sprintf(`{"jobId":%d}`, job), cookie: bo, hdr: hdr})
	}
	real, missing := add(1), add(999)
	if real.Code != missing.Code || real.Code < 400 {
		t.Errorf("following an adopted job: %d, a missing one: %d; want the same refusal", real.Code, missing.Code)
	}
	if v := g.do(req{path: fmt.Sprintf("/api/boards/%d", board.ID), cookie: bo, hdr: hdr}); strings.Contains(v.Body.String(), leak) {
		t.Errorf("the board shows workspace 1's job: %s", v.Body)
	}
}
