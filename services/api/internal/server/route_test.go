package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// fullServer is a Server with every optional store present (empty, never called), so Handler mounts every route a build
// can have. Handlers are not run; only the declarations are read.
func fullServer(t *testing.T) (*Server, map[string]authz.Permission) {
	t.Helper()
	return fullServerWith(t, nil)
}

// fullServerWith lets a test change the server (switch it to the group server) before Handler runs.
func fullServerWith(t *testing.T, prepare func(*Server)) (*Server, map[string]authz.Permission) {
	t.Helper()
	s := &Server{EditorEnabled: true, TerminalEnabled: true, VPNEnabled: true, WebDist: t.TempDir(), Analysis: &AnalysisConfig{}}
	v := reflect.ValueOf(s).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.CanSet() && f.Kind() == reflect.Pointer && f.IsNil() {
			f.Set(reflect.New(f.Type().Elem()))
		}
	}
	declared := map[string]authz.Permission{}
	s.onRoute = func(pattern string, perm authz.Permission) {
		if _, dup := declared[pattern]; dup {
			t.Errorf("%q is declared twice on one mux", pattern)
		}
		declared[pattern] = perm
	}
	if prepare != nil {
		prepare(s)
	}
	s.Handler()
	return s, declared
}

// Every route a fully configured server mounts declares a permission that exists, and the OpenAPI file says the same thing:
// x-vesqira-permission is generated from the declarations (tools/contracts.py), and this fails if it is stale.
func TestEveryMountedRouteDeclaresAKnownPermissionAndTheSpecAgrees(t *testing.T) {
	_, declared := fullServer(t)
	if len(declared) < 200 {
		t.Fatalf("only %d routes mounted: the fixture is not enabling every optional store", len(declared))
	}
	checkDeclarationsAgainstSpec(t, declared)
}

func checkDeclarationsAgainstSpec(t *testing.T, declared map[string]authz.Permission) {
	t.Helper()
	doc := loadSpec(t)
	for pattern, perm := range declared {
		if !perm.Known() {
			t.Errorf("%s declares %q, which is not in authz.All", pattern, perm)
		}
		method, path, _ := strings.Cut(pattern, " ")
		item := doc.Paths.Find(path)
		if item == nil || item.GetOperation(method) == nil {
			t.Errorf("%s is not in the spec", pattern)
			continue
		}
		raw, ok := item.GetOperation(method).Extensions["x-vesqira-permission"]
		var got string
		if ok {
			var b []byte
			switch v := raw.(type) {
			case json.RawMessage:
				b = v
			default:
				b, _ = json.Marshal(v)
			}
			_ = json.Unmarshal(b, &got)
		}
		if got != string(perm) {
			t.Errorf("%s: the code declares %q, the spec says %q: run tools/contracts.py", pattern, perm, got)
		}
	}
}

func TestRouteChecksTheActor(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
	s.route(mux, "GET /open", authz.Anonymous, ok)
	s.route(mux, "GET /closed", authz.DatasetRead, ok)
	do := func(path string, a *authz.Actor) int {
		r := httptest.NewRequest("GET", path, nil)
		if a != nil {
			r = r.WithContext(authz.WithActor(r.Context(), *a))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	owner, stranger := authz.LocalOwner(), authz.Actor{ID: "acct-7"}
	for _, c := range []struct {
		path string
		a    *authz.Actor
		want int
	}{
		{"/open", nil, 204}, {"/closed", &owner, 204},
		{"/closed", nil, 403}, {"/closed", &stranger, 403},
	} {
		if got := do(c.path, c.a); got != c.want {
			t.Errorf("%s as %+v answered %d, want %d", c.path, c.a, got, c.want)
		}
	}
}

// The owner listener installs the local owner on every request, so a request through Handler reaches its route.
func TestHandlerActsAsTheLocalOwner(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/capabilities", nil)
	r.Host = "127.0.0.1:1"
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("the local owner was refused: %s", w.Body)
	}
}

// The audit package cannot import authz, so the actor it records in local mode is checked against it here.
func TestAuditLocalOwnerMatchesAuthz(t *testing.T) {
	o := authz.LocalOwner()
	if audit.LocalOwner.ID != o.ID || audit.LocalOwner.Via != string(o.Via) || audit.LocalOwner.OnBehalfOf != "" {
		t.Fatalf("audit.LocalOwner %+v differs from authz.LocalOwner %+v", audit.LocalOwner, o)
	}
}
