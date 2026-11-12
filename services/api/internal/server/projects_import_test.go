package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// tinyFITS writes a valid 4x4 float32 primary HDU whose data are seeded by seed (equal seeds are equal bytes). With bunit the
// header states its own unit.
func tinyFITS(t *testing.T, root, rel string, seed byte, bunit string) {
	t.Helper()
	card := func(s string) string { return s + strings.Repeat(" ", 80-len(s)) }
	h := card("SIMPLE  =                    T") + card("BITPIX  =                  -32") + card("NAXIS   =                    2") +
		card("NAXIS1  =                    4") + card("NAXIS2  =                    4")
	if bunit != "" {
		h += card(fmt.Sprintf("BUNIT   = '%-8s'", bunit))
	}
	h += card("END")
	h += strings.Repeat(" ", 2880-len(h)%2880)
	data := bytes.Repeat([]byte{seed, 0, 0, 0}, 16)
	data = append(data, make([]byte, 2880-len(data)%2880)...)
	put(t, root, rel, h+string(data))
}

// importWorker answers a header for the tiny files above without Python: two axes of 4, and the unit when the file states one.
type importWorker struct{}

func (importWorker) Header(_ context.Context, p string) (json.RawMessage, error) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 2880 || !bytes.HasPrefix(b, []byte("SIMPLE")) {
		return nil, errors.New("not a FITS file")
	}
	if i := bytes.Index(b[:2880], []byte("BUNIT")); i >= 0 {
		return json.RawMessage(`{"axes":[{"length":4},{"length":4}],"bunit":"K","bunit_status":"declared"}`), nil
	}
	return json.RawMessage(`{"axes":[{"length":4},{"length":4}],"bunit":null,"bunit_status":"absent"}`), nil
}

// putJSON is e.call for PUT with a JSON content type.
func (e *projEnv) putJSON(t *testing.T, p string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", e.ts.URL+p, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func importEnv(t *testing.T) *projEnv {
	e := newProjEnv(t)
	e.srv.Worker = importWorker{}
	return e
}

// the owner's layout, in miniature
func ownerProject(t *testing.T, root string) {
	put(t, root, ".vesqira/project.toml", "# mine\n[project]\nname = \"owner\"\n")
	tinyFITS(t, root, "validation_output/rohsa/n4/maps/mu_kms.fits", 1, "")
	tinyFITS(t, root, "validation_output/rohsa/n4/maps/sigma_kms.fits", 2, "")
	put(t, root, "validation_output/rohsa/n4/rohsa_n4_run_trace.toml", "n_gauss = 4\nregion = \"n4\"\nstatus = \"ran\"\n")
	put(t, root, "validation_output/rohsa/n4/rohsa_n4.dat", "# i j\n")
	tinyFITS(t, root, "data/exp_raw/cube_a.fits", 3, "")
	put(t, root, "data/sims/box.h5", "x")
	tinyFITS(t, root, "vda/n3/vgt psi maps/psi_1.fits", 4, "")
	tinyFITS(t, root, "vda/n3/vgt psi maps/psi_2.fits", 5, "")
	tinyFITS(t, root, "vda/n4/vgt psi maps/psi_1.fits", 6, "")
	tinyFITS(t, root, "extra/own_unit_kms.fits", 7, "Jy/beam")
	put(t, root, "stars/s1.dat", "a b\n1 2\n3 4\n")
	put(t, root, "README.md", "x")
	put(t, root, "blind_set_key/key_DO_NOT_OPEN.csv", "secret")
}

type previewOut struct {
	Folder       string `json:"folder"`
	Entries      int    `json:"entries"`
	Truncated    bool   `json:"truncated"`
	TruncatedWhy string `json:"truncatedWhy"`
	Files        int    `json:"files"`
	RulesFrom    string `json:"rulesFrom"`
	Groups       []struct {
		Role, Ext, By, Why string
		Count              int
		Bytes              int64
		Samples            []struct {
			Path         string
			ProposedRole string
			By           string
			Rule         string
			Matched      []string
			Declares     []string
			Warnings     []string
			Why          string
		}
	} `json:"groups"`
	Rules []struct {
		Name                    string
		Error                   string
		Matches, Wins, Shadowed int
		Examples                []string
	} `json:"rules"`
	Conflicts []struct {
		Path, Winner string
		Rules        []string
		Differ       bool
	} `json:"conflicts"`
	ConflictCount int `json:"conflictCount"`
	Selected      struct {
		Files, Pending, Applied int
		ByRole                  map[string]int
	} `json:"selected"`
	Skipped struct {
		NeverRead    []string
		ArchiveCount int
	} `json:"skipped"`
}

func (e *projEnv) preview(t *testing.T, id int64, body map[string]any) previewOut {
	t.Helper()
	var out previewOut
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/preview", id), body, 200, &out)
	return out
}

