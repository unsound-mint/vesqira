package regions

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Pending marks a DS9 sky region that is read but not yet placed on the image: that needs the dataset's WCS, which only the
// scientific worker evaluates. ResolveSky settles every pending item as approximated or skipped.
const Pending = "pending"

// SkyShape is a circle or box given on the sky, in degrees for the centre and arcseconds for sizes.
type SkyShape struct {
	Frame    string // fk5 | icrs | galactic | fk4 | ecliptic | dataset (DS9's "wcs": whatever the file uses)
	Kind     string // circle | box
	Lon, Lat float64
	R        float64 // circle radius, arcsec
	W, H     float64 // box size, arcsec
	Unitless bool    // a size given without a unit mark, read as arcsec (DS9's default for sky coordinates)
}

var (
	sexLon = regexp.MustCompile(`^([+-]?)(\d+)[:hd](\d+)[:m](\d+(?:\.\d*)?)s?$`)
	sizeRe = regexp.MustCompile(`^([0-9.]+(?:[eE][+-]?\d+)?)(["'dr]?)$`)
)

// skyFrame maps DS9's system names to the frames this reader knows.
func skyFrame(name string) string {
	switch name {
	case "fk5", "j2000":
		return "fk5"
	case "icrs":
		return "icrs"
	case "galactic":
		return "galactic"
	case "fk4", "b1950":
		return "fk4"
	case "ecliptic":
		return "ecliptic"
	case "wcs":
		return "dataset"
	}
	return ""
}

func parseSkyLon(s, frame string) (float64, error) {
	if m := sexLon.FindStringSubmatch(s); m != nil {
		h, _ := strconv.ParseFloat(m[2], 64)
		mi, _ := strconv.ParseFloat(m[3], 64)
		se, _ := strconv.ParseFloat(m[4], 64)
		v := h + mi/60 + se/3600
		if m[1] == "-" {
			v = -v
		}
		if frame == "galactic" || frame == "ecliptic" {
			return v, nil // sexagesimal longitudes there are degrees
		}
		return v * 15, nil // right ascension is written in hours
	}
	return plain(s)
}

func parseSkyLat(s string) (float64, error) {
	if m := sexLon.FindStringSubmatch(s); m != nil {
		d, _ := strconv.ParseFloat(m[2], 64)
		mi, _ := strconv.ParseFloat(m[3], 64)
		se, _ := strconv.ParseFloat(m[4], 64)
		v := d + mi/60 + se/3600
		if m[1] == "-" {
			v = -v
		}
		return v, nil
	}
	return plain(s)
}

func plain(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q", clip(s, 20))
	}
	return v, nil
}

// parseSize returns arcseconds and whether the unit was missing (then arcseconds, as DS9 does for sky coordinates).
func parseSize(s string) (arcsec float64, unitless bool, err error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false, fmt.Errorf("%q", clip(s, 20))
	}
	v, e := strconv.ParseFloat(m[1], 64)
	if e != nil || v < 0 || math.IsInf(v, 0) {
		return 0, false, fmt.Errorf("%q", clip(s, 20))
	}
	switch m[2] {
	case `"`:
		return v, false, nil
	case `'`:
		return v * 60, false, nil
	case "d":
		return v * 3600, false, nil
	case "r":
		return v * 180 / math.Pi * 3600, false, nil
	}
	return v, true, nil
}

