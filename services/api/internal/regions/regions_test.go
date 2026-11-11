package regions

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/r.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateEditDuplicateAndDelete(t *testing.T) {
	s := newStore(t)
	r, err := s.Create(1, "  Core\n", Shape{Shape: "rect", X0: 2, Y0: 3, X1: 9, Y1: 7})
	if err != nil || r.Name != "Core" || r.Shape.X1 != 9 {
		t.Fatalf("%+v %v", r, err)
	}
	c, err := s.Create(1, "Peak", Shape{Shape: "circle", CX: 10, CY: 11, R: 4})
	if err != nil {
		t.Fatal(err)
	}
	// edit the name only, then the shape only: the other part stays
	n := "Core (wide)"
	r2, err := s.Update(r.ID, &n, nil)
	if err != nil || r2.Name != n || r2.Shape.X0 != 2 {
		t.Fatalf("rename: %+v %v", r2, err)
	}
	r3, err := s.Update(r.ID, nil, &Shape{Shape: "rect", X0: 0, Y0: 0, X1: 20, Y1: 20})
	if err != nil || r3.Name != n || r3.Shape.X1 != 20 {
		t.Fatalf("reshape: %+v %v", r3, err)
	}
	// a region belongs to its dataset; another dataset's list is unaffected
	s.Create(2, "Other", Shape{Shape: "circle", CX: 1, CY: 1, R: 1})
	if l, _ := s.List(1); len(l) != 2 {
		t.Errorf("list: %+v", l)
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := s.Get(c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted region is gone: %v", err)
	}
	if l, _ := s.List(1); len(l) != 1 {
		t.Errorf("after delete: %+v", l)
	}
}

func TestShapesAndNamesAreValidated(t *testing.T) {
	s := newStore(t)
	for name, sh := range map[string]Shape{
		"negative":   {Shape: "rect", X0: -1, Y1: 3},
		"inverted":   {Shape: "rect", X0: 5, X1: 2, Y0: 0, Y1: 0},
		"huge":       {Shape: "circle", CX: 1 << 30, CY: 0, R: 1},
		"negative r": {Shape: "circle", CX: 1, CY: 1, R: -2},
		"unknown":    {Shape: "polygon"},
		"empty":      {},
	} {
		if _, err := s.Create(1, "x", sh); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Create(1, "  \n ", Shape{Shape: "circle", CX: 1, CY: 1, R: 1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty name: %v", err)
	}
	long, _ := s.Create(1, strings.Repeat("é", 200), Shape{Shape: "circle", CX: 1, CY: 1, R: 1})
	if len([]rune(long.Name)) != 80 {
		t.Errorf("name length %d", len([]rune(long.Name)))
	}
	for i := 0; i < MaxPerDataset; i++ {
		s.Create(9, "n", Shape{Shape: "circle", CX: 1, CY: 1, R: 1})
	}
	if _, err := s.Create(9, "one more", Shape{Shape: "circle", CX: 1, CY: 1, R: 1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("the limit: %v", err)
	}
}

func TestDS9OutputUsesOneBasedPixelCentresAndKeepsNamesSafe(t *testing.T) {
	// a box of pixels x=2..9, y=3..7 (0-based) is x 3..10, y 4..8 in DS9: centre (6.5, 6), size 8 by 5
	out := DS9([]Region{
		{Name: "Core {a}\nb", Shape: Shape{Shape: "rect", X0: 2, Y0: 3, X1: 9, Y1: 7}},
		{Name: "Peak", Shape: Shape{Shape: "circle", CX: 10, CY: 11, R: 4}},
	})
	for _, want := range []string{"# Region file format: DS9 version 4.1\n", "\nimage\n", "box(6.5,6,8,5,0) # text={Core  a  b}\n", "circle(11,12,4) # text={Peak}\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// the number of pixels DS9 would select by the pixel-centre rule equals ours: 8x5 for the box
	if strings.Count(out, "box(") != 1 || strings.Count(out, "circle(") != 1 {
		t.Error("one line per region")
	}
}

func TestALineRoundTripsAndIsHeldToItsBounds(t *testing.T) {
	s := newStore(t)
	r, err := s.Create(1, "Cut", Shape{Shape: "line", Points: [][]int{{2, 3}, {20, 9}, {30, 9}}, Width: 3, WidthUnit: "pixel"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(r.ID)
	if err != nil || got.Shape.Shape != "line" || len(got.Shape.Points) != 3 || got.Shape.Points[1][0] != 20 || got.Shape.Width != 3 || got.Shape.WidthUnit != "pixel" {
		t.Fatalf("%+v %v", got, err)
	}
	// a line keeps no stray rectangle or circle fields: they are what the shape is not
	if got.Shape.X1 != 0 || got.Shape.R != 0 {
		t.Errorf("%+v", got.Shape)
	}
	arc, err := s.Update(r.ID, nil, &Shape{Shape: "line", Points: [][]int{{1, 1}, {9, 9}}, Width: 12.5, WidthUnit: "arcsec"})
	if err != nil || arc.Shape.WidthUnit != "arcsec" || arc.Shape.Width != 12.5 {
		t.Fatalf("%+v %v", arc, err)
	}
	for name, sh := range map[string]Shape{
		"one vertex":   {Shape: "line", Points: [][]int{{1, 1}}, Width: 1, WidthUnit: "pixel"},
		"no length":    {Shape: "line", Points: [][]int{{4, 4}, {4, 4}}, Width: 1, WidthUnit: "pixel"},
		"no unit":      {Shape: "line", Points: [][]int{{1, 1}, {5, 5}}, Width: 1},
		"zero width":   {Shape: "line", Points: [][]int{{1, 1}, {5, 5}}, WidthUnit: "pixel"},
		"huge width":   {Shape: "line", Points: [][]int{{1, 1}, {5, 5}}, Width: 1e9, WidthUnit: "pixel"},
		"bad unit":     {Shape: "line", Points: [][]int{{1, 1}, {5, 5}}, Width: 1, WidthUnit: "deg"},
		"negative":     {Shape: "line", Points: [][]int{{-1, 1}, {5, 5}}, Width: 1, WidthUnit: "pixel"},
		"three coords": {Shape: "line", Points: [][]int{{1, 1, 1}, {5, 5}}, Width: 1, WidthUnit: "pixel"},
		"too many":     {Shape: "line", Points: make([][]int, MaxLinePoints+1), Width: 1, WidthUnit: "pixel"},
	} {
		if _, err := s.Create(1, "bad", sh); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDS9WritesATwoVertexPixelWidthLineAsAProjectionAndNeverApproximatesTheRest(t *testing.T) {
	out := DS9([]Region{
		{Name: "Cut", Shape: Shape{Shape: "line", Points: [][]int{{2, 3}, {12, 9}}, Width: 3, WidthUnit: "pixel"}},
		{Name: "Bend", Shape: Shape{Shape: "line", Points: [][]int{{2, 3}, {12, 9}, {20, 9}}, Width: 3, WidthUnit: "pixel"}},
		{Name: "Wide", Shape: Shape{Shape: "line", Points: [][]int{{2, 3}, {12, 9}}, Width: 30, WidthUnit: "arcsec"}},
	})
	if !strings.Contains(out, "# projection(3,4,13,10,3) # text={Cut}\n") {
		t.Errorf("projection missing in\n%s", out)
	}
	if strings.Count(out, "projection(") != 1 || strings.Count(out, "# not written as a DS9 shape") != 2 {
		t.Errorf("only the exact case becomes a shape:\n%s", out)
	}
}
