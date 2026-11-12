package outbox

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	dir := filepath.Join(t.TempDir(), "outbox")
	s, err := Open(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return s, dir
}

// Linking again to the same server keeps the instance (lineages continue); another server starts a new instance and empties the
// queue, so nothing frozen for one server goes to another. Queued pictures are files, removed once nothing waits for them.
func TestLinkQueueAndPictures(t *testing.T) {
	s, dir := open(t)
	if _, err := s.Link(); err != ErrNotLinked {
		t.Fatal(err)
	}
	a, err := s.SetLink("https://lab.example.org", "Lab")
	if err != nil || len(a.Instance) != 32 || len(a.Secret) != 32 {
		t.Fatal(a, err)
	}
	e, err := s.Enqueue(1, "figure", 7, "o1", "Peak", "h1", []byte(`{"x":1}`), map[string][]byte{"aa": []byte("png")})
	if err != nil || e.State != Waiting {
		t.Fatal(e, err)
	}
	if again, _ := s.Enqueue(1, "figure", 7, "o1", "Peak", "h1", []byte(`{"x":1}`), nil); again.ID != e.ID {
		t.Fatal("the same hash was queued twice")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "aa")); err != nil || string(b) != "png" {
		t.Fatal("the picture is not waiting as a file")
	}
	if b, _ := s.SetLink("https://lab.example.org", "Lab renamed"); b.Instance != a.Instance {
		t.Fatal("relinking to the same server changed the instance")
	}
	if err := s.MarkSent(e.ID, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aa")); !os.IsNotExist(err) {
		t.Fatal("a sent item's picture is still kept")
	}
	if k, id, err := s.Origin("o1"); err != nil || k != "figure" || id != 7 {
		t.Fatal("the origin key does not lead back to the local object", k, id, err)
	}
	s.Enqueue(1, "run", 8, "o2", "Run", "h2", []byte(`{}`), nil)
	c, _ := s.SetLink("https://other.example.org", "")
	if c.Instance == a.Instance {
		t.Fatal("another server kept the instance")
	}
	if es, _ := s.List(1); len(es) != 0 {
		t.Fatalf("the queue survived a change of server: %+v", es)
	}
}
