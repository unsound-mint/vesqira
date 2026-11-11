package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

func TestResearcherDeclaresControlsAndTheyAreStoredAsDeclared(t *testing.T) {
	e := newRecEnv(t)
	run, err := e.srv.Runs.Save("/data/run-c", runs.Parsed{Run: runs.Run{Name: "run-c", Kind: "vgt"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	url := e.api("/api/runs/" + itoa(int(run.ID)) + "/controls")
	put := func(body any) (int, runs.Run, string) {
		st, b := callJSON(t, "PUT", url, body)
		var out runs.Run
		json.Unmarshal(b, &out)
		return st, out, string(b)
	}
	if st, _ := callJSON(t, "PUT", e.api("/api/runs/9999/controls"), map[string]any{}); st != 404 {
		t.Errorf("unknown run: %d", st)
	}
	// nothing stated is not "none applied": the run has no controls until someone declares them
	if got, _ := e.srv.Runs.Get(run.ID); got.Controls != nil {
		t.Fatalf("a new run has controls: %+v", got.Controls)
	}
	// whatever the request says about who stated a block, the stored block is the researcher's declaration
	st, out, raw := put(map[string]any{
		"smoothing":    map[string]any{"kernel": "gaussian_fwhm", "width": 3.5, "unit": "arcmin", "basis": "verified", "by": "vesqira", "at": "1999-01-01T00:00:00Z"},
		"noise":        map[string]any{"kind": "scalar_sigma", "sigma": 0.17, "sigmaUnit": "K", "estimated": "robust MAD of line-free channels"},
		"construction": map[string]any{"kind": "shifted_map_null", "params": []map[string]string{{"key": "n_shifts", "value": "1000"}, {"key": "seed", "value": "5"}}, "note": "second map rolled with wrap"},
	})
	if st != 200 || out.Controls == nil {
		t.Fatalf("declare: %d %s", st, raw)
	}
	doc := loadSpec(t)
	const route = "/api/runs/{id}/controls"
	conforms(t, requestSchema(t, doc, route, "PUT"), "controls request", mustJSON(t, map[string]any{
		"smoothing": map[string]any{"kernel": "gaussian_fwhm", "width": 3.5, "unit": "arcmin"}, "noise": map[string]any{"kind": "rms_map"}}))
	conforms(t, responseSchemaAt(t, doc, route, "PUT", 200, "application/json"), "controls reply", []byte(raw))
	_, gb0 := get(t, e.api("/api/runs/"+itoa(int(run.ID))))
	var ctl struct {
		Controls json.RawMessage `json:"controls"`
	}
	json.Unmarshal(gb0, &ctl)
	conforms(t, doc.Components.Schemas["Controls"], "controls", ctl.Controls)
	c := out.Controls
	for name, s := range map[string]runs.Stated{"smoothing": c.Smoothing.Stated, "noise": c.Noise.Stated, "construction": c.Construction.Stated} {
		if s.Basis != "declared" || s.By != "researcher" || s.At == "" || s.At == "1999-01-01T00:00:00Z" {
			t.Errorf("%s stated as %+v", name, s)
		}
	}
	if *c.Smoothing.Width != 3.5 || c.Smoothing.Unit != "arcmin" || *c.Noise.Sigma != 0.17 || c.Noise.Estimated == "" || len(c.Construction.Params) != 2 {
		t.Errorf("stored: %+v", c)
	}
	// it comes back on the run, in the list, and is what a reviewer's summary of the run carries
	_, gb := get(t, e.api("/api/runs/"+itoa(int(run.ID))))
	if !strings.Contains(string(gb), `"controls":{"smoothing":{"kernel":"gaussian_fwhm","width":3.5,"unit":"arcmin"`) {
		t.Errorf("the run: %s", gb)
	}
	sum, err := e.srv.Runs.SummaryOf(run.ID, "psi")
	if err != nil || sum.Controls == nil || sum.Controls.Noise == nil || sum.Controls.Construction.Kind != "shifted_map_null" {
		t.Fatalf("the reviewer's summary: %+v %v", sum, err)
	}
	if ents, _ := e.srv.Audit.List("run", run.ID, 10); len(ents) == 0 || ents[0].Action != "run controls declared" || !strings.Contains(ents[0].Detail, "noise model") {
		t.Errorf("audit: %+v", ents)
	}
	// a block that is not sent is cleared: the body is the whole statement
	if st, out, raw := put(map[string]any{"noise": map[string]any{"kind": "rms_map", "estimated": "per-pixel rms of the off-source cube"}}); st != 200 || out.Controls.Smoothing != nil || out.Controls.Noise.Kind != "rms_map" || out.Controls.Noise.Sigma != nil {
		t.Errorf("replace: %d %s", st, raw)
	}
	if st, out, _ := put(map[string]any{}); st != 200 || out.Controls != nil {
		t.Errorf("clear: %d %+v", st, out.Controls)
	}
	if sum, _ := e.srv.Runs.SummaryOf(run.ID, "psi"); sum.Controls != nil {
		t.Error("a run with no stated controls carries none to a reviewer")
	}
	if b, _ := json.Marshal(sum2(e, run.ID)); strings.Contains(string(b), "controls") {
		t.Errorf("a summary without controls must not name the field (summaries frozen before it existed look the same): %s", b)
	}
}

func sum2(e *recEnv, id int64) *runs.Summary { s, _ := e.srv.Runs.SummaryOf(id, "psi"); return s }

func TestControlsAreRefusedWhenTheyWouldInventAUnitOrAMeaning(t *testing.T) {
	e := newRecEnv(t)
	run, _ := e.srv.Runs.Save("/data/run-d", runs.Parsed{Run: runs.Run{Name: "run-d", Kind: "vgt"}}, nil)
	url := e.api("/api/runs/" + itoa(int(run.ID)) + "/controls")
	bad := map[string]map[string]any{
		"width without a unit":     {"smoothing": map[string]any{"kernel": "boxcar", "width": 5}},
		"width in a made-up unit":  {"smoothing": map[string]any{"kernel": "boxcar", "width": 5, "unit": "beams-ish"}},
		"unit without a width":     {"smoothing": map[string]any{"kernel": "boxcar", "unit": "pixel"}},
		"zero width":               {"smoothing": map[string]any{"kernel": "boxcar", "width": 0, "unit": "pixel"}},
		"negative width":           {"smoothing": map[string]any{"kernel": "gaussian_sigma", "width": -2, "unit": "pixel"}},
		"unknown kernel":           {"smoothing": map[string]any{"kernel": "lanczos", "width": 2, "unit": "pixel"}},
		"other kernel unexplained": {"smoothing": map[string]any{"kernel": "other"}},
		"sigma without a unit":     {"noise": map[string]any{"kind": "scalar_sigma", "sigma": 0.2}},
		"sigma of a map model":     {"noise": map[string]any{"kind": "rms_map", "sigma": 0.2, "sigmaUnit": "K"}},
		"negative sigma":           {"noise": map[string]any{"kind": "scalar_sigma", "sigma": -1, "sigmaUnit": "K"}},
		"unknown noise model":      {"noise": map[string]any{"kind": "poisson"}},
		"unknown construction":     {"construction": map[string]any{"kind": "bootstrap"}},
		"other without a note":     {"construction": map[string]any{"kind": "other"}},
		"bad parameter name":       {"construction": map[string]any{"kind": "permutation", "params": []map[string]string{{"key": "N Shifts", "value": "1"}}}},
		"repeated parameter":       {"construction": map[string]any{"kind": "permutation", "params": []map[string]string{{"key": "n", "value": "1"}, {"key": "n", "value": "2"}}}},
		"too long a note":          {"construction": map[string]any{"kind": "permutation", "note": strings.Repeat("x", 301)}},
		"unknown field":            {"smoothing": map[string]any{"kernel": "boxcar", "fwhm": 3}},
	}
	for name, body := range bad {
		if st, b := callJSON(t, "PUT", url, body); st != 422 && st != 400 {
			t.Errorf("%s: %d %s, want a refusal", name, st, b)
		}
	}
	if got, _ := e.srv.Runs.Get(run.ID); got.Controls != nil {
		t.Errorf("a refused statement was stored: %+v", got.Controls)
	}
	// a kernel with no stated width is allowed: smoothing was applied and its width is not known (null, never a default)
	if st, b := callJSON(t, "PUT", url, map[string]any{"smoothing": map[string]any{"kernel": "gaussian_fwhm"}}); st != 200 || !strings.Contains(string(b), `"width":null`) {
		t.Errorf("unknown width: %d %s", st, b)
	}
}
