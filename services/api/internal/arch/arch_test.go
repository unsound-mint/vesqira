// Package arch holds tests that keep the layering of the control plane honest. They do not test behaviour: they fail
// when a package starts depending on something it should not, so a boundary cannot erode through one convenient import.
package arch

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const mod = "github.com/unsound-mint/vesqira/services/api/internal/"

// allowed lists, for every internal package, the internal packages it may import directly. A new import is a design
// decision: add it here on purpose, and if the importing package faces a reviewer, re-read the reviewer boundary below.
var allowed = map[string][]string{
	"arch": {}, "audit": {}, "backup": {}, "compat": {}, "derived": {}, "regions": {}, "sourceroot": {}, "worker": {}, "schema": {},
	"boards":  {"schema"},
	"catalog": {"schema"},
	"jobs":    {"schema"},
	"runs":    {"schema"},
	"hosts":   {"worker"},
	"review":  {"boards"},
	"server":  {"audit", "boards", "catalog", "compat", "derived", "hosts", "jobs", "regions", "runs", "sourceroot", "worker"},
}

// reviewerReach is everything the reviewer-facing package may reach, directly or not. A reviewer holds a capability to a
// published snapshot and nothing else: no catalog, files, hosts, jobs, workers or runs may be reachable from that code.
var reviewerReach = []string{"boards", "schema"}

func list(t *testing.T, format string, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list", "-f", format}, args...)...).Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	return strings.Fields(string(out))
}

func internal(paths []string) []string {
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, mod) {
			out = append(out, strings.TrimPrefix(p, mod))
		}
	}
	sort.Strings(out)
	return out
}

func TestInternalPackagesImportOnlyWhatTheyAreAllowedTo(t *testing.T) {
	pkgs := list(t, "{{.ImportPath}}", "../...")
	for _, p := range internal(pkgs) {
		name := strings.TrimPrefix(p, "")
		want, known := allowed[name]
		if !known {
			t.Errorf("package %q is new: add it to the layering table in this test, with the imports it may have", name)
			continue
		}
		got := internal(list(t, `{{join .Imports "\n"}}`, mod+name))
		for _, g := range got {
			if !contains(want, g) {
				t.Errorf("%s imports %s, which the layering table does not allow", name, g)
			}
		}
	}
}

func TestTheReviewerFacingCodeReachesNothingBeyondTheSnapshotStore(t *testing.T) {
	deps := internal(list(t, `{{join .Deps "\n"}}`, mod+"review"))
	for _, d := range deps {
		if !contains(reviewerReach, d) {
			t.Errorf("the reviewer package can now reach %s: a reviewer's capability must never lead to the catalog, files, hosts, jobs, workers or runs", d)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
