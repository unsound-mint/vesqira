package pushed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	// the tables a match reads, as the catalog has them
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, workspace_id INTEGER NOT NULL)`,
		`CREATE TABLE dataset_locations(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER, size_bytes INTEGER, sampled_fp TEXT NOT NULL DEFAULT '', full_sha256 TEXT NOT NULL DEFAULT '', workspace_id INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(db, filepath.Join(t.TempDir(), "pushed"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func item(t *testing.T, origin, caption string) pushwire.Item {
	t.Helper()
	doc, _ := json.Marshal(map[string]string{"name": caption})
	it := pushwire.Item{Kind: "run", Origin: origin, Title: "r", Doc: doc, Assets: []string{}, References: []pushwire.Reference{}}
	h, err := pushwire.ContentHash(it)
	if err != nil {
		t.Fatal(err)
	}
	it.Hash = h
	return it
}

var (
	ada   = Pusher{Account: "ada", TokenID: "t1", Instance: strings.Repeat("1", 32)}
	ada2  = Pusher{Account: "ada", TokenID: "t2", Instance: strings.Repeat("2", 32)}
	bob   = Pusher{Account: "bob", TokenID: "t3", Instance: strings.Repeat("1", 32)}
	lineA = strings.Repeat("a", 32)
)

// The same hash twice is one row; a new hash supersedes only the head of its own lineage (account, instance, kind, origin); A,
// then B, then A again answers A as it was and leaves B the head.
func TestPutIsIdempotentAndSupersedesOnlyItsOwnLineage(t *testing.T) {
	s := open(t)
	ws := workspace.ID(1)
	a, b := item(t, lineA, "a"), item(t, lineA, "b")
	r1, err := s.Put(ws, ada, a, nil)
	if err != nil || !r1.Stored {
		t.Fatal(r1, err)
	}
	r2, err := s.Put(ws, ada, a, nil)
	if err != nil || r2.Stored || r2.ID != r1.ID || s.Count(ws, a.Hash) != 1 {
		t.Fatalf("the same hash twice: %+v %v", r2, err)
	}
	// another instance of the same account, and another account, never supersede ada's first instance
	if _, err := s.Put(ws, ada2, item(t, lineA, "other instance"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ws, bob, item(t, lineA, "other account"), nil); err != nil {
		t.Fatal(err)
	}
	if heads, _ := s.Heads(ws, "ada", ada.Instance); len(heads) != 1 || heads[0].Hash != a.Hash {
		t.Fatalf("another instance or account moved the lineage: %+v", heads)
	}
	r3, err := s.Put(ws, ada, b, nil)
	if err != nil || r3.Superseded != a.Hash {
		t.Fatalf("B does not supersede A: %+v %v", r3, err)
	}
	// A again: the same row becomes the head again (nothing duplicated), B is superseded by it, and the history keeps the order of
	// the pushes: A, B, A
	r4, err := s.Put(ws, ada, a, nil)
	if err != nil || r4.Stored || r4.ID != r1.ID || r4.Superseded != b.Hash || r4.SupersededBy != "" || s.Count(ws, a.Hash) != 1 {
		t.Fatalf("A again: %+v %v", r4, err)
	}
	if heads, _ := s.Heads(ws, "ada", ada.Instance); len(heads) != 1 || heads[0].Hash != a.Hash {
		t.Fatalf("A is not the head again: %+v", heads)
	}
	d, err := s.Get(ws, r1.ID)
	if err != nil || d.SupersededBy != 0 || len(d.Lineage) != 3 {
		t.Fatalf("lineage: %+v %v", d, err)
	}
	if got := []string{d.Lineage[0].Hash, d.Lineage[1].Hash, d.Lineage[2].Hash}; got[0] != a.Hash || got[1] != b.Hash || got[2] != a.Hash {
		t.Fatalf("the history is not A, B, A: %v", got)
	}
	if db, _ := s.Get(ws, r3.ID); db.SupersededBy != r1.ID {
		t.Fatalf("B is not superseded by A: %+v", db.Summary)
	}
	// the same A once more changes nothing: it is the head already
	if r5, _ := s.Put(ws, ada, a, nil); r5.Superseded != "" {
		t.Fatalf("pushing the head again moved the lineage: %+v", r5)
	}
	if d, _ := s.Get(ws, r1.ID); len(d.Lineage) != 3 {
		t.Fatalf("pushing the head again added history: %d", len(d.Lineage))
	}
	// another account replaying A never moves ada's lineage
	if _, err := s.Put(ws, ada, b, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ws, bob, a, nil); err != nil {
		t.Fatal(err)
	}
	if heads, _ := s.Heads(ws, "ada", ada.Instance); heads[0].Hash != b.Hash {
		t.Fatalf("another account made A the head of ada's lineage: %+v", heads)
	}
	// another workspace holds nothing of it
	if _, err := s.Get(workspace.ID(2), r1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("an item was found from another workspace")
	}
	if m, _ := s.Missing(workspace.ID(2), []string{a.Hash}); len(m) != 1 {
		t.Fatal("another workspace claims to hold the hash")
	}
}

func TestPutRefusesAnItemWhosePicturesAreMissing(t *testing.T) {
	s := open(t)
	it := item(t, lineA, "pic")
	it.Assets = []string{strings.Repeat("f", 64)}
	it.Hash, _ = pushwire.ContentHash(it)
	var miss MissingError
	if _, err := s.Put(1, ada, it, nil); !errors.As(err, &miss) || len(miss.Hashes) != 1 || s.Count(1, it.Hash) != 0 {
		t.Fatalf("got %v", err)
	}
}
