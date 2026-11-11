package notify

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAnEventKeyedTwiceIsRecordedOnce(t *testing.T) {
	s := open(t)
	j := int64(3)
	for i, want := range []bool{true, false} {
		got, err := s.Add("job:3:end", Notification{Kind: KindCompleted, Title: "t", Body: "b", JobID: &j})
		if err != nil || got != want {
			t.Fatalf("add %d: %v %v", i, got, err)
		}
	}
	ns, unread, _ := s.List(0)
	if len(ns) != 1 || unread != 1 || ns[0].JobID == nil || *ns[0].JobID != 3 || ns[0].Read {
		t.Fatalf("%+v unread %d", ns, unread)
	}
}

func TestReadMarksAreSeparateFromTheListAndOldEventsAreDropped(t *testing.T) {
	s := open(t)
	for i := 0; i < Keep+10; i++ {
		s.Add(fmt.Sprint("k", i), Notification{Kind: KindFailed, Title: "t\nwith a newline", Body: "b"})
	}
	ns, unread, _ := s.List(Keep)
	if len(ns) != Keep || unread != Keep {
		t.Fatalf("kept %d, unread %d", len(ns), unread)
	}
	if ns[0].Title != "t with a newline" {
		t.Errorf("control characters stay in the text: %q", ns[0].Title)
	}
	s.MarkRead([]int64{ns[0].ID}, false)
	if _, unread, _ = s.List(1); unread != Keep-1 {
		t.Errorf("unread after one: %d", unread)
	}
	s.MarkRead(nil, true)
	if _, unread, _ = s.List(1); unread != 0 {
		t.Errorf("unread after all: %d", unread)
	}
	var nilStore *Store
	if ok, err := nilStore.Add("x", Notification{}); ok || err != nil {
		t.Errorf("a nil store must record nothing quietly")
	}
}
