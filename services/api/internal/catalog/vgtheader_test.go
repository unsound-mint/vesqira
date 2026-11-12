package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func hdr(psiframe, psiangle, bunit string) json.RawMessage {
	q := func(s string) string {
		if s == "" {
			return "null"
		}
		return `"` + s + `"`
	}
	return json.RawMessage(`{"bunit":` + q(bunit) + `,"radesys":"ICRS","axes":[{"ctype":"RA---TAN"}],"vgt":{"psiframe":` + q(psiframe) + `,"psiangle":` + q(psiangle) +
		`,"vgtprod":"psi","params":{"VGTDN":10,"VGTFIT":"axial_vonmises","VGTPATH":"/home/x/y"},"resolution":{"status":"not_established"}}}`)
}

func strp(s string) *string { return &s }

func TestHeaderFrameMapping(t *testing.T) {
	for _, c := range []struct {
		frame, frameWant string
		sky              string
	}{{"PIXEL", "pixel", ""}, {"GALACTIC", "sky", "galactic"}, {"icrs", "sky", "icrs"}} {
		o := VGTOf(hdr(c.frame, "BFIELD", "rad")).Orientation
		if o == nil || o.Frame != c.frameWant || (c.sky == "") != (o.SkyFrame == nil) || (o.SkyFrame != nil && *o.SkyFrame != c.sky) {
			t.Fatalf("%s: %+v", c.frame, o)
		}
		if o.Quantity == nil || *o.Quantity != "b_field" || o.Unit != "rad" || o.Basis["unit"] != "file" {
			t.Errorf("%s: %+v", c.frame, o)
		}
	}
}

func TestHeaderNeverFillsWhatItDoesNotSay(t *testing.T) {
	if v := VGTOf(hdr("", "", "rad")); v == nil || v.Orientation != nil {
		t.Fatalf("no PSIFRAME: no declaration, and not 'pixel' from the WCS: %+v", v)
	}
	v := VGTOf(hdr("ECLIPTIC", "BFIELD", "rad"))
	if v.Orientation != nil || len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "ECLIPTIC") {
		t.Fatalf("unknown frame: %+v", v)
	}
	if o := VGTOf(hdr("PIXEL", "POL", "rad")).Orientation; o == nil || o.Quantity != nil {
		t.Errorf("unknown PSIANGLE leaves the quantity undeclared: %+v", o)
	}
	if v := VGTOf(hdr("PIXEL", "BFIELD", "K")); v.Orientation != nil {
		t.Errorf("a BUNIT that is not an angle unit is not declared as an orientation map: %+v", v.Orientation)
	}
	if o := VGTOf(hdr("PIXEL", "BFIELD", "")).Orientation; o == nil || o.Unit != "rad" || o.Basis["unit"] != "convention" {
		t.Errorf("no BUNIT: the convention, labelled as such: %+v", o)
	}
	for _, p := range VGTOf(hdr("PIXEL", "BFIELD", "rad")).Params {
		if p.Key == "VGTPATH" {
			t.Errorf("a value that names a place is never kept: %+v", p)
		}
	}
}

func TestConflictsAreListedNotResolved(t *testing.T) {
	file := *VGTOf(hdr("GALACTIC", "BFIELD", "rad")).Orientation
	user := Interpretation{Kind: "axial_orientation", Unit: "deg", Frame: strp("pixel"), Quantity: strp("gradient"), DeclaredBy: "user"}
	if got := orientationConflicts(file, user, "icrs"); len(got) != 3 {
		t.Fatalf("frame, quantity and unit should each be listed: %q", got)
	}
	// sky in the grid's own frame agrees with a Galactic header only when the grid is Galactic
	same := Interpretation{Kind: "axial_orientation", Unit: "rad", Frame: strp("sky"), Quantity: strp("b_field"), DeclaredBy: "user"}
	if c := orientationConflicts(file, same, "galactic"); len(c) != 0 {
		t.Errorf("Galactic grid, Galactic header: %q", c)
	}
	if c := orientationConflicts(file, same, "icrs"); len(c) != 1 {
		t.Errorf("ICRS grid, Galactic header: %q", c)
	}
	if c := orientationConflicts(file, Interpretation{Kind: "axial_orientation", Unit: "rad"}, "icrs"); len(c) != 0 {
		t.Errorf("an undeclared frame and quantity is a gap, not a conflict: %q", c)
	}
}
