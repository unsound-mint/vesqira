package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Golden files for the two job templates (ADR 0068, slice 5). They were written from the renderers and config editors as they
// were before run templates became profile data, and they must not change: a profile-driven render of VGT.jl@1 or ROHSA-LMC@1
// has to be the same bytes for the same inputs. Regenerate only with VESQIRA_UPDATE_GOLDEN=1, and only on purpose.

const goldenVGTConfig = `# a VGT config shaped like the pipeline's own
[input]
input_file = "/data/cube.fits"
output_dir = "/tmp/old"

[vgt]
dn = 8
ker = 4
min_valid = 3

[noise]
noise = 0.17
noise_mode = "scalar"
`

// goldenVGTBare has no [vgt] and no [noise] table, so a key set on it makes a table.
const goldenVGTBare = "[input]\ninput_file = \"/data/cube.fits\"\n"

type goldenSet struct {
	name   string
	base   string
	values map[string]string
}

// The sets stay deterministic under the old code: SetConfigValues walks a map, so two keys absent from the same table of the
// config are inserted in either order. Each case here adds at most one new line per table.
var goldenVGTSets = []goldenSet{
	{"vgt-none", goldenVGTConfig, nil},
	{"vgt-output-only", goldenVGTConfig, map[string]string{"output_dir": "/data/vesqira_runs/x-0a1b2c3d"}},
	{"vgt-typed", goldenVGTConfig, map[string]string{"ker": "8", "dn": "16", "min_valid": "5", "noise": "0.2", "noise_mode": "channel", "output_dir": "/data/o"}},
	{"vgt-exponent", goldenVGTConfig, map[string]string{"noise_sigma_multiplier": "-1.5e-3"}},
	{"vgt-angle-frame-new-table", goldenVGTBare, map[string]string{"angle_frame": "galactic"}},
	{"vgt-output-bin-new-table", goldenVGTBare, map[string]string{"output_bin": "4"}},
	{"vgt-input-file", goldenVGTConfig, map[string]string{"input_file": "/data/other/cube2.fits"}},
	{"vgt-new-key-existing-table", goldenVGTConfig, map[string]string{"noise_tile_size": "64"}},
	{"vgt-empty-config", "", map[string]string{"ker": "2"}},
}

var goldenVGTRefused = []goldenSet{
	{"refuse-vgt-unknown-key", goldenVGTConfig, map[string]string{"shell": "1"}},
	{"refuse-vgt-retired-noise-mode", goldenVGTConfig, map[string]string{"noise_mode": "separable_shrinkage"}},
	{"refuse-vgt-injection-enum", goldenVGTConfig, map[string]string{"angle_frame": `galactic"` + "\n" + `x = "1`}},
	{"refuse-vgt-injection-number", goldenVGTConfig, map[string]string{"noise": "1\nrm -rf /"}},
	{"refuse-vgt-path-dotdot", goldenVGTConfig, map[string]string{"input_file": "/data/../etc/passwd"}},
	{"refuse-vgt-path-space", goldenVGTConfig, map[string]string{"output_dir": "/data/a b"}},
	{"refuse-vgt-path-relative", goldenVGTConfig, map[string]string{"input_file": "data/cube.fits"}},
	{"refuse-vgt-int-negative", goldenVGTConfig, map[string]string{"ker": "-1"}},
	{"refuse-vgt-int-real", goldenVGTConfig, map[string]string{"dn": "8.5"}},
	{"refuse-vgt-posint-zero", goldenVGTConfig, map[string]string{"output_bin": "0"}},
	{"refuse-vgt-number-text", goldenVGTConfig, map[string]string{"noise": "abc"}},
	{"refuse-vgt-int-long", goldenVGTConfig, map[string]string{"ker": "1234567890"}},
}

