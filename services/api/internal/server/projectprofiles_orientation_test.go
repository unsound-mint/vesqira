package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// The orientation a project profile declares (ADR 0068, slice 6): applied to a map with no cards, refused against cards that say
// something else.

const degToolProfile = `{"format": 1, "profile": {
  "name": "DegTool", "version": "1", "kind": "degtool",
  "orientation": %s,
  "record": {"file": "deg_tool.toml", "reader": "run_metadata", "anyKey": ["degtool.version"]},
  "outputs": {"table": "outputs", "files": {"angle_file": {"product": "angle", "role": "orientation"}}}}}`

// degTool sets up a project with a DegTool run folder holding one angle file copied from the fixtures, and writes the profile with
// the given orientation object.
func (e *profEnv) degTool(t *testing.T, angleFixture, orientation string) (proj, run string) {
	t.Helper()
	proj = e.project(t, "degtool")
	run = filepath.Join(proj, "angles")
	put(t, proj, "angles/deg_tool.toml", "[degtool]\nversion = \"1\"\n[outputs]\nangle_file = \"/x/psi.fits\"\n")
	e.cp(t, angleFixture, filepath.Join(run, "psi.fits"))
	e.degProfile(t, proj, orientation)
	return proj, run
}

func (e *profEnv) degProfile(t *testing.T, proj, orientation string) {
	t.Helper()
	put(t, proj, ".vesqira/profiles/degtool.json", strings.Replace(degToolProfile, "%s", orientation, 1))
}

type importedRun struct {
	Outputs []struct {
		Role      string
		DatasetID int64
	}
}

func (e *profEnv) importAngles(t *testing.T, run string) catalog.Dataset {
	t.Helper()
	var rn importedRun
	e.json(t, "POST", "/api/runs/import", map[string]any{"path": run}, 201, &rn)
	if len(rn.Outputs) != 1 {
		t.Fatalf("run outputs %+v", rn.Outputs)
	}
	var d catalog.Dataset
	e.json(t, "GET", "/api/datasets/"+itoa(int(rn.Outputs[0].DatasetID)), nil, 200, &d)
	return d
}

// vgt_psi_deg.fits has no card but its grid: axial angles in [0, 180) degrees, and the file says neither the unit nor that they are
// axial. The unit is checked against an independent copy of the same angles stored in radians (vgt_psi_rad.fits) and declared by
// the researcher: the axial difference of the two is zero everywhere only if each unit was applied as declared.
func TestProjectProfileOrientationIsAppliedToAFileWithNoCards(t *testing.T) {
	e := newProfEnv(t)
	proj, run := e.degTool(t, "vgt_psi_deg.fits", `{"frame": "pixel", "unit": "deg", "statistics": "axial", "quantity": "gradient"}`)
	e.cp(t, "vgt_psi_rad.fits", filepath.Join(proj, "reference_rad.fits"))
	e.open(t, proj)
	d := e.importAngles(t, run)
	in := d.Interpretation
	if d.OrientationHeader != nil {
		t.Errorf("the file has no cards, yet its header reads as an orientation map: %+v", d.OrientationHeader)
	}
	if in == nil || in.Kind != "axial_orientation" || in.Unit != "deg" || in.Frame == nil || *in.Frame != "pixel" || in.Quantity == nil || *in.Quantity != "gradient" ||
		in.DeclaredBy != "project-profile:DegTool@1" || in.Tier != catalog.TierDeclared || in.Stale {
		t.Fatalf("interpretation %+v, want an axial map in degrees on the pixel grid declared by project-profile:DegTool@1", in)
	}

	var all struct{ Datasets []catalog.Dataset }
	e.json(t, "GET", "/api/datasets", nil, 200, &all)
	var ref *catalog.Dataset
	for i := range all.Datasets {
		if filepath.Base(all.Datasets[i].Path) == "reference_rad.fits" {
			ref = &all.Datasets[i]
		}
	}
	if ref == nil {
		t.Fatal("the reference map was not registered")
	}
	e.json(t, "POST", "/api/datasets/"+itoa(int(ref.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "pixel"}, 200, nil)
	var res struct {
		Summary struct {
			Compared int      `json:"pixels_compared"`
			RMS      *float64 `json:"rms_deg"`
		}
	}
	e.json(t, "POST", "/api/analysis", map[string]any{"op": "axial_diff", "datasetId": d.ID, "referenceId": ref.ID}, 201, &res)
	if res.Summary.Compared == 0 || res.Summary.RMS == nil || *res.Summary.RMS > 1e-9 {
		t.Errorf("axial difference of the imported degrees map and the same angles in radians: %+v, want rms 0", res.Summary)
	}

	// the same map under a wrong unit would differ: the declaration, not a default, decides what the numbers mean
	e.degProfile(t, proj, `{"frame": "pixel", "unit": "rad", "statistics": "axial"}`)
	e.json(t, "POST", "/api/runs/import", map[string]any{"path": run}, 201, nil)
	var wrong catalog.Dataset
	e.json(t, "GET", "/api/datasets/"+itoa(int(d.ID)), nil, 200, &wrong)
	if wrong.Interpretation == nil || wrong.Interpretation.Unit != "rad" {
		t.Errorf("a changed declaration was not applied on the next import: %+v", wrong.Interpretation)
	}
}

// psi_demo.fits says PSIFRAME=GALACTIC and BUNIT=rad; the profile below says the pixel grid and degrees. The import is refused with one
// sentence naming both and no run is saved. Corrected to what the cards say, the file's own declaration is the one in force.
func TestProjectProfileOrientationThatContradictsTheCardsRefusesTheImport(t *testing.T) {
	e := newProfEnv(t)
	proj, run := e.degTool(t, "vgt_run_current/psi_demo.fits", `{"frame": "pixel", "unit": "deg", "statistics": "axial"}`)
	e.open(t, proj)
	count := func() int {
		var all struct{ Datasets []catalog.Dataset }
		e.json(t, "GET", "/api/datasets", nil, 200, &all)
		return len(all.Datasets)
	}
	before := count()
	code, out := e.call(t, "POST", "/api/runs/import", map[string]any{"path": run})
	if code != 422 {
		t.Fatalf("contradicting import: %d %s, want 422", code, out)
	}
	for _, want := range []string{"PSIFRAME=GALACTIC", "DegTool@1", ".vesqira/profiles/degtool.json", "pixel grid", "differ in frame and unit", "nothing was imported"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the refusal %s does not name %q", out, want)
		}
	}
	if after := count(); after != before {
		t.Errorf("a refused import left %d dataset(s) in the catalog (%d before, %d after): nothing was imported", after-before, before, after)
	}
	var list struct{ Runs []json.RawMessage }
	if code, b := e.call(t, "GET", "/api/runs", nil); code == 200 {
		json.Unmarshal(b, &list)
		if len(list.Runs) != 0 {
			t.Errorf("a refused import saved a run: %s", b)
		}
	}

	e.degProfile(t, proj, `{"frame": "sky", "unit": "rad", "statistics": "axial"}`)
	d := e.importAngles(t, run)
	if d.Interpretation == nil || d.Interpretation.DeclaredBy != "profile:VGT.jl@1" || d.Interpretation.Unit != "rad" {
		t.Errorf("when the cards and the declaration agree the file's own declaration is the one in force: %+v", d.Interpretation)
	}
}
