package sweeps

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func fp(v float64) *float64 { return &v }

func TestExpandKeepsParameterOrderAndCapsTheProduct(t *testing.T) {
	ps := []Param{{Key: "N", Values: []string{"8", "12"}}, {Key: "lambda", Label: "λ", Values: []string{"3", "10", "30"}}}
	got, err := Expand(ps)
	if err != nil || len(got) != 6 {
		t.Fatalf("%v %d", err, len(got))
	}
	// the first parameter is the slowest-changing, each list keeps its order
	want := [][2]string{{"8", "3"}, {"8", "10"}, {"8", "30"}, {"12", "3"}, {"12", "10"}, {"12", "30"}}
	for i, w := range want {
		if got[i]["N"] != w[0] || got[i]["lambda"] != w[1] {
			t.Errorf("combination %d is %v, want %v", i, got[i], w)
		}
	}
	if l := Label(ps, got[4]); l != "N = 12, λ = 10" {
		t.Errorf("label %q", l)
	}
	// 64 is allowed, 65 is not, and so is a fourth parameter
	vals := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		return out
	}
	if _, err := Expand([]Param{{Key: "a", Values: vals(8)}, {Key: "b", Values: vals(8)}}); err != nil {
		t.Errorf("64 jobs refused: %v", err)
	}
	if _, err := Expand([]Param{{Key: "a", Values: vals(13)}, {Key: "b", Values: vals(5)}}); err == nil || !strings.Contains(err.Error(), "64") {
		t.Errorf("65 jobs: %v", err)
	}
	four := []Param{{Key: "a", Values: []string{"1"}}, {Key: "b", Values: []string{"1"}}, {Key: "c", Values: []string{"1"}}, {Key: "d", Values: []string{"1"}}}
	if Validate(four) == nil || Validate(nil) == nil {
		t.Error("1 to 3 parameters")
	}
}

