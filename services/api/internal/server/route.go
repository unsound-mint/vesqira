package server

import (
	"context"
	"net/http"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// route is the only way a handler is registered on the owner listener (ADR 0050 section 4). It states what the route
// needs and checks the request's actor against it before the handler runs. In local mode the actor is the local owner,
// who holds every permission, so the check passes and the route behaves as it did before; a request that reaches a
// route with no actor in its context is refused, never let through. internal/arch fails for a registration that does
// not go through here, and tools/contracts.py writes each declaration into the OpenAPI file as x-vesqira-permission.
//
// On the group server (--multi-user) a route whose permission is not available there (notOnGroupServer: the local-only
// features, clusters, jobs, sweeps and host access, ADR 0050 sections 1 and 5) is not mounted: its pattern answers 404 and no
// handler is reachable. A request without an actor there is answered 401 (sign in), and a permission that needs a recent
// sign-in answers 401 with code reauth_required when the session's sign-in is older than the window.
func (s *Server) route(mux *http.ServeMux, pattern string, perm authz.Permission, h http.HandlerFunc) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if perm != authz.Anonymous {
			a, ok := authz.From(r.Context())
			switch {
			case !ok && signInRequired(r.Context()):
				failCode(w, http.StatusUnauthorized, "signin_required", "sign in to use this", nil)
				return
			case !ok || !a.Can(perm):
				fail(w, http.StatusForbidden, "not permitted")
				return
			case perm.NeedsRecentSignIn() && !a.Recent:
				failCode(w, http.StatusUnauthorized, "reauth_required", "confirm your password and code to continue: this needs a sign-in within the last 15 minutes", nil)
				return
			}
			r = r.WithContext(audit.WithActor(r.Context(), audit.Actor{ID: a.ID, Via: string(a.Via)}))
		}
		h(w, r)
	}
	if s.multiUser() && notOnGroupServer[perm] {
		handler = func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "not found") }
	} else if s.onRoute != nil {
		s.onRoute(pattern, perm)
	}
	mux.HandleFunc(pattern, handler)
}

// notOnGroupServer are the permissions whose routes the group server does not mount (ADR 0050 section 9, slices 1 to 4: no ssh
// credential on the server, so no cluster, job, sweep or host route; and the local-only features never).
var notOnGroupServer = map[authz.Permission]bool{
	authz.LocalOnly: true, authz.ClusterRead: true, authz.ClusterUse: true, authz.ClusterAdmin: true,
	authz.JobRead: true, authz.JobSubmit: true, authz.JobAccess: true, authz.SweepRead: true, authz.SweepSubmit: true,
}

// withLocalOwner installs the local owner as the actor of every request, the whole of authentication in local mode: the
// listener is loopback only and localOnly has already refused anything that is not the owner's own browser or client.
func withLocalOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authz.WithActor(r.Context(), authz.LocalOwner())))
	})
}

type signInKey struct{}

// signInRequired is set by the group server's actor middleware: a request there that carries no actor is told to sign in.
func signInRequired(ctx context.Context) bool { v, _ := ctx.Value(signInKey{}).(bool); return v }