func (p previewOut) group(role, ext, by string) (int, bool) {
	for _, g := range p.Groups {
		if g.Role == role && g.Ext == ext && g.By == by {
			return g.Count, true
		}
	}
	return 0, false
}

var psiRule = map[string]any{"name": "psi maps", "kind": "glob", "pattern": "vda/*/vgt psi maps/psi_*.fits", "role": "orientation", "angleUnit": "rad", "angleFrame": "pixel", "quantity": "b_field"}
var kmsRule = map[string]any{"name": "kms", "kind": "glob", "pattern": "*_kms.fits", "role": "dataset", "unit": "km/s"}

func datasetCount(t *testing.T, e *projEnv) int {
	ds, err := e.cat.List(workspace.Local)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds)
}

// The preview is read-only and says, for every file, what the built-in rules recognise and why; what they do not recognise is
// listed by extension, and nothing marked as not to be opened is ever listed.
func TestImportPreviewGroupsWhatIsAndIsNotRecognisedAndChangesNothing(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	before := datasetCount(t, e)
	file := filepath.Join(root, ".vesqira", "project.toml")
	toml0, _ := os.ReadFile(file)

	p := e.preview(t, s.ID, map[string]any{})
	if p.RulesFrom != "saved" || p.Files < 10 {
		t.Fatalf("%+v", p)
	}
	if n, ok := p.group("dataset", ".fits", "built-in"); !ok || n != 5 {
		t.Fatalf("FITS files are datasets by the built-in rules: %+v", p.Groups)
	}
	if n, ok := p.group("rohsa_product", ".fits", "built-in"); !ok || n != 2 {
		t.Fatalf("the ROHSA maps are products of the fit: %+v", p.Groups)
	}
	if _, ok := p.group("rohsa_fit", ".dat", "built-in"); !ok {
		t.Fatalf("%+v", p.Groups)
	}
	if _, ok := p.group("run_record", ".toml", "built-in"); !ok {
		t.Fatalf("%+v", p.Groups)
	}
	if _, ok := p.group("other_discipline", ".h5", "built-in"); !ok {
		t.Fatalf("a file of another discipline is listed without a viewer: %+v", p.Groups)
	}
	if n, ok := p.group("other", ".dat", "built-in"); !ok || n != 1 {
		t.Fatalf("a table outside a declared data folder is not offered as a catalog: %+v", p.Groups)
	}
	if _, ok := p.group("other", ".md", "built-in"); !ok {
		t.Fatalf("%+v", p.Groups)
	}
	for _, g := range p.Groups {
		if g.Why == "" || g.Count < len(g.Samples) {
			t.Fatalf("a group without a reason: %+v", g)
		}
		for _, sm := range g.Samples {
			if strings.Contains(strings.ToLower(sm.Path), "do_not_open") || strings.Contains(sm.Path, "blind_set_key") {
				t.Fatalf("a never-read name was listed: %s", sm.Path)
			}
		}
	}
	if len(p.Skipped.NeverRead) != 1 || p.Skipped.NeverRead[0] != "blind_set_key" {
		t.Fatalf("%+v", p.Skipped)
	}
	// a chosen folder narrows the walk; paths stay relative to the project; a folder outside it is refused
	sub := e.preview(t, s.ID, map[string]any{"folder": "vda"})
	if sub.Folder != "vda" || sub.Files != 3 {
		t.Fatalf("%+v", sub)
	}
	for _, bad := range []string{"../x", "/etc", "vda/../../x", "nope", "README.md"} {
		if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/import/preview", s.ID), map[string]any{"folder": bad}); code != 422 {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	// opening a group lists more of its files
	sub = e.preview(t, s.ID, map[string]any{"expand": "dataset|.fits|built-in"})
	for _, g := range sub.Groups {
		if g.Role == "dataset" && len(g.Samples) != g.Count {
			t.Fatalf("expand: %+v", g)
		}
	}
	// nothing changed: no dataset, no application record, no byte of project.toml
	if datasetCount(t, e) != before {
		t.Fatal("the preview registered something")
	}
	if toml1, _ := os.ReadFile(file); !bytes.Equal(toml0, toml1) {
		t.Fatalf("the preview wrote project.toml: %q", toml1)
	}
	if a, _ := e.srv.Projects.AppliedFiles(s.ID); len(a) != 0 {
		t.Fatalf("%+v", a)
	}
	if rs, _ := e.srv.Projects.Rules(s.ID); len(rs) != 0 {
		t.Fatalf("%+v", rs)
	}
}

func TestImportPreviewIsBoundedByDepth(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "deep")
	rel := strings.Repeat("d/", importDepth+3) + "x.txt"
	put(t, root, rel, "x")
	put(t, root, "top.txt", "x")
	s := e.open(t, root)
	p := e.preview(t, s.ID, map[string]any{})
	if !p.Truncated || !strings.Contains(p.TruncatedWhy, fmt.Sprintf("%d folder levels", importDepth)) {
		t.Fatalf("%+v", p)
	}
	if n, _ := p.group("other", ".txt", "built-in"); n != 1 {
		t.Fatalf("only what is above the cap is listed: %+v", p.Groups)
	}
}

