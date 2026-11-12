package jobs

import (
	"strings"
	"testing"
)

// The masks a cutout, slab, align or convolve job carries are written to fixed names directly in the job's own output folder (ADR 0064
// addendum): the script has the options with exactly the names the API planned, and anything else is refused before it reaches the shell.
func TestAnalysisScriptCarriesMasksToPlannedNames(t *testing.T) {
	dir := "/data/vesqira_derived/cutout-1-ab12cd34"
	carryOut := dir + "/mask-carried-1-saturated-0a1b2c3d.fits"
	missOut := dir + "/mask-missing-1-missing-4e5f6a7b.fits"
	base := AnalysisSettings{Name: "cutout-1", Python: "python3", ScriptDir: "$HOME/.cache/vesqira/0123456789abcdef", Op: "cutout",
		Input: "/data/cube.fits", OutDir: dir, CPUs: 1, Mem: "1G", Time: "00:10:00", Args: map[string]string{"x0": "0", "x1": "5", "y0": "0", "y1": "5"}}
	ok := base
	ok.Carry = `[{"out":"` + carryOut + `","path":"/data/vesqira_derived/mask-1-saturated-99aabbcc.fits","type":"saturated"}]`
	ok.MissingOut = missOut
	s, err := RenderAnalysis(ok)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{" --carry-masks '" + ok.Carry + "' --missing-out " + missOut + " --out "} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	noCarry, err := RenderAnalysis(base)
	if err != nil || strings.Contains(noCarry, "--carry-masks") || strings.Contains(noCarry, "--missing-out") {
		t.Errorf("a job that carries nothing has no carry options: %v\n%s", err, noCarry)
	}
	for name, mut := range map[string]func(*AnalysisSettings){
		"another folder": func(a *AnalysisSettings) { a.MissingOut = "/data/elsewhere/mask-missing-1-missing-4e5f6a7b.fits" },
		"a carried copy over a source": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"/data/cube.fits","path":"/data/m.fits","type":"saturated"}]`
		},
		"a free name": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + dir + `/anything.fits","path":"/data/m.fits","type":"saturated"}]`
		},
		"a missing-kind name in the carry list": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + missOut + `","path":"/data/m.fits","type":"saturated"}]`
		},
		"a carried-kind name as the no-value mask": func(a *AnalysisSettings) { a.MissingOut = carryOut },
		"the no-value type carried": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + carryOut + `","path":"/data/m.fits","type":"missing"}]`
		},
		"a quote": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + carryOut + `","path":"/data/a'b.fits","type":"saturated"}]`
		},
		"an extra key": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + carryOut + `","path":"/data/m.fits","type":"saturated","name":"x"}]`
		},
		"a traversal": func(a *AnalysisSettings) { a.MissingOut = dir + "/../mask-missing-1-missing-4e5f6a7b.fits" },
		"the same file twice": func(a *AnalysisSettings) {
			a.Carry = `[{"out":"` + carryOut + `","path":"/data/m.fits","type":"saturated"},{"out":"` + carryOut + `","path":"/data/n.fits","type":"saturated"}]`
		},
		"a moment job": func(a *AnalysisSettings) { a.Op = "moment" },
	} {
		bad := ok
		mut(&bad)
		if out, err := RenderAnalysis(bad); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
}
