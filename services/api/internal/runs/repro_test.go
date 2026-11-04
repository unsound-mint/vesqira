package runs

import (
	"strings"
	"testing"
)

func fact(fs []Fact, key string) Fact {
	for _, f := range fs {
		if f.Key == key {
			return f
		}
	}
	return Fact{}
}

func TestReproducibilityNeverTurnsAnAbsentRecordIntoAReassuringOne(t *testing.T) {
	bare := Reproducibility(Run{}, false)
	for _, k := range []string{"code", "parameters", "input", "environment", "seed", "equivalence", "evaluations"} {
		if f := fact(bare, k); f.Status != "unresolved" {
			t.Errorf("%s with nothing recorded must be unresolved: %+v", k, f)
		}
	}
	if fact(bare, "outputs").Key != "" {
		t.Error("no outputs, nothing to say about their checksums")
	}

	run := Run{GitCommit: "bcbaed4ec9800ef95c561cfb4ad45c344743e469-dirty", Dirty: true, InputName: "demo.fits", InputSizeBytes: 207360,
		Params:      []Param{{Key: "ker", Value: "8"}, {Key: "random_seed", Value: "42"}},
		Diagnostics: map[string]string{"fit_attempts": "1000", "fit_failures": "13"},
		Outputs:     []Output{{Role: "psi"}, {Role: "reliability"}}}
	f := Reproducibility(run, true)
	if c := fact(f, "code"); c.Status != "declared" || !strings.Contains(c.Detail, "uncommitted") || !strings.Contains(c.Detail, "bcbaed4ec980") {
		t.Errorf("dirty code: %+v", c)
	}
	if c := fact(f, "input"); c.Status != "inferred" || !strings.Contains(c.Detail, "do not prove") {
		t.Errorf("input matched by name and size is inferred, not verified: %+v", c)
	}
	if c := fact(f, "seed"); c.Status != "declared" || c.Detail != "random_seed = 42" {
		t.Errorf("seed: %+v", c)
	}
	if c := fact(f, "evaluations"); c.Status != "declared" || !strings.Contains(c.Detail, "13 of 1000") || !strings.Contains(c.Detail, "not recorded") {
		t.Errorf("evaluations: %+v", c)
	}
	if c := fact(f, "outputs"); c.Status != "unresolved" || !strings.Contains(c.Detail, "2 files") {
		t.Errorf("outputs: %+v", c)
	}
	if c := fact(Reproducibility(Run{GitCommit: "abc"}, false), "code"); !strings.Contains(c.Detail, "Not checked") {
		t.Errorf("a clean commit is only reported by the run: %+v", c)
	}
}