// A draft is previewed against the walk already made: nothing is saved by previewing, the first matching rule wins, and a
// conflict (two rules matching one file) is listed with its winner.
func TestImportRulePreviewIsLiveAndFirstMatchWins(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	broad := map[string]any{"name": "all of vda", "kind": "regex", "pattern": `^vda/`, "role": "ignore"}

	p := e.preview(t, s.ID, map[string]any{"rules": []any{psiRule, kmsRule, broad}})
	if p.RulesFrom != "draft" {
		t.Fatal(p.RulesFrom)
	}
	if n, ok := p.group("orientation", ".fits", "psi maps"); !ok || n != 3 {
		t.Fatalf("%+v", p.Groups)
	}
	if n, ok := p.group("dataset", ".fits", "kms"); !ok || n != 3 { // the two ROHSA maps and the file that has its own BUNIT
		t.Fatalf("%+v", p.Groups)
	}
	if len(p.Rules) != 3 || p.Rules[0].Wins != 3 || p.Rules[2].Wins != 0 || p.Rules[2].Shadowed != 3 || p.ConflictCount != 3 {
		t.Fatalf("%+v", p.Rules)
	}
	for _, c := range p.Conflicts {
		if c.Winner != "psi maps" || !c.Differ || len(c.Rules) != 2 {
			t.Fatalf("%+v", c)
		}
	}
	var declared bool
	for _, g := range p.Groups {
		for _, sm := range g.Samples {
			if sm.Rule == "psi maps" {
				declared = strings.Contains(strings.Join(sm.Declares, ";"), "declared by your rule psi maps")
			}
		}
	}
	if !declared {
		t.Fatal("a proposal from a rule must say it is declared by that rule")
	}
	// reordering decides the winner; a broken pattern is reported, matches nothing and does not fail the preview
	p = e.preview(t, s.ID, map[string]any{"rules": []any{broad, psiRule, map[string]any{"name": "broken", "kind": "regex", "pattern": "(", "role": "ignore"}}})
	if n, ok := p.group("ignore", ".fits", "all of vda"); !ok || n != 3 {
		t.Fatalf("%+v", p.Groups)
	}
	if p.Rules[2].Error == "" || p.Rules[2].Matches != 0 {
		t.Fatalf("%+v", p.Rules[2])
	}
	// nothing was saved by previewing
	var saved struct{ Rules []any }
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", s.ID), nil, 200, &saved)
	if len(saved.Rules) != 0 {
		t.Fatal("a draft was saved")
	}
	// saving refuses what is not valid, with every reason; unit text is checked by the catalog's own rule
	bad := []any{map[string]any{"name": "o", "kind": "glob", "pattern": "*.fits", "role": "orientation"}, map[string]any{"name": "u", "kind": "glob", "pattern": "*.fits", "role": "dataset", "unit": " K  "}}
	code, b := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": bad})
	if code != 422 || !strings.Contains(string(b), "angle unit is required") || !strings.Contains(string(b), "unit") {
		t.Fatalf("%d %s", code, b)
	}
	if code, _ := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{psiRule}, "extra": 1}); code != 400 {
		t.Fatalf("%d", code)
	}
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{kmsRule, psiRule}})
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", s.ID), nil, 200, &saved)
	if len(saved.Rules) != 2 || saved.Rules[0].(map[string]any)["name"] != "kms" {
		t.Fatalf("%+v", saved.Rules)
	}
	// the saved rules are what a preview with no draft uses
	if p = e.preview(t, s.ID, map[string]any{}); p.RulesFrom != "saved" || p.Selected.Files != 6 || p.Selected.Pending != 6 {
		t.Fatalf("%+v", p.Selected)
	}
}

type applyOut struct {
	Results []struct {
		Path, Rule, Role, Outcome, Detail string
		DatasetID                         *int64
		Declared                          []string
		Matches                           []struct {
			DatasetID int64
			Relation  string
		}
	}
	Summary struct{ Registered, Declared, Duplicates, Skipped, Failed, Remaining int }
}

func (o applyOut) by(path string) (r struct {
	Path, Rule, Role, Outcome, Detail string
	DatasetID                         *int64
	Declared                          []string
	Matches                           []struct {
		DatasetID int64
		Relation  string
	}
}) {
	for _, x := range o.Results {
		if x.Path == path {
			return x
		}
	}
	return r
}

