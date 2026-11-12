package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const hashCfg = "[input]\ninput_file = \"/data/a.fits\"\noutput_dir = \"/data/out-1\"\n\n[noise]\nnoise = 0.17\n\n[vgt]\n# comment\ndn = 20\nker = 4\nlist = [1, 2]\n"

// The configuration hash leaves out where the run read and wrote, and nothing else: another input, another output folder and a
// different order of the overrides give the same value; any setting, a list entry or a key Vesqira does not edit changes it.
func TestConfigHashIgnoresPathsOnly(t *testing.T) {
	req := VGTRequest{BaseConfigText: hashCfg, Set: map[string]string{"ker": "6"}}
	base, ok := ConfigHash("vgt", req)
	if !ok || len(base) != 64 {
		t.Fatalf("hash %q %v", base, ok)
	}
	same := []VGTRequest{
		{BaseConfigText: strings.Replace(hashCfg, "/data/a.fits", "/other/b.fits", 1), Set: map[string]string{"ker": "6"}},
		{BaseConfigText: strings.Replace(hashCfg, "/data/out-1", "/x/out-9", 1), Set: map[string]string{"ker": "6", "output_dir": "/y"}},
		{BaseConfigText: strings.Replace(hashCfg, "ker = 4", "ker = 6", 1)},                                       // the override written into the text
		{BaseConfigText: hashCfg, Set: map[string]string{"ker": "6"}, CPUs: 64, Mem: "1T", RepoDir: "/elsewhere"}, // how it was carried out
	}
	for i, r := range same {
		if h, ok := ConfigHash("vgt", r); !ok || h != base {
			t.Errorf("same configuration %d hashed differently (%v)", i, ok)
		}
	}
	differ := []VGTRequest{
		{BaseConfigText: hashCfg, Set: map[string]string{"ker": "7"}},
		{BaseConfigText: strings.Replace(hashCfg, "[1, 2]", "[1, 3]", 1), Set: map[string]string{"ker": "6"}},
		{BaseConfigText: hashCfg + "extra = 1\n", Set: map[string]string{"ker": "6"}},
	}
	for i, r := range differ {
		if h, ok := ConfigHash("vgt", r); !ok || h == base {
			t.Errorf("different configuration %d hashed the same (%v)", i, ok)
		}
	}
	if h, _ := ConfigHash("rohsa", VGTRequest{BaseConfigText: "[input]\ninput = \"/a\"\n[rohsa]\nn_gauss = [4]\n"}); h == "" {
		t.Error("a ROHSA config has no hash")
	}
	for _, bad := range []VGTRequest{{}, {BaseConfigText: "not = [toml"}, {BaseConfigText: hashCfg, Set: map[string]string{"nope": "1"}}} {
		if h, ok := ConfigHash("vgt", bad); ok || h != "" {
			t.Errorf("an unreadable configuration got a hash: %+v", bad)
		}
	}
}

// Replacing the input changes that one line and says so when it cannot.
func TestSetInputFileChangesOnlyTheInputLine(t *testing.T) {
	out, err := SetInputFile("vgt", hashCfg, "/data/new.fits")
	if err != nil || strings.Replace(out, "/data/new.fits", "/data/a.fits", 1) != hashCfg {
		t.Fatalf("%v\n%s", err, out)
	}
	rohsa := "[input]\ninput = \"/data/a.fits\" # the cube\ncrop_size = 0\n[rohsa]\nn_gauss = [4]\n"
	out, err = SetInputFile("rohsa", rohsa, "/data/new.fits")
	if err != nil || InputPathOf("rohsa", out) != "/data/new.fits" || !strings.Contains(out, "crop_size = 0") || !strings.Contains(out, "n_gauss = [4]") {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, p := range []string{"relative.fits", "/data/a b.fits", `/data/a"b.fits`, ""} {
		if _, err := SetInputFile("vgt", hashCfg, p); err == nil {
			t.Errorf("%q was accepted as an input path", p)
		}
	}
}

// The preamble hashes the input only when asked, in job mode, and the script is valid bash.
func TestEnvPreambleHashesInputOnlyWhenAsked(t *testing.T) {
	plain := EnvPreamble(EnvSpec{RepoDir: "/r", InputFile: "/data/a.fits"})
	hashed := EnvPreamble(EnvSpec{RepoDir: "/r", InputFile: "/data/a.fits", HashInput: true})
	if !strings.Contains(plain, `job`+"\n") || strings.Contains(plain, `job full`) || !strings.Contains(hashed, `ve_collect "/r" "/data/a.fits" job full`) {
		t.Errorf("preamble flags:\n%s\n%s", plain, hashed)
	}
	_, probe, _ := EnvProbe("/r", "/data/a.fits")
	if !strings.HasSuffix(strings.TrimSpace(probe), `ve_collect "$1" "$2" probe`) {
		t.Error("the probe hashes the input")
	}
	if out, err := exec.Command("bash", "-n", "-c", "jobdir=/tmp\n"+hashed).CombinedOutput(); err != nil {
		t.Errorf("bash -n: %v %s", err, out)
	}
}

// Run for real: the job's own preamble writes the SHA-256 of every byte of its input, and says why when it cannot.
func TestEnvPreambleRecordsTheInputsFullHash(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "cube.fits")
	content := "SIMPLE  = T" + strings.Repeat("x", 5000)
	if err := os.WriteFile(in, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	run := func(path string) string {
		jd := t.TempDir()
		script := "jobdir=" + jd + "\n" + EnvPreamble(EnvSpec{RepoDir: dir, InputFile: path, HashInput: true})
		if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		b, err := os.ReadFile(filepath.Join(jd, "vesqira-env.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	got := run(in)
	if !strings.Contains(got, `"input.full_sha256": {"status": "verified", "value": "`+hex.EncodeToString(sum[:])+`"}`) {
		t.Errorf("env record:\n%s", got)
	}
	if got := run(filepath.Join(dir, "missing.fits")); !strings.Contains(got, `"input.full_sha256": {"status": "unresolved", "reason": "the input file is not readable on this node"}`) {
		t.Errorf("missing input:\n%s", got)
	}
}
