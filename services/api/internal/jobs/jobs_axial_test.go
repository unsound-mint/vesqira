package jobs

import (
	"strings"
	"testing"
)

func TestAnalysisScriptCarriesAnOrientationDeclarationAndRefusesAForgedOne(t *testing.T) {
	base := AnalysisSettings{Name: "align-1", Python: "python3", ScriptDir: "$HOME/.cache/vesqira/0123456789abcdef", Op: "align",
		Input: "/data/a.fits", Reference: "/data/b.fits", OutDir: "/data/out", CPUs: 1, Mem: "1G", Time: "00:10:00"}
	plain, err := RenderAnalysis(base)
	if err != nil || strings.Contains(plain, "axial") || strings.Contains(plain, "rel-out") {
		t.Fatalf("a scalar analysis must be unchanged: %v\n%s", err, plain)
	}
	ax := base
	ax.AxialUnit, ax.AxialFrame, ax.Weights = "rad", "pixel", "/data/w.fits"
	s, err := RenderAnalysis(ax)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--axial-unit rad", "--axial-frame pixel", "--weights /data/w.fits", `--rel-out "$out/result.reliability.fits"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for name, mut := range map[string]func(*AnalysisSettings){
		"unit":    func(a *AnalysisSettings) { a.AxialUnit = "deg; rm -rf ~" },
		"frame":   func(a *AnalysisSettings) { a.AxialFrame = "$(x)" },
		"weights": func(a *AnalysisSettings) { a.Weights = "/data/w x.fits" },
		"no unit": func(a *AnalysisSettings) { a.AxialUnit = "" },
	} {
		bad := ax
		mut(&bad)
		if _, err := RenderAnalysis(bad); err == nil {
			t.Errorf("%s: a forged declaration was accepted", name)
		}
	}
}
