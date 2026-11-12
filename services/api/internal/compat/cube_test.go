package compat

import (
	"encoding/json"
	"strings"
	"testing"
)

func axesHdr(types ...string) json.RawMessage {
	axes := []map[string]any{}
	for i, t := range types {
		ax := map[string]any{"axis": i + 1, "length": 10 * (i + 1), "ctype": t}
		if i < 2 {
			ax["cunit"], ax["cdelt"] = "deg", 0.001
		}
		axes = append(axes, ax)
	}
	b, _ := json.Marshal(map[string]any{"axes": axes})
	return b
}

// A cube is two sky axes then a spectral one, in the pipelines' own order; anything else is refused with its reason.
func TestReadCube(t *testing.T) {
	c, why := ReadCube(axesHdr("RA---SIN", "DEC--SIN", "VRAD"))
	if why != "" || c.NX != 10 || c.NY != 20 || c.NV != 30 || !c.HasScale || c.PixelArcsec[0] != 3.6 {
		t.Fatalf("%+v %q", c, why)
	}
	if c, why := ReadCube(axesHdr("GLON-CAR", "GLAT-CAR", "FREQ")); why != "" || c.NV != 30 {
		t.Errorf("galactic cube: %+v %q", c, why)
	}
	for name, h := range map[string]json.RawMessage{
		"two axes":       axesHdr("RA---SIN", "DEC--SIN"),
		"spectral first": axesHdr("VRAD", "RA---SIN", "DEC--SIN"),
		"no spectral":    axesHdr("RA---SIN", "DEC--SIN", "STOKES"),
		"unstated":       axesHdr("", "", ""),
	} {
		if _, why := ReadCube(h); why == "" {
			t.Errorf("%s was read as a cube", name)
		}
	}
	if _, why := ReadCube(axesHdr("RA---SIN", "STOKES", "VRAD")); !strings.Contains(why, "STOKES") {
		t.Errorf("reason: %q", why)
	}
	// no unit, no scale: nothing is assumed
	b := json.RawMessage(`{"axes":[{"axis":1,"length":5,"ctype":"RA---SIN","cdelt":0.1},{"axis":2,"length":5,"ctype":"DEC--SIN","cdelt":0.1,"cunit":"deg"},{"axis":3,"length":5,"ctype":"VRAD"}]}`)
	nc, why := ReadCube(b)
	if why != "" || nc.HasScale {
		t.Errorf("a step without a unit gave a scale: %+v %q", nc, why)
	}
	a, _ := ReadCube(axesHdr("RA---SIN", "DEC--SIN", "VRAD"))
	if same, known := SamePixelScale(a, a); !same || !known {
		t.Error("equal scales")
	}
	if _, known := SamePixelScale(a, nc); known {
		t.Error("an unknown scale was compared")
	}
}
