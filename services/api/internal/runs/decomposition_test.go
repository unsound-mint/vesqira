package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mapsProfile(t *testing.T, edit func(d map[string]any)) error {
	t.Helper()
	data, err := os.ReadFile("testdata/synthetic_maps_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	d := doc["profiles"].([]any)[0].(map[string]any)["decomposition"].(map[string]any)
	edit(d)
	b, _ := json.Marshal(doc)
	_, err = LoadProfiles(b)
	return err
}

func TestShippedROHSADeclaresItsReadingAndROHSALMCFollowsIt(t *testing.T) {
	reg := Shipped()
	d := reg.DecompositionOf(reg.ByName("ROHSA"))
	if d == nil || d.Source.Kind != SourceTable || d.Centre.Base == nil || *d.Centre.Base != 1 || d.Width.Kind != "sigma" || d.Width.Unit != "channel" {
		t.Fatalf("ROHSA's declaration: %+v", d)
	}
	if reg.DecompositionOf(reg.ByName("ROHSA-LMC")) != d {
		t.Error("ROHSA-LMC reads its components as ROHSA does")
	}
	if reg.DecompositionOf(reg.ByName("VGT.jl")) != nil {
		t.Error("VGT.jl has no components")
	}
	if b := d.Declaration()["centre"].(map[string]any)["base"]; b != 1 {
		t.Errorf("the 1-based mean is data sent to the worker: %v", b)
	}
}

func TestDecompositionDeclarationsThatClaimTooMuchAreRefused(t *testing.T) {
	for name, edit := range map[string]func(d map[string]any){
		"unknown field":          func(d map[string]any) { d["colour"] = "red" },
		"unknown width kind":     func(d map[string]any) { d["width"] = map[string]any{"kind": "variance"} },
		"base other than 0 or 1": func(d map[string]any) { d["centre"] = map[string]any{"unit": "channel", "base": 2} },
		"a cube with no axis":    func(d map[string]any) { d["source"].(map[string]any)["componentAxis"] = nil },
		"component axis 4":       func(d map[string]any) { d["source"].(map[string]any)["componentAxis"] = 4 },
		"a missing map":          func(d map[string]any) { delete(d["source"].(map[string]any)["maps"].(map[string]any), "width") },
		"maps need a crop key":   func(d map[string]any) { delete(d, "crop") },
		"a pattern without a group": func(d map[string]any) {
			d["source"].(map[string]any)["maps"].(map[string]any)["width"] = map[string]any{"pattern": "^w\\.fits$"}
		},
		"extver on a numbered hdu": func(d map[string]any) {
			d["source"].(map[string]any)["maps"].(map[string]any)["width"] = map[string]any{"file": "w.fits", "hdu": 1, "extver": 1}
		},
		"extver with no hdu": func(d map[string]any) {
			d["source"].(map[string]any)["maps"].(map[string]any)["width"] = map[string]any{"file": "w.fits", "extver": 1}
		},
		"extver below 1": func(d map[string]any) {
			d["source"].(map[string]any)["maps"].(map[string]any)["width"] = map[string]any{"file": "w.fits", "hdu": "W", "extver": 0}
		},
		"a file name with a folder": func(d map[string]any) {
			d["source"].(map[string]any)["maps"].(map[string]any)["width"] = map[string]any{"file": "../w.fits"}
		},
	} {
		if err := mapsProfile(t, edit); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := mapsProfile(t, func(map[string]any) {}); err != nil {
		t.Errorf("the synthetic profile itself: %v", err)
	}
}

func TestAnHDUNameAndItsExtverAreSentToTheWorker(t *testing.T) {
	data, _ := os.ReadFile("testdata/synthetic_maps_profile.json")
	reg, err := LoadProfiles(data)
	if err != nil {
		t.Fatal(err)
	}
	d := reg.DecompositionOf(reg.ByName("SynthDecomp-hdus"))
	maps := d.Declaration()["source"].(map[string]any)["maps"].(map[string]any)
	if got := maps["amplitude"].(map[string]any); got["hdu"] != "AMP" || got["extver"] != 1 {
		t.Errorf("amplitude: %v", got)
	}
	if got := maps["centre"].(map[string]any); got["hdu"] != "CENTRE" || got["extver"] != nil {
		t.Errorf("centre (no EXTVER declared, so none is sent): %v", got)
	}
}

func TestMapFilesAreNumberedFromOneWithoutGaps(t *testing.T) {
	data, _ := os.ReadFile("testdata/synthetic_maps_profile.json")
	reg, err := LoadProfiles(data)
	if err != nil {
		t.Fatal(err)
	}
	d := reg.ByName("SynthDecomp-maps").Decomposition
	names := []string{"synth_decomp_amp_g2.fits", "synth_decomp_amp_g1.fits", "synth_decomp_centre_g1.fits", "synth_decomp_centre_g2.fits",
		"synth_decomp_fwhm_g1.fits", "synth_decomp_fwhm_g2.fits", "synth_decomp_ncomp.fits"}
	files, err := d.MapFiles(names)
	if err != nil || strings.Join(files["amplitude"], ",") != "synth_decomp_amp_g1.fits,synth_decomp_amp_g2.fits" {
		t.Fatalf("%v %v", files, err)
	}
	if _, err := d.MapFiles(append(names[:5:5], "synth_decomp_fwhm_g3.fits")); err == nil {
		t.Error("a width map without its partner for every component was accepted")
	}
	if _, err := d.MapFiles([]string{"synth_decomp_amp_g1.fits", "synth_decomp_amp_g3.fits"}); err == nil {
		t.Error("a gap in the numbering was accepted")
	}
}
