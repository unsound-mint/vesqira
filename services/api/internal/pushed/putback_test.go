package pushed

import (
	"errors"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Putting an older version back is a push: it counts towards the daily limit like any other. The lineage a reader gets is the
// newest pushes, capped, with the total, however often an item was toggled.
func TestPutBackCountsAndTheLineageIsCapped(t *testing.T) {
	s := open(t)
	ws := workspace.ID(1)
	a, b := item(t, lineA, "a"), item(t, lineA, "b")
	ra, _ := s.Put(ws, ada, a, nil)
	if _, err := s.Put(ws, ada, b, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < LineageShown+10; i++ {
		x := a
		if i%2 == 1 {
			x = b
		}
		if _, err := s.Put(ws, ada, x, nil); err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.Get(ws, ra.ID)
	if err != nil || len(d.Lineage) != LineageShown || d.LineageTotal != LineageShown+12 {
		t.Fatalf("lineage: %d entries of %d, %v", len(d.Lineage), d.LineageTotal, err)
	}
	heads, _ := s.Heads(ws, "ada", ada.Instance)
	if d.Lineage[len(d.Lineage)-1].Hash != heads[0].Hash {
		t.Fatal("the capped lineage does not end with the current version")
	}
	// fill the day, then a put-back is refused like a new push
	for i := 0; i < pushwire.DailyItems; i++ {
		if _, err := s.db.Exec(`INSERT INTO pushed_history(item_id, pushed_at) VALUES(?, ?)`, ra.ID, s.now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	back := a
	if heads[0].Hash == a.Hash {
		back = b
	}
	var limit LimitError
	if _, err := s.Put(ws, ada, back, nil); !errors.As(err, &limit) {
		t.Fatalf("a put-back past the daily limit: %v", err)
	}
}
