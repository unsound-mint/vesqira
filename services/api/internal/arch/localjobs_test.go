package arch

import (
	"go/ast"
	"sort"
	"testing"
)

// jobFiles are the files of internal/server that belong to the job feature itself, whose routes the group server never mounts
// (ADR 0050 owner answer 3): only they may use the job store directly. Code shared with mounted routes reads it through
// localJobs, which is nil on the group server, because the jobs table is not scoped by workspace (scope_test.go, localOnlyTables)
// and an adopted catalog brings workspace 1's jobs in.
var jobFiles = set("jobs.go", "jobwatch.go", "sweeps.go", "sweepfamily.go", "hostwrite.go", "hoststorage.go", "runenv.go", "analysis.go",
	"capabilities.go", "server.go", "route.go", "rerun_input.go",
	"relay_local.go",                                  // the key holder's side of relayed run requests: local-only routes that render and submit drafts
	"relay_grants_local.go", "relay_grants_submit.go") // its standing approvals (ADR 0074), local-only as well

func TestMountedCodeReadsJobsThroughLocalJobs(t *testing.T) {
	var bad []string
	for name, af := range parseDir(t, "../server") {
		if jobFiles[name] {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "Jobs" {
				if x, ok := se.X.(*ast.Ident); ok && x.Name == "s" {
					bad = append(bad, name)
				}
			}
			return true
		})
	}
	sort.Strings(bad)
	for _, f := range bad {
		t.Errorf("internal/server/%s uses s.Jobs: read jobs through s.localJobs() (nil on the group server), or move the code into a job file", f)
	}
}
