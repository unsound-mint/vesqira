package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Deleting every rule sticks even while project.toml still holds them: the file is read back only into a project whose
// researcher has never saved a list; removing the section is its own confirmed write.
func TestImportDeletingAllRulesSticksWhileTheFileStillHoldsThem(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	base := fmt.Sprintf("/api/projects/%d/import", s.ID)
	e.putJSON(t, base+"/rules", map[string]any{"rules": []any{psiRule}})
	e.json(t, "POST", base+"/rules/write", map[string]any{"write": true}, 200, nil)
	e.settle(t, s.ID)
	e.putJSON(t, base+"/rules", map[string]any{"rules": []any{}})
	e.call(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), nil)
	e.settle(t, s.ID)
	var st struct {
		Rules []any
		File  struct{ State string }
	}
	e.json(t, "GET", base+"/rules", nil, 200, &st)
	if len(st.Rules) != 0 || st.File.State != "differs" {
		t.Fatalf("the deleted rules came back from the file: %+v", st)
	}
	// removing the section is a confirmed write of the empty list; the rest of the file stays
	e.json(t, "POST", base+"/rules/write", map[string]any{"write": true}, 200, nil)
	b, _ := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if strings.Contains(string(b), "import.rules") || !strings.Contains(string(b), "# mine") {
		t.Fatalf("%s", b)
	}
	e.settle(t, s.ID)
	e.json(t, "GET", base+"/rules", nil, 200, &st)
	if st.File.State != "none" {
		t.Fatalf("%+v", st)
	}
}

// Applying inside a folder does not turn the project's count into that folder's.
func TestImportApplyInASubfolderKeepsTheWholeProjectCount(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{psiRule, kmsRule}})
	counts := func() (int, int) {
		e.call(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), nil)
		e.settle(t, s.ID)
		var out struct {
			ImportRules *struct{ Matched, Pending int } `json:"importRules"`
		}
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d", s.ID), nil, 200, &out)
		return out.ImportRules.Matched, out.ImportRules.Pending
	}
	if m, p := counts(); m != 6 || p != 6 {
		t.Fatalf("%d %d", m, p)
	}
	var out applyOut
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/apply", s.ID), map[string]any{"confirm": true, "folder": "vda"}, 200, &out)
	if len(out.Results) != 3 {
		t.Fatalf("%+v", out)
	}
	var sum struct {
		ImportRules *struct{ Matched, Pending int } `json:"importRules"`
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d", s.ID), nil, 200, &sum)
	if sum.ImportRules.Matched != 6 || sum.ImportRules.Pending != 6 {
		t.Fatalf("a subfolder walk replaced the project's count: %+v", sum.ImportRules)
	}
	if m, p := counts(); m != 6 || p != 3 {
		t.Fatalf("%d %d", m, p)
	}
}

// An "ignore" rule is honoured by the scan's own registration too: the file stays listed and is not registered.
func TestImportIgnoreRuleStopsTheScanRegisteringThoseFiles(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "ign")
	put(t, root, ".vesqira/project.toml", "[[import.rules]]\nname = \"scratch\"\nkind = \"glob\"\npattern = \"scratch/**\"\nrole = \"ignore\"\n")
	tinyFITS(t, root, "keep/a.fits", 1, "")
	tinyFITS(t, root, "scratch/b.fits", 2, "")
	s := e.open(t, root)
	facts, _ := e.srv.Store.FactsByPath(workspace.Local, 0)
	if _, ok := facts[filepath.Join(root, "keep/a.fits")]; !ok {
		t.Fatal("the other file is registered as before")
	}
	if _, ok := facts[filepath.Join(root, "scratch/b.fits")]; ok {
		t.Fatal("a scan registered a file the researcher's rule ignores")
	}
	p := e.preview(t, s.ID, map[string]any{})
	if n, ok := p.group("ignore", ".fits", "scratch"); !ok || n != 1 {
		t.Fatalf("the ignored file stays listed, under the rule: %+v", p.Groups)
	}
}

// Two previews asked for at once make one walk: the second waits and uses the first's answer.
func TestImportWalksOnceForConcurrentPreviews(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	p, _ := e.srv.Projects.Get(workspace.Local, s.ID)
	got := make(chan *importInv, 2)
	for i := 0; i < 2; i++ {
		go func() {
			inv, code, msg := e.srv.inventory(context.Background(), p, "", true)
			if code != 0 {
				t.Errorf("%d %s", code, msg)
			}
			got <- inv
		}()
	}
	a, b := <-got, <-got
	if a == nil || a != b {
		t.Fatal("two concurrent previews walked twice")
	}
}

func TestWhoDeclaredAnOrientationIsSaidTruthfully(t *testing.T) {
	t.Parallel()
	if got := reviewerAuthor("rule:psi maps"); strings.Contains(got, "psi maps") || !strings.Contains(got, "import rule") || !strings.Contains(got, "not the file") {
		t.Fatalf("%q", got)
	}
	if reviewerAuthor("user") != "the author of this board" || !strings.Contains(reviewerAuthor("vgt-run-adapter"), "run import adapter") || !strings.Contains(reviewerAuthor("analysis"), "analysis") {
		t.Fatal("authors")
	}
	// a run import may replace nothing, or a proposal; never the researcher's declaration or a rule's
	for by, want := range map[string]bool{"user": false, "rule:psi maps": false, "vgt-run-adapter": true, "relative-orientation": true} {
		if got := adapterMayDeclare(&catalog.Interpretation{DeclaredBy: by}); got != want {
			t.Errorf("%s: %v", by, got)
		}
	}
	if !adapterMayDeclare(nil) {
		t.Fatal("nothing declared yet")
	}
}
