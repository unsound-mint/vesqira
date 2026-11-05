package runs

import (
	"database/sql"
	"math"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const withMetrics = sample + `
[metrics]
plain = 0.5
unknown_one = nan
[metrics.alignment_measure]
value = 0.344
unit = ""
uncertainty = 0.02
uncertainty_kind = "standard_error"
n = 2695
definition = "Axial agreement with the Planck 353 GHz field, masked where the VGT reliability is below 0.5"
[metrics.bad_unc]
value = 1.0
uncertainty = -0.1
uncertainty_kind = "standard_error"
[metrics.guessed_kind]
value = 2.0
uncertainty = 0.3
uncertainty_kind = "made up"
[metrics.where]
value = 3.0
unit = "/home/someone/units"
definition = "see /home/someone/notes/ for how"
[metrics."bad key!"]
value = 9.0
`

func byKey(ms []Metric) map[string]Metric {
	out := map[string]Metric{}
	for _, m := range ms {
		out[m.Key] = m
	}
	return out
}

func TestMetricsKeepWhatTheRunStatesAndNothingMore(t *testing.T) {
	p, err := ParseVGT([]byte(withMetrics), "run1")
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(p.Metrics)
	am := m["alignment_measure"]
	if am.Value == nil || *am.Value != 0.344 || am.Uncertainty == nil || *am.Uncertainty != 0.02 || am.UncertaintyKind != "standard_error" || am.N == nil || *am.N != 2695 || am.Status != "declared" {
		t.Errorf("alignment_measure: %+v", am)
	}
	if !strings.Contains(am.Definition, "Planck") {
		t.Errorf("definition lost: %q", am.Definition)
	}
	if v := m["plain"].Value; v == nil || *v != 0.5 {
		t.Errorf("a bare number is a value: %+v", m["plain"])
	}
	if m["unknown_one"].Value != nil {
		t.Error("NaN must be unknown, never a number")
	}
	if b := m["bad_unc"]; b.Uncertainty != nil || b.UncertaintyKind != "" || b.Value == nil {
		t.Errorf("a negative uncertainty is not kept: %+v", b)
	}
	if g := m["guessed_kind"]; g.Uncertainty == nil || g.UncertaintyKind != "" {
		t.Errorf("an unknown kind must stay empty, not be guessed: %+v", g)
	}
	if w := m["where"]; w.Unit != "" || w.Definition != "" || w.Value == nil {
		t.Errorf("locations must not leave the machine through a unit or a definition: %+v", w)
	}
	if _, ok := m["bad key!"]; ok {
		t.Error("an invalid key was kept")
	}
	if clean, _ := ParseVGT([]byte(sample), "r"); len(clean.Metrics) != 0 {
		t.Errorf("a run without metrics has none: %v", clean.Metrics)
	}
}

func TestMetricsSurviveTheStoreAndTheReviewerSummaryKeepsUnknownUnknown(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ParseVGT([]byte(withMetrics), "run1")
	run, err := s.Save("/runs/one", p, []Output{{Role: "psi", DatasetID: 7, Name: "psi.fits"}})
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(run.Metrics)
	if v := got["alignment_measure"].Value; v == nil || math.Abs(*v-0.344) > 1e-12 {
		t.Errorf("stored value: %+v", got["alignment_measure"])
	}
	if got["unknown_one"].Value != nil {
		t.Error("an unknown value came back as a number")
	}
	sum, err := s.ForDataset(7)
	if err != nil || sum == nil {
		t.Fatalf("%v %v", sum, err)
	}
	if byKey(sum.Metrics)["unknown_one"].Value != nil || byKey(sum.Metrics)["alignment_measure"].N == nil {
		t.Errorf("summary metrics: %+v", sum.Metrics)
	}
	again, _ := ParseVGT([]byte(sample), "run1") // the same folder imported again without metrics
	run2, _ := s.Save("/runs/one", again, nil)
	if len(run2.Metrics) != 0 {
		t.Errorf("re-import must replace the metrics: %v", run2.Metrics)
	}
}

func TestAddressesOfFilesOnOtherSystemsNeverReachTheReviewer(t *testing.T) {
	for _, bad := range []string{`file:///home/user/x.fits`, `\\nas\share\x`, `s3://bucket/key/x`, `see sftp://login.example.org/data`, `C:\Users\user\x`, `FILE:///x`} {
		m := parseMetrics(map[string]any{"a": map[string]any{"value": 1.0, "unit": bad, "definition": bad}})
		if len(m) != 1 || m[0].Unit != "" || m[0].Definition != "" {
			t.Errorf("%q was kept: %+v", bad, m)
		}
	}
	m := parseMetrics(map[string]any{"a": map[string]any{"value": 1.0, "definition": "agreement with Planck 353 GHz, see https://arxiv.org/abs/2000.00001"}})
	if m[0].Definition == "" {
		t.Error("an https link in a definition is fine")
	}
}
