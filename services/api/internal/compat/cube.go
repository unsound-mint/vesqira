package compat

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Cube is the shape of a spectral cube as the VGT and ROHSA pipelines read it: the first two FITS axes are the sky (nx, ny)
// and the third is the spectral axis (nv). Both pipelines take the cube in that native order (VGT.jl reads size(f[1]) as
// nx, ny, nv), so a cube whose axes come in another order is not read here as if they did (AGENTS.md: never assume an axis order).
type Cube struct {
	NX, NY, NV int
	SpecType   string // the spectral axis's CTYPE, as written
	SpecUnit   string // its CUNIT; empty when the header does not state one
	// PixelArcsec is the size of a pixel along axes 1 and 2 in arcseconds; HasScale is false unless both steps are declared in
	// degrees (CUNIT deg). Nothing is assumed for a missing unit.
	PixelArcsec [2]float64
	HasScale    bool
}

func isSky(ctype string) bool {
	if len(ctype) < 4 {
		return false
	}
	b := ctype[:4]
	return b == "RA--" || b == "DEC-" || strings.HasSuffix(b, "LON") || strings.HasSuffix(b, "LAT")
}

func isSpectral(ctype string) bool {
	return len(ctype) >= 4 && spectralBases[ctype[:4]]
}

// ReadCube reads a dataset's catalogued header as a three-axis cube: two sky axes, then a spectral one. The string is the reason
// it is not one (a plain sentence for the person who chose the file); empty when it is.
func ReadCube(raw json.RawMessage) (Cube, string) {
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return Cube{}, "the file's header could not be read"
	}
	if len(h.Axes) != 3 {
		return Cube{}, fmt.Sprintf("the file has %d axes; a VGT or ROHSA input is a cube of two sky axes and a spectral axis", len(h.Axes))
	}
	types := make([]string, 3)
	for i, a := range h.Axes {
		if a.CType != nil {
			types[i] = strings.ToUpper(strings.TrimSpace(*a.CType))
		}
	}
	if !isSky(types[0]) || !isSky(types[1]) || !isSpectral(types[2]) {
		return Cube{}, fmt.Sprintf("its axes are %s; the pipelines read axes 1 and 2 as the sky and axis 3 as the spectral axis, and an unstated or different order is not reordered here",
			describeAxes(types))
	}
	c := Cube{NX: h.Axes[0].Length, NY: h.Axes[1].Length, NV: h.Axes[2].Length, SpecType: types[2]}
	if u := h.Axes[2].CUnit; u != nil {
		c.SpecUnit = strings.TrimSpace(*u)
	}
	if c.NX < 1 || c.NY < 1 || c.NV < 1 {
		return Cube{}, "an axis of the file has no length"
	}
	c.HasScale = true
	for i := 0; i < 2; i++ {
		a := h.Axes[i]
		if a.CDelt == nil || a.CUnit == nil || strings.ToLower(strings.TrimSpace(*a.CUnit)) != "deg" || *a.CDelt == 0 {
			c.HasScale = false
			break
		}
		c.PixelArcsec[i] = math.Abs(*a.CDelt) * 3600
	}
	return c, ""
}

func describeAxes(types []string) string {
	out := make([]string, len(types))
	for i, t := range types {
		if t == "" {
			t = "unstated"
		}
		out[i] = t
	}
	return strings.Join(out, ", ")
}

// SamePixelScale reports whether two cubes have the same pixel size on both sky axes, to the precision a header states it.
// Unknown when either scale is not declared.
func SamePixelScale(a, b Cube) (same, known bool) {
	if !a.HasScale || !b.HasScale {
		return false, false
	}
	return same2(a.PixelArcsec[0], b.PixelArcsec[0]) && same2(a.PixelArcsec[1], b.PixelArcsec[1]), true
}

func same2(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(a, b) }