var goldenROHSASets = []goldenSet{
	{"rohsa-none", ownerShaped, nil},
	{"rohsa-output-only", "", nil}, // handled below with the output folder
	{"rohsa-typed", ownerShaped, map[string]string{"n_gauss": "8", "lambda_amp": "30", "maxiter": "500", "noise": "false", "memory_gb": "96", "spectral_kernel": "gauss1.5"}},
	{"rohsa-reals", ownerShaped, map[string]string{"lambda_mu": "2.5", "ub_sig": "1e2", "lb_sig": "0"}},
	{"rohsa-tied", ownerShaped, map[string]string{"lambda": "100"}},
	{"rohsa-tied-and-explicit", ownerShaped, map[string]string{"lambda": "100", "lambda_amp": "7"}},
	{"rohsa-memory-auto", ownerShaped, map[string]string{"memory_gb": "auto"}},
	{"rohsa-crop", ownerShaped, map[string]string{"crop_size": "1024", "crop_x0": "10", "crop_y0": "20"}},
	{"rohsa-new-key", "[rohsa]\nn_gauss = [4]\n", map[string]string{"maxiter_init": "200"}},
	{"rohsa-new-table", "[rohsa]\nn_gauss = [4]\n", map[string]string{"crop_size": "256"}},
	{"rohsa-key-in-other-table", "[input]\ninput = \"/d/c.fits\"\n[extra]\nnoise = true\n[auto_config]\nenabled = false\n", map[string]string{"noise": "false"}},
}

var goldenROHSARefused = []goldenSet{
	{"refuse-rohsa-unknown-key", ownerShaped, map[string]string{"regul": "false"}},
	{"refuse-rohsa-output-dir", ownerShaped, map[string]string{"output_dir": "/tmp/x"}},
	{"refuse-rohsa-input", ownerShaped, map[string]string{"input": "/tmp/x"}},
	{"refuse-rohsa-number", ownerShaped, map[string]string{"lambda_amp": "ten"}},
	{"refuse-rohsa-injection-kernel", ownerShaped, map[string]string{"spectral_kernel": "none\"\nx=\"1"}},
	{"refuse-rohsa-kernel-unknown", ownerShaped, map[string]string{"spectral_kernel": "boxcar"}},
	{"refuse-rohsa-bool", ownerShaped, map[string]string{"noise": "yes"}},
	{"refuse-rohsa-int-range", ownerShaped, map[string]string{"maxiter": "0"}},
	{"refuse-rohsa-int-range-high", ownerShaped, map[string]string{"maxiter": "1000001"}},
	{"refuse-rohsa-ngauss-range", ownerShaped, map[string]string{"n_gauss": "65"}},
	{"refuse-rohsa-memory-exponent", ownerShaped, map[string]string{"memory_gb": "1e2"}},
	{"refuse-rohsa-memory-range", ownerShaped, map[string]string{"memory_gb": "0.5"}},
	{"refuse-rohsa-crop-range", ownerShaped, map[string]string{"crop_size": "100001"}},
	{"refuse-rohsa-auto-config", strings.Replace(ownerShaped, "enabled = false", "enabled = true", 1), map[string]string{"n_gauss": "8"}},
	{"refuse-rohsa-not-tables", "n_gauss = 4\n", map[string]string{"maxiter": "5"}},
	{"refuse-rohsa-not-toml", "[[[", nil},
	{"refuse-rohsa-multiline-setting", "[rohsa]\nn_gauss = [\n  16,\n]\n", map[string]string{"n_gauss": "8"}},
}

