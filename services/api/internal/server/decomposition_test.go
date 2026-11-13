package server

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
)

// A tool that is neither VGT nor ROHSA and writes its components as FITS maps goes through the whole decomposition path from its
// profile alone (ADR 0068, slice 4): import, the index, the components of a pixel, the sigma-v diagram and the width class maps.
// The expected numbers are the closed form of fixtures/generate.py decomp_truth, computed here with plain loops, not by the reader.

const (
	dxPix, dyPix, dnComp = 7, 5, 3
	fwhmPerSigma         = 2.3548200450309493 // 2 sqrt(2 ln 2)
)

// truth is one slot of the synthetic fit: amplitude (K), centre (km/s, LSRK) and FWHM (km/s); ok is false where the tool did not fit it.
func truth(k, x, y int) (amp, centre, fwhm float64, ok bool) {
	if k == 2 && (x+y)%4 == 0 {
		return 0, 0, 0, false
	}
	return 2 + 0.5*float64(k) + 0.1*float64(x) + 0.05*float64(y), -12 + 9*float64(k) + 0.4*float64(x) + 0.6*float64(y), 2.5 + 2*float64(k) + 0.2*float64(x), true
}

func TestDecompositionOfAToolThatHasOnlyAProfile(t *testing.T) {
	t.Parallel()
	e, _, _ := reviewEnv(t)
	folder := filepath.Join(e.root, "synth_decomp_demo")

	// shipped profiles only: nothing claims the record
	if r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": folder}); r.StatusCode < 400 {
		t.Fatalf("an unprofiled tool was imported: %d %s", r.StatusCode, out)
	}
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)

	for _, layout := range []struct{ name, dir string }{{"one cube per parameter", folder}, {"one map per component", filepath.Join(folder, "per_component")}} {
		run, raw := e.importRun(t, layout.dir, 201)
		if run.Kind != "synthdecomp" || run.Rohsa == nil || run.Rohsa.NGauss != dnComp || run.Rohsa.CropDatasetID == nil || run.Rohsa.Decomposition == nil {
			t.Fatalf("%s: run %s", layout.name, raw)
		}
		if strings.Contains(raw, "/scratch/someone") {
			t.Errorf("%s: the author's paths must not come through: %s", layout.name, raw)
		}
		base := e.ts.URL + "/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa"
		_, lb := get(t, base)
		if !strings.Contains(string(lb), `"usable":true`) {
			t.Fatalf("%s: the run is usable over its cube: %s", layout.name, lb)
		}
		if r, ib := postJSON(t, base+"/index", map[string]any{"runId": run.ID}); r.StatusCode != 200 || !strings.Contains(string(ib), `"ready"`) {
			t.Fatalf("%s: index %d %s", layout.name, r.StatusCode, ib)
		}

		// the components of a pixel against the closed form
		x, y := 3, 2
		resp, frame := get(t, base+"/components?run="+itoa(int(run.ID))+"&x=3&y=2")
		if resp.StatusCode != 200 {
			t.Fatalf("%s: components %d %s", layout.name, resp.StatusCode, frame)
		}
		n := int(binary.LittleEndian.Uint32(frame))
		var head struct {
			Components []struct {
				Amp, Centre, Sigma float64
				Present, Valid     bool
			}
			Drawn  bool
			Record struct {
				Centre struct{ Frame, Unit, Status string }
				Width  struct{ Kind, Unit, Status string }
			}
		}
		if err := json.Unmarshal(frame[4:4+n], &head); err != nil {
			t.Fatal(err)
		}
		if len(head.Components) != dnComp || !head.Drawn || head.Record.Centre.Unit != "m/s" || head.Record.Centre.Status != "resolved" || head.Record.Width.Kind != "fwhm" {
			t.Fatalf("%s: head %+v", layout.name, head)
		}
		for k, c := range head.Components {
			amp, centre, fwhm, _ := truth(k, x, y)
			if math.Abs(c.Amp-amp) > 1e-12 || math.Abs(c.Centre-centre) > 1e-9 || math.Abs(c.Sigma-fwhm/fwhmPerSigma) > 1e-9 || !c.Valid {
				t.Errorf("%s: component %d = %+v, want amp %v centre %v sigma %v", layout.name, k, c, amp, centre, fwhm/fwhmPerSigma)
			}
		}

		// the sigma-v diagram: the count of components and the ones the tool did not fit, by plain loops
		var fitted, absent int
		cells := map[[2]int]int{}
		for k := 0; k < dnComp; k++ {
			for yy := 0; yy < dyPix; yy++ {
				for xx := 0; xx < dxPix; xx++ {
					if _, c, w, ok := truth(k, xx, yy); !ok {
						absent++
					} else {
						fitted++
						cells[[2]int{int(math.Floor(w / fwhmPerSigma / 0.5)), int(math.Floor((c + 20) / 5))}]++
					}
				}
			}
		}
		req := map[string]any{"runId": run.ID, "weight": "count", "velocityBins": map[string]any{"min": -20.0, "max": 30.0, "n": 10}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
		resp, raw2 := postJSON(t, base+"/sigmav", req)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: sigmav %d %s", layout.name, resp.StatusCode, raw2)
		}
		var d struct {
			Components int
			NotFitted  int `json:"not_fitted"`
			Cells      [][]float64
		}
		json.Unmarshal(raw2, &d)
		if d.Components != fitted || d.NotFitted != absent {
			t.Errorf("%s: %d components %d not fitted, want %d and %d", layout.name, d.Components, d.NotFitted, fitted, absent)
		}
		got := map[[2]int]int{}
		for _, c := range d.Cells {
			got[[2]int{int(c[0]), int(c[1])}] = int(c[2])
		}
		if len(got) != len(cells) {
			t.Errorf("%s: cells %v, want %v", layout.name, got, cells)
		}
		for key, want := range cells {
			if got[key] != want {
				t.Errorf("%s: cell %v holds %d, want %d", layout.name, key, got[key], want)
			}
		}

		// the width classes: components with sigma below 2.03 km/s and the rest
		resp, raw3 := postJSON(t, base+"/class-maps", map[string]any{"runId": run.ID, "weight": "count", "edges": []float64{2.03}})
		if resp.StatusCode != 201 {
			t.Fatalf("%s: class maps %d %s", layout.name, resp.StatusCode, raw3)
		}
		var cm struct {
			Classes []struct{ Components int }
		}
		json.Unmarshal(raw3, &cm)
		var narrow int
		for k := 0; k < dnComp; k++ {
			for yy := 0; yy < dyPix; yy++ {
				for xx := 0; xx < dxPix; xx++ {
					if _, _, w, ok := truth(k, xx, yy); ok && w/fwhmPerSigma < 2.03 {
						narrow++
					}
				}
			}
		}
		if len(cm.Classes) != 2 || cm.Classes[0].Components != narrow || cm.Classes[1].Components != fitted-narrow {
			t.Errorf("%s: classes %+v, want %d and %d", layout.name, cm.Classes, narrow, fitted-narrow)
		}
	}
}

func TestDecompositionOfARunWhoseProfileIsNotLoadedIsRefusedByName(t *testing.T) {
	t.Parallel()
	e, _, _ := reviewEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)
	run, _ := e.importRun(t, filepath.Join(e.root, "synth_decomp_demo"), 201)
	e.srv.Runs.Profiles = nil // a server that restarted without the test profile
	base := e.ts.URL + "/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa"
	resp, body := get(t, base+"/components?run="+itoa(int(run.ID))+"&x=0&y=0")
	if resp.StatusCode != 409 || !strings.Contains(string(body), "SynthDecomp-cubes@1") {
		t.Errorf("a run read with a profile this server lacks must say which: %d %s", resp.StatusCode, body)
	}
}
