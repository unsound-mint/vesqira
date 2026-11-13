package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
)

// A job's results are imported by itself only when the profile that declares its template also says how its run record is read and
// what it holds (ADR 0068, slice 6). Anything else gets a sentence, and a custom script belongs to no profile.
func TestJobImportFollowsTheTemplatesProfile(t *testing.T) {
	srv := &Server{Runs: &runs.Store{Profiles: runstest.WithSyntheticTool(t)}}
	for _, tpl := range []string{"vgt", "rohsa"} {
		if !srv.autoImports(tpl) {
			t.Errorf("%s results are imported automatically", tpl)
		}
	}
	// SynthFit declares a template and no record
	if srv.autoImports("synthfit") {
		t.Errorf("a template whose profile has no run record cannot import by itself")
	}
	if _, rec, why := srv.jobImport("synthfit"); rec != "" || !strings.Contains(why, "declares no run record") || !strings.Contains(srv.noAutoImport("synthfit"), "register their files by hand") {
		t.Errorf("synthfit: record %q, sentence %q", rec, why)
	}
	// a custom script is nobody's
	if srv.autoImports("custom") {
		t.Errorf("a custom script's results were taken as a profile's")
	}

	// the same profile with a record and an outputs table reads its jobs' results
	raw, err := os.ReadFile(filepath.Join("..", "runs", "testdata", "synthetic_tool_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	p := doc["profiles"].([]any)[0].(map[string]any)
	p["record"] = map[string]any{"file": "synthfit_run.toml", "reader": "run_metadata", "anyKey": []string{"synthfit.version"}}
	p["outputs"] = map[string]any{"table": "products", "files": map[string]any{"o": map[string]any{"product": "orientation_map", "role": "orientation"}}}
	withRecord, _ := json.Marshal(doc)
	reg, err := runs.Shipped().With(withRecord)
	if err != nil {
		t.Fatal(err)
	}
	srv = &Server{Runs: &runs.Store{Profiles: reg}}
	prof, rec, why := srv.jobImport("synthfit")
	if why != "" || rec != "synthfit_run.toml" || prof == nil || prof.Name != "SynthFit" || !srv.autoImports("synthfit") {
		t.Errorf("profile %v record %q sentence %q", prof, rec, why)
	}
}
