package pushed

import (
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Reading an item matches its references again only when the workspace's datasets changed since the last match (a location
// added or removed, a fingerprint computed); otherwise a read writes nothing.
func TestRematchRunsOnlyWhenDatasetsChanged(t *testing.T) {
	s := open(t)
	ws := workspace.ID(1)
	it := item(t, lineA, "with a reference")
	it.References = []pushwire.Reference{{Role: "source", Name: "a.fits", SizeBytes: 10}}
	it.Hash, _ = pushwire.ContentHash(it)
	full := "ab" + "0000000000000000000000000000000000000000000000000000000000000000"[2:]
	r, err := s.Put(ws, ada, it, []pushwire.Identity{{Full: full}}, Quota{})
	if err != nil {
		t.Fatal(err)
	}
	before := s.rematches.Load()
	for i := 0; i < 3; i++ {
		if _, err := s.Rematch(ws, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if s.rematches.Load() != before {
		t.Fatalf("reads with no dataset change matched again %d times", s.rematches.Load()-before)
	}
	if _, err := s.db.Exec(`INSERT INTO datasets(id, name, workspace_id) VALUES(5, 'server.fits', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dataset_locations(dataset_id, size_bytes, full_sha256, workspace_id) VALUES(5, 10, ?, 1)`, full); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Rematch(ws, r.ID)
	if err != nil || len(ch) != 1 || ch[0].Match != "verified" || s.rematches.Load() != before+1 {
		t.Fatalf("after a dataset gained the same full hash: %+v %v", ch, err)
	}
	if _, _ = s.Rematch(ws, r.ID); s.rematches.Load() != before+1 {
		t.Fatal("matched again with nothing changed")
	}
}
