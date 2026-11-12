// Package authz names who is acting and what a route needs (ADR 0050). In local mode there is one actor, the implicit local
// owner, who holds every permission, so a check never fails and nothing behaves differently. On the group server
// (--multi-user, slice 2) the actor is an account reached through a session or a personal access token: every account is
// an admin until the roles of slice 3, and holds every permission except the local-only ones; a token holds only what its
// scopes allow, and never an account or admin permission.
//
// It imports nothing from the program, so the reviewer-facing packages (internal/review, internal/publish) keep their own,
// narrower capability checks and never see it.
package authz

import "context"

// Via says how an actor was authenticated.
type Via string

const (
	ViaLocal   Via = "local"   // the implicit local owner
	ViaSession Via = "session" // a signed-in browser session on the group server
	ViaToken   Via = "token"   // a personal access token (Authorization: Bearer)
)

// LocalOwnerID is the account id the audit log records for the implicit local owner.
const LocalOwnerID = "local-owner"

// Actor is whoever a request acts for.
type Actor struct {
	ID   string // account id; LocalOwnerID in local mode
	Name string // display name
	Via  Via
	// Admin is the server-wide admin flag of the account (ADR 0050 section 3). It is never implied: an actor without it holds
	// nothing in slice 2.
	Admin bool
	// Scopes are the scopes of the token the actor came through; empty for a session.
	Scopes []Scope
	// Recent is true when the session signed in (password and second factor) within the recent sign-in window, which some
	// permissions need (NeedsRecentSignIn). The local owner always is.
	Recent bool
}

// LocalOwner is the one principal of local mode: it needs no login and holds every permission in the single implicit
// workspace.
func LocalOwner() Actor {
	return Actor{ID: LocalOwnerID, Name: "Local owner", Via: ViaLocal, Recent: true}
}

// IsLocalOwner reports whether the actor is the implicit local owner.
func (a Actor) IsLocalOwner() bool { return a.ID == LocalOwnerID && a.Via == ViaLocal }

// Can decides whether the actor holds a permission. Default deny: an unknown permission, an actor with no recognised way in,
// and an account without the admin flag hold nothing.
func (a Actor) Can(p Permission) bool {
	if p == Anonymous {
		return true
	}
	if !p.Known() {
		return false
	}
	if a.IsLocalOwner() {
		return true
	}
	if a.ID == "" || a.ID == LocalOwnerID || p == LocalOnly || !a.Admin {
		return false
	}
	switch a.Via {
	case ViaSession:
		return true
	case ViaToken:
		for _, s := range a.Scopes {
			if s.Allows(p) {
				return true
			}
		}
	}
	return false
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
