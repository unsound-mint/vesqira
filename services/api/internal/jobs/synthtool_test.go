package jobs

import (
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
)

// The synthetic SynthFit template (runs/testdata) renders through the same function as VGT and ROHSA. This pins what is specific
// to a tool that declares no Julia probe: its script must not record facts about an interpreter it does not use.
func TestSyntheticTemplateRendersWithoutJuliaProbes(t *testing.T) {
	tpl := runstest.WithSyntheticTool(t).TemplateByID("synthfit")
	if tpl == nil {
		t.Fatal("the synthetic template is not registered")
	}
	cfg, err := SetConfig(tpl, "[input]\ninput_cube = \"x.fits\"\n", map[string]string{"n_bins": "8", "threshold": "1.5"}, "/r/out")
	if err != nil {
		t.Fatal(err)
	}
	script, err := RenderTemplate(tpl, Settings{Name: "s", RepoDir: "/r/synthfit", Config: cfg, CPUs: 4, Mem: "8G", Time: "02:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "ve_no julia.version \"this tool's profile declares no Julia probe\"") ||
		!strings.Contains(script, "ve_no julia.manifest_sha256 \"this tool's profile declares no Julia probe\"") {
		t.Errorf("the Julia facts must be unresolved with a reason, not collected:\n%s", script)
	}
	if strings.Contains(script, "ve_manifest \"$1\"") || strings.Contains(script, "julia --version") {
		t.Errorf("a tool without a Julia probe must not run one:\n%s", script)
	}
	// the shipped Julia tools still record both
	vgt, err := RenderVGT(VGTSettings{Name: "v", RepoDir: "/r/vgt", Config: "[input]\ninput_file = \"x\"\n", CPUs: 2, Mem: "4G", Time: "01:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vgt, "ve_manifest \"$1\"") || !strings.Contains(vgt, "julia --version") {
		t.Errorf("VGT must keep its Julia probes")
	}
	// a value outside the template's range is refused by the same typed check
	if _, err := SetConfig(tpl, "", map[string]string{"n_bins": "65"}, ""); err == nil || !strings.Contains(err.Error(), "n_bins") {
		t.Errorf("n_bins 65 must be refused: %v", err)
	}
}
