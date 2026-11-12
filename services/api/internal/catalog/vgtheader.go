package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// What a VGT product's FITS header says about itself (ADR 0014, amendment). VGT.jl writes PSIFRAME (the frame the angle is measured
// in), PSIANGLE (what the angle is), VGTPROD (the product's role), the run's parameters (VGT*) and the input cube's beam (INB*).
// The worker reports those cards verbatim under the header's "vgt" key; this file is the one place that maps them onto Vesqira's
// orientation declaration, so the mapping has one definition and one version (adapterMappings).

// FileHeaderAuthor labels an orientation declaration read from the file's own header. It is the file's statement, not the
// researcher's and not an adapter's assumption.
const FileHeaderAuthor = "vgt-file-header"

// HeaderParam is one keyword of the header as the file states it.
type HeaderParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// FileOrientation is the orientation declaration a file's header makes: nil when PSIFRAME is absent or not one Vesqira knows.
// Absence is never filled in from the WCS or from a convention here; the run adapter's convention is the caller's fallback.
type FileOrientation struct {
	Unit string `json:"unit"` // "rad" or "deg"
	// Frame is "pixel" (PSIFRAME=PIXEL) or "sky" (GALACTIC or ICRS).
	Frame string `json:"frame"`
	// SkyFrame is the celestial frame a sky angle is stated in ("galactic" or "icrs"); nil for a pixel angle. It is the frame of the
	// angle, which may differ from the grid's own (VGT writes GALACTIC on an equatorial grid): every consumer carries the angle
	// through the sky from this frame into the grid's.
	SkyFrame *string `json:"skyFrame"`
	// Quantity is "b_field" when PSIANGLE=BFIELD and nil otherwise: a missing or unknown PSIANGLE is not guessed.
	Quantity *string `json:"quantity"`
	// Basis says, for unit, frame and quantity, whether the file states it ("file"), the run adapter's convention supplies it
	// ("convention": only the unit, when BUNIT does not state rad or deg) or it is not declared ("").
	Basis map[string]string `json:"basis"`
}

// VGTHeader is what a dataset's header declares of a VGT product, for display and for the run import.
type VGTHeader struct {
	Product  *string `json:"product"`  // VGTPROD
	PsiFrame *string `json:"psiFrame"` // PSIFRAME as written
	PsiAngle *string `json:"psiAngle"` // PSIANGLE as written
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
	Bunit *string `json:"bunit"`
	Axes  []struct {
		Ctype *string `json:"ctype"`
	} `json:"axes"`
	Radesys *string `json:"radesys"`
}

// VGTOf reads the VGT keywords out of a dataset's stored header (nil when it has none). Conflicts are filled by the caller that
// knows the declaration in force (Dataset.attachVGT).
func VGTOf(header json.RawMessage) *VGTHeader {
	if !bytes.Contains(header, []byte(`"psiframe"`)) {
		return nil
	}
	var h headerCards
	if json.Unmarshal(header, &h) != nil || h.VGT == nil {
		return nil
	}
	v := &VGTHeader{Product: h.VGT.VgtProd, PsiFrame: h.VGT.PsiFrame, PsiAngle: h.VGT.PsiAngle, Resolution: h.VGT.Resolution, Params: []HeaderParam{}, Notes: []string{}, Conflicts: []string{}}
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
	if h.VGT.PsiFrame == nil {
		return v // an older output: no frame is stated, and none is inferred from the WCS
	}
	o := &FileOrientation{Basis: map[string]string{}}
	switch strings.ToUpper(strings.TrimSpace(*h.VGT.PsiFrame)) {
	case "PIXEL":
		o.Frame = "pixel"
	case "GALACTIC":
		o.Frame, o.SkyFrame = "sky", ptr("galactic")
	case "ICRS":
		o.Frame, o.SkyFrame = "sky", ptr("icrs")
	default:
		v.Notes = append(v.Notes, fmt.Sprintf("PSIFRAME is %q, a frame Vesqira does not know (PIXEL, GALACTIC or ICRS), so the header's frame is not used", *h.VGT.PsiFrame))
		return v
	}
	o.Basis["frame"] = "file"
	if h.VGT.PsiAngle != nil {
		if strings.ToUpper(strings.TrimSpace(*h.VGT.PsiAngle)) == "BFIELD" {
			o.Quantity, o.Basis["quantity"] = ptr("b_field"), "file"
		} else {
			v.Notes = append(v.Notes, fmt.Sprintf("PSIANGLE is %q, not BFIELD, so what the angle is stays undeclared", *h.VGT.PsiAngle))
		}
	}
	bu := ""
	if h.Bunit != nil {
		bu = strings.ToLower(strings.TrimSpace(*h.Bunit))
	}
	switch {
	case bu == "rad" || bu == "deg":
		o.Unit, o.Basis["unit"] = bu, "file"
	case bu != "":
		v.Notes = append(v.Notes, fmt.Sprintf("BUNIT is %q, not an angle unit (rad or deg), so the header does not declare this as an orientation map", *h.Bunit))
		return v
	default:
		o.Unit, o.Basis["unit"] = "rad", "convention" // VGT's psi is radians; a file that states no unit is not guessed from its values
	}
	v.Orientation = o
	return v
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
	where := "measured on the pixel grid, counter-clockwise from +axis 1 (PSIFRAME=PIXEL)"
	if o.SkyFrame != nil {
		where = "a position angle east of north in the " + SkyFrameName(*o.SkyFrame) + " frame (PSIFRAME=" + strings.ToUpper(*o.SkyFrame) + ")"
	}
	return where
}

// attachVGT fills d.VGT from the stored header and, when a declaration other than the file's is in force, lists where they differ.
func (d *Dataset) attachVGT() {
	d.VGT = VGTOf(d.Header)
	if d.VGT == nil || d.VGT.Orientation == nil || d.Interpretation == nil || d.Interpretation.Kind != "axial_orientation" || d.Interpretation.DeclaredBy == FileHeaderAuthor {
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
		out = append(out, "the header says the angle is the B-field (PSIANGLE=BFIELD); the declaration in force says "+strings.ReplaceAll(*in.Quantity, "_", " "))
	}
	if file.Basis["unit"] == "file" && file.Unit != in.Unit {
		out = append(out, "the header's BUNIT says "+file.Unit+"; the declaration in force says "+in.Unit)
	}
	return out
}

// applyFileSkyFrame sets the computed sky frame of an interpretation the file's header made, from the dataset's stored header. When
// the header no longer says what the stored declaration claims (it was replaced, or the file changed) the interpretation is marked
// stale: a sky angle whose frame cannot be established must not be read as the grid's own.
func applyFileSkyFrame(in *Interpretation, d Dataset) {
	if in == nil || in.DeclaredBy != FileHeaderAuthor {
		return
	}
	v := VGTOf(d.Header)
	if v == nil || v.Orientation == nil || in.Frame == nil || *in.Frame != v.Orientation.Frame {
		in.Stale = true
		return
	}
	in.SkyFrame = v.Orientation.SkyFrame
}
