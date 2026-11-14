package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type needRow struct {
	ID, Kind, State string
	Scope           struct {
		Count  int
		Folder string
	}
	Answer *struct{ Answer, Value, DeclaredBy string }
}

func (e *projEnv) bunitNeeds(t *testing.T, id int64) map[string]needRow {
	t.Helper()
	var r struct{ Needs []needRow }
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", id), nil, 200, &r)
	m := map[string]needRow{}
	for _, n := range r.Needs {
		if n.Kind == "missing_bunit" {
			m[n.ID] = n
		}
	}
	return m
}

// treeProject has VGT runs whose outputs are an orientation map, a support map and a fit R̄ map, a tool environment, and
// FITS cubes without BUNIT in a folder tree.
func treeProject(t *testing.T, root string) {
	put(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	meta := "generated_at_utc = \"2026-08-01T10:00:00Z\"\ngitcommit = \"abc1234\"\n[config]\nlambda = 0.5\n[input]\npath = \"/x/cube.fits\"\nsize_bytes = 100\n" +
		"[outputs]\npsi_path = \"/x/psi.fits\"\nreliability_path = \"/x/rel.fits\"\nsupport_path = \"/x/support.fits\"\nfit_rbar2_path = \"/x/rbar.fits\"\n"
	for _, r := range []string{"r1", "r2"} {
		put(t, root, "validation_output/"+r+"/run_metadata.toml", meta)
		for _, f := range []string{"psi.fits", "rel.fits", "support.fits", "rbar.fits"} {
			put(t, root, "validation_output/"+r+"/"+f, "x")
		}
	}
	put(t, root, "data/rohsa/a/x.fits", "x")
	put(t, root, "data/rohsa/b/y.fits", "x")
	put(t, root, "data/rohsa/b/z.fits", "x")
	put(t, root, "data/rohsa/b/deep/w.fits", "x")
	put(t, root, "data/other/o.fits", "x")
	put(t, root, ".venv-py/pyvenv.cfg", "home = /usr/bin")
	put(t, root, ".venv-py/lib/astropy/tests/data/t.fits", "x")
	put(t, root, ".julia-x/registries/General/r", "x")
	put(t, root, ".julia-x/packages/P/p.fits", "x")
}

func TestToolEnvironmentsAndNonBrightnessRunOutputsAreNotAsked(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "treeA")
	treeProject(t, root)
	s := e.open(t, root)
	got := e.bunitNeeds(t, s.ID)
	for id := range got {
		if strings.HasPrefix(id, "bunit:validation_output") || strings.Contains(id, "venv") || strings.Contains(id, "julia") {
			t.Errorf("asked about %s", id)
		}
	}
	for _, id := range []string{"bunit:data/rohsa/a", "bunit:data/rohsa/b", "bunit:data/rohsa/b/deep", "bunit:data/other"} {
		if got[id].State != "open" {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if len(got) != 4 {
		t.Errorf("%v", got)
	}
	if s.Counts.ToolEnvs != 2 {
		t.Errorf("tool environments counted: %d", s.Counts.ToolEnvs)
	}
}

type treeReply struct {
	Folder                         string
	Applied                        bool
	Folders, Files, Answered, Open int
	Sample                         []string
	WrittenToProject               bool
}

func TestAFolderTreeIsAnsweredOnceAfterAPreviewAndAnsweredNeedsAreKept(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "treeB")
	treeProject(t, root)
	s := e.open(t, root)
	url := fmt.Sprintf("/api/projects/%d/answers/tree", s.ID)

	// one folder answered by hand first: the tree answer must leave it alone
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": "bunit:data/rohsa/a", "answer": "declare", "value": "mK"}, 200, nil)

	var pre treeReply
	e.json(t, "POST", url, map[string]any{"folder": "data/rohsa", "answer": "declare", "value": "K"}, 200, &pre)
	if pre.Applied || pre.Folders != 2 || pre.Files != 3 || pre.Answered != 1 {
		t.Fatalf("preview: %+v", pre)
	}
	// a preview changes nothing
	if n := e.bunitNeeds(t, s.ID); n["bunit:data/rohsa/b"].State != "open" || n["bunit:data/rohsa/b/deep"].State != "open" {
		t.Fatalf("preview changed state: %+v", n)
	}
	if _, err := os.Stat(filepath.Join(root, ".vesqira", "project.toml")); err == nil {
		t.Fatal("a preview wrote the project file")
	}

	var done treeReply
	e.json(t, "POST", url, map[string]any{"folder": "data/rohsa", "answer": "declare", "value": "K", "confirm": true}, 200, &done)
	if !done.Applied || done.Folders != 2 || done.WrittenToProject {
		t.Fatalf("applied: %+v", done)
	}
	n := e.bunitNeeds(t, s.ID)
	for _, id := range []string{"bunit:data/rohsa/b", "bunit:data/rohsa/b/deep"} {
		a := n[id].Answer
		if n[id].State != "answered" || a == nil || a.Value != "K" || a.DeclaredBy != "you, for the folder tree data/rohsa" {
			t.Fatalf("%s: %+v", id, n[id])
		}
	}
	if a := n["bunit:data/rohsa/a"].Answer; a == nil || a.Value != "mK" || a.DeclaredBy != "you" {
		t.Fatalf("the earlier answer was overwritten: %+v", n["bunit:data/rohsa/a"])
	}
	if n["bunit:data/other"].State != "open" {
		t.Fatalf("a folder outside the tree was answered: %+v", n["bunit:data/other"])
	}
	// the files got the declaration, labelled as the person's
	var all struct {
		Datasets []struct {
			Path            string
			UnitDeclaration *struct{ Unit, DeclaredBy string }
		}
	}
	e.json(t, "GET", "/api/datasets", nil, 200, &all)
	declared := 0
	for _, d := range all.Datasets {
		if strings.Contains(d.Path, "/data/rohsa/b/") {
			if d.UnitDeclaration == nil || d.UnitDeclaration.Unit != "K" || d.UnitDeclaration.DeclaredBy != "user" {
				t.Fatalf("%s: %+v", d.Path, d.UnitDeclaration)
			}
			declared++
		}
		if strings.Contains(d.Path, "/data/rohsa/a/") && (d.UnitDeclaration == nil || d.UnitDeclaration.Unit != "mK") {
			t.Fatalf("%s: %+v", d.Path, d.UnitDeclaration)
		}
	}
	if declared != 3 {
		t.Fatalf("declared %d files under rohsa/b", declared)
	}
	// a second answer finds nothing left
	var again treeReply
	e.json(t, "POST", url, map[string]any{"folder": "data/rohsa", "answer": "declare", "value": "Jy", "confirm": true}, 200, &again)
	if again.Applied || again.Folders != 0 || again.Answered != 3 {
		t.Fatalf("second: %+v", again)
	}
}

func TestATreeAnswerIsWrittenToTheProjectFileOnceWithItsScope(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "treeC")
	treeProject(t, root)
	s := e.open(t, root)
	var r treeReply
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers/tree", s.ID), map[string]any{"folder": ".", "answer": "unknown", "confirm": true, "write": true}, 200, &r)
	if !r.Applied || r.Folders != 4 || !r.WrittenToProject {
		t.Fatalf("%+v", r)
	}
	text, err := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(text), "[[answers]]") != 4 || !strings.Contains(string(text), `declared_by = "you, for the folder tree ."`) {
		t.Fatalf("%s", text)
	}
	n := e.bunitNeeds(t, s.ID)
	if n["bunit:data/other"].State != "answered" || n["bunit:data/other"].Answer.Answer != "unknown" {
		t.Fatalf("%+v", n["bunit:data/other"])
	}
}

func TestATreeAnswerRefusesWhatItCannotTake(t *testing.T) {
	e := newProjEnv(t)
	root := e.project(t, "treeD")
	treeProject(t, root)
	s := e.open(t, root)
	url := fmt.Sprintf("/api/projects/%d/answers/tree", s.ID)
	for name, body := range map[string]map[string]any{
		"escape":     {"folder": "../x", "answer": "unknown", "confirm": true},
		"absolute":   {"folder": "/etc", "answer": "unknown", "confirm": true},
		"unclean":    {"folder": "data//rohsa", "answer": "unknown"},
		"bad answer": {"folder": "data", "answer": "confirm"},
		"bad unit":   {"folder": "data", "answer": "declare", "value": "  ", "confirm": true},
		"no unit":    {"folder": "data", "answer": "declare"},
		"a value":    {"folder": "data", "answer": "unknown", "value": "K"},
	} {
		if code, _ := e.call(t, "POST", url, body); code != 422 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if n := e.bunitNeeds(t, s.ID); n["bunit:data/other"].State != "open" {
		t.Fatalf("a refused request changed something: %+v", n)
	}
}
