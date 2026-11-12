// Package authz names who is acting and what a route needs (ADR 0050). In local mode there is one actor, the implicit local
// owner, who holds every permission in the one workspace, so a check never fails and nothing behaves differently. On the
// group server (--multi-user) the actor is an account reached through a session or a personal access token, acting in one
// workspace with the role its membership gives it there (slice 3, roles.go); the admin flag adds the server-wide account and
// workspace administration, never a workspace's content. A token holds only what its scopes allow, and never an account or
// admin permission.
//
// It imports only the workspace id type, so the reviewer-facing packages (internal/review, internal/publish) keep their own,
// narrower capability checks and never see it.
package authz

import (
	"context"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

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
	// Workspace is the workspace the request acts in, and Role the actor's role there, read from the membership on every
	// request (a role change or removal takes effect at once). Zero and RoleNone when the account is a member of none: then it
	// holds no workspace permission, and a store queried with workspace 0 finds nothing.
	Workspace workspace.ID
	Role      Role
}

// LocalOwner is the one principal of local mode: it needs no login and holds every permission in the single implicit
// workspace.
func LocalOwner() Actor {
	return Actor{ID: LocalOwnerID, Name: "Local owner", Via: ViaLocal, Recent: true, Workspace: workspace.Local, Role: RoleOwner}
}

// IsLocalOwner reports whether the actor is the implicit local owner.
func (a Actor) IsLocalOwner() bool { return a.ID == LocalOwnerID && a.Via == ViaLocal }

// Can decides whether the actor holds a permission. Default deny: an unknown permission, an actor with no recognised way in, a
// workspace permission without a membership, and an admin permission without the admin flag hold nothing. A token is then
// limited to its scopes.
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
	if a.ID == "" || a.ID == LocalOwnerID || p == LocalOnly {
		return false
	}
	if adminOnly, ok := accountLevel[p]; ok {
		if adminOnly && !a.Admin {
			return false
		}
	} else if min := matrix[p]; min == RoleNone || a.Workspace <= 0 || !a.Role.AtLeast(min) {
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
