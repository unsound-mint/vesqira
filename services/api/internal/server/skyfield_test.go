package server

import "testing"

func square(lon, lat, half float64) [][2]float64 {
	return [][2]float64{{lon - half, lat - half}, {lon + half, lat - half}, {lon + half, lat + half}, {lon - half, lat + half}}
}

// A sky view records only the outlines its picture shows. The field is the panel's square tangent-plane view.
func TestOutlineInField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		poly          [][2]float64
		lon, lat, fov float64
		want          bool
	}{
		{"inside", square(150, 2, 0.1), 150, 2, 1, true},
		{"partly inside, corner only", square(150.5, 2.5, 0.2), 150, 2, 1, true},
		{"crossing with no vertex inside", [][2]float64{{149.9, 1.0}, {150.1, 1.0}, {150.1, 3.0}, {149.9, 3.0}}, 150, 2, 0.5, true},
		{"field inside a large outline", square(150, 2, 5), 150, 2, 0.5, true},
		{"apart", square(160, 2, 0.1), 150, 2, 1, false},
		{"just outside", square(150.9, 2, 0.1), 150, 2, 1, false},
		{"across longitude 0", square(0.1, 0, 0.3), 359.9, 0, 1, true},
		{"on the far side of the sky", square(330, -2, 0.1), 150, 2, 20, false},
	}
	for _, c := range cases {
		if got := outlineInField(c.poly, c.lon, c.lat, c.fov); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