// Applying registers what the rules select through the registration path, applies their declarations labelled as declared by the
// rule, never replaces the researcher's own declaration, lets a file's own BUNIT win, and does nothing twice.
func TestImportApplyRegistersAndDeclaresWithProvenance(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{psiRule, kmsRule}})
	apply := fmt.Sprintf("/api/projects/%d/import/apply", s.ID)

	if code, _ := e.call(t, "POST", apply, map[string]any{}); code != 400 {
		t.Fatalf("a missing confirmation: %d", code)
	}
	if code, _ := e.call(t, "POST", apply, map[string]any{"confirm": true, "paths": []string{"README.md"}}); code != 422 {
		t.Fatalf("a file no rule selects: %d", code)
	}
	// one file already carries a declaration made by the researcher
	mine, _ := e.srv.Store.FactsByPath(workspace.Local, 0)
	own := filepath.Join(root, "vda/n4/vgt psi maps/psi_1.fits")
	if fa, ok := mine[own]; ok {
		d, _ := e.srv.Store.Get(workspace.Local, fa.ID)
		frame, q := "sky", "gradient"
		if err := e.srv.Store.SetInterpretation(d, catalog.Interpretation{Kind: "axial_orientation", Unit: "deg", Frame: &frame, Quantity: &q}); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("the scan registers FITS files")
	}

	var out applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)
	if out.Summary.Failed != 0 || out.Summary.Remaining != 0 || len(out.Results) != 6 {
		t.Fatalf("%+v", out)
	}
	// psi maps: declared by the rule, with frame and quantity
	r := out.by("vda/n3/vgt psi maps/psi_1.fits")
	if r.Outcome != "declared" || r.DatasetID == nil || len(r.Declared) != 3 || !strings.Contains(r.Declared[0], "declared by your rule psi maps") {
		t.Fatalf("%+v", r)
	}
	d, _ := e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if d.Interpretation != nil && (d.Interpretation.Tier != catalog.TierDeclared || d.Interpretation.MappingVersion != "") {
		t.Fatalf("a rule's declaration is declared, authored by the rule: %+v", d.Interpretation)
	}
	if d.Interpretation == nil || d.Interpretation.DeclaredBy != "rule:psi maps" || d.Interpretation.Unit != "rad" || *d.Interpretation.Frame != "pixel" || *d.Interpretation.Quantity != "b_field" {
		t.Fatalf("%+v", d.Interpretation)
	}
	// the researcher's own declaration is kept
	r = out.by("vda/n4/vgt psi maps/psi_1.fits")
	d, _ = e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if r.Outcome != "skipped" || !strings.Contains(r.Detail, "kept your own orientation declaration") || d.Interpretation.DeclaredBy != "user" || d.Interpretation.Unit != "deg" {
		t.Fatalf("%+v %+v", r, d.Interpretation)
	}
	// a unit by rule, labelled; a file that states its own BUNIT keeps it
	r = out.by("validation_output/rohsa/n4/maps/mu_kms.fits")
	d, _ = e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if r.Outcome != "declared" || d.UnitDeclaration == nil || d.UnitDeclaration.Unit != "km/s" || d.UnitDeclaration.DeclaredBy != "rule:kms" {
		t.Fatalf("%+v %+v", r, d.UnitDeclaration)
	}
	r = out.by("extra/own_unit_kms.fits")
	d, _ = e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if r.Outcome != "skipped" || !strings.Contains(r.Detail, "BUNIT") || d.UnitDeclaration != nil {
		t.Fatalf("%+v %+v", r, d.UnitDeclaration)
	}
	// the declaration reads as the rule's everywhere it is labelled
	if got := declBy("rule:kms"); got != "declared by your rule kms" || declBy("user") != "declared by you" {
		t.Fatal(got)
	}
	var needs struct {
		Needs []struct{ Kind, Source, Message string }
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &needs)
	found := false
	for _, n := range needs.Needs {
		if n.Kind == "axial_unconfirmed" && strings.Contains(n.Source, "your import rule psi maps") && strings.Contains(n.Message, "your rule psi maps") {
			found = true
		}
		if strings.Contains(n.Source, "run import (rule:") {
			t.Fatalf("a rule declaration is labelled as a run import: %+v", n)
		}
	}
	if !found {
		t.Fatalf("a rule's orientation is awaiting confirmation: %+v", needs)
	}
	// applying again changes nothing and applies nothing: every file is settled at its size and time
	n := datasetCount(t, e)
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)
	if len(out.Results) != 0 || datasetCount(t, e) != n {
		t.Fatalf("%+v", out)
	}
	var p previewOut = e.preview(t, s.ID, map[string]any{})
	if p.Selected.Pending != 0 || p.Selected.Applied != 6 {
		t.Fatalf("%+v", p.Selected)
	}
	// an audit entry records it
	entries, _ := e.srv.Audit.List(workspace.Local, "project", s.ID, 20)
	seen := false
	for _, en := range entries {
		seen = seen || en.Action == "import rules applied"
	}
	if !seen {
		t.Fatalf("%+v", entries)
	}
}

