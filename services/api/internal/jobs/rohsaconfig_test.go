package jobs

import (
	"errors"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// ownerShaped is written for these tests in the shape of the pipeline's configs (header comment, [input] with the crop keys
// after the paths, [rohsa] with n_gauss as a list, a commented-out auto_config axis, a key the editor does not know). It is
// not a copy of any real config.
const ownerShaped = `# Run: test region 512x512, N=16, spatial smoothing lambda=10
# ROHSA preprocessing config. Edit this file directly.

[input]
input = "data/interim/test_region.fits"
output_dir = "validation_output/rohsa/test_region_n16"

# crop_size = 0 means full cube.
crop_size = 512
crop_x0 = 1
crop_y0 = 1
spatial_stride = 1
spectral_kernel = "hanning"
memory_gb = "auto"   # keep this comment

[rohsa]
n_gauss = [16]
lambda_amp = 10.0
lambda_mu = 10.0
lambda_sig = 10.0
lambda_var_amp = 0.0
lambda_var_mu = 0.0
lambda_var_sig = 0.0
lb_sig = 1.2
ub_sig = 100.0
maxiter = 800
noise = true
regul = true

[auto_config]
enabled = false
max_candidates = 24
# n_gauss = [4, 6, 8]
# lambda_amp = [10.0, 100.0, 1000.0]
`

func decode(t *testing.T, text string) map[string]map[string]any {
	t.Helper()
	var m map[string]map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		t.Fatalf("not TOML: %v\n%s", err, text)
	}
	return m
}

func asText(v any) string { s, _ := tomlText(v); return s }

func TestROHSAConfigRoundTripKeepsEverythingElseVerbatim(t *testing.T) {
	cfg, err := ReadROHSAConfig(ownerShaped)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Values["n_gauss"] != "16" || cfg.Values["lambda_amp"] != "10" || cfg.Values["noise"] != "true" || cfg.Values["spectral_kernel"] != "hanning" || cfg.CropSize != 512 || cfg.NGauss != 16 {
		t.Errorf("read: %+v", cfg)
	}
	if _, ok := cfg.Values["output_dir"]; ok {
		t.Error("output_dir is Vesqira's, never an editable value")
	}
	// no changes: only the output folder differs, as the job sets it
	out, err := SetROHSAConfig(ownerShaped, nil, "/shared/home/u/vgt/vesqira_runs/rohsa-again-ab12")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(ownerShaped, `output_dir = "validation_output/rohsa/test_region_n16"`, `output_dir = "/shared/home/u/vgt/vesqira_runs/rohsa-again-ab12"`, 1)
	if out != want {
		t.Errorf("only the output_dir line may change:\n%s", out)
	}
	// changes: N, the tied smoothing weights and noise; every other entry, comments and unknown keys included, is as it was
	out, err = SetROHSAConfig(ownerShaped, map[string]string{"n_gauss": "12", "lambda": "30", "noise": "false"}, "/shared/home/u/vgt/vesqira_runs/x-1")
	if err != nil {
		t.Fatal(err)
	}
	got, base := decode(t, out), decode(t, ownerShaped)
	if n := got["rohsa"]["n_gauss"].([]any); len(n) != 1 || n[0].(int64) != 12 {
		t.Errorf("n_gauss stays a list of one: %v", got["rohsa"]["n_gauss"])
	}
	for _, k := range []string{"lambda_amp", "lambda_mu", "lambda_sig"} {
		if got["rohsa"][k].(float64) != 30 {
			t.Errorf("%s = %v", k, got["rohsa"][k])
		}
	}
	if got["rohsa"]["noise"].(bool) {
		t.Error("noise not switched off")
	}
	changed := map[string]bool{"n_gauss": true, "lambda_amp": true, "lambda_mu": true, "lambda_sig": true, "noise": true}
	for tbl, m := range base {
		for k, v := range m {
			if tbl == "rohsa" && changed[k] || k == "output_dir" {
				continue
			}
			if asText(got[tbl][k]) != asText(v) {
				t.Errorf("[%s] %s changed: %v -> %v", tbl, k, v, got[tbl][k])
			}
		}
	}
	for _, line := range []string{"# Run: test region 512x512, N=16, spatial smoothing lambda=10", "memory_gb = \"auto\"   # keep this comment", "# n_gauss = [4, 6, 8]", "regul = true", "[auto_config]", "enabled = false"} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("line lost: %q", line)
		}
	}
	// the same request renders the same text, whatever order the map is walked in
	for i := 0; i < 20; i++ {
		again, _ := SetROHSAConfig(ownerShaped, map[string]string{"noise": "false", "lambda": "30", "n_gauss": "12"}, "/shared/home/u/vgt/vesqira_runs/x-1")
		if again != out {
			t.Fatal("not deterministic")
		}
	}
}

