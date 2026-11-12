package runs

import (
	"strings"
	"testing"
)

const sample = `generated_at_utc = "2026-06-25T09:21:44Z"
gitcommit = "bcbaed4ec9800ef95c561cfb4ad45c344743e469-dirty"
[config]
dn = 10
fitter_mode = "axial_vonmises"
input_file = "/tmp/vgt_inputs/hnv1.fits"
output_dir = "validation_output/x"
noise = 0.37
flag = true
[input]
path = "/tmp/vgt_inputs/hnv1.fits"
size_bytes = 4007885760
[outputs]
psi_path = "validation_output/x/psi_hnv1.fits"
reliability_path = "../../etc/passwd"
unknown_path = "a/b.fits"
[performance]
elapsed_seconds = 106.162
[run_parameters]
label = "vgt"
home_dir = "relative"
note = "see /home/someone/x"
`

func TestParseKeepsParametersAndDropsEveryLocation(t *testing.T) {
	p, err := ParseVGT([]byte(sample), "run1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, q := range p.Params {
		got[q.Key] = q.Value
	}
	for _, want := range []string{"dn", "fitter_mode", "noise", "flag", "label"} {
		if _, ok := got[want]; !ok {
			t.Errorf("parameter %q lost: %v", want, got)
		}
	}
	for _, banned := range []string{"input_file", "output_dir", "home_dir", "note"} {
		if _, ok := got[banned]; ok {
			t.Errorf("location-like parameter %q kept", banned)
		}
	}
	for _, q := range p.Params {
		if strings.Contains(q.Value, "/") || strings.Contains(q.Key, "path") {
			t.Errorf("a location leaked into the parameters: %+v", q)
		}
	}
	if p.InputName != "hnv1.fits" || p.InputSizeBytes != 4007885760 {
		t.Errorf("input hint: %q %d", p.InputName, p.InputSizeBytes)
	}
}

func TestOutputsAreFileNamesOnlyAndDirtyCommitIsNotReproducible(t *testing.T) {
	p, _ := ParseVGT([]byte(sample), "run1")
	if p.OutputFiles["psi"] != "psi_hnv1.fits" {
		t.Errorf("%v", p.OutputFiles)
	}
	if p.OutputFiles["reliability"] != "passwd" { // a hostile relative path is reduced to a name next to the metadata
		t.Errorf("%v", p.OutputFiles)
	}
	if _, ok := p.OutputFiles["unknown"]; ok {
		t.Error("an unrecognised output key was accepted")
	}
	if !p.Dirty || p.Reproducible {
		t.Errorf("a dirty commit must not be reproducible from its hash: %+v", p.Run)
	}
	clean, _ := ParseVGT([]byte(strings.Replace(sample, "-dirty", "", 1)), "r")
	if clean.Dirty || !clean.Reproducible {
		t.Error("a clean commit should be reproducible")
	}
}

func TestRefusesNonMetadataAndOversizedInput(t *testing.T) {
	if _, err := ParseVGT([]byte("not toml ["), "x"); err == nil {
		t.Error("invalid TOML accepted")
	}
	if _, err := ParseVGT([]byte("a = 1\n"), "x"); err == nil {
		t.Error("an unrelated TOML file was accepted as a VGT run")
	}
	if _, err := ParseVGT(make([]byte, MaxMetadataBytes+1), "x"); err == nil {
		t.Error("oversized metadata accepted")
	}
}

// run_metadata.toml changed: the current VGT writes the project name "VGT" and no longer writes finalize_mask_mode, histogram_edge_mode,
// gradient_sample_mode or the noise exponents. The parser requires none of them, and an old file that has them keeps them as parameters.
func TestParserNeitherRequiresNorDropsTheRetiredKeys(t *testing.T) {
	const current = `generated_at_utc = "2026-11-10T09:21:44Z"
gitcommit = "bcbaed4ec9800ef95c561cfb4ad45c344743e469"
[config]
angle_frame = "galactic"
dn = 10
ker = 8
[outputs]
psi_path = "out/psi_demo.fits"
[project]
name = "VGT"
`
	p, err := ParseVGT([]byte(current), "run")
	if err != nil || p.OutputFiles["psi"] != "psi_demo.fits" || !p.Reproducible {
		t.Fatalf("current layout: %v %+v", err, p)
	}
	old := strings.Replace(current, `name = "VGT"`, `name = "Astronomy"`, 1)
	old = strings.Replace(old, "dn = 10", "dn = 10\nfinalize_mask_mode = \"keep\"\nhistogram_edge_mode = \"clip\"\ngradient_sample_mode = \"center\"\nnoise_exponent = 2", 1)
	q, err := ParseVGT([]byte(old), "run")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range q.Params {
		got[x.Key] = x.Value
	}
	if got["finalize_mask_mode"] != "keep" || got["noise_exponent"] != "2" || got["angle_frame"] != "galactic" {
		t.Errorf("an old file's keys are recorded as written: %v", got)
	}
}