// A file whose content a dataset already holds is offered, not registered (ADR 0053); "register separately" is a click.
func TestImportApplyOffersDuplicatesInsteadOfRegisteringThem(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "dup")
	tinyFITS(t, root, "a/orig.fits", 9, "")
	s := e.open(t, root)
	tinyFITS(t, root, "b/copy_kms.fits", 9, "") // the same bytes under another name and folder
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{kmsRule}})
	apply := fmt.Sprintf("/api/projects/%d/import/apply", s.ID)
	n := datasetCount(t, e)
	var out applyOut
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)
	r := out.by("b/copy_kms.fits")
	if out.Summary.Duplicates != 1 || r.Outcome != "duplicate" || len(r.Matches) != 1 || r.DatasetID != nil || !strings.Contains(r.Detail, "already holds this content") {
		t.Fatalf("%+v", out)
	}
	if datasetCount(t, e) != n {
		t.Fatal("a duplicate was registered silently")
	}
	// the offer is settled at this size and time: it is not offered again by a plain apply
	e.json(t, "POST", apply, map[string]any{"confirm": true}, 200, &out)
	if len(out.Results) != 0 {
		t.Fatalf("%+v", out)
	}
	// "register separately": the researcher's click
	e.json(t, "POST", apply, map[string]any{"confirm": true, "paths": []string{"b/copy_kms.fits"}, "asNew": []string{"b/copy_kms.fits"}}, 200, &out)
	if r = out.by("b/copy_kms.fits"); r.Outcome != "registered" || r.DatasetID == nil || datasetCount(t, e) != n+1 {
		t.Fatalf("%+v", out)
	}
	d, _ := e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if d.UnitDeclaration == nil || d.UnitDeclaration.DeclaredBy != "rule:kms" {
		t.Fatalf("%+v", d.UnitDeclaration)
	}
}

// A file inside a run belongs to the run: a rule does not register it behind the run import's back.
func TestImportRuleOnARunFileWaitsForTheRunImport(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "runs")
	put(t, root, "validation_output/a/run_metadata.toml", "generated_at_utc = \"2026-08-01T10:00:00Z\"\ngitcommit = \"abc1234\"\n[config]\nlambda = 0.5\n[outputs]\npsi_path = \"/x/psi.fits\"\n")
	tinyFITS(t, root, "validation_output/a/psi.fits", 5, "")
	tinyFITS(t, root, "validation_output/a/mom_kms.fits", 6, "")
	s := e.open(t, root)
	p := e.preview(t, s.ID, map[string]any{"rules": []any{kmsRule}})
	if _, ok := p.group("vgt_psi", ".fits", "built-in"); !ok {
		t.Fatalf("the VGT output keeps its role from the run record: %+v", p.Groups)
	}
	for _, g := range p.Groups {
		for _, sm := range g.Samples {
			if sm.Rule == "kms" && !strings.Contains(strings.Join(sm.Warnings, ";"), "inside the run") {
				t.Fatalf("%+v", sm)
			}
		}
	}
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{kmsRule}})
	var out applyOut
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/apply", s.ID), map[string]any{"confirm": true}, 200, &out)
	r := out.by("validation_output/a/mom_kms.fits")
	if r.Outcome != "skipped" || !strings.Contains(r.Detail, "import the run first") || r.DatasetID != nil {
		t.Fatalf("%+v", r)
	}
}