func goldenCheck(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if os.Getenv("VESQIRA_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (regenerate with VESQIRA_UPDATE_GOLDEN=1)", name, err)
	}
	if string(want) != got {
		t.Errorf("%s: output differs from the golden file\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func outcome(text string, err error) string {
	if err != nil {
		return "ERROR: " + err.Error() + "\n"
	}
	return text
}

type goldenResources struct {
	name                          string
	cpus                          int
	mem, time, partition, account string
	expect                        string
	hashInput                     bool
}

var goldenResourceSets = []goldenResources{
	{"plain", 32, "64G", "1-00:00:00", "", "", "", false},
	{"partition-account", 8, "4096M", "12:00:00", "zen4", "proj-1", "", false},
	{"strict", 16, "12G", "03:00:00", "", "", "af0a7946f4579a67f0135e09c96237a4271db28e", false},
	{"hash-input", 256, "180G", "3-00:00:00", "gpu", "", "", true},
	{"strict-hash-account", 1, "1G", "00:10:00", "", "acct.x", "baf4a3bfc32112da219d8fbc2fc4a0051faab853", true},
}

var goldenRefusedRender = []struct {
	name string
	mut  func(*goldenResources, *string)
}{
	{"cpus-zero", func(r *goldenResources, _ *string) { r.cpus = 0 }},
	{"cpus-257", func(r *goldenResources, _ *string) { r.cpus = 257 }},
	{"mem-bad", func(r *goldenResources, _ *string) { r.mem = "64 G" }},
	{"time-bad", func(r *goldenResources, _ *string) { r.time = "1 day" }},
	{"partition-injection", func(r *goldenResources, _ *string) { r.partition = "a;rm -rf /" }},
	{"account-space", func(r *goldenResources, _ *string) { r.account = "a b" }},
	{"config-empty", func(_ *goldenResources, c *string) { *c = "" }},
	{"config-marker", func(_ *goldenResources, c *string) { *c += "\nVESQIRA_CONFIG_END\nrm -rf /\n" }},
}

func TestGoldenVGT(t *testing.T) {
	for _, g := range append(append([]goldenSet{}, goldenVGTSets...), goldenVGTRefused...) {
		text, err := SetConfigValues(g.base, g.values)
		goldenCheck(t, g.name, outcome(text, err))
	}
	cfg, _ := SetConfigValues(goldenVGTConfig, map[string]string{"ker": "8", "output_dir": "/data/vesqira_runs/vgt-run-0a1b2c3d"})
	for _, r := range goldenResourceSets {
		script, err := RenderVGT(VGTSettings{Name: "vgt-run", RepoDir: "/shared/home/you/vgt", Config: cfg, CPUs: r.cpus, Mem: r.mem, Time: r.time, Partition: r.partition, Account: r.account, ExpectCommit: r.expect, HashInput: r.hashInput})
		goldenCheck(t, "script-vgt-"+r.name, outcome(script, err))
	}
	for _, m := range goldenRefusedRender {
		r, c := goldenResourceSets[0], cfg
		m.mut(&r, &c)
		script, err := RenderVGT(VGTSettings{Name: "vgt-run", RepoDir: "/shared/home/you/vgt", Config: c, CPUs: r.cpus, Mem: r.mem, Time: r.time, Partition: r.partition, Account: r.account})
		goldenCheck(t, "script-vgt-refuse-"+m.name, outcome(script, err))
	}
	for name, p := range map[string]VGTSettings{
		"name-space":    {Name: "a b", RepoDir: "/r", Config: cfg},
		"repo-relative": {Name: "n", RepoDir: "r", Config: cfg},
		"repo-dotdot":   {Name: "n", RepoDir: "/r/../x", Config: cfg},
		"repo-quote":    {Name: "n", RepoDir: `/r"x`, Config: cfg},
	} {
		p.CPUs, p.Mem, p.Time = 4, "8G", "01:00:00"
		script, err := RenderVGT(p)
		goldenCheck(t, "script-vgt-refuse-"+name, outcome(script, err))
	}
}

func TestGoldenROHSA(t *testing.T) {
	for _, g := range append(append([]goldenSet{}, goldenROHSASets...), goldenROHSARefused...) {
		out := ""
		if g.name == "rohsa-output-only" {
			g.base, out = ownerShaped, "/data/vesqira_runs/rohsa-run-0a1b2c3d"
		}
		text, err := SetROHSAConfig(g.base, g.values, out)
		goldenCheck(t, g.name, outcome(text, err))
	}
	// the job's own output folder goes through the same path check as a value
	for name, out := range map[string]string{"out-dir-space": "/a b", "out-dir-dotdot": "/a/../b", "out-dir-relative": "a"} {
		text, err := SetROHSAConfig(ownerShaped, nil, out)
		goldenCheck(t, "refuse-rohsa-"+name, outcome(text, err))
	}
	cfg, _ := SetROHSAConfig(ownerShaped, map[string]string{"n_gauss": "8", "lambda": "30"}, "/data/vesqira_runs/rohsa-run-0a1b2c3d")
	for _, r := range goldenResourceSets {
		script, err := RenderROHSA(ROHSASettings{Name: "rohsa-run", RepoDir: "/shared/home/you/ROHSA-LMC", ConfigPath: "/shared/home/you/ROHSA-LMC/configs/rohsa/run.toml", Config: cfg, CPUs: r.cpus, Mem: r.mem, Time: r.time, Partition: r.partition, Account: r.account, ExpectCommit: r.expect, HashInput: r.hashInput})
		goldenCheck(t, "script-rohsa-"+r.name, outcome(script, err))
	}
	for _, m := range goldenRefusedRender {
		r, c := goldenResourceSets[0], cfg
		m.mut(&r, &c)
		script, err := RenderROHSA(ROHSASettings{Name: "rohsa-run", RepoDir: "/shared/home/you/ROHSA-LMC", Config: c, CPUs: r.cpus, Mem: r.mem, Time: r.time, Partition: r.partition, Account: r.account})
		goldenCheck(t, "script-rohsa-refuse-"+m.name, outcome(script, err))
	}
	for name, p := range map[string]ROHSASettings{
		"name-space":     {Name: "a b", RepoDir: "/r", Config: cfg},
		"repo-relative":  {Name: "n", RepoDir: "r", Config: cfg},
		"configpath-bad": {Name: "n", RepoDir: "/r", ConfigPath: "c.toml", Config: cfg},
	} {
		p.CPUs, p.Mem, p.Time = 4, "8G", "01:00:00"
		script, err := RenderROHSA(p)
		goldenCheck(t, "script-rohsa-refuse-"+name, outcome(script, err))
	}
}

// The checks behind the editable keys: the same value is accepted or refused the same way by CheckSetting, CheckROHSASetting
// and the sweep and relay paths that call them.
func TestGoldenSettingChecks(t *testing.T) {
	var lines []string
	vgt := map[string][]string{
		"noise": {"0.17", "abc", "1e5", ""}, "noise_mode": {"scalar", "separable", "separable_shrinkage"}, "dn": {"8", "-1", "0", "8.0"},
		"output_bin": {"1", "0", "99"}, "angle_frame": {"pixel", "galactic", "icrs", "fk5"}, "input_file": {"/a/b", "a", "/a/../b"}, "shell": {"1"},
	}
	rohsa := map[string][]string{
		"lambda": {"10", "x"}, "n_gauss": {"1", "64", "65", "0"}, "noise": {"true", "false", "1"}, "memory_gb": {"auto", "96", "96.5", "0", "100001", "1e2"},
		"spectral_kernel": {"none", "hanning", "gauss1", "gauss1.5", "gauss", "x"}, "output_dir": {"/a"}, "crop_size": {"0", "100000", "100001"}, "maxiter": {"1", "1000000", "1000001"},
	}
	for _, set := range []struct {
		name  string
		vals  map[string][]string
		check func(string, string) error
	}{{"vgt", vgt, CheckSetting}, {"rohsa", rohsa, CheckROHSASetting}} {
		keys := make([]string, 0, len(set.vals))
		for k := range set.vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range set.vals[k] {
				lines = append(lines, fmt.Sprintf("%s %s=%q %s", set.name, k, v, outcome("ok\n", set.check(k, v))))
			}
		}
	}
	lines = append(lines, "rohsa keys "+strings.Join(ROHSAEditableKeys(), ",")+"\n")
	kinds := []string{}
	for _, k := range []string{"dn", "noise_mode", "input_file", "angle_frame", "output_bin"} {
		kind, ok := SettingKind("vgt", k)
		kinds = append(kinds, fmt.Sprintf("vgt %s %s %v", k, kind, ok))
	}
	for _, k := range []string{"lambda", "n_gauss", "output_dir", "memory_gb", "spectral_kernel", "nope"} {
		kind, ok := SettingKind("rohsa", k)
		kinds = append(kinds, fmt.Sprintf("rohsa %s %s %v", k, kind, ok))
	}
	goldenCheck(t, "setting-checks", strings.Join(lines, "")+strings.Join(kinds, "\n")+"\n")
}
