package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// hdr is a stored header as the worker writes it for a VGT.jl product: the cards under "vgt", and what the VGT.jl profile placed under
// "orientation" (the table itself lives in the worker and is tested there; this only shapes its report).
func hdr(psiframe, psiangle, bunit string) json.RawMessage {
	q := func(s string) string {
		if s == "" {
			return "null"
		}
		return `"` + s + `"`
	}
	frame, sky, quantity, unit := "", "", "", ""
	var unresolved, notes []string
	switch strings.ToUpper(psiframe) {
	case "PIXEL":
		frame = "pixel"
	case "GALACTIC", "ICRS":
		frame, sky = "sky", strings.ToLower(psiframe)
	default:
		unresolved = append(unresolved, "frame")
		if psiframe != "" {
			notes = append(notes, "PSIFRAME is '"+psiframe+"', which profile profile:VGT.jl@1 does not place, so the frame is unresolved")
		}
	}
	if strings.ToUpper(psiangle) == "BFIELD" {
		quantity = "b_field"
	} else {
		unresolved = append(unresolved, "quantity")
	}
	if b := strings.ToLower(bunit); b == "rad" || b == "deg" {
		unit = b
	} else {
		unresolved = append(unresolved, "unit")
		if bunit != "" {
			notes = append(notes, "BUNIT is '"+bunit+"', which profile profile:VGT.jl@1 does not place, so the unit is unresolved")
		}
	}
	list := func(x []string) string { b, _ := json.Marshal(append([]string{}, x...)); return string(b) }
	from := `{"frame":"PSIFRAME=` + psiframe + `","quantity":"PSIANGLE=` + psiangle + `","unit":"BUNIT=` + bunit + `"}`
	sense, zero := "east of north", "north"
	if frame == "pixel" {
		sense, zero = "counter-clockwise", "+axis 1"
	}
	return json.RawMessage(`{"bunit":` + q(bunit) + `,"radesys":"ICRS","axes":[{"ctype":"RA---TAN"}],"vgt":{"psiframe":` + q(psiframe) + `,"psiangle":` + q(psiangle) +
		`,"vgtprod":"psi","params":{"VGTDN":10,"VGTFIT":"axial_vonmises","VGTPATH":"/home/x/y"},"resolution":{"status":"not_established"}},` +
		`"orientation":{"label":"profile:VGT.jl@1","frame":` + q(frame) + `,"sky_frame":` + q(sky) + `,"zero_direction":` + q(zero) + `,"sense":` + q(sense) + `,"quantity":` + q(quantity) +
		`,"unit":` + q(unit) + `,"from":` + from + `,"unresolved":` + list(unresolved) + `,"notes":` + list(notes) + `}}`)
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
	if v := VGTOf(hdr("", "", "rad")); v == nil || v.Orientation != nil || strings.Join(v.Unresolved, ",") != "frame,quantity" {
		t.Fatalf("no PSIFRAME: no declaration, and not 'pixel' from the WCS: %+v", v)
	}
	if v := VGTOf(json.RawMessage(`{"vgt":{"psiframe":"PIXEL","psiangle":"BFIELD","params":{}}}`)); v == nil || v.Orientation != nil || v.Profile != nil ||
		strings.Join(v.Unresolved, ",") != "frame,quantity,unit" || len(v.Notes) != 1 {
		t.Fatalf("a header stored before profiles is read as unresolved, with the reason: %+v", v)
	}
	if o := VGTOf(hdr("GALACTIC", "BFIELD", "rad")).Orientation; o == nil || o.Profile != "profile:VGT.jl@1" || o.Cards["frame"] != "PSIFRAME=GALACTIC" {
		t.Fatalf("the declaration names its profile and the card it came from: %+v", o)
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
	if v := VGTOf(hdr("PIXEL", "BFIELD", "")); v.Orientation != nil || len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "unit is unresolved") {
		t.Errorf("no BUNIT: the unit is unresolved, not rad by the tool's habit: %+v", v)
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