// The rules reach project.toml only on a confirmed request; the file's other text stays byte for byte; rules the researcher wrote
// there themselves are never rewritten.
func TestImportRulesAreWrittenToProjectTomlOnlyAfterConfirmation(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	file := filepath.Join(root, ".vesqira", "project.toml")
	s := e.open(t, root)
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{psiRule, kmsRule}})
	theirs, _ := os.ReadFile(file)
	write := fmt.Sprintf("/api/projects/%d/import/rules/write", s.ID)
	// saving, previewing and applying never touch the file
	e.preview(t, s.ID, map[string]any{})
	if b, _ := os.ReadFile(file); !bytes.Equal(b, theirs) {
		t.Fatalf("the file changed before confirmation: %q", b)
	}
	for _, body := range []map[string]any{{}, {"write": false}} {
		if code, _ := e.call(t, "POST", write, body); code != 400 {
			t.Fatalf("%v: %d", body, code)
		}
	}
	if b, _ := os.ReadFile(file); !bytes.Equal(b, theirs) {
		t.Fatalf("a refused write changed the file: %q", b)
	}
	var st struct{ File struct{ State string } }
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", s.ID), nil, 200, &st)
	if st.File.State != "none" {
		t.Fatalf("%+v", st)
	}
	e.json(t, "POST", write, map[string]any{"write": true}, 200, nil)
	got, _ := os.ReadFile(file)
	if !bytes.HasPrefix(got, theirs) || !strings.Contains(string(got), "[[import.rules]]") {
		t.Fatalf("%s", got)
	}
	pf, err := discover.ParseProjectFile(got)
	if err != nil || len(pf.Import.Rules) != 2 || pf.Import.Rules[0].Name != "psi maps" || pf.Import.Rules[0].AngleFrame != "pixel" || pf.Import.Rules[1].Unit != "km/s" {
		t.Fatalf("%v %+v", err, pf.Import.Rules)
	}
	e.settle(t, s.ID)
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", s.ID), nil, 200, &st)
	if st.File.State != "same" {
		t.Fatalf("%+v", st)
	}
	// reordering and writing again replaces only the section
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{kmsRule, psiRule}})
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", s.ID), nil, 200, &st)
	if st.File.State != "differs" {
		t.Fatalf("%+v", st)
	}
	e.json(t, "POST", write, map[string]any{"write": true}, 200, nil)
	got2, _ := os.ReadFile(file)
	pf, _ = discover.ParseProjectFile(got2)
	if !bytes.HasPrefix(got2, theirs) || pf.Import.Rules[0].Name != "kms" || strings.Count(string(got2), "[[import.rules]]") != 2 {
		t.Fatalf("%s", got2)
	}
	// rules the researcher wrote outside the section are theirs: never rewritten
	foreign := string(theirs) + "\n[[import.rules]]\nname = \"mine\"\nkind = \"glob\"\npattern = \"*.fits\"\nrole = \"ignore\"\n"
	os.WriteFile(file, []byte(foreign), 0o644)
	if code, b := e.call(t, "POST", write, map[string]any{"write": true}); code != 409 || !strings.Contains(string(b), "did not write") {
		t.Fatalf("%d %s", code, b)
	}
	if b, _ := os.ReadFile(file); string(b) != foreign {
		t.Fatalf("%q", b)
	}
	// with no file at all, the confirmed write creates it
	os.RemoveAll(filepath.Join(root, ".vesqira"))
	e.json(t, "POST", write, map[string]any{"write": true}, 200, nil)
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "vesqira:import-rules begin") {
		t.Fatalf("%s", b)
	}
}

// A scan counts what the saved rules select and says how many are new; it never registers a file for a rule or applies a
// declaration; a project opened from another computer takes its rules from project.toml.
func TestImportRescanCountsNewMatchesAndNeverRegistersThem(t *testing.T) {
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	s := e.open(t, root)
	// a rule over a file nothing else registers: a plain text table outside a data folder
	rule := map[string]any{"name": "stars", "kind": "glob", "pattern": "stars/*.dat", "role": "catalog", "lon": "a", "lat": "b", "coordFormat": "degrees", "coordFrame": "icrs"}
	e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{rule, kmsRule}})
	n := datasetCount(t, e)
	scan := func() struct {
		ImportRules *struct{ Rules, Matched, Pending, New int } `json:"importRules"`
	} {
		e.call(t, "POST", fmt.Sprintf("/api/projects/%d/scan", s.ID), nil)
		e.settle(t, s.ID)
		var out struct {
			ImportRules *struct{ Rules, Matched, Pending, New int } `json:"importRules"`
		}
		e.json(t, "GET", fmt.Sprintf("/api/projects/%d", s.ID), nil, 200, &out)
		return out
	}
	first := scan()
	if first.ImportRules == nil || first.ImportRules.Rules != 2 || first.ImportRules.Matched != 4 || first.ImportRules.Pending != 4 || first.ImportRules.New != 4 {
		t.Fatalf("%+v", first.ImportRules)
	}
	if datasetCount(t, e) != n {
		t.Fatal("a scan registered a file only a rule selects")
	}
	if a, _ := e.srv.Projects.AppliedFiles(s.ID); len(a) != 4 || a["stars/s1.dat"].Outcome != "pending" {
		t.Fatalf("%+v", a)
	}
	// unchanged: still pending, none new
	if again := scan(); again.ImportRules.Pending != 4 || again.ImportRules.New != 0 {
		t.Fatalf("%+v", again.ImportRules)
	}
	// new files that match are counted as new, and still not registered
	put(t, root, "stars/s2.dat", "a b\n5 6\n7 8\n")
	tinyFITS(t, root, "validation_output/rohsa/n5/maps/mu_kms.fits", 11, "")
	third := scan()
	if third.ImportRules.Pending != 6 || third.ImportRules.New != 2 {
		t.Fatalf("%+v", third.ImportRules)
	}
	if datasetCount(t, e) != n+1 { // the scan registers the new FITS file by its own (built-in) rule; the stars table stays unregistered
		t.Fatalf("%d datasets, want %d", datasetCount(t, e), n+1)
	}
	facts, _ := e.srv.Store.FactsByPath(workspace.Local, 0)
	if _, ok := facts[filepath.Join(root, "stars/s2.dat")]; ok {
		t.Fatal("the text table was registered by a scan")
	}
	// no unit was declared by any scan for the FITS files the rule selects
	for _, fa := range facts {
		if d, _ := e.srv.Store.Get(workspace.Local, fa.ID); d.UnitDeclaration != nil || d.Interpretation != nil {
			t.Fatalf("a scan applied a declaration: %+v", d)
		}
	}
	// a project opened on a computer that has never saved a list takes its rules from project.toml
	other := e.project(t, "from-file")
	put(t, other, ".vesqira/project.toml", "[[import.rules]]\nname = \"from file\"\nkind = \"glob\"\npattern = \"*_kms.fits\"\nrole = \"dataset\"\nunit = \"km/s\"\n")
	tinyFITS(t, other, "m_kms.fits", 3, "")
	o := e.open(t, other)
	if rs, _ := e.srv.Projects.Rules(o.ID); len(rs) != 1 || rs[0].Name != "from file" {
		t.Fatalf("%+v", rs)
	}
	var st struct{ File struct{ State string } }
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/import/rules", o.ID), nil, 200, &st)
	if st.File.State != "foreign" { // the researcher wrote them there themselves: Vesqira reads them and never rewrites them
		t.Fatalf("%+v", st)
	}
}

