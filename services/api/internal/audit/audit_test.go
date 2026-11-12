package audit

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "a.db")+"?_pragma=foreign_keys(1)")
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

// Local mode records every entry for the local owner, whichever call site wrote it.
func TestRecordAttributesEntriesToTheDefaultActor(t *testing.T) {
	s := open(t)
	s.Record("host added", "host", 1, "h")
	s.RecordAs(Actor{ID: "system", OnBehalfOf: "acct-3", Via: "system"}, "sweep job submitted", "sweep", 2, "j")
	es, err := s.List("", 0, 10)
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if e := es[1]; e.Actor != "local-owner" || e.OnBehalfOf != "" || e.Via != "local" {
		t.Errorf("default entry %+v", e)
	}
	if e := es[0]; e.Actor != "system" || e.OnBehalfOf != "acct-3" || e.Via != "system" {
		t.Errorf("explicit entry %+v", e)
	}
}

func TestNilStoreRecordsNothing(t *testing.T) {
	var s *Store
	s.Record("x", "host", 0, "")
	s.RecordAs(LocalOwner, "x", "host", 0, "")
}
