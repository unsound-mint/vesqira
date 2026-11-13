package server

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// A project profile whose components are a table (files.component_table) and one that reads orientation from the header conventions
// of the worker's table (ADR 0068, slice 6).

// cp copies fixture files into a project folder under new names.
func (e *profEnv) cp(t *testing.T, from, to string) {
	t.Helper()
	if out, err := exec.Command("cp", filepath.Join(e.fixtures, from), to).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The profile is the shipped ROHSA table declaration under another tool's name, record and file pattern, so the numbers can be checked
// against the closed form of fixtures/generate.py rohsa_truth (amp = 1 + 0.5 k + 0.1 x + 0.01 y; mean = 8 + 11 k + 0.25 x + 0.5 y + 1
// in 1-based channels; sigma = 1.2 + 0.7 k + 0.05 x channels; VRAD axis CRVAL -12000 m/s, CDELT 800 m/s, CRPIX 3).
const tabProfile = `{"format": 1, "about": "written by a test", "profile": {
  "name": "TabFit", "version": "1", "kind": "tabfit", "label": "TabFit",
  "record": {"file": "tabfit_run.toml", "reader": "run_metadata", "anyKey": ["tabfit.version"]},
  "files": {"component_table": {"pattern": "^fit_[A-Za-z0-9._-]+\\.dat$"}},
  "decomposition": {
    "source": {"kind": "table", "columns": {"row": 0, "col": 1, "amplitude": 2, "centre": 3, "width": 4}, "ncols": 5, "pixelBase": 0},
    "amplitude": {"unit": "grid"},
    "centre": {"unit": "channel", "base": 1, "frame": "grid"},
    "width": {"kind": "sigma", "unit": "channel"},
    "order": "the source's own numbering, 1 to n",
    "components": "config.n_gauss", "crop": "input.path"}}}`

func TestProjectProfileReadsComponentsFromATableItNames(t *testing.T) {
	e := newProfEnv(t)
	proj := e.project(t, "tabtool")
	run := filepath.Join(proj, "tabrun")
	put(t, proj, "tabrun/tabfit_run.toml", "[tabfit]\nversion = \"1\"\n[config]\nn_gauss = 3\n[input]\npath = \"/elsewhere/region_a.fits\"\n")
	e.cp(t, "rohsa_run_demo/region_a/region_a.fits", filepath.Join(run, "region_a.fits"))
	e.cp(t, "rohsa_run_demo/region_a/rohsa_n3.dat", filepath.Join(run, "fit_main.dat"))
	put(t, proj, ".vesqira/profiles/tabfit.json", tabProfile)
	// a second file the pattern also names makes the table ambiguous: refused with both names, nothing guessed
	e.cp(t, "rohsa_run_demo/region_a/rohsa_n3.dat", filepath.Join(run, "fit_other.dat"))
	e.open(t, proj)
	if r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": run}); r.StatusCode < 400 || !strings.Contains(string(out), "fit_main.dat") || !strings.Contains(string(out), "fit_other.dat") {
		t.Fatalf("two candidate tables: %d %s", r.StatusCode, out)
	}
	if out, err := exec.Command("rm", filepath.Join(run, "fit_other.dat")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": run})
	if r.StatusCode != 201 {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	var rr rohsaRunJSON
	json.Unmarshal(out, &rr)
	if rr.Kind != "tabfit" || rr.Rohsa == nil || rr.Rohsa.CropDatasetID == nil || rr.Rohsa.NGauss != 3 {
		t.Fatalf("run %s", out)
	}
	base := e.ts.URL + "/api/datasets/" + itoa(int(*rr.Rohsa.CropDatasetID)) + "/rohsa"
	if r, ib := postJSON(t, base+"/index", map[string]any{"runId": rr.ID}); r.StatusCode != 200 || !strings.Contains(string(ib), `"ready"`) {
		t.Fatalf("index %d %s", r.StatusCode, ib)
	}
	x, y := 2, 1
	resp, frame := get(t, base+"/components?run="+itoa(int(rr.ID))+"&x=2&y=1")
	if resp.StatusCode != 200 {
		t.Fatalf("components %d %s", resp.StatusCode, frame)
	}
	n := int(binary.LittleEndian.Uint32(frame))
	var head struct {
		Components []struct {
			Amp, Centre, Sigma float64
			Valid              bool
		}
	}
	if err := json.Unmarshal(frame[4:4+n], &head); err != nil {
		t.Fatal(err)
	}
	if len(head.Components) != 3 {
		t.Fatalf("components %+v", head)
	}
	for k, c := range head.Components {
		amp := 1 + 0.5*float64(k) + 0.1*float64(x) + 0.01*float64(y)
		meanFile := 8 + 11*float64(k) + 0.25*float64(x) + 0.5*float64(y) + 1 // 1-based channels
		centre := (-12000.0 + (meanFile-1+1-3)*800.0) / 1000.0               // km/s: CRVAL + (pixel - CRPIX) CDELT, channel c is pixel c + 1
		sigma := (1.2 + 0.7*float64(k) + 0.05*float64(x)) * 800.0 / 1000.0
		if math.Abs(c.Amp-amp) > 1e-12 || math.Abs(c.Centre-centre) > 1e-9 || math.Abs(c.Sigma-sigma) > 1e-9 || !c.Valid {
			t.Errorf("component %d = %+v, want amp %v centre %v sigma %v", k, c, amp, centre, sigma)
		}
	}
}

// A non-VGT tool whose project profile maps an angle map and a reliability map to generic roles, writing VGT.jl's header cards and
// declaring the same convention: the worker's convention reads the cards, so the imported orientation is declared by the file through
// profile:VGT.jl@1, and nothing is declared for the scalar map.
func TestProjectProfileOrientationCarriesTheWorkerConvention(t *testing.T) {
	e := newProfEnv(t)
	proj := e.project(t, "psitool")
	run := filepath.Join(proj, "angles")
	put(t, proj, "angles/psi_tool.toml", "[psitool]\nversion = \"1\"\n[outputs]\nangle_file = \"/x/psi_demo.fits\"\nconf_file = \"/x/psi_reliability_demo.fits\"\n")
	e.cp(t, "vgt_run_current/psi_demo.fits", filepath.Join(run, "psi_demo.fits"))
	e.cp(t, "vgt_run_current/psi_reliability_demo.fits", filepath.Join(run, "psi_reliability_demo.fits"))
	put(t, proj, ".vesqira/profiles/psitool.json", `{"format": 1, "profile": {
  "name": "PsiTool", "version": "1", "kind": "psitool",
  "orientation": {"frame": "sky", "unit": "rad", "statistics": "axial", "quantity": "b_field"},
  "record": {"file": "psi_tool.toml", "reader": "run_metadata", "anyKey": ["psitool.version"]},
  "outputs": {"table": "outputs", "files": {
    "angle_file": {"product": "angle", "role": "orientation"},
    "conf_file": {"product": "confidence", "role": "reliability"}}}}}`)
	e.open(t, proj)
	r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": run})
	if r.StatusCode != 201 {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	var rn struct {
		Profile string
		Outputs []struct {
			Role, Product string
			DatasetID     int64
		}
	}
	json.Unmarshal(out, &rn)
	if rn.Profile != "PsiTool@1" || len(rn.Outputs) != 2 {
		t.Fatalf("run %s", out)
	}
	for _, o := range rn.Outputs {
		_, db := get(t, e.ts.URL+"/api/datasets/"+itoa(int(o.DatasetID)))
		var d catalog.Dataset
		json.Unmarshal(db, &d)
		if o.Role == "reliability" {
			if d.Interpretation != nil || d.OrientationHeader != nil && d.OrientationHeader.Orientation != nil {
				t.Errorf("a scalar map is not read as orientation: %+v", d.Interpretation)
			}
			continue
		}
		oh := d.OrientationHeader
		if oh == nil || oh.Orientation == nil || oh.Orientation.Profile != "profile:VGT.jl@1" || oh.Orientation.Frame != "sky" || oh.Orientation.SkyFrame == nil || *oh.Orientation.SkyFrame != "galactic" || oh.Orientation.Unit != "rad" {
			t.Fatalf("the cards PSIFRAME=GALACTIC and BUNIT=rad were not read through the worker's convention: %+v", oh)
		}
		if d.Interpretation == nil || d.Interpretation.Kind != "axial_orientation" || !strings.HasPrefix(d.Interpretation.DeclaredBy, "profile:VGT.jl@1") {
			t.Errorf("interpretation %+v, want an axial orientation declared by the file through profile:VGT.jl@1", d.Interpretation)
		}
	}
}
