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