// Applying a catalog rule needs the worker that reads tables: the columns are declared as the rule says, labelled, after being
// checked against the file's own columns.
func TestImportApplyDeclaresACatalogByRule(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	e := newProjEnv(t)
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	e.srv.Worker, e.srv.Planes = pw, pw
	root := e.project(t, "stars")
	put(t, root, "stars/starlight.dat", starlight)
	put(t, root, "stars/other.dat", "x y\n1 2\n3 4\n")
	s := e.open(t, root)
	rule := map[string]any{"name": "starlight", "kind": "glob", "pattern": "stars/starlight.dat", "role": "catalog", "lon": "RA(J2000)", "lat": "DEC(J2000)",
		"coordFormat": "sexagesimal_hours_degrees", "coordFrame": "fk5", "equinox": "J2000", "angleColumn": "PA(deg)", "angleUnit": "deg", "quantity": "starlight_polarization", "sigmaColumn": "sPA(deg)", "values": []string{"Star"}}
	wrong := map[string]any{"name": "wrong", "kind": "glob", "pattern": "stars/other.dat", "role": "catalog", "lon": "RA", "lat": "DEC", "coordFormat": "degrees", "coordFrame": "icrs"}
	if code, b := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", s.ID), map[string]any{"rules": []any{rule, wrong}}); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var out applyOut
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/apply", s.ID), map[string]any{"confirm": true}, 200, &out)
	r := out.by("stars/starlight.dat")
	if r.Outcome != "registered" || r.DatasetID == nil || len(r.Declared) != 1 {
		t.Fatalf("%+v", out)
	}
	d, _ := e.srv.Store.Get(workspace.Local, *r.DatasetID)
	if d.Kind != "catalog" || d.CatalogDeclaration == nil || d.CatalogDeclaration.DeclaredBy != "rule:starlight" || d.CatalogDeclaration.Frame != "fk5" ||
		d.CatalogDeclaration.Angle == nil || d.CatalogDeclaration.Angle.Column != "PA(deg)" || d.CatalogDeclaration.Angle.Frame != "sky" {
		t.Fatalf("%+v", d.CatalogDeclaration)
	}
	// columns that the file does not have are not declared, and the reason is the file's own columns
	r = out.by("stars/other.dat")
	if r.Outcome != "registered" || !strings.Contains(r.Detail, "not declared, because the file's columns do not fit the rule") {
		t.Fatalf("%+v", r)
	}
	if d, _ = e.srv.Store.Get(workspace.Local, *r.DatasetID); d.CatalogDeclaration != nil {
		t.Fatalf("%+v", d.CatalogDeclaration)
	}
}

