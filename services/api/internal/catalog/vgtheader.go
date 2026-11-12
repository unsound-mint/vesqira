package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// What a product's FITS header says about itself (ADR 0014, amendment; ADR 0068, slice 2). VGT.jl writes PSIFRAME (the frame the angle is
// measured in), PSIANGLE (what the angle is), VGTPROD (the product's role), the run's parameters (VGT*) and the input cube's beam (INB*).
// The worker reports those cards verbatim under the header's "vgt" key, and what they declare of an orientation under "orientation",
// read through its profile table (services/scientific-worker/orientation_profiles.json: the one copy of which card means what).
// This file only carries that reading into Vesqira's declaration; a field the profile could not place is unresolved, never defaulted.

// FileHeaderAuthor is the author a declaration read from a file's header by the VGT.jl reading had before profiles were versioned.
// Stored rows keep it; IsFileHeaderAuthor recognises it and the profile labels.
const FileHeaderAuthor = "vgt-file-header"

// ProfileAuthorPrefix starts the author of a declaration a shipped orientation profile made: "profile:<name>@<version>", the file's own
// statement read by that profile's table. It is declared by the file's header, never verified.
const ProfileAuthorPrefix = "profile:"

// IsFileHeaderAuthor reports whether by names a declaration read from the file's own header: by a profile, or by the VGT.jl reading
// that came before profiles.
func IsFileHeaderAuthor(by string) bool {
	return by == FileHeaderAuthor || strings.HasPrefix(by, ProfileAuthorPrefix)
}

// HeaderParam is one keyword of the header as the file states it.
type HeaderParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// FileOrientation is the orientation declaration a file's header makes through a profile: nil when the profile cannot place the
// frame or the unit. Absence is never filled in from the WCS or from a convention.
type FileOrientation struct {
	// Profile is the label of the profile that read the header, "profile:<name>@<version>", and the author of a declaration adopted from it.
	Profile string `json:"profile"`
	Unit    string `json:"unit"` // "rad" or "deg"
	// Frame is "pixel" or "sky" (a position angle east of north in SkyFrame).
	Frame string `json:"frame"`
	// ZeroDirection and Sense say where the angle starts and which way it grows, as the profile documents them for this frame.
	ZeroDirection string `json:"zeroDirection"`
	Sense         string `json:"sense"`
	// Cards are the cards the fields were read from, as "CARD=value" as written, by field (frame, quantity, unit).
	Cards map[string]string `json:"cards"`
	// SkyFrame is the celestial frame a sky angle is stated in ("galactic" or "icrs"); nil for a pixel angle. It is the frame of the
	// angle, which may differ from the grid's own (VGT writes GALACTIC on an equatorial grid): every consumer carries the angle
	// through the sky from this frame into the grid's.
	SkyFrame *string `json:"skyFrame"`
	// Quantity is what the profile says the angle is (for VGT.jl "b_field" when PSIANGLE=BFIELD) and nil otherwise: a missing or
	// unknown card is not guessed.
	Quantity *string `json:"quantity"`
	// Basis says, for unit, frame and quantity, that the file states it ("file", read through the profile); a field the file does
	// not state is absent from the map.
	Basis map[string]string `json:"basis"`
}

// VGTHeader is what a dataset's header declares of a VGT product, for display and for the run import.
type VGTHeader struct {
	Product  *string `json:"product"`  // VGTPROD
	PsiFrame *string `json:"psiFrame"` // PSIFRAME as written
	PsiAngle *string `json:"psiAngle"` // PSIANGLE as written
	// Profile is the label of the orientation profile that read the header ("profile:VGT.jl@1"); nil when the header was stored before profiles.
	Profile *string `json:"profile"`
	// Unresolved lists what the profile could not place among frame, quantity and unit. An orientation map with unresolved fields still
	// displays; an analysis that needs one says so and asks for it to be declared.
	Unresolved []string `json:"unresolved"`
	// Params are the run's VGT* keywords and the input beam, for a file imported without a run_metadata.toml: declared by the file.
	Params []HeaderParam `json:"params"`
	// Resolution is the worker's reading of the keywords' resolution: approximate and declared, or "not_established" with the reasons.
	Resolution  json.RawMessage  `json:"resolution"`
	Orientation *FileOrientation `json:"orientation"`
	// Notes say why part of the header was not used (an unknown PSIFRAME or PSIANGLE, a BUNIT that is not an angle unit).
	Notes []string `json:"notes"`
	// Conflicts are the places where the declaration in force differs from the header, in full sentences. Nothing is resolved
	// silently: the declaration in force stays in use and the header's is shown beside it.
	Conflicts []string `json:"conflicts"`
}

