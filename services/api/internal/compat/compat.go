// Package compat decides whether two registered maps sit on the same grid. Two measurements are never merged,
// overlaid or used as each other's weights unless this says they are compatible; every reason is reported.
package compat

import (
	"encoding/json"
	"fmt"
	"math"
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
