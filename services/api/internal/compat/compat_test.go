package compat

import (
	"encoding/json"
	"strings"
	"testing"
)

func hdr(mut func(m map[string]any)) json.RawMessage {
	ax := func(ct string, crval, cdelt float64) map[string]any {
		return map[string]any{"length": 4, "ctype": ct, "cunit": "deg", "crval": crval, "crpix": 2.5, "cdelt": cdelt}
	}
	m := map[string]any{"axes": []any{ax("RA---TAN", 150, -0.01), ax("DEC--TAN", 2, 0.01)}, "radesys": "ICRS"}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestMissingUnitOnBothSidesIsNotAMatch(t *testing.T) {
	drop := func(m map[string]any) { delete(m["axes"].([]any)[0].(map[string]any), "cunit") }
	if w, _ := Grids(hdr(drop), hdr(drop)); len(w) == 0 {
		t.Fatal("an unresolved unit on both sides must not count as equal")
	}
}

func TestIdenticalGridsAreCompatible(t *testing.T) {
	if w, err := Grids(hdr(nil), hdr(nil)); err != nil || len(w) != 0 {
		t.Fatalf("%v %v", w, err)
	}
}

func TestEveryDifferenceIsReported(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"length":   func(m map[string]any) { m["axes"].([]any)[0].(map[string]any)["length"] = 5 },
		"type":     func(m map[string]any) { m["axes"].([]any)[0].(map[string]any)["ctype"] = "GLON-TAN" },
		"step":     func(m map[string]any) { m["axes"].([]any)[1].(map[string]any)["cdelt"] = 0.02 },
		"frame":    func(m map[string]any) { m["radesys"] = "FK5" },
		"unknown":  func(m map[string]any) { delete(m["axes"].([]any)[0].(map[string]any), "crval") },
		"noframe":  func(m map[string]any) { delete(m, "radesys") },
		"rotation": func(m map[string]any) { m["wcs_extra"] = map[string]any{"PC1_2": 0.1} },
		"nounit":   func(m map[string]any) { delete(m["axes"].([]any)[0].(map[string]any), "cunit") },
	}
	for name, mut := range cases {
		w, err := Grids(hdr(nil), hdr(mut))
		if err != nil || len(w) == 0 {
			t.Errorf("%s: not reported (%v %v)", name, w, err)
		}
	}
}

func TestUnresolvedOnBothSidesIsNotAMatch(t *testing.T) {
	drop := func(m map[string]any) { delete(m["axes"].([]any)[0].(map[string]any), "crval") }
	w, _ := Grids(hdr(drop), hdr(drop))
	if len(w) == 0 || !strings.Contains(strings.Join(w, ";"), "unresolved") {
		t.Fatalf("%v", w)
	}
}

func cubeHdr(ctype string, specsys any) json.RawMessage {
	return hdr(func(m map[string]any) {
		m["axes"] = append(m["axes"].([]any), map[string]any{"length": 4, "ctype": ctype, "cunit": "m/s", "crval": 0, "crpix": 1, "cdelt": 1000})
		if specsys != nil {
			m["specsys"] = specsys
		}
	})
}

func TestSpectralFramesGateChannelLinking(t *testing.T) {
	cases := []struct {
		name   string
		a, b   json.RawMessage
		ok     bool
		reason string
	}{
		{"no spectral axis", hdr(nil), hdr(nil), true, ""},
		{"one spectral axis only", cubeHdr("VRAD", "LSRK"), hdr(nil), false, "only one file"},
		{"same frame, case and space ignored", cubeHdr("VRAD", "LSRK"), cubeHdr("VRAD", " lsrk "), true, ""},
		{"frames differ", cubeHdr("VRAD", "LSRK"), cubeHdr("VRAD", "TOPOCENT"), false, "spectral frames differ (LSRK vs TOPOCENT)"},
		{"undeclared on both", cubeHdr("VRAD", nil), cubeHdr("VRAD", nil), false, "not declared in either file"},
		{"undeclared on A", cubeHdr("VRAD", nil), cubeHdr("VRAD", "LSRK"), false, "not declared in file A"},
		{"undeclared on B", cubeHdr("VRAD", "LSRK"), cubeHdr("VRAD", nil), false, "not declared in file B"},
		{"empty SPECSYS is undeclared", cubeHdr("VRAD", ""), cubeHdr("VRAD", "LSRK"), false, "not declared in file A"},
		{"same legacy suffix, no SPECSYS", cubeHdr("VELO-LSR", nil), cubeHdr("VELO-LSR", nil), true, ""},
		{"different legacy suffixes", cubeHdr("VELO-LSR", nil), cubeHdr("VELO-HEL", nil), false, "spectral frames differ"},
		{"legacy suffix against SPECSYS", cubeHdr("VELO-LSR", nil), cubeHdr("VELO-LSR", "LSRK"), false, "spectral frames differ"},
		{"SPECSYS wins over a conflicting legacy suffix", cubeHdr("VELO-LSR", "TOPOCENT"), cubeHdr("VELO-HEL", "TOPOCENT"), true, ""},
		{"frequency axis", cubeHdr("FREQ", "LSRK"), cubeHdr("FREQ", "BARYCENT"), false, "(LSRK vs BARYCENT)"},
	}
	for _, c := range cases {
		ok, why := SpectralFrames(c.a, c.b)
		if ok != c.ok || !strings.Contains(why, c.reason) || (ok && why != "") {
			t.Errorf("%s: ok=%v reason=%q, want ok=%v containing %q", c.name, ok, why, c.ok, c.reason)
		}
	}
	if ok, why := SpectralFrames(json.RawMessage("{"), hdr(nil)); ok || why == "" {
		t.Error("an unreadable header must not link channels")
	}
}
