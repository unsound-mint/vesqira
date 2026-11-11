// Package arch holds tests that keep the layering of the control plane honest. They do not test behaviour: they fail
// when a package starts depending on something it should not, so a boundary cannot erode through one convenient import.
package arch

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const mod = "github.com/unsound-mint/vesqira/services/api/internal/"

// allowed lists, for every internal package, the internal packages it may import directly. A new import is a design
// decision: add it here on purpose, and if the importing package faces a reviewer, re-read the reviewer boundary below.
var allowed = map[string][]string{
	"arch": {}, "audit": {}, "lifecycle": {}, "backup": {}, "notify": {}, "compat": {}, "derived": {}, "regions": {}, "sourceroot": {}, "worker": {}, "schema": {}, "vgtlog": {}, "scene": {},
	"notionimport": {}, "publish": {}, "imgmeta": {},
	"transfers":  {"schema"},
	"boards":     {"schema"},
	"reports":    {"publish"},
	"catalog":    {"schema"},
	"jobs":       {"schema"},
	"sweeps":     {"schema"},
	"runs":       {"schema"},
	"discover":   {"runs"},
	"projects":   {"discover"},
	"fullschema": {"audit", "boards", "derived", "hosts", "jobs", "notify", "projects", "regions", "reports", "runs", "sweeps", "transfers"},
	"hosts":      {"worker"},
	"review":     {"boards"},
	"server":     {"audit", "boards", "catalog", "compat", "derived", "discover", "hosts", "imgmeta", "jobs", "lifecycle", "notify", "projects", "publish", "regions", "reports", "runs", "scene", "schema", "sourceroot", "sweeps", "transfers", "vgtlog", "worker"},
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

// The publish server faces the internet and holds only what was published (ADR 0036): it must reach no other
// internal package at all, so a reader or a stolen publisher key never leads to the catalog, files, hosts or runs.
func TestThePublishServerReachesNothingButItself(t *testing.T) {
	for _, d := range internal(list(t, `{{join .Deps "\n"}}`, mod+"publish")) {
		t.Errorf("the publish server can now reach %s: it must be built from its own store alone", d)
	}
}

// Every writable SQLite connection must enable foreign keys (SQLite ignores REFERENCES and ON DELETE CASCADE
// otherwise), and read-only ones must say so. A new sql.Open that does neither fails here: use catalog.WithPragmas.
func TestEverySQLiteOpenSetsForeignKeysOrIsReadOnly(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if strings.Contains(src, "sql.Open(") && !strings.Contains(src, "WithPragmas(") &&
			!strings.Contains(src, "foreign_keys(1)") && !strings.Contains(src, "mode=ro") {
			t.Errorf("%s opens SQLite without foreign_keys(1): wrap the DSN in catalog.WithPragmas, or open it mode=ro", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A file past this size is two packages' worth of code in one place. internal/server already holds the largest files, so
// this is a tripwire on growth, not a style rule: when it fires, move that area's logic into its own package (as
// boards and runs are) and keep only the HTTP handlers in server.
func TestNoGoFileGrowsWithoutBound(t *testing.T) {
	const limit = 1300
	_ = filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, _ := os.ReadFile(path)
		if n := strings.Count(string(b), "\n"); n > limit {
			t.Errorf("%s has %d lines (limit %d): split it by responsibility", path, n, limit)
		}
		return nil
	})
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
