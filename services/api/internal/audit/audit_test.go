package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
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
	s.Record(workspace.Local, "host added", "host", 1, "h")
	s.RecordAs(workspace.Local, Actor{ID: "system", OnBehalfOf: "acct-3", Via: "system"}, "sweep job submitted", "sweep", 2, "j")
	es, err := s.List(workspace.Local, "", 0, 10)
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
	s.Record(workspace.Local, "x", "host", 0, "")
	s.RecordAs(workspace.Local, LocalOwner, "x", "host", 0, "")
	s.RecordCtx(context.Background(), "x", "host", 0, "")
}

// RecordCtx writes the actor the request's context carries, and the default when it carries none.
func TestRecordCtxTakesTheRequestsActor(t *testing.T) {
	s := open(t)
	s.Default = System
	s.Default.Workspace = workspace.Local // an entry without an actor lands in the default actor's workspace
	s.RecordCtx(WithActor(context.Background(), Actor{ID: "acct-1", Via: "session", Workspace: workspace.Local}), "unit declared", "dataset", 1, "")
	s.RecordCtx(context.Background(), "full sha256 computed", "dataset", 1, "")
	es, _ := s.List(workspace.Local, "", 0, 10)
	if len(es) != 2 || es[1].Actor != "acct-1" || es[1].Via != "session" || es[0].Actor != "system" {
		t.Fatalf("%+v", es)
	}
}
