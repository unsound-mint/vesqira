package authz

import (
	"context"
	"testing"
)

func TestLocalOwnerHoldsEveryPermissionAndNothingElseDoes(t *testing.T) {
	owner := LocalOwner()
	for _, p := range All() {
		if !owner.Can(p) {
			t.Errorf("the local owner lacks %s", p)
		}
		if p != Anonymous && (Actor{ID: "acct-1"}).Can(p) {
			t.Errorf("an unknown actor holds %s: default deny", p)
		}
	}
	if owner.Can("made.up") {
		t.Error("the local owner holds a permission that is not declared")
	}
}

func TestPermissionNamesAreUniqueAndShaped(t *testing.T) {
	seen := map[Permission]bool{}
	for _, p := range All() {
		if seen[p] {
			t.Errorf("%s listed twice", p)
		}
		seen[p] = true
		if p != Anonymous && len(p) < 3 {
			t.Errorf("%q is not resource.action", p)
		}
	}
}

func TestActorTravelsInTheContext(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Fatal("an actor appeared from nowhere")
	}
	a, ok := From(WithActor(context.Background(), LocalOwner()))
	if !ok || !a.IsLocalOwner() {
		t.Fatalf("got %+v %v", a, ok)
	}
}

// Slice 3: the admin flag is not a workspace role. An admin who is no member holds the account and admin permissions and no
// workspace content; a member holds exactly what the matrix gives their role; nobody holds a local-only permission.
func TestAdminIsNotAWorkspaceRole(t *testing.T) {
	admin := Actor{ID: "acct-1", Via: ViaSession, Admin: true, Recent: true}
	for _, p := range All() {
		_, account := accountLevel[p]
		if want := account; admin.Can(p) != want && p != Anonymous {
			t.Errorf("an admin without a membership: Can(%s) = %v, want %v", p, !want, want)
		}
	}
	plain := Actor{ID: "acct-2", Via: ViaSession}
	for _, p := range All() {
		adminOnly, account := accountLevel[p]
		if want := account && !adminOnly; p != Anonymous && plain.Can(p) != want {
			t.Errorf("an account with no membership and no admin flag: Can(%s) = %v, want %v", p, !want, want)
		}
	}
	if (Actor{ID: LocalOwnerID, Via: ViaSession, Admin: true, Workspace: 1, Role: RoleOwner}).IsLocalOwner() {
		t.Error("an account that calls itself local-owner is not the local owner")
	}
	if (Actor{ID: LocalOwnerID, Via: ViaSession, Admin: true, Workspace: 1, Role: RoleOwner}).Can(DatasetRead) {
		t.Error("an account with the local owner's id holds a permission")
	}
}

// The matrix of ADR 0050 section 3, row by row for the permissions slice 3 enforces.
func TestRolesHoldWhatTheMatrixSays(t *testing.T) {
	want := map[Permission][4]bool{ // viewer, researcher, maintainer, owner
		DatasetRead: {true, true, true, true}, CommentWrite: {true, true, true, true}, MemberRead: {true, true, true, true},
		DatasetWrite: {false, true, true, true}, BoardWrite: {false, true, true, true}, ReportWrite: {false, true, true, true},
		FigureWrite: {false, true, true, true}, AnalysisRun: {false, true, true, true}, FilesRead: {false, true, true, true},
		LifecycleDelete: {false, false, true, true}, AuditRead: {false, false, true, true}, MemberInvite: {false, false, true, true},
		MemberManage:   {false, false, false, true},
		WorkspaceAdmin: {}, AccountAdmin: {}, LocalOnly: {}, JobSubmit: {}, ClusterUse: {}, SweepSubmit: {},
	}
	for p, row := range want {
		for i, r := range Roles() {
			a := Actor{ID: "acct-1", Via: ViaSession, Workspace: 2, Role: r, Recent: true}
			if a.Can(p) != row[i] {
				t.Errorf("%s Can(%s) = %v, want %v", r, p, !row[i], row[i])
			}
		}
	}
	for _, p := range All() {
		if _, account := accountLevel[p]; !account && p != Anonymous && p != LocalOnly && MinRole(p) == RoleNone {
			if (Actor{ID: "a", Via: ViaSession, Workspace: 1, Role: RoleOwner}).Can(p) {
				t.Errorf("%s is held by an Owner although no role is given it", p)
			}
		}
	}
	if (Actor{ID: "a", Via: ViaSession, Role: RoleOwner}).Can(DatasetRead) {
		t.Error("a role without a workspace holds a workspace permission")
	}
}