// On a host the folder is walked by the host's worker under the same roots, the preview and rules work the same, a FITS file is
// registered there with the rule's declarations, a point catalog is not registered (ADR 0044), and the project file is written
// only when Vesqira wrote it.
func TestImportOnAHostFolderWithTheFakeHostHarness(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	seed := filepath.Join(base, "seed_root")
	proj := filepath.Join(base, "remote_proj")
	for _, d := range []string{remoteHome, seed, proj} {
		os.MkdirAll(d, 0o755)
	}
	t.Setenv("HOME", remoteHome)
	tinyFITS(t, proj, "vda/n3/vgt psi maps/psi_1.fits", 4, "")
	tinyFITS(t, proj, "maps/mu_kms.fits", 5, "")
	put(t, proj, "stars/s1.dat", "a b\n1 2\n3 4\n")
	put(t, proj, "blind_set_key/key_DO_NOT_OPEN.csv", "secret")
	put(t, proj, ".vesqira/project.toml", "[project]\nname = \"theirs\"\n")

	files := workerFiles(t, repo)
	mgr := hosts.NewManager(fakeSSH(t, remoteHome), files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	ps, _ := projects.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rs, _ := sourceroot.New([]string{seed})
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), Projects: ps, Runs: rn, Audit: au, HostDB: hs, Remote: mgr}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	e := &projEnv{ts: ts, srv: srv, cat: cat, base: base, home: remoteHome}
	py, _ := exec.LookPath("python3")
	h, err := hs.Create(hosts.Host{Name: "Second laptop", Dest: "laptop2", Python: py, Roots: []string{seed}})
	if err != nil {
		t.Fatal(err)
	}
	var o sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"hostId": h.ID, "path": proj}, 200, &o)
	e.settle(t, o.ID)

	catRule := map[string]any{"name": "stars", "kind": "glob", "pattern": "stars/*.dat", "role": "catalog", "lon": "a", "lat": "b", "coordFormat": "degrees", "coordFrame": "icrs"}
	p := e.preview(t, o.ID, map[string]any{"rules": []any{psiRule, kmsRule, catRule}})
	if n, ok := p.group("orientation", ".fits", "psi maps"); !ok || n != 1 {
		t.Fatalf("%+v", p.Groups)
	}
	if len(p.Skipped.NeverRead) != 1 {
		t.Fatalf("%+v", p.Skipped)
	}
	for _, g := range p.Groups {
		for _, sm := range g.Samples {
			if strings.Contains(strings.ToLower(sm.Path), "do_not_open") {
				t.Fatalf("the host listed %s", sm.Path)
			}
		}
	}
	if sub := e.preview(t, o.ID, map[string]any{"folder": "vda"}); sub.Files != 1 {
		t.Fatalf("%+v", sub)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/import/preview", o.ID), map[string]any{"folder": "nope"}); code != 422 {
		t.Fatalf("%d", code)
	}
	if code, b := e.putJSON(t, fmt.Sprintf("/api/projects/%d/import/rules", o.ID), map[string]any{"rules": []any{psiRule, kmsRule, catRule}}); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var out applyOut
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/apply", o.ID), map[string]any{"confirm": true}, 200, &out)
	r := out.by("vda/n3/vgt psi maps/psi_1.fits")
	if r.DatasetID == nil || (r.Outcome != "declared" && r.Outcome != "registered") || len(r.Declared) != 3 {
		t.Fatalf("%+v", out)
	}
	d, _ := cat.Get(workspace.Local, *r.DatasetID)
	if d.HostID != h.ID || d.Interpretation == nil || d.Interpretation.DeclaredBy != "rule:psi maps" {
		t.Fatalf("%+v", d)
	}
	if r = out.by("stars/s1.dat"); r.Outcome != "skipped" || !strings.Contains(r.Detail, "this computer only") || r.DatasetID != nil {
		t.Fatalf("a catalog on a host is not registered: %+v", r)
	}
	// the researcher's own project.toml on the host is not replaced, and says so
	hostFile := filepath.Join(proj, ".vesqira", "project.toml")
	before, _ := os.ReadFile(hostFile)
	if code, b := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/import/rules/write", o.ID), map[string]any{"write": true}); code != 409 || !strings.Contains(string(b), "not written by Vesqira") {
		t.Fatalf("%d %s", code, b)
	}
	if after, _ := os.ReadFile(hostFile); !bytes.Equal(before, after) {
		t.Fatalf("%q", after)
	}
	// a file Vesqira wrote is replaced in its own section
	os.Remove(hostFile)
	e.call(t, "POST", fmt.Sprintf("/api/projects/%d/scan", o.ID), nil)
	e.settle(t, o.ID)
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": true}, 200, nil)
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/import/rules/write", o.ID), map[string]any{"write": true}, 200, nil)
	if b, _ := os.ReadFile(hostFile); !strings.HasPrefix(string(b), projectMarker) || strings.Count(string(b), "[[import.rules]]") != 3 {
		t.Fatalf("%s", b)
	}
}
