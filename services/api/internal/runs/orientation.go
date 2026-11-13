package runs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The orientation a profile declares for its tool's orientation output (ADR 0068, slice 6).
//
// A shipped profile names an entry of the worker's orientation_profiles.json ("VGT.jl"): the convention is read from the header cards
// that tool writes. A project profile cannot point into that table; it declares the convention itself, from the closed vocabulary
// below, for files that carry no such cards. What it declares is the researcher's statement, labelled by the project profile and
// never verified; a file's own cards that contradict it stop the import (server.checkDeclaredOrientation).

// OrientationDecl is a convention a project profile declares for the angles its tool writes.
type OrientationDecl struct {
	// Frame is "pixel" (from +axis 1 towards +axis 2 as stored) or "sky" (east of north in the grid's own celestial frame, as a
	// person's own declaration is).
	Frame string `json:"frame"`
	// Unit is "rad" or "deg".
	Unit string `json:"unit"`
	// Statistics is "axial" (0 and 180 degrees are the same orientation). "directional" is in the vocabulary so a profile can say it,
	// and is refused with a reason: this version has no directional angle interpretation, and reading such a map as axial would
	// change what it means.
	Statistics string `json:"statistics"`
	// Quantity is what the angle describes (b_field, gradient, dust_polarization, starlight_polarization, unspecified); optional.
	Quantity string `json:"quantity,omitempty"`
	// ZeroDirection and Sense restate where the angle starts and which way it grows. Vesqira has one convention per frame, so they are
	// optional and, when given, must be that one.
	ZeroDirection string `json:"zero_direction,omitempty"`
	Sense         string `json:"sense,omitempty"`
}

// OrientationRef is a profile's "orientation" field: the name of a worker convention (a string, shipped profiles) or a declared
// convention (an object, project profiles).
type OrientationRef struct {
	Worker string
	Decl   *OrientationDecl
}

func (o *OrientationRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &o.Worker)
	}
	var d OrientationDecl
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("orientation: %w", err)
	}
	o.Decl = &d
	return nil
}

func (o OrientationRef) MarshalJSON() ([]byte, error) {
	if o.Decl != nil {
		return json.Marshal(o.Decl)
	}
	return json.Marshal(o.Worker)
}

// frameConvention is the zero direction and sense Vesqira reads for each frame; the same words the worker's table uses.
var frameConvention = map[string][2]string{
	"pixel": {"+axis 1", "counter-clockwise"},
	"sky":   {"north", "east of north"},
}

var declQuantities = map[string]bool{"b_field": true, "gradient": true, "dust_polarization": true, "starlight_polarization": true, "unspecified": true}

// Check says what is wrong with a declared convention, in a sentence that names the field.
func (d OrientationDecl) Check() error {
	conv, ok := frameConvention[d.Frame]
	if !ok {
		return fmt.Errorf(`orientation.frame must be "pixel" or "sky", not %q`, d.Frame)
	}
	if d.Unit != "rad" && d.Unit != "deg" {
		return fmt.Errorf(`orientation.unit must be "rad" or "deg", not %q`, d.Unit)
	}
	switch d.Statistics {
	case "axial":
	case "directional":
		return fmt.Errorf("orientation.statistics is directional, which this version cannot carry: it reads angles only as axial orientations (0 and 180 degrees the same), and reading a directional map that way would change its meaning, so the profile is refused rather than the declaration dropped")
	default:
		return fmt.Errorf(`orientation.statistics must be "axial" (or "directional", which is refused), not %q`, d.Statistics)
	}
	if d.Quantity != "" && !declQuantities[d.Quantity] {
		return fmt.Errorf("orientation.quantity must be b_field, gradient, dust_polarization, starlight_polarization or unspecified, not %q", d.Quantity)
	}
	if d.ZeroDirection != "" && d.ZeroDirection != conv[0] {
		return fmt.Errorf("orientation.zero_direction for frame %s is %q here, not %q; another zero point cannot be read", d.Frame, conv[0], d.ZeroDirection)
	}
	if d.Sense != "" && d.Sense != conv[1] {
		return fmt.Errorf("orientation.sense for frame %s is %q here, not %q; another direction cannot be read", d.Frame, conv[1], d.Sense)
	}
	return nil
}

// Text is the convention in words, for the refusal that names it next to the file's own statement.
func (d OrientationDecl) Text() string {
	conv := frameConvention[d.Frame]
	where := "measured on the pixel grid, " + conv[1] + " from " + conv[0]
	if d.Frame == "sky" {
		where = "a position angle " + conv[1] + " in the grid's own frame"
	}
	s := where + ", in " + d.Unit + ", " + d.Statistics
	if d.Quantity != "" {
		s += ", the " + strings.ReplaceAll(d.Quantity, "_", " ")
	}
	return s
}
