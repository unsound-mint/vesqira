package runs

import (
	"strings"
	"testing"
)

const rohsaNamelist = `&user_parameters
    filename = '/shared/home/x/full.bin'
    ,fileout = 'rohsa_n4.dat'
    ,timeout = 'timestep_n4.dat'
    ,n_gauss = 4
    ,lambda_amp = 10.0d0
    ,init_option = 'mean'
    ,noise = .true.
    /
`

func TestParseROHSADeclaresSettingsAndDropsLocations(t *testing.T) {
	p, err := ParseROHSA(nil, nil, []byte(rohsaNamelist), "rohsa_n4", "full")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range p.Params {
		got[x.Key] = x.Value
		if x.Source != "parameters" {
			t.Errorf("%s came from %s", x.Key, x.Source)
		}
	}
	if got["lambda_amp"] != "10.0d0" || got["init_option"] != "mean" || got["n_gauss"] != "4" {
		t.Errorf("settings as the file wrote them: %v", got)
	}
	for _, k := range []string{"filename", "fileout", "timeout"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s names a file on the fit's machine", k)
		}
	}
	if p.Info.NGauss != 4 || p.Info.NGaussBasis != "declared by the parameter file" {
		t.Errorf("n_gauss: %+v", p.Info)
	}
	if p.Info.Region != "full" || p.Info.RegionBasis != "inferred from the folder's name" {
		t.Errorf("a region the records do not name is the folder's, labelled inferred: %+v", p.Info)
	}
	if p.Kind != "rohsa" || p.GitCommit != "" || p.GeneratedAt != "" {
		t.Errorf("nothing about code version or time is invented: %+v", p.Run)
	}
}

func TestParseROHSAWithoutAGaussianCountSaysSo(t *testing.T) {
	p, err := ParseROHSA(nil, nil, nil, "rohsa_x", "full")
	if err != nil {
		t.Fatal(err)
	}
	if p.Info.NGauss != 0 || !strings.Contains(strings.Join(p.Info.Notes, " "), "not declared") {
		t.Errorf("%+v", p.Info)
	}
	if _, err := ParseROHSA([]byte("n_gauss = ["), nil, nil, "rohsa_x", "full"); err == nil {
		t.Error("a trace that is not TOML is refused")
	}
}

func TestProposeUnitIsAProposalFromTheName(t *testing.T) {
	for role, want := range map[string]string{"component_g2_mu_kms": "km/s", "component_g1_amp_k": "K", "model_nhi_cm2": "cm^-2", "residual_moment0_kkms": "K km/s"} {
		if u, basis, ok := ProposeUnit(role); !ok || u != want || !strings.Contains(basis, "proposed from the file name") {
			t.Errorf("%s: %q %q %v", role, u, basis, ok)
		}
	}
	if _, _, ok := ProposeUnit("broad_fraction"); ok {
		t.Error("no unit is invented for a name that does not say one")
	}
	if !SameUnit("cm-2", "cm^-2") || SameUnit("K", "km/s") {
		t.Error("unit comparison")
	}
}