// parseSky reads a circle or box written in a sky system. A shape it cannot place returns a reason instead.
func parseSky(frame, shape, args string) (*SkyShape, string) {
	if frame == "" || frame == "fk4" || frame == "ecliptic" {
		return nil, "its sky system is not one this reader places (FK5, ICRS and galactic are)"
	}
	f := strings.Split(args, ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	var err error
	sh := &SkyShape{Frame: frame, Kind: shape}
	switch {
	case shape == "circle" && len(f) >= 3:
		if sh.Lon, err = parseSkyLon(f[0], frame); err == nil {
			if sh.Lat, err = parseSkyLat(f[1]); err == nil {
				sh.R, sh.Unitless, err = parseSize(f[2])
			}
		}
	case shape == "box" && len(f) >= 4:
		if len(f) >= 5 {
			if a, e := plain(f[4]); e != nil || math.Abs(math.Mod(a, 360)) > eps {
				return nil, "a rotated box is not a whole-pixel rectangle"
			}
		}
		var u1, u2 bool
		if sh.Lon, err = parseSkyLon(f[0], frame); err == nil {
			if sh.Lat, err = parseSkyLat(f[1]); err == nil {
				if sh.W, u1, err = parseSize(f[2]); err == nil {
					sh.H, u2, err = parseSize(f[3])
					sh.Unitless = u1 || u2
				}
			}
		}
	default:
		return nil, "the shape " + clip(shape, 20) + " is not supported (boxes and circles are)"
	}
	if err != nil {
		return nil, "its numbers are not readable coordinates (" + err.Error() + ")"
	}
	if sh.Lat < -90 || sh.Lat > 90 {
		return nil, "its latitude is outside -90 to 90 degrees"
	}
	if sh.Lon < 0 {
		sh.Lon += 360
	} else if sh.Lon >= 360 {
		sh.Lon -= 360
	}
	return sh, ""
}

// Placement is what the worker reports for a list of sky positions on one dataset.
type Placement struct {
	Pixels     [][2]float64 // 0-based pixel positions; NaN when a position does not map onto the image plane
	ScaleX     float64      // arcsec per pixel along x and y at the reference pixel
	ScaleY     float64
	NorthAngle *float64 // degrees from the image +y axis to north, or nil when it could not be measured
}

// ResolveSky places pending sky regions on the image. frame is the dataset's declared celestial frame ("icrs", "fk5",
// "galactic") or "" when it is not declared. place converts positions with the dataset's WCS. Every pending item
// becomes approximated (a sky shape is never an exact whole-pixel shape) or skipped, with the reason.
func ResolveSky(items []ImportItem, frame string, place func(pts [][2]float64) (Placement, error)) []ImportItem {
	var idx []int
	var pts [][2]float64
	for i := range items {
		it := &items[i]
		if it.Status != Pending || it.Sky == nil {
			continue
		}
		if ok, note := frameFits(it.Sky.Frame, frame); !ok {
			it.Status, it.Note = Skipped, note
			continue
		}
		idx = append(idx, i)
		pts = append(pts, [2]float64{it.Sky.Lon, it.Sky.Lat})
	}
	if len(idx) == 0 {
		return items
	}
	const chunk = 60
	var pl Placement
	var all [][2]float64
	for s := 0; s < len(pts); s += chunk {
		e := min(s+chunk, len(pts))
		p, err := place(pts[s:e])
		if err != nil {
			for _, i := range idx {
				items[i].Status, items[i].Note = Skipped, "the dataset's WCS could not place it: "+err.Error()
			}
			return items
		}
		if s == 0 {
			pl = p
		}
		all = append(all, p.Pixels...)
	}
	for k, i := range idx {
		it := &items[i]
		x, y := all[k][0], all[k][1]
		if math.IsNaN(x) || math.IsNaN(y) {
			it.Status, it.Note = Skipped, "its position does not map onto this image"
			continue
		}
		sh := it.Sky
		meanScale := (pl.ScaleX + pl.ScaleY) / 2
		if meanScale <= 0 || math.IsNaN(meanScale) {
			it.Status, it.Note = Skipped, "the pixel scale of the dataset is not known"
			continue
		}
		var notes []string
		if sh.Unitless {
			notes = append(notes, "the size had no unit mark, read as arcseconds (DS9's default for sky coordinates)")
		}
		if sh.Frame != "dataset" && frame != sh.Frame && (sh.Frame == "fk5" || sh.Frame == "icrs") {
			notes = append(notes, "FK5 and ICRS are treated as the same frame (they differ by about 0.02 arcsec)")
		}
		switch sh.Kind {
		case "circle":
			if math.Abs(pl.ScaleX-pl.ScaleY) > 0.02*meanScale {
				notes = append(notes, "the pixels are not square on the sky; the mean scale was used")
			}
			r := sh.R / meanScale
			rcx, rcy, rr := int(math.Round(x)), int(math.Round(y)), int(math.Round(r))
			if rcx < 0 || rcy < 0 {
				it.Status, it.Note = Skipped, "its centre is outside the image grid"
				continue
			}
			it.Shape = &Shape{Shape: "circle", CX: rcx, CY: rcy, R: rr}
			it.Status = Approximate
			notes = append(notes, fmt.Sprintf("placed with the dataset's WCS and rounded to a whole-pixel centre and radius: centre (%.2f, %.2f) became (%d, %d), radius %.2f pixels became %d, so it may select a few other pixels", x+1, y+1, rcx+1, rcy+1, r, rr))
		case "box":
			if pl.NorthAngle == nil || !(math.Abs(*pl.NorthAngle) < 0.5 || math.Abs(math.Abs(*pl.NorthAngle)-180) < 0.5) {
				it.Status, it.Note = Skipped, "the image is rotated against the sky, so a box on the sky is not a whole-pixel rectangle"
				continue
			}
			w, h := sh.W/pl.ScaleX, sh.H/pl.ScaleY
			x0, x1 := int(math.Ceil(x-w/2-eps)), int(math.Floor(x+w/2+eps))
			y0, y1 := int(math.Ceil(y-h/2-eps)), int(math.Floor(y+h/2+eps))
			if x0 < 0 || y0 < 0 || x1 < x0 || y1 < y0 {
				it.Status, it.Note = Skipped, "it selects no pixel inside the image grid"
				continue
			}
			it.Shape = &Shape{Shape: "rect", X0: x0, Y0: y0, X1: x1, Y1: y1}
			it.Status = Approximate
			notes = append(notes, "placed with the dataset's WCS: selects the pixels whose centres lie inside the box, with its edges treated as straight lines in the image")
		}
		it.Note = strings.Join(notes, "; ")
	}
	return items
}

func frameFits(region, dataset string) (bool, string) {
	eq := func(f string) bool { return f == "fk5" || f == "icrs" }
	switch {
	case region == "dataset":
		return true, ""
	case dataset == "":
		return false, "the dataset does not declare its celestial frame, so a sky region cannot be placed on it"
	case region == dataset || (eq(region) && eq(dataset)):
		return true, ""
	}
	return false, "the region is in " + region + " but the dataset is in " + dataset + "; no frame transformation is done here"
}
