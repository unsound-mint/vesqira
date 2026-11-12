package server

import (
	"context"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
	"net/http"
	"strings"

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
//
// Slice 3 (workspaces.go): on the group server a route whose path names an object acts in that object's workspace, with the
// actor's role there; an object the actor cannot see answers 404 like a missing one, before the permission is checked, so a
// status never tells a stranger that an id exists. Then the role must hold the permission (403 otherwise), and for the
// permissions of the ownership rule (authz.OwnershipApplies) a role below Maintainer acts only on what the actor created. A
// mounted route with a path parameter nobody classified does not start.
func (s *Server) route(mux *http.ServeMux, pattern string, perm authz.Permission, h http.HandlerFunc) {
	unmounted := (s.multiUser() && (notOnGroupServer[perm] || pathsNotOnGroupServer[pattern] || groupServerOff(pattern))) ||
		(!s.multiUser() && groupServerOnly[perm])
	scoped := ""
	if s.multiUser() && !unmounted && perm != authz.Anonymous {
		var err error
		if scoped, err = scopeOf(pattern); err != nil {
			panic("server: " + err.Error())
		}
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if perm != authz.Anonymous {
			a, ok := authz.From(r.Context())
			if ok && scoped != "" {
				_, param, _ := strings.Cut(scoped, "/")
				ws, role, by, found := s.objectWorkspace(scoped, r.PathValue(param), a.ID, a.Workspace)
				if !found || (a.TokenWorkspace != 0 && ws != a.TokenWorkspace) { // a bound token sees nothing of another workspace
					fail(w, http.StatusNotFound, "not found")
					return
				}
				a.Workspace, a.Role = ws, role
				if authz.OwnershipApplies(perm) && a.Can(perm) && !a.Owns(by) {
					fail(w, http.StatusForbidden, "only the person who created this, a Maintainer or an Owner can change it")
					return
				}
				r = r.WithContext(authz.WithActor(r.Context(), a))
			}
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
			r = r.WithContext(audit.WithActor(r.Context(), audit.Actor{ID: a.ID, Via: string(a.Via), Workspace: a.Workspace}))
		}
		h(w, r)
	}
	if unmounted {
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
	// the publish server: its publisher key is not yet per workspace (ADR 0050 section 3)
	authz.PublishManage: true,
}

// groupServerOnly are the permissions whose routes only the group server mounts: a local instance never accepts a push (its
// local owner holds every permission, so without this the routes would answer there), and has nothing pushed to read.
var groupServerOnly = map[authz.Permission]bool{authz.PushUpload: true, authz.PushedRead: true, authz.PushedArchive: true,
	authz.RelayServe: true, authz.RunRequestDecline: true, authz.RunRequestRead: true, authz.RunRequestFile: true}

// groupServerOff are the route prefixes of the publish server's side of the catalog (comments readers left on a published
// report, reply drafts, readers), which share permissions with routes that stay. Their tables are local-only
// (internal/arch scope_test.go) until a publisher key per workspace exists.
func groupServerOff(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	for _, p := range []string{"/api/report-comments", "/api/reply-drafts", "/api/publish"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// pathsNotOnGroupServer: none since slice 3. Path completion and opening a folder as a project are mounted again, confined to
// the workspace's data roots.
var pathsNotOnGroupServer = map[string]bool{}

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

// wsOf is the workspace the request acts in: the actor's (workspace.Local for the local owner). Without an actor it is 0, which
// no row belongs to, so a store queried with it finds nothing.
func wsOf(r *http.Request) workspace.ID { return wsCtx(r.Context()) }

func wsCtx(ctx context.Context) workspace.ID {
	if a, ok := authz.From(ctx); ok {
		return a.Workspace
	}
	return 0
}

// actorID is the account id the request acts for, recorded as created_by on what it creates ("" without an actor).
func actorID(ctx context.Context) string {
	if a, ok := authz.From(ctx); ok {
		return a.ID
	}
	return ""
}

// localJobs is the job store for code that mounted routes share with local mode (board panels that follow a job, a run's
// lineage, rerun settings, project needs). Jobs are a local-only feature whose table is not scoped by workspace (internal/arch
// scope_test.go), and an adopted catalog brings workspace 1's jobs onto the group server, so there it is nil: a job is never
// looked up from a request in another workspace. internal/arch TestMountedCodeReadsJobsThroughLocalJobs keeps it that way.
func (s *Server) localJobs() *jobs.Store {
	if s.multiUser() {
		return nil
	}
	return s.Jobs
}
