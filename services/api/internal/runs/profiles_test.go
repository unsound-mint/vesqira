package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedProfilesLoadAndNameTheThreeTools(t *testing.T) {
	reg := Shipped()
	for _, label := range []string{"VGT.jl@1", "ROHSA@1", "ROHSA-LMC@1"} {
		if reg.ByLabel(label) == nil {
			t.Errorf("no profile %s", label)
		}
	}
	for _, role := range []string{"orientation", "reliability", "support", "component_table", "component_maps", "model_cube", "residual_cube", "crop"} {
		if !reg.IsGeneric(role) {
			t.Errorf("%s is not a generic role", role)
		}
	}
	// every record is recognised by content, never by its name alone
	for _, p := range reg.All() {
		if p.Record != nil && len(p.Record.AnyKey) == 0 {
			t.Errorf("%s: a record without a key", p.Label())
		}
	}
}

// The run profile names the orientation profile that reads its tool's header cards. One name in two files is the seam until they
// are one file (ADR 0068): this keeps the pair from drifting.
func TestEveryOrientationNameIsAProfileOfTheWorker(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "scientific-worker", "orientation_profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Profiles []struct{ Name, Version string }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, p := range doc.Profiles {
		have[p.Name+"@"+p.Version] = true
	}
	for _, p := range Shipped().All() {
		if p.Orientation != "" && !have[p.Orientation+"@"+p.Version] {
			t.Errorf("%s names the orientation profile %s@%s, which the worker's table does not have", p.Label(), p.Orientation, p.Version)
		}
	}
}

func TestProfilesAreRefusedWhenTheyDeclareMoreThanTheyMay(t *testing.T) {
	base := func(p string) string {
		return `{"format":1,"roles":{"orientation":"Orientation map"},"profiles":[` + p + `]}`
	}
	good := `{"name":"T","version":"1","kind":"t","record":{"file":"t.toml","reader":"run_metadata","anyKey":["a"]},"outputs":{"table":"o","files":{"k":{"product":"p","role":"orientation"}}}}`
	if _, err := LoadProfiles([]byte(base(good))); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"no key":           strings.Replace(good, `"anyKey":["a"]`, `"anyKey":[]`, 1),
		"unknown reader":   strings.Replace(good, `run_metadata`, `shell`, 1),
		"unknown role":     strings.Replace(good, `"role":"orientation"`, `"role":"invented"`, 1),
		"label in a name":  strings.Replace(good, `"name":"T"`, `"name":"T@2"`, 1),
		"bad pattern":      strings.Replace(good, `"file":"t.toml"`, `"pattern":"("`, 1),
		"an unknown field": strings.Replace(good, `"kind":"t"`, `"kind":"t","command":"rm -rf"`, 1),
	} {
		if _, err := LoadProfiles([]byte(base(p))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	extra := strings.Replace(good, `"role":"orientation"`, `"role":"my_extra","extra":"My extra"`, 1)
	if _, err := LoadProfiles([]byte(base(extra))); err != nil {
		t.Errorf("a labelled extra role is allowed: %v", err)
	}
}

func TestAFileCannotRedefineAShippedProfile(t *testing.T) {
	dup := `{"format":1,"roles":{"orientation":"Orientation map"},"profiles":[{"name":"VGT.jl","version":"1","kind":"vgt","record":{"file":"x.toml","reader":"run_metadata","anyKey":["a"]}}]}`
	if _, err := Shipped().With([]byte(dup)); err == nil {
		t.Error("VGT.jl@1 was redefined")
	}
}

func TestClaimsReadsContentNotTheName(t *testing.T) {
	vgt := Shipped().ByName("VGT.jl")
	if !vgt.Claims([]byte("gitcommit = \"abc\"\n")) || !vgt.Claims([]byte("[outputs]\npsi_path = \"a\"\n")) {
		t.Error("a VGT.jl record with its keys is VGT.jl's")
	}
	if vgt.Claims([]byte("unrelated = 1\n[other]\nx = 2\n")) {
		t.Error("run_metadata.toml with none of the keys is not VGT.jl's")
	}
	if !vgt.Claims([]byte("not = [valid")) {
		t.Error("an unreadable record is claimed, so the reader can say why it cannot read it")
	}
}

func TestVGTRecordMapsItsOutputsToGenericRolesKeepingTheProduct(t *testing.T) {
	p, err := ParseVGT([]byte("gitcommit = \"abc\"\n[outputs]\npsi_path = \"/x/psi.fits\"\nreliability_path = \"rel.fits\"\nsupport_path = \"s.fits\"\nfit_rbar2_path = \"f.fits\"\n"), "run")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"psi": "orientation", "reliability": "reliability", "support": "support", "fit_rbar2": "fit_rbar2"}
	for product, role := range want {
		if p.OutputRoles[product] != role || p.OutputFiles[product] == "" {
			t.Errorf("%s: role %q file %q, want %s", product, p.OutputRoles[product], p.OutputFiles[product], role)
		}
	}
	if p.Profile != "VGT.jl@1" || p.Kind != "vgt" {
		t.Errorf("profile %q kind %q", p.Profile, p.Kind)
	}
	if Shipped().ByName("VGT.jl").ExtraLabel("fit_rbar2") == "" || Shipped().ByName("VGT.jl").ExtraLabel("orientation") != "" {
		t.Error("fit_rbar2 is the one declared extra of VGT.jl")
	}
}

func TestROHSAProductsKeepAnUnplacedNameInsteadOfAGuessedRole(t *testing.T) {
	p := Shipped().ByName("ROHSA-LMC")
	for product, want := range map[string]string{
		"component_g1_mu_kms":   "component_maps",
		"component_g4_amp_k":    "component_maps",
		"model_nhi_cm2":         "model_nhi_cm2", // a map the profile does not place: its own name, a declared extra
		"residual_moment0_kkms": "residual_moment0_kkms",
	} {
		if got := p.FolderRole("maps", product); got != want {
			t.Errorf("maps/%s: %q, want %q", product, got, want)
		}
	}
	if p.FolderRole("cubes", "cube_model") != "model_cube" || p.FolderRole("cubes", "cube_other") != "cube_other" {
		t.Error("cubes: only the model cube is placed")
	}
	if name, ok := p.FileName(RoleCrop, map[string]string{"region": "full"}); !ok || name != "full.fits" {
		t.Errorf("crop: %q", name)
	}
}