func TestOwnershipAndInvitationRules(t *testing.T) {
	res := Actor{ID: "r", Via: ViaSession, Workspace: 1, Role: RoleResearcher}
	if !res.Owns("r") || res.Owns("someone-else") || res.Owns("") {
		t.Error("a Researcher owns exactly what they created")
	}
	m := Actor{ID: "m", Via: ViaSession, Workspace: 1, Role: RoleMaintainer}
	if !m.Owns("someone-else") || !LocalOwner().Owns("") {
		t.Error("a Maintainer and the local owner act on anything")
	}
	if !m.MayInvite(RoleViewer) || !m.MayInvite(RoleResearcher) || m.MayInvite(RoleMaintainer) || m.MayInvite(RoleOwner) {
		t.Error("a Maintainer invites only Viewers and Researchers")
	}
	o := Actor{ID: "o", Via: ViaSession, Workspace: 1, Role: RoleOwner}
	if !o.MayInvite(RoleOwner) || res.MayInvite(RoleViewer) || o.MayInvite("admin") {
		t.Error("an Owner invites any role, a Researcher none, and no unknown role exists")
	}
}

// Every permission is classified for tokens, and the classes say what the ADR says: read and write only, never an account,
// admin, delete, outsider-publishing or local-only permission; push and relay allow nothing yet.
func TestTokenScopesAreExhaustiveAndNarrow(t *testing.T) {
	for _, p := range All() {
		if _, ok := classes[p]; !ok {
			t.Errorf("%s has no token class: decide whether a token may use it", p)
		}
	}
	never := []Permission{AccountSelf, AccountCredentials, AccountAdmin, LifecycleDelete, ReviewPublish, PublishManage, SupportExport, ClusterAdmin, LocalOnly}
	for _, s := range Scopes() {
		tok := Actor{ID: "acct-1", Via: ViaToken, Admin: true, Workspace: 1, Role: RoleOwner, Scopes: []Scope{s}}
		for _, p := range never {
			if tok.Can(p) {
				t.Errorf("a %s token holds %s", s, p)
			}
		}
		for _, p := range All() {
			if p.NeedsRecentSignIn() && tok.Can(p) {
				t.Errorf("a %s token holds %s, which needs a recent sign-in", s, p)
			}
		}
	}
	read := Actor{ID: "a", Via: ViaToken, Workspace: 1, Role: RoleOwner, Scopes: []Scope{ScopeRead}}
	write := Actor{ID: "a", Via: ViaToken, Workspace: 1, Role: RoleOwner, Scopes: []Scope{ScopeWrite}}
	if !read.Can(DatasetRead) || read.Can(DatasetWrite) || !write.Can(DatasetRead) || !write.Can(DatasetWrite) {
		t.Error("read and write scopes do not split as stated")
	}
	for _, s := range []Scope{ScopePush, ScopeRelay} {
		tok := Actor{ID: "a", Via: ViaToken, Admin: true, Workspace: 1, Role: RoleOwner, Scopes: []Scope{s}}
		for _, p := range All() {
			if p != Anonymous && tok.Can(p) {
				t.Errorf("a %s token holds %s; its routes do not exist yet", s, p)
			}
		}
	}
	if (Actor{ID: "a", Via: ViaToken, Workspace: 1, Role: RoleViewer, Scopes: []Scope{ScopeWrite}}).Can(DatasetWrite) {
		t.Error("a token is more than its account: a Viewer's write token writes")
	}
	if Scope("admin").Known() || !ScopeRelay.Known() {
		t.Error("scope list")
	}
}
