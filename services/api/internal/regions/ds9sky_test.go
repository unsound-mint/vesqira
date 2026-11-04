package regions

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestSkyRegionsAreReadInEveryUsualNotation(t *testing.T) {
	text := "fk5\ncircle(83.8,-69.5,10\")\n" +
		"icrs\ncircle(5:35:12,-69:30:00,1')\n" +
		"galactic\nbox(280.5,-32.8,60\",30\",0) # text={Arm}\n" +
		"wcs; circle(1,2,0.01d)\n" +
		"fk5; circle(83,-69,5)\n" +
		"fk4\ncircle(83,-69,5\")\n" +
		"fk5; box(83,-69,60\",30\",20)\n" +
		"fk5; ellipse(83,-69,5\",3\",0)\n" +
		"fk5; circle(83,95,5\")\n"
	it := ParseDS9(text)
	if len(it) != 9 {
		t.Fatalf("%d items", len(it))
	}
	c := it[0].Sky
	if it[0].Status != Pending || c.Frame != "fk5" || c.Lon != 83.8 || c.Lat != -69.5 || c.R != 10 || c.Unitless {
		t.Errorf("decimal degrees and arcsec: %+v %+v", it[0], c)
	}
	if s := it[1].Sky; math.Abs(s.Lon-(5+35.0/60+12.0/3600)*15) > 1e-9 || math.Abs(s.Lat-(-69.5)) > 1e-9 || s.R != 60 || s.Frame != "icrs" {
		t.Errorf("right ascension in hours, declination in degrees, arcmin: %+v", s)
	}
	if s := it[2].Sky; s.Kind != "box" || s.W != 60 || s.H != 30 || s.Frame != "galactic" || it[2].Name != "Arm" {
		t.Errorf("galactic box: %+v", it[2])
	}
	if s := it[3].Sky; s.Frame != "dataset" || math.Abs(s.R-36) > 1e-9 {
		t.Errorf("wcs means the file's own frame, 0.01 d is 36 arcsec: %+v", s)
	}
	if !it[4].Sky.Unitless || it[4].Sky.R != 5 {
		t.Errorf("a size without a unit is read as arcsec and says so: %+v", it[4].Sky)
	}
	for i, why := range map[int]string{5: "sky system", 6: "rotated", 7: "not supported", 8: "latitude"} {
		if it[i].Status != Skipped || !strings.Contains(it[i].Note, why) {
			t.Errorf("item %d should be skipped for %q: %+v", i, why, it[i])
		}
	}
}

// a grid of 36 arcsec pixels with north up
func grid(north float64) func([][2]float64) (Placement, error) {
	return func(pts [][2]float64) (Placement, error) {
		out := make([][2]float64, len(pts))
		for i, p := range pts {
			out[i] = [2]float64{15.2 + (p[0]-150)*100, 11.4 + (p[1]-2)*100} // 100 pixels per degree
			if p[1] > 80 {
				out[i] = [2]float64{math.NaN(), math.NaN()}
			}
		}
		return Placement{Pixels: out, ScaleX: 36, ScaleY: 36, NorthAngle: &north}, nil
	}
}

func TestSkyRegionsBecomeApproximatedWholePixelShapesOrAreSkippedWithAReason(t *testing.T) {
	text := "fk5\ncircle(150.0,2.0,360\")\nbox(150.0,2.0,720\",360\")\ncircle(150.0,2.0,360)\nbox(150.0,2.0,30\",30\")\n"
	it := ResolveSky(ParseDS9(text), "icrs", grid(0))
	c := it[0]
	if c.Status != Approximate || *c.Shape != (Shape{Shape: "circle", CX: 15, CY: 11, R: 10}) || !strings.Contains(c.Note, "became") || !strings.Contains(c.Note, "FK5 and ICRS") {
		t.Errorf("circle: %+v", c)
	}
	b := it[1] // centre (15.2, 11.4), 20 x 10 pixels: x 5.2..25.2 and y 6.4..16.4
	if b.Status != Approximate || *b.Shape != (Shape{Shape: "rect", X0: 6, Y0: 7, X1: 25, Y1: 16}) {
		t.Errorf("box: %+v", b)
	}
	if !strings.Contains(it[2].Note, "no unit mark") || it[2].Status != Approximate {
		t.Errorf("unitless size: %+v", it[2])
	}
	if got := ResolveSky(ParseDS9("fk5; box(150.0,2.0,720\",360\")"), "fk5", grid(30)); got[0].Status != Skipped || !strings.Contains(got[0].Note, "rotated against the sky") {
		t.Errorf("a rotated image cannot hold a box on the sky: %+v", got[0])
	}
	if got := ResolveSky(ParseDS9("fk5; box(150.0,2.0,720\",360\")"), "fk5", grid(180)); got[0].Status != Approximate {
		t.Errorf("north down is still aligned: %+v", got[0])
	}
}

func TestSkyRegionsRefuseWhatTheyCannotPlaceHonestly(t *testing.T) {
	gal := ParseDS9("galactic\ncircle(280,-32,10\")")
	if got := ResolveSky(gal, "icrs", grid(0)); got[0].Status != Skipped || !strings.Contains(got[0].Note, "no frame transformation") {
		t.Errorf("different frames: %+v", got[0])
	}
	if got := ResolveSky(ParseDS9("fk5; circle(150,2,5\")"), "", grid(0)); got[0].Status != Skipped || !strings.Contains(got[0].Note, "does not declare") {
		t.Errorf("an undeclared dataset frame: %+v", got[0])
	}
	if got := ResolveSky(ParseDS9("galactic\ncircle(150.0,2.0,10\")"), "galactic", grid(0)); got[0].Status != Approximate {
		t.Errorf("same frame: %+v", got[0])
	}
	if got := ResolveSky(ParseDS9("fk5; circle(150,85,5\")"), "fk5", grid(0)); got[0].Status != Skipped || !strings.Contains(got[0].Note, "does not map") {
		t.Errorf("off the image plane: %+v", got[0])
	}
	if got := ResolveSky(ParseDS9("fk5; circle(100,2,5\")"), "fk5", grid(0)); got[0].Status != Skipped || !strings.Contains(got[0].Note, "outside the image") {
		t.Errorf("a centre left of the grid: %+v", got[0])
	}
	failing := func([][2]float64) (Placement, error) { return Placement{}, errors.New("no WCS") }
	if got := ResolveSky(ParseDS9("fk5; circle(150,2,5\")"), "fk5", failing); got[0].Status != Skipped || !strings.Contains(got[0].Note, "no WCS") {
		t.Errorf("a worker error: %+v", got[0])
	}
	var many strings.Builder
	many.WriteString("fk5\n")
	for i := 0; i < 130; i++ {
		many.WriteString("circle(150.0,2.0,36\")\n")
	}
	calls := 0
	g := grid(0)
	counting := func(p [][2]float64) (Placement, error) { calls++; return g(p) }
	got := ResolveSky(ParseDS9(many.String()), "fk5", counting)
	if calls != 3 || got[129].Status != Approximate {
		t.Errorf("positions are sent in chunks the worker accepts: %d calls, last %+v", calls, got[129])
	}
}
