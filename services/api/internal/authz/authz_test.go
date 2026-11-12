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

// Slice 2: a signed-in admin holds every permission except the local-only one; without the admin flag, nothing.
func TestSessionAdminHoldsEverythingButLocalOnly(t *testing.T) {
	admin := Actor{ID: "acct-1", Via: ViaSession, Admin: true}
	plain := Actor{ID: "acct-2", Via: ViaSession}
	for _, p := range All() {
		if want := p != LocalOnly; admin.Can(p) != want {
			t.Errorf("admin session Can(%s) = %v, want %v", p, !want, want)
		}
		if p != Anonymous && plain.Can(p) {
			t.Errorf("an account without the admin flag holds %s", p)
		}
	}
	if (Actor{ID: LocalOwnerID, Via: ViaSession, Admin: true}).IsLocalOwner() {
		t.Error("an account that calls itself local-owner is not the local owner")
	}
	if (Actor{ID: LocalOwnerID, Via: ViaSession, Admin: true}).Can(DatasetRead) {
		t.Error("an account with the local owner's id holds a permission")
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
		tok := Actor{ID: "acct-1", Via: ViaToken, Admin: true, Scopes: []Scope{s}}
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
	read := Actor{ID: "a", Via: ViaToken, Admin: true, Scopes: []Scope{ScopeRead}}
	write := Actor{ID: "a", Via: ViaToken, Admin: true, Scopes: []Scope{ScopeWrite}}
	if !read.Can(DatasetRead) || read.Can(DatasetWrite) || !write.Can(DatasetRead) || !write.Can(DatasetWrite) {
		t.Error("read and write scopes do not split as stated")
	}
	for _, s := range []Scope{ScopePush, ScopeRelay} {
		tok := Actor{ID: "a", Via: ViaToken, Admin: true, Scopes: []Scope{s}}
		for _, p := range All() {
			if p != Anonymous && tok.Can(p) {
				t.Errorf("a %s token holds %s; its routes do not exist yet", s, p)
			}
		}
	}
	if (Actor{ID: "a", Via: ViaToken, Scopes: []Scope{ScopeWrite}}).Can(DatasetRead) {
		t.Error("a token is more than its account: an account without the admin flag holds nothing")
	}
	if Scope("admin").Known() || !ScopeRelay.Known() {
		t.Error("scope list")
	}
}