type headerCards struct {
	VGT *struct {
		PsiFrame   *string         `json:"psiframe"`
		PsiAngle   *string         `json:"psiangle"`
		VgtProd    *string         `json:"vgtprod"`
		Params     map[string]any  `json:"params"`
		Resolution json.RawMessage `json:"resolution"`
	} `json:"vgt"`
	// Orientation is the worker's reading of the header through its profile table (orientation_profiles.py).
	Orientation *struct {
		Label         string            `json:"label"`
		Frame         *string           `json:"frame"`
		SkyFrame      *string           `json:"sky_frame"`
		ZeroDirection *string           `json:"zero_direction"`
		Sense         *string           `json:"sense"`
		Quantity      *string           `json:"quantity"`
		Unit          *string           `json:"unit"`
		From          map[string]string `json:"from"`
		Unresolved    []string          `json:"unresolved"`
		Notes         []string          `json:"notes"`
	} `json:"orientation"`
	Bunit *string `json:"bunit"`
	Axes  []struct {
		Ctype *string `json:"ctype"`
	} `json:"axes"`
	Radesys *string `json:"radesys"`
}

// VGTOf reads what a dataset's stored header declares of a profile's product (nil when it has none). Conflicts are filled by the
// caller that knows the declaration in force (Dataset.attachVGT).
func VGTOf(header json.RawMessage) *VGTHeader {
	if !bytes.Contains(header, []byte(`"psiframe"`)) {
		return nil
	}
	var h headerCards
	if json.Unmarshal(header, &h) != nil || h.VGT == nil {
		return nil
	}
	v := &VGTHeader{Product: h.VGT.VgtProd, PsiFrame: h.VGT.PsiFrame, PsiAngle: h.VGT.PsiAngle, Resolution: h.VGT.Resolution, Params: []HeaderParam{}, Unresolved: []string{}, Notes: []string{}, Conflicts: []string{}}
	keys := make([]string, 0, len(h.VGT.Params))
	for k := range h.VGT.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var s string
		switch x := h.VGT.Params[k].(type) {
		case string:
			s = x
		case float64:
			s = fmt.Sprint(x)
		default:
			continue
		}
		// the same rule as a run's parameters: nothing that names a place on a machine reaches a view, a figure or a reviewer
		if strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, "~") {
			continue
		}
		v.Params = append(v.Params, HeaderParam{Key: k, Value: s})
	}
	p := h.Orientation
	if p == nil {
		// stored before orientation profiles: the cards are there but nothing was read from them, and nothing is guessed here
		v.Unresolved = []string{"frame", "quantity", "unit"}
		v.Notes = append(v.Notes, "this header was stored before orientation profiles existed, so what it declares has not been read; register the file again")
		return v
	}
	v.Profile = &p.Label
	v.Unresolved = append(v.Unresolved, p.Unresolved...)
	v.Notes = append(v.Notes, p.Notes...)
	if p.Frame == nil {
		return v // no frame is stated that the profile knows, and none is inferred from the WCS
	}
	if p.Unit == nil {
		if !hasNote(v.Notes, "the unit is unresolved") {
			v.Notes = append(v.Notes, "the header states no angle unit the profile "+p.Label+" places, so the unit is unresolved and the header does not declare this as an orientation map")
		}
		return v
	}
	o := &FileOrientation{Profile: p.Label, Unit: *p.Unit, Frame: *p.Frame, SkyFrame: p.SkyFrame, Quantity: p.Quantity, Cards: p.From,
		Basis: map[string]string{"frame": "file", "unit": "file"}}
	if p.ZeroDirection != nil {
		o.ZeroDirection = *p.ZeroDirection
	}
	if p.Sense != nil {
		o.Sense = *p.Sense
	}
	if p.Quantity != nil {
		o.Basis["quantity"] = "file"
	}
	v.Orientation = o
	return v
}

