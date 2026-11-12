package server

import (
	"net/http"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// route is the only way a handler is registered on the owner listener (ADR 0050 section 4). It states what the route
// needs and checks the request's actor against it before the handler runs. In local mode the actor is the local owner,
// who holds every permission, so the check passes and the route behaves as it did before; a request that reaches a
// route with no actor in its context is refused, never let through. internal/arch fails for a registration that does
// not go through here, and tools/contracts.py writes each declaration into the OpenAPI file as x-vesqira-permission.
func (s *Server) route(mux *http.ServeMux, pattern string, perm authz.Permission, h http.HandlerFunc) {
	if s.onRoute != nil {
		s.onRoute(pattern, perm)
	}
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if perm != authz.Anonymous {
			if a, ok := authz.From(r.Context()); !ok || !a.Can(perm) {
				fail(w, http.StatusForbidden, "not permitted")
				return
			}
		}
		h(w, r)
	})
}

// withLocalOwner installs the local owner as the actor of every request, the whole of authentication in local mode: the
// listener is loopback only and localOnly has already refused anything that is not the owner's own browser or client.
func withLocalOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authz.WithActor(r.Context(), authz.LocalOwner())))
	})
}
