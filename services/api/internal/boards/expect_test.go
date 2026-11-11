package boards

import (
	"errors"
	"sync"
	"testing"
)

func order(t *testing.T, s *Store, id int64) string {
	t.Helper()
	b, _ := s.Board(id)
	out := ""
	for _, it := range b.Items {
		out += it.Figure.Title
	}
	return out
}

// An edit that names what it expects at a position is applied only if that is still there; otherwise nothing changes.
func TestExpectedItemGuardsPositionEdits(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard("B")
	a, c, d := fig(t, s, "A", 1), fig(t, s, "C", 2), fig(t, s, "D", 3)
	for _, f := range []Figure{a, c, d} {
		s.AddItem(b.ID, f.ID)
	}
	s.SetNote(b.ID, 1, "n", nil)
	fe := func(f Figure) *Expect { return &Expect{Kind: KindFigure, ID: f.ID} }

	for name, err := range map[string]error{
		"move":   s.MoveItem(b.ID, 1, 1, fe(a)),
		"remove": s.RemoveItem(b.ID, 1, fe(d)),
		"note":   s.SetNote(b.ID, 1, "x", &Expect{Kind: KindFigure, ID: c.ID, Note: ptr("other")}),
		"kind":   s.RemoveItem(b.ID, 1, &Expect{Kind: KindJob, ID: c.ID}),
		"gone":   s.RemoveItem(b.ID, 7, fe(c)),
	} {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s: want conflict, got %v", name, err)
		}
	}
	if order(t, s, b.ID) != "ACD" {
		t.Fatalf("a refused edit changed the board: %s", order(t, s, b.ID))
	}
	if bb, _ := s.Board(b.ID); bb.Items[1].Note != "n" {
		t.Fatal("a refused note edit changed the note")
	}
	if err := s.MoveItem(b.ID, 1, 1, fe(c)); err != nil || order(t, s, b.ID) != "ADC" {
		t.Fatalf("matching move: %v %s", err, order(t, s, b.ID))
	}
	if err := s.SetNote(b.ID, 2, "y", &Expect{Kind: KindFigure, ID: c.ID, Note: ptr("n")}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveItem(b.ID, 2, fe(c)); err != nil || order(t, s, b.ID) != "AD" {
		t.Fatalf("matching remove: %v %s", err, order(t, s, b.ID))
	}
	// without expect an unknown position is still not found, not a conflict
	if err := s.RemoveItem(b.ID, 9, nil); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
}

// Two windows undoing the same removal at once: the guard and the edit are one transaction, so exactly one wins and the
// other panels are not touched.
func TestConcurrentGuardedRemovesApplyOnce(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard("B")
	a, c, d := fig(t, s, "A", 1), fig(t, s, "C", 2), fig(t, s, "D", 3)
	for _, f := range []Figure{a, c, d} {
		s.AddItem(b.ID, f.ID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.RemoveItem(b.ID, 0, &Expect{Kind: KindFigure, ID: a.ID})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 || order(t, s, b.ID) != "CD" {
		t.Fatalf("won=%d board=%s", won, order(t, s, b.ID))
	}
}

func ptr(s string) *string { return &s }