func hasNote(notes []string, part string) bool {
	for _, n := range notes {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

func ptr(s string) *string { return &s }

// gridFrame names the celestial frame a header's grid is in from its CTYPE and RADESYS, for deciding whether an angle stated in a
// frame is stated in the grid's own: "galactic", "icrs" (RA/Dec in ICRS or FK5, which differ by less than a tenth of an arcsecond) or "".
func gridFrame(header json.RawMessage) string {
	var h headerCards
	if json.Unmarshal(header, &h) != nil || len(h.Axes) < 1 || h.Axes[0].Ctype == nil {
		return ""
	}
	ct := strings.ToUpper(*h.Axes[0].Ctype)
	rs := ""
	if h.Radesys != nil {
		rs = strings.ToUpper(*h.Radesys)
	}
	switch {
	case strings.HasPrefix(ct, "GLON"):
		return "galactic"
	case strings.HasPrefix(ct, "RA--") && (rs == "" || rs == "ICRS" || rs == "FK5"):
		return "icrs"
	}
	return ""
}

var skyNames = map[string]string{"galactic": "Galactic", "icrs": "ICRS"}

// SkyFrameName is a celestial frame in words, as figures and notices state it.
func SkyFrameName(f string) string {
	if n, ok := skyNames[f]; ok {
		return n
	}
	return f
}

// Text is the declaration in words, for notices.
func (o FileOrientation) Text() string {
	card := o.Cards["frame"]
	if o.SkyFrame != nil {
		return "a position angle " + o.Sense + " in the " + SkyFrameName(*o.SkyFrame) + " frame (" + card + ")"
	}
	return "measured on the pixel grid, " + o.Sense + " from " + o.ZeroDirection + " (" + card + ")"
}

// attachVGT fills d.VGT from the stored header and, when a declaration other than the file's is in force, lists where they differ.
func (d *Dataset) attachVGT() {
	d.VGT = VGTOf(d.Header)
	if d.VGT == nil || d.VGT.Orientation == nil || d.Interpretation == nil || d.Interpretation.Kind != "axial_orientation" || IsFileHeaderAuthor(d.Interpretation.DeclaredBy) {
		return
	}
	d.VGT.Conflicts = orientationConflicts(*d.VGT.Orientation, *d.Interpretation, gridFrame(d.Header))
}

func orientationConflicts(file FileOrientation, in Interpretation, grid string) []string {
	var out []string
	if in.Frame != nil {
		switch {
		case file.Frame == "pixel" && *in.Frame == "sky":
			out = append(out, "the header states the angle "+file.Text()+"; the declaration in force says it is east of north on the sky")
		case file.Frame == "sky" && *in.Frame == "pixel":
			out = append(out, "the header states the angle as "+file.Text()+"; the declaration in force says it is measured on the pixel grid")
		case file.Frame == "sky" && *in.Frame == "sky" && (grid == "" || grid != *file.SkyFrame):
			out = append(out, "the header states the angle as "+file.Text()+", which is not this grid's own frame; the declaration in force says east of north in the grid's own frame, "+
				"which differs from "+SkyFrameName(*file.SkyFrame)+" north by an angle that changes across the map")
		}
	}
	if file.Quantity != nil && in.Quantity != nil && *file.Quantity != *in.Quantity {
		out = append(out, "the header says what the angle is ("+file.Cards["quantity"]+", the "+strings.ReplaceAll(*file.Quantity, "_", " ")+"); the declaration in force says "+strings.ReplaceAll(*in.Quantity, "_", " "))
	}
	if file.Basis["unit"] == "file" && file.Unit != in.Unit {
		out = append(out, "the header's "+strings.Replace(file.Cards["unit"], "=", " says ", 1)+" ("+file.Unit+"); the declaration in force says "+in.Unit)
	}
	return out
}

// applyFileSkyFrame sets the computed sky frame of an interpretation the file's header made, from the dataset's stored header. When
// the header no longer says what the stored declaration claims (it was replaced, or the file changed) the interpretation is marked
// stale: a sky angle whose frame cannot be established must not be read as the grid's own.
func applyFileSkyFrame(in *Interpretation, d Dataset) {
	if in == nil || !IsFileHeaderAuthor(in.DeclaredBy) {
		return
	}
	v := VGTOf(d.Header)
	if v == nil || v.Orientation == nil || in.Frame == nil || *in.Frame != v.Orientation.Frame {
		in.Stale = true
		return
	}
	in.SkyFrame = v.Orientation.SkyFrame
}
