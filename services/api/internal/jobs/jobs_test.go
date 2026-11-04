package jobs

import (
	"os/exec"
	"strings"
	"testing"
)

const baseConfig = `[input]
input_file = "data/x.fits"
output_dir = ""

[noise]
noise = 0.17
noise_mode = "scalar"

[vgt]
dn = 20
ker = 4
`

func good() VGTSettings {
	return VGTSettings{Name: "vgt-ker8", RepoDir: "/shared/home/u/vgt", Config: baseConfig, CPUs: 32, Mem: "64G", Time: "1-00:00:00"}
}

func TestRenderedScriptIsValidShellAndCarriesTheConfigVerbatim(t *testing.T) {
	s, err := RenderVGT(good())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#SBATCH --cpus-per-task=32", "#SBATCH --mem=64G", "cd /shared/home/u/vgt", "noise_mode = \"scalar\"", "srun --ntasks=1 julia"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("not valid bash: %v\n%s", err, out)
	}
}

func TestSettingsCannotInjectShell(t *testing.T) {
	bad := map[string]func(*VGTSettings){
		"name":       func(p *VGTSettings) { p.Name = "a;rm -rf ~" },
		"repo":       func(p *VGTSettings) { p.RepoDir = "/x; rm -rf ~" },
		"repo quote": func(p *VGTSettings) { p.RepoDir = "/x'y" },
		"repo subst": func(p *VGTSettings) { p.RepoDir = "/x/$(id)" },
		"repo dots":  func(p *VGTSettings) { p.RepoDir = "/x/../y" },
		"mem":        func(p *VGTSettings) { p.Mem = "64G\n#SBATCH --x" },
		"time":       func(p *VGTSettings) { p.Time = "1;2" },
		"cpus":       func(p *VGTSettings) { p.CPUs = 0 },
		"partition":  func(p *VGTSettings) { p.Partition = "a b" },
		"config end": func(p *VGTSettings) { p.Config = "x\n" + configEnd + "\nrm -rf ~" },
		"empty cfg":  func(p *VGTSettings) { p.Config = "" },
	}
	for name, mut := range bad {
		p := good()
		mut(&p)
		if _, err := RenderVGT(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestConfigEditingIsTypedAndWhitelisted(t *testing.T) {
	out, err := SetConfigValues(baseConfig, map[string]string{"ker": "8", "noise_mode": "channel", "output_dir": "/shared/home/u/runs/a", "noise_sigma_multiplier": "3.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ker = 8", `noise_mode = "channel"`, `output_dir = "/shared/home/u/runs/a"`, "noise_sigma_multiplier = 3.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `output_dir = ""`) || strings.Count(out, "ker =") != 1 {
		t.Errorf("a value was duplicated or left behind:\n%s", out)
	}
	for _, bad := range []map[string]string{{"ker": "8; x"}, {"ker": "-1"}, {"noise_mode": "bogus"}, {"output_dir": "/a b"}, {"input_file": "rel/x.fits"}, {"unknown": "1"}, {"noise": "1e"}} {
		if _, err := SetConfigValues(baseConfig, bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestStatusParsing(t *testing.T) {
	cases := []struct {
		out, state string
	}{
		{"RUNNING\n---\nRUNNING|0:0|00:01:00|2026-11-04T10:00:00|Unknown\n", StateRunning},
		{"PENDING\n---\n", StateSubmitted},
		{"---\nCOMPLETED|0:0|01:02:03|a|b\n", StateCompleted},
		{"---\nFAILED|1:0|00:00:05|a|b\n", StateFailed},
		{"---\nTIMEOUT|0:15|1-00:00:00|a|b\n", StateFailed},
		{"---\nOUT_OF_MEMORY|0:125|00:10:00|a|b\n", StateFailed},
		{"---\nCANCELLED by 1000|0:0|00:00:03|a|b\n", StateCancelled},
		{"---\n", ""},
	}
	for _, c := range cases {
		st, _ := ParseStatus(c.out)
		if st != c.state {
			t.Errorf("%q -> %q, want %q", c.out, st, c.state)
		}
	}
	if st, d := ParseStatus("---\nCOMPLETED|0:0|01:02:03|a|b\n"); st != StateCompleted || !strings.Contains(d, "exit 0:0") {
		t.Errorf("detail %q", d)
	}
}

func TestRemoteCommandsTakeOnlyValidatedIdentifiers(t *testing.T) {
	for _, id := range []string{"12; rm -rf ~", "", "1 2", "$(id)", "12345678901234"} {
		if _, err := StatusCommand(id); err == nil {
			t.Errorf("status accepted %q", id)
		}
		if _, err := CancelCommand(id); err == nil {
			t.Errorf("cancel accepted %q", id)
		}
	}
	for _, wd := range []string{"../x", "job-123", "job-0123456789abcdef; id", ""} {
		if _, err := SubmitCommand(wd); err == nil {
			t.Errorf("submit accepted %q", wd)
		}
	}
	if c, err := SubmitCommand(NewWorkDir()); err != nil || !strings.Contains(c, "sbatch --parsable") {
		t.Errorf("%v %q", err, c)
	}
	if id, err := ParseSubmit("12345;cluster\n"); err != nil || id != "12345" {
		t.Errorf("%q %v", id, err)
	}
	if _, err := ParseSubmit("Submitted batch job 12345 by mistake"); err == nil {
		t.Error("a non-numeric submit answer was accepted")
	}
}
