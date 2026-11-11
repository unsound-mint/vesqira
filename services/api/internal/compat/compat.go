// Package compat decides whether two registered maps sit on the same grid. Two measurements are never merged,
// overlaid or used as each other's weights unless this says they are compatible; every reason is reported.
package compat

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

type axis struct {
	Length int      `json:"length"`
	CType  *string  `json:"ctype"`
	CUnit  *string  `json:"cunit"`
	CRVal  *float64 `json:"crval"`
	CRPix  *float64 `json:"crpix"`
	CDelt  *float64 `json:"cdelt"`
}

type header struct {
	Axes   []axis             `json:"axes"`
	RadeSy *string            `json:"radesys"`
	SpecSy *string            `json:"specsys"`
	Extra  map[string]float64 `json:"wcs_extra"` // PC/CD/CROTA/LONPOLE/LATPOLE/EQUINOX as declared
}

func same(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// Grids compares the declared headers of two datasets and returns the reasons they are not known to share a
// grid. An empty result means shape, axis types, units, reference values, reference pixels, steps and the
// celestial frame all match. A value unresolved on either side is a reason, never a match.
func Grids(a, b json.RawMessage) ([]string, error) {
	var ha, hb header
	if err := json.Unmarshal(a, &ha); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &hb); err != nil {
		return nil, err
	}
	var why []string
	if len(ha.Axes) != len(hb.Axes) {
		return []string{fmt.Sprintf("the files have %d and %d axes", len(ha.Axes), len(hb.Axes))}, nil
	}
	for i := range ha.Axes {
		x, y := ha.Axes[i], hb.Axes[i]
		n := i + 1
		if x.Length != y.Length {
			why = append(why, fmt.Sprintf("axis %d length differs (%d and %d)", n, x.Length, y.Length))
			continue
		}
		if x.CType == nil || y.CType == nil || *x.CType != *y.CType {
			why = append(why, fmt.Sprintf("axis %d type differs or is unresolved", n))
		}
		if x.CUnit == nil || y.CUnit == nil || *x.CUnit != *y.CUnit {
			why = append(why, fmt.Sprintf("axis %d unit differs or is unresolved", n))
		}
		for _, f := range []struct {
			name string
			a, b *float64
		}{{"reference value", x.CRVal, y.CRVal}, {"reference pixel", x.CRPix, y.CRPix}, {"step", x.CDelt, y.CDelt}} {
			switch {
			case f.a == nil || f.b == nil:
				why = append(why, fmt.Sprintf("axis %d %s is unresolved, so the grids cannot be confirmed equal", n, f.name))
			case !same(*f.a, *f.b):
				why = append(why, fmt.Sprintf("axis %d %s differs", n, f.name))
			}
		}
	}
	seen := map[string]bool{}
	for k, a := range ha.Extra {
		seen[k] = true
		if b, ok := hb.Extra[k]; !ok || !same(a, b) {
			why = append(why, fmt.Sprintf("%s differs or is missing on one side (rotation, projection pole or equinox)", k))
		}
	}
	for k := range hb.Extra {
		if !seen[k] {
			why = append(why, fmt.Sprintf("%s differs or is missing on one side (rotation, projection pole or equinox)", k))
		}
	}
	if ha.RadeSy == nil || hb.RadeSy == nil || *ha.RadeSy != *hb.RadeSy {
		why = append(why, "celestial frame (RADESYS) differs or is unresolved")
	}
	return why, nil
}

var spectralBases = map[string]bool{"FREQ": true, "WAVE": true, "AWAV": true, "VRAD": true, "VOPT": true, "VELO": true, "FELO": true, "ZOPT": true, "BETA": true, "ENER": true, "WAVN": true}

// spectralFrame is the spectral reference frame a header states for its spectral axis: has is false when it has no
// spectral axis; frame is "" when the frame is not stated. SPECSYS wins. A legacy CTYPE suffix (VELO-LSR, FELO-HEL, ...)
// counts as a statement of its frame only when SPECSYS is absent, and as a label distinct from every SPECSYS value
// ("LSR" is not "LSRK"), the same reading the worker's channel link uses, so the two paths never disagree.
func spectralFrame(h header) (has bool, frame string) {
	for _, x := range h.Axes {
		if x.CType == nil || len(*x.CType) < 4 || !spectralBases[(*x.CType)[:4]] {
			continue
		}
		if h.SpecSy != nil && strings.TrimSpace(*h.SpecSy) != "" {
			return true, strings.ToUpper(strings.TrimSpace(*h.SpecSy))
		}
		if len(*x.CType) >= 8 {
			switch (*x.CType)[4:8] {
			case "-LSR", "-HEL", "-OBS":
				return true, "legacy CTYPE suffix " + (*x.CType)[5:8]
			}
		}
		return true, ""
	}
	return false, ""
}

// SpectralFrames says whether two maps' channels may be linked by index: the spectral reference frame (SPECSYS) must be
// stated by both and equal. Maps without a spectral axis have no channel to link, so that is ok. This is separate from
// Grids on purpose: many images carry no SPECSYS, and the cursor and view links concern sky position only.
func SpectralFrames(a, b json.RawMessage) (ok bool, reason string) {
	var ha, hb header
	if json.Unmarshal(a, &ha) != nil || json.Unmarshal(b, &hb) != nil {
		return false, "the headers could not be read, so channels are not linked by index"
	}
	hasA, fa := spectralFrame(ha)
	hasB, fb := spectralFrame(hb)
	switch {
	case !hasA && !hasB:
		return true, ""
	case hasA != hasB:
		return false, "only one file has a spectral axis, so channels are not linked by index"
	case fa == "" && fb == "":
		return false, "spectral reference frame (SPECSYS) is not declared in either file, so channels are not linked by index"
	case fa == "":
		return false, "spectral reference frame (SPECSYS) is not declared in file A, so channels are not linked by index"
	case fb == "":
		return false, "spectral reference frame (SPECSYS) is not declared in file B, so channels are not linked by index"
	case fa != fb:
		return false, fmt.Sprintf("spectral frames differ (%s vs %s); changing frame is not offered", fa, fb)
	}
	return true, ""
}
