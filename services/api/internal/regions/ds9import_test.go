package regions

import (
	"strings"
	"testing"
)

func TestImportReadsExactlyWhatItCanAndSaysWhyForTheRest(t *testing.T) {
	text := `# Region file format: DS9 version 4.1
global color=green
image
box(6.5,6,8,5,0) # text={Core}
circle(11,12,4) # text={Peak}
circle(11.4,12,3.5)
box(10,10,4,4,30)
-circle(5,5,2)
ellipse(5,5,3,2,0)
fk5
circle(150.1,2.0,10")
physical; box(3,3,3,3,0)
circle(abc,2,3)
`
	items := ParseDS9(text)
	if len(items) != 9 {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	// the box written by Vesqira for x 2..9, y 3..7 comes back as exactly that
	if b := items[0]; b.Status != Exact || b.Name != "Core" || *b.Shape != (Shape{Shape: "rect", X0: 2, Y0: 3, X1: 9, Y1: 7}) {
		t.Errorf("box: %+v", b)
	}
	if c := items[1]; c.Status != Exact || c.Name != "Peak" || *c.Shape != (Shape{Shape: "circle", CX: 10, CY: 11, R: 4}) {
		t.Errorf("circle: %+v", c)
	}
	if c := items[2]; c.Status != Approximate || !strings.Contains(c.Note, "became") || c.Name != "Imported region 3" {
		t.Errorf("a circle that needs rounding must say so: %+v", c)
	}
	for i, why := range map[int]string{3: "rotated", 4: "excluded", 5: "not supported", 8: "plain image coordinates"} {
		if items[i].Status != Skipped || !strings.Contains(items[i].Note, why) || items[i].Shape != nil {
			t.Errorf("item %d should be skipped for %q: %+v", i, why, items[i])
		}
	}
	if sky := items[6]; sky.Status != Pending || sky.Sky == nil || sky.Shape != nil { // placed later, with the dataset's WCS
		t.Errorf("a sky region waits for the WCS: %+v", sky)
	}
	// a system named on the same line applies to that shape
	if p := items[7]; p.Status != Exact || p.Shape.X0 != 1 || p.Shape.X1 != 3 {
		t.Errorf("physical box: %+v", p)
	}
}

func TestImportIsTheInverseOfExport(t *testing.T) {
	in := []Region{
		{Name: "A", Shape: Shape{Shape: "rect", X0: 0, Y0: 0, X1: 20, Y1: 20}},
		{Name: "B", Shape: Shape{Shape: "rect", X0: 2, Y0: 3, X1: 3, Y1: 9}},
		{Name: "C", Shape: Shape{Shape: "circle", CX: 7, CY: 7, R: 0}},
		{Name: "D", Shape: Shape{Shape: "circle", CX: 100, CY: 5, R: 13}},
	}
	for i, it := range ParseDS9(DS9(in)) {
		if it.Status != Exact || it.Shape == nil || *it.Shape != in[i].Shape || it.Name != in[i].Name {
			t.Errorf("%s did not survive the round trip: %+v", in[i].Name, it)
		}
	}
}

func TestImportBoundsItsWork(t *testing.T) {
	big := strings.Repeat("circle(1,1,1)\n", maxImportLines+50)
	items := ParseDS9(big)
	if len(items) != maxImportLines+1 || items[len(items)-1].Status != Skipped {
		t.Errorf("%d items; the last must say the rest was not read", len(items))
	}
}
