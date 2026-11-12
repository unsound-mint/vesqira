package boards

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func rp(x, y, w, h int) *Rect { return &Rect{x, y, w, h} }

func layoutBoard(t *testing.T) (*Store, Board, []Expect) {
	t.Helper()
	s, _ := newStore(t)
	b, _ := s.CreateBoard(workspace.Local, "local-owner", "B")
	var ex []Expect
	for i, n := range []string{"A", "B", "C"} {
		f := fig(t, s, n, uint8(i+1))
		if err := s.AddItem(workspace.Local, b.ID, f.ID); err != nil {
			t.Fatal(err)
		}
		ex = append(ex, Expect{Kind: KindFigure, ID: f.ID})
	}
	b, _ = s.Board(workspace.Local, b.ID)
	return s, b, ex
}

// A panel's place is stored on its row: a board nobody arranged has none, a saved layout round-trips, moving a panel in the
// order takes its place with it, and clearing returns the board to "no layout".
func TestLayoutRoundTripAndTravelsWithThePanel(t *testing.T) {
	s, b, ex := layoutBoard(t)
	for _, it := range b.Items {
		if it.Layout != nil {
			t.Fatal("a new board must have no layout")
		}
	}
	want := []*Rect{rp(0, 0, 6, 7), rp(6, 0, 6, 7), rp(0, 7, 12, 4)}
	if err := s.SetLayout(workspace.Local, b.ID, want, ex, []*Rect{nil, nil, nil}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Board(workspace.Local, b.ID)
	for i, it := range got.Items {
		if it.Layout == nil || *it.Layout != *want[i] {
			t.Fatalf("item %d: %+v", i, it.Layout)
		}
	}
	if err := s.MoveItem(workspace.Local, b.ID, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Board(workspace.Local, b.ID)
	if got.Items[1].Figure.Title != "A" || *got.Items[1].Layout != *want[0] {
		t.Fatalf("the place did not travel with the panel: %+v", got.Items[1])
	}
	// mixed state: a panel added after the layout has no place
	f := fig(t, s, "D", 9)
	s.AddItem(workspace.Local, b.ID, f.ID)
	got, _ = s.Board(workspace.Local, b.ID)
	if got.Items[3].Layout != nil || got.Items[0].Layout == nil {
		t.Fatal("an added panel must start without a place and leave the others alone")
	}
	// clear
	cur := []Expect{}
	for _, it := range got.Items {
		cur = append(cur, Expect{Kind: KindFigure, ID: it.Figure.ID})
	}
	if err := s.SetLayout(workspace.Local, b.ID, []*Rect{nil, nil, nil, nil}, cur, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Board(workspace.Local, b.ID)
	for _, it := range got.Items {
		if it.Layout != nil {
			t.Fatal("clear left a place")
		}
	}
}

func TestLayoutRefusesWhatCannotBeShownAndChangesNothing(t *testing.T) {
	s, b, ex := layoutBoard(t)
	ok := []*Rect{rp(0, 0, 6, 7), rp(6, 0, 6, 7), rp(0, 7, 12, 4)}
	if err := s.SetLayout(workspace.Local, b.ID, ok, ex, nil); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		rects  []*Rect
		expect []Expect
		from   []*Rect
		conf   bool
	}{
		"overlap":      {[]*Rect{rp(0, 0, 6, 7), rp(5, 0, 6, 7), rp(0, 7, 12, 4)}, ex, nil, false},
		"past column":  {[]*Rect{rp(7, 0, 6, 7), rp(0, 7, 6, 7), rp(0, 14, 12, 4)}, ex, nil, false},
		"zero height":  {[]*Rect{rp(0, 0, 6, 0), rp(6, 0, 6, 7), rp(0, 7, 12, 4)}, ex, nil, false},
		"negative":     {[]*Rect{rp(-1, 0, 6, 7), rp(6, 0, 6, 7), rp(0, 7, 12, 4)}, ex, nil, false},
		"too tall":     {[]*Rect{rp(0, 0, 6, MaxSpan+1), rp(6, 0, 6, 7), rp(0, 300, 12, 4)}, ex, nil, false},
		"wrong count":  {ok[:2], ex, nil, true},
		"other panel":  {ok, []Expect{ex[1], ex[0], ex[2]}, nil, true},
		"stale from":   {ok, ex, []*Rect{nil, nil, nil}, true},
		"short expect": {ok, ex[:2], nil, true},
	} {
		err := s.SetLayout(workspace.Local, b.ID, c.rects, c.expect, c.from)
		if err == nil || errors.Is(err, ErrConflict) != c.conf {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, _ := s.Board(workspace.Local, b.ID)
	for i, it := range got.Items {
		if it.Layout == nil || *it.Layout != *ok[i] {
			t.Fatalf("a refused save changed item %d: %+v", i, it.Layout)
		}
	}
	if err := s.SetLayout(workspace.Local, 999, ok, ex, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown board: %v", err)
	}
}

// The reviewer sees the layout as it was when the board was shared, for figures and for runs alike, and a later change by the
// owner does not reach the snapshot. A board that was never arranged freezes with no layout at all.
func TestFrozenSnapshotKeepsTheLayoutAsPublished(t *testing.T) {
	s, b, ex := layoutBoard(t)
	_, tok0, err := s.Publish(workspace.Local, "local-owner", b.ID, "plain", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r0, _ := s.Review(tok0)
	for _, it := range r0.Snapshot.Items {
		if it.Layout != nil {
			t.Fatal("a board that was never arranged must freeze without a layout")
		}
	}
	first := []*Rect{rp(0, 0, 12, 7), rp(0, 7, 6, 7), rp(6, 7, 6, 7)}
	if err := s.SetLayout(workspace.Local, b.ID, first, ex, nil); err != nil {
		t.Fatal(err)
	}
	_, tok, err := s.Publish(workspace.Local, "local-owner", b.ID, "v1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLayout(workspace.Local, b.ID, []*Rect{rp(0, 0, 6, 7), rp(6, 0, 6, 7), rp(0, 7, 12, 7)}, ex, first); err != nil {
		t.Fatal(err)
	}
	rv, err := s.Review(tok)
	if err != nil {
		t.Fatal(err)
	}
	for i, it := range rv.Snapshot.Items {
		if it.Layout == nil || *it.Layout != *first[i] {
			t.Fatalf("frozen item %d: %+v", i, it.Layout)
		}
	}
	// a run that had not finished is frozen with its place too, through the hand-written whitelist
	out, _ := json.Marshal(FrozenItem{Kind: KindJob, Title: "r", State: "running", Layout: rp(0, 0, 12, 4)})
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["layout"] == nil || len(m) != 6 {
		t.Fatalf("job item: %s", out)
	}
}