func TestValidateRefusesBadValuesAndDuplicates(t *testing.T) {
	for name, ps := range map[string][]Param{
		"empty list":    {{Key: "N", Values: nil}},
		"empty value":   {{Key: "N", Values: []string{""}}},
		"space":         {{Key: "N", Values: []string{"1 2"}}},
		"shell":         {{Key: "N", Values: []string{"$(x)"}}},
		"duplicate val": {{Key: "N", Values: []string{"1", "1"}}},
		"duplicate key": {{Key: "N", Values: []string{"1"}}, {Key: "N", Values: []string{"2"}}},
		"bad key":       {{Key: "N=1", Values: []string{"1"}}},
		"long label":    {{Key: "N", Label: strings.Repeat("x", 21), Values: []string{"1"}}},
	} {
		if Validate(ps) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestJobNamesAreSafeAndUnique(t *testing.T) {
	ps := []Param{{Key: "dn", Values: []string{"16"}}, {Key: "ker", Values: []string{"4"}}}
	if n := JobName("vgt-sweep", ps, map[string]string{"dn": "16", "ker": "4"}, 0); n != "vgt-sweep-dn16-ker4" {
		t.Errorf("name %q", n)
	}
	long := JobName(strings.Repeat("a", 32), []Param{{Key: "noise_sigma_multiplier", Values: nil}}, map[string]string{"noise_sigma_multiplier": "0.5"}, 6)
	if len(long) > 40 || !strings.HasSuffix(long, "-s07") {
		t.Errorf("fallback %q", long)
	}
}

func TestDiffClassifiesEveryDifferingLine(t *testing.T) {
	base := "#!/bin/bash\n#SBATCH --job-name=base\ncat > c <<'E'\n[input]\noutput_dir = \"/r/base-1\"\n[vgt]\ndn = 20\nker = 4\nE\nrun\n"
	draft := "#!/bin/bash\n#SBATCH --job-name=sw-dn16\ncat > c <<'E'\n[input]\noutput_dir = \"/r/sw-dn16-2\"\n[vgt]\ndn = 16\nker = 4\nE\nrun\n"
	d := Diff(base, draft, []string{"dn", "ker"})
	kinds := map[string]int{}
	for _, l := range d {
		kinds[l.Kind]++
	}
	if len(d) != 6 || kinds["parameter"] != 2 || kinds["output"] != 2 || kinds["name"] != 2 || Unexpected(d) != 0 {
		t.Fatalf("%v %v", kinds, d)
	}
	// an unexpected change is flagged as such
	d2 := Diff(base, strings.Replace(draft, "run\n", "run --fast\n", 1), []string{"dn"})
	if Unexpected(d2) != 2 {
		t.Errorf("unexpected = %d, want 2: %v", Unexpected(d2), d2)
	}
	if len(Diff(base, base, nil)) != 0 {
		t.Error("identical scripts differ")
	}
}

func rows() []Row {
	// N x lambda, 2 x 2; the (12, 10) run has no recorded value, (8, 10) has no run
	mk := func(id int64, n, l string, v *float64, unit string) Row {
		r := Row{JobID: id, Values: map[string]string{"N": n, "lambda": l}, Metrics: []Metric{}}
		if v != nil || unit != "none" {
			r.Metrics = []Metric{{Key: "rms", Value: v, Unit: unit, Definition: "rms of the residual"}}
		}
		return r
	}
	return []Row{mk(1, "8", "3", fp(0.5), "K"), mk(2, "8", "10", nil, "none"), mk(3, "12", "3", fp(0.4), "K"), mk(4, "12", "10", nil, "K")}
}

func TestPlotKeepsGapsLinesAndNumericAxis(t *testing.T) {
	ps := []Param{{Key: "N", Values: []string{"8", "12"}}, {Key: "lambda", Label: "λ", Values: []string{"3", "10"}}}
	p, why := BuildPlot(ps, rows(), "rms", "N")
	if p == nil {
		t.Fatal(why)
	}
	if !p.Numeric || p.Unit != "K" || len(p.Lines) != 2 || p.Lines[0].Label != "λ = 3" || p.Lines[1].Label != "λ = 10" {
		t.Fatalf("%+v", p)
	}
	l10 := p.Lines[1].Points
	if l10[0].Value != nil || l10[1].Value != nil {
		t.Errorf("a missing value became a number: %+v", l10)
	}
	if l3 := p.Lines[0].Points; *l3[0].Value != 0.5 || *l3[1].Value != 0.4 || *l3[1].XNum != 12 {
		t.Errorf("%+v", l3)
	}
	if info := Metrics(rows()); len(info) != 1 || info[0].Runs != 3 || info[0].Values != 2 || info[0].Unit != "K" {
		t.Errorf("%+v", info)
	}
	// the other parameter on the x axis gives one line per N
	q, _ := BuildPlot(ps, rows(), "rms", "lambda")
	if q == nil || len(q.Lines) != 2 || q.Lines[0].Label != "N = 8" {
		t.Errorf("%+v", q)
	}
}

func TestPlotRefusesMixedUnitsAndCategoricalAxisIsInListOrder(t *testing.T) {
	ps := []Param{{Key: "mode", Values: []string{"scalar", "channel"}}}
	rs := []Row{
		{JobID: 1, Values: map[string]string{"mode": "scalar"}, Metrics: []Metric{{Key: "rms", Value: fp(1), Unit: "K"}}},
		{JobID: 2, Values: map[string]string{"mode": "channel"}, Metrics: []Metric{{Key: "rms", Value: fp(2), Unit: "Jy/beam"}}},
	}
	if p, why := BuildPlot(ps, rs, "rms", "mode"); p != nil || !strings.Contains(why, "different units") {
		t.Errorf("mixed units plotted: %v %q", p, why)
	}
	if info := Metrics(rs); !info[0].UnitsDiffer || info[0].Unit != "" {
		t.Errorf("%+v", info)
	}
	rs[1].Metrics[0].Unit = "K"
	p, _ := BuildPlot(ps, rs, "rms", "mode")
	if p == nil || p.Numeric || !reflect.DeepEqual(p.XValues, []string{"scalar", "channel"}) {
		t.Errorf("%+v", p)
	}
	rs[1].Metrics[0].Definition = "other"
	if p, _ := BuildPlot(ps, rs, "rms", "mode"); len(p.Notes) != 1 {
		t.Errorf("definitions differ and nothing said so: %+v", p)
	}
	if p, _ := BuildPlot(ps, rs, "nope", "mode"); p != nil {
		t.Error("an undeclared metric plotted")
	}
}

func TestStoreApproveStopAndLookups(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ps := []Param{{Key: "dn", Values: []string{"8", "16"}}}
	sw, err := st.Create(Sweep{Name: "s", HostID: 1, BaseJobID: 9, Params: ps}, []int64{10, 11}, []map[string]string{{"dn": "8"}, {"dn": "16"}})
	if err != nil || sw.State != StateDraft || sw.Concurrency != 1 {
		t.Fatalf("%+v %v", sw, err)
	}
	if _, _, ok := st.OfJob(11); !ok {
		t.Error("job 11 belongs to the sweep")
	}
	if _, err := st.Approve(sw.ID, 0, false, nil); err == nil {
		t.Error("concurrency 0")
	}
	if won, _ := st.Approve(sw.ID, 2, true, map[int64]string{10: "h10", 11: "h11"}); !won {
		t.Fatal("approve")
	}
	if won, _ := st.Approve(sw.ID, 2, true, nil); won {
		t.Error("a running sweep was approved again")
	}
	ms, _ := st.Members(sw.ID)
	if ms[0].ApprovedHash != "h10" || ms[1].Values["dn"] != "16" {
		t.Errorf("%+v", ms)
	}
	if r, _ := st.Running(); len(r) != 1 {
		t.Error("running")
	}
	if ok, _ := st.Stop(sw.ID, "x"); !ok {
		t.Error("stop")
	}
	if ok, _ := st.Stop(sw.ID, "x"); ok {
		t.Error("stopped twice")
	}
	if ok, _ := st.SetBoard(sw.ID, 5); !ok {
		t.Error("board")
	}
	if ok, _ := st.SetBoard(sw.ID, 6); ok {
		t.Error("a second board")
	}
	if got, ok := st.OfBoard(5); !ok || got.ID != sw.ID {
		t.Error("of board")
	}
}