func TestROHSAWhitelistRefusals(t *testing.T) {
	for _, k := range []string{"output_dir", "input", "input_file", "reuse", "force_full_cube", "memory_gb", "n-gauss", "dn", ""} {
		if _, err := SetROHSAConfig(ownerShaped, map[string]string{k: "/shared/home/u/x"}, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted: %v", k, err)
		}
		if CheckROHSASetting(k, "1") == nil {
			t.Errorf("%q passes the check", k)
		}
	}
	for _, c := range [][2]string{{"n_gauss", "0"}, {"n_gauss", "65"}, {"lambda", "ten"}, {"lambda_amp", "1;rm"}, {"noise", "yes"}, {"maxiter", "-1"}, {"spectral_kernel", "boxcar"}, {"crop_size", "5e2"}, {"lb_sig", "1\n[x]"}} {
		if _, err := SetROHSAConfig(ownerShaped, map[string]string{c[0]: c[1]}, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s=%q accepted: %v", c[0], c[1], err)
		}
	}
	if _, err := SetROHSAConfig(ownerShaped, nil, "relative/dir"); err == nil {
		t.Error("relative output dir accepted")
	}
	auto := strings.Replace(ownerShaped, "enabled = false", "enabled = true", 1)
	if _, err := SetROHSAConfig(auto, map[string]string{"n_gauss": "8"}, ""); err == nil || !strings.Contains(err.Error(), "auto_config") {
		t.Errorf("auto_config base with a changed N: %v", err)
	}
	if _, err := SetROHSAConfig("n_gauss = [4]\n", nil, ""); err == nil {
		t.Error("a config with a top-level key is refused, as the pipeline refuses it")
	}
	if _, err := SetROHSAConfig("[rohsa]\nn_gauss = [4,\n 6]\n", map[string]string{"n_gauss": "8"}, ""); err == nil {
		t.Error("a multi-line list is refused, not mangled")
	}
	for _, k := range ROHSAEditableKeys() {
		if k == "output_dir" || k == "input" {
			t.Errorf("%s is listed as editable", k)
		}
	}
}

func TestROHSATiedKeyAndItsMembersNeverDependOnMapOrder(t *testing.T) {
	// lambda sets the three weights; an explicit member beats it, always
	for i := 0; i < 50; i++ {
		out, err := SetROHSAConfig(ownerShaped, map[string]string{"lambda": "30", "lambda_amp": "5"}, "")
		if err != nil {
			t.Fatal(err)
		}
		d := decode(t, out)["rohsa"]
		if d["lambda_amp"].(float64) != 5 || d["lambda_mu"].(float64) != 30 || d["lambda_sig"].(float64) != 30 {
			t.Fatalf("run %d: %v %v %v", i, d["lambda_amp"], d["lambda_mu"], d["lambda_sig"])
		}
	}
	if got := ROHSAExpand(map[string]string{"lambda": "3", "lambda_mu": "9", "n_gauss": "4"}); got["lambda_amp"] != "3" || got["lambda_mu"] != "9" || got["lambda_sig"] != "3" || got["n_gauss"] != "4" || len(got) != 4 {
		t.Errorf("%v", got)
	}
	if !ROHSAOverlap("lambda", "lambda_sig") || ROHSAOverlap("lambda", "n_gauss") || ROHSAOverlap("lambda_amp", "lambda_mu") || !ROHSAOverlap("noise", "noise") {
		t.Error("overlap")
	}
}

func TestROHSAKeyFoundInAnotherTableIsTheOneEdited(t *testing.T) {
	// the pipeline reads keys by name, so a crop_size under [rohsa] is still crop_size; a second copy must not appear
	text := "[input]\ninput = \"a.fits\"\noutput_dir = \"o\"\n[rohsa]\nn_gauss = [4]\ncrop_size = 256\n"
	out, err := SetROHSAConfig(text, map[string]string{"crop_size": "512"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "crop_size") != 1 || !strings.Contains(out, "crop_size = 512") {
		t.Errorf("%s", out)
	}
	// a key the file lacks goes to its conventional table
	out, err = SetROHSAConfig(text, map[string]string{"spectral_kernel": "gauss1.5"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if d := decode(t, out); d["input"]["spectral_kernel"] != "gauss1.5" {
		t.Errorf("%s", out)
	}
}

func TestROHSASizingMirrorsTheSubmitScript(t *testing.T) {
	// the cases of hpc/irgetas/submit_rohsa.sh: crop up to 512 / 1024 / 2048 (N <= 6 or more) / the whole cube
	for _, c := range []struct {
		crop, n   int
		cpus      int
		mem, time string
	}{
		{512, 16, 16, "12G", "03:00:00"}, {1, 6, 16, "12G", "03:00:00"},
		{513, 16, 16, "40G", "12:00:00"}, {1024, 4, 16, "40G", "12:00:00"},
		{1025, 6, 32, "100G", "1-00:00:00"}, {2048, 6, 32, "100G", "1-00:00:00"},
		{2048, 7, 32, "140G", "1-12:00:00"}, {2048, 16, 32, "140G", "1-12:00:00"},
		{2049, 16, 32, "180G", "3-00:00:00"}, {0, 16, 32, "180G", "3-00:00:00"},
	} {
		z := SuggestROHSASizing(c.crop, c.n, true, true)
		if z.CPUs != c.cpus || z.Mem != c.mem || z.Time != c.time || len(z.Assumed) != 0 {
			t.Errorf("crop %d N %d: %+v", c.crop, c.n, z)
		}
	}
	// a config without crop_size or n_gauss: the script's own defaults (0 and 6), and the suggestion says it assumed them
	z := SuggestROHSASizing(0, 0, false, false)
	if z.CropSize != 0 || z.NGauss != 6 || z.Mem != "180G" || len(z.Assumed) != 2 {
		t.Errorf("%+v", z)
	}
	if !strings.Contains(z.Basis, "submit_rohsa.sh") || z.ScriptPartition != "ZEN4" {
		t.Errorf("the source is named, the partition is information: %+v", z)
	}
	cfg, err := ReadROHSAConfig(strings.Replace(ownerShaped, "crop_size = 512", "crop_size = 2048", 1))
	if err != nil {
		t.Fatal(err)
	}
	if z := cfg.SizingOf(); z.Mem != "140G" || z.CPUs != 32 || z.Time != "1-12:00:00" {
		t.Errorf("2048 square, N=16: %+v", z)
	}
}
