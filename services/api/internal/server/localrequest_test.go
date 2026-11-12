package server

import (
	"net/http"
	"net/http/httptest"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// localRequest is a request as the route middleware hands it to a handler in local mode: it carries the local owner, who acts in
// workspace 1. Tests that call a helper with a bare request would otherwise ask workspace 0, which holds nothing.
func localRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(authz.WithActor(r.Context(), authz.LocalOwner()))
}
