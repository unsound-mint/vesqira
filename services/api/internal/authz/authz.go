// Package authz names who is acting and what a route needs (ADR 0050, slice 1). Today there is one actor, the implicit
// local owner, who holds every permission, so a check never fails and nothing behaves differently. The point of the
// package is that every route already says what it needs: the accounts, sessions and roles of the later slices change
// who the actor is and how Can decides, not which routes exist or what they declare.
//
// It imports nothing from the program, so the reviewer-facing packages (internal/review, internal/publish) keep their own,
// narrower capability checks and never see it.
package authz

import "context"

// Via says how an actor was authenticated. Slice 1 has only ViaLocal.
type Via string

const ViaLocal Via = "local"

// LocalOwnerID is the account id the audit log records for the implicit local owner.
const LocalOwnerID = "local-owner"

// Actor is whoever a request acts for.
type Actor struct {
	ID   string // account id; LocalOwnerID in local mode
	Name string // display name
	Via  Via
}

// LocalOwner is the one principal of local mode: it needs no login and holds every permission in the single implicit
// workspace.
func LocalOwner() Actor { return Actor{ID: LocalOwnerID, Name: "Local owner", Via: ViaLocal} }

// IsLocalOwner reports whether the actor is the implicit local owner.
func (a Actor) IsLocalOwner() bool { return a.ID == LocalOwnerID }

// Can decides whether the actor holds a permission. The local owner holds all of them; no other kind of actor exists yet,
// so everything else is refused (default deny), which is the rule the roles of slice 3 will refine.
func (a Actor) Can(p Permission) bool {
	if p == Anonymous {
		return true
	}
	return a.IsLocalOwner() && p.Known()
}

type ctxKey struct{}

// WithActor returns a context that carries the actor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// From returns the actor in the context, if a middleware installed one.
func From(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKey{}).(Actor)
	return a, ok
}
