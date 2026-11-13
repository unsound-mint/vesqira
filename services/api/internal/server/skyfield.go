package server

import "math"

// outlineInField reports whether an outline (longitude, latitude in degrees, in the figure's frame) meets the square field a sky
// view shows: centred on (lon0, lat0) and fovDeg across, laid out the way the panel lays it out (a tangent plane about the centre,
// the field the square of half-width tan(fov/2)). It is a plain geometric test on coordinates the worker already computed; it
// reads no header and converts no frame. A vertex 90 degrees or more from the centre has no place on the tangent plane and is
// skipped, so an outline that wraps far round the sky is judged by the part that does.
func outlineInField(polygon [][2]float64, lon0, lat0, fovDeg float64) bool {
	const rad = math.Pi / 180
	h := math.Tan(math.Min(fovDeg, 179) * rad / 2)
	type pt struct {
		x, y float64
		ok   bool
	}
	pts := make([]pt, len(polygon))
	p0 := lat0 * rad
	for i, v := range polygon {
		dl, p := (v[0]-lon0)*rad, v[1]*rad
		cosc := math.Sin(p0)*math.Sin(p) + math.Cos(p0)*math.Cos(p)*math.Cos(dl)
		if !(cosc > 1e-3) {
			continue
		}
		pts[i] = pt{math.Cos(p) * math.Sin(dl) / cosc, (math.Cos(p0)*math.Sin(p) - math.Sin(p0)*math.Cos(p)*math.Cos(dl)) / cosc, true}
	}
	// a vertex in the field
	for _, q := range pts {
		if q.ok && math.Abs(q.x) <= h && math.Abs(q.y) <= h {
			return true
		}
	}
	corners := [4][2]float64{{-h, -h}, {h, -h}, {h, h}, {-h, h}}
	// an edge of the outline crossing an edge of the field
	n := len(pts)
	for i := 0; i < n; i++ {
		a, b := pts[i], pts[(i+1)%n]
		if !a.ok || !b.ok {
			continue
		}
		for k := 0; k < 4; k++ {
			c, d := corners[k], corners[(k+1)%4]
			if segmentsCross(a.x, a.y, b.x, b.y, c[0], c[1], d[0], d[1]) {
				return true
			}
		}
	}
	// the field inside the outline (a large outline round a small field): one corner is enough when no edge crosses
	inside := false
	c := corners[0]
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		a, b := pts[i], pts[j]
		if !a.ok || !b.ok {
			continue
		}
		if (a.y > c[1]) != (b.y > c[1]) && c[0] < (b.x-a.x)*(c[1]-a.y)/(b.y-a.y)+a.x {
			inside = !inside
		}
	}
	return inside
}

func segmentsCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	orient := func(px, py, qx, qy, rx, ry float64) float64 { return (qx-px)*(ry-py) - (qy-py)*(rx-px) }
	d1, d2 := orient(ax, ay, bx, by, cx, cy), orient(ax, ay, bx, by, dx, dy)
	d3, d4 := orient(cx, cy, dx, dy, ax, ay), orient(cx, cy, dx, dy, bx, by)
	return (d1 > 0) != (d2 > 0) && (d3 > 0) != (d4 > 0)
}
