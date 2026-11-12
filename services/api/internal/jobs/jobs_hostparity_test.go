package jobs

import (
	"strings"
	"testing"
)

// Masks and corrections land in an analysis job's script inside single quotes (ADR 0064). Only a strict shape passes: a single quote,
// a newline, a key with free text, a path with a space or a file the job does not read is refused, so nothing in them can end the quote
// or reach the shell.
func TestAnalysisScriptCarriesMasksAndCorrectionsInertly(t *testing.T) {
	base := AnalysisSettings{Name: "stats-1", Python: "python3", ScriptDir: "$HOME/.cache/vesqira/0123456789abcdef", Op: "stats",
		Input: "/data/cube.fits", OutDir: "/data/vesqira_derived/stats-1-ab", CPUs: 1, Mem: "1G", Time: "00:10:00"}
	ok := base
	ok.Masks = `[{"path":"/data/vesqira_derived/mask-1-saturated-0a.fits","type":"saturated","policy":"weight","weight":0.5}]`
	ok.Corrections = `{"/data/cube.fits":{"bunit":"Jy/beam","hash":"0123456789abcdef0123","wcs":{"CRVAL2":2.05}}}`
	s, err := RenderAnalysis(ok)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{" --masks '" + ok.Masks + "'", " --corrections '" + ok.Corrections + "'"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for name, mut := range map[string]func(*AnalysisSettings){
		"quote in a mask path": func(a *AnalysisSettings) {
			a.Masks = `[{"path":"/data/a'b.fits","type":"saturated","policy":"exclude"}]`
		},
		"a mask's name": func(a *AnalysisSettings) {
			a.Masks = `[{"path":"/data/m.fits","type":"saturated","policy":"exclude","name":"x"}]`
		},
		"space in a mask path": func(a *AnalysisSettings) {
			a.Masks = `[{"path":"/data/m x.fits","type":"saturated","policy":"exclude"}]`
		},
		"relative mask path": func(a *AnalysisSettings) { a.Masks = `[{"path":"../m.fits","type":"saturated","policy":"exclude"}]` },
		"no policy":          func(a *AnalysisSettings) { a.Masks = `[{"path":"/data/m.fits","type":"saturated"}]` },
		"weight out of range": func(a *AnalysisSettings) {
			a.Masks = `[{"path":"/data/m.fits","type":"saturated","policy":"weight","weight":2}]`
		},
		"not JSON":                  func(a *AnalysisSettings) { a.Masks = `[{"path":` },
		"masks on a moment-less op": func(a *AnalysisSettings) { a.Op = "cutout"; a.Masks = ok.Masks },
		"quote in a corrected unit": func(a *AnalysisSettings) {
			a.Corrections = `{"/data/cube.fits":{"bunit":"K'; rm -rf ~; '","hash":"0123456789abcdef"}}`
		},
		"newline in a correction": func(a *AnalysisSettings) {
			a.Corrections = "{\"/data/cube.fits\":{\"hash\":\"0123456789abcdef\",\n\"bunit\":\"K\"}}"
		},
		"a file the job does not read": func(a *AnalysisSettings) { a.Corrections = `{"/etc/passwd":{"hash":"0123456789abcdef"}}` },
		"no hash":                      func(a *AnalysisSettings) { a.Corrections = `{"/data/cube.fits":{"bunit":"K"}}` },
	} {
		bad := ok
		mut(&bad)
		if out, err := RenderAnalysis(bad); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
}
