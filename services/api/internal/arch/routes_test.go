package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ADR 0050 section 4: every route of the owner listener says what permission it needs, through s.route, and nothing
// registers a handler any other way. The checks read the source, so a route in a branch no test configures (a feature
// that is off by default) is covered too.

var patternShape = regexp.MustCompile(`^(GET|POST|PUT|DELETE|PATCH) /\S*$`)

func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s (%v)", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = af
	}
	return out
}

// permissionNames returns the constants of type Permission in internal/authz, and the ones All lists.
func permissionNames(t *testing.T) (declared, listed map[string]bool) {
	t.Helper()
	declared, listed = map[string]bool{}, map[string]bool{}
	for _, af := range parseDir(t, "../authz") {
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Permission" {
						for _, n := range vs.Names {
							declared[n.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name.Name == "All" && d.Recv == nil {
					ast.Inspect(d.Body, func(n ast.Node) bool {
						if cl, ok := n.(*ast.CompositeLit); ok {
							for _, e := range cl.Elts {
								if id, ok := e.(*ast.Ident); ok {
									listed[id.Name] = true
								}
							}
						}
						return true
					})
				}
			}
		}
	}
	return declared, listed
}

func TestAuthzAllListsEveryPermission(t *testing.T) {
	declared, listed := permissionNames(t)
	if len(declared) == 0 {
		t.Fatal("found no permissions in internal/authz")
	}
	for n := range declared {
		if !listed[n] {
			t.Errorf("authz.%s is declared but missing from authz.All()", n)
		}
	}
	for n := range listed {
		if !declared[n] {
			t.Errorf("authz.All() lists %s, which is not a declared permission", n)
		}
	}
}

// Guard 1: every route is declared exactly once, with a permission that exists.
func TestEveryRouteDeclaresExactlyOnePermission(t *testing.T) {
	declared, _ := permissionNames(t)
	routes := 0
	for name, af := range parseDir(t, "../server") {
		for _, d := range af.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			seen := map[string]bool{} // one registry per function: the in-process offline mux repeats four owner patterns on purpose
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "route" {
					return true
				}
				routes++
				where := name + " " + fn.Name.Name
				if len(call.Args) != 4 {
					t.Errorf("%s: route takes (mux, pattern, permission, handler), got %d arguments", where, len(call.Args))
					return true
				}
				lit, ok := call.Args[1].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: a route's pattern must be a string literal, so tools/contracts.py and this test can read it", where)
					return true
				}
				pat, _ := strconv.Unquote(lit.Value)
				if pat != "GET /" && !patternShape.MatchString(pat) {
					t.Errorf("%s: pattern %q is not \"METHOD /path\"", where, pat)
				}
				if seen[pat] {
					t.Errorf("%s: %q is declared twice", where, pat)
				}
				seen[pat] = true
				perm, ok := call.Args[2].(*ast.SelectorExpr)
				good := ok && declared[perm.Sel.Name]
				if ok {
					id, isID := perm.X.(*ast.Ident)
					good = good && isID && id.Name == "authz"
				}
				if !good {
					t.Errorf("%s: %q must name a permission as authz.<Name> from internal/authz", where, pat)
				}
				return true
			})
		}
	}
	if routes < 200 {
		t.Errorf("found only %d route declarations in internal/server: the check is not reading what it should", routes)
	}
}

// Guard 2: no bare registration on the owner listener. The single Handle call allowed is the one inside route itself.
// internal/review and internal/publish register their own capability-token routes and may not import authz
// (TestTheReviewerFacingCodeReachesNothingBeyondTheSnapshotStore, TestThePublishServerReachesNothingButItself).
func TestNoBareRouteRegistrationInTheOwnerServer(t *testing.T) {
	bare := 0
	for name, af := range parseDir(t, "../server") {
		for _, d := range af.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
					return true
				}
				if fn.Name.Name == "route" && name == "route.go" {
					bare++
					return true
				}
				t.Errorf("%s %s calls %s directly: register through s.route(mux, pattern, authz.<Permission>, handler)", name, fn.Name.Name, sel.Sel.Name)
				return true
			})
		}
	}
	if bare != 1 {
		t.Errorf("expected exactly one registration (inside s.route), found %d", bare)
	}
}
