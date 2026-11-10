package catalog

import (
	"path/filepath"
	"testing"
)

// The schema relies on REFERENCES ... ON DELETE CASCADE. SQLite ignores both unless foreign_keys is on for the
// connection, so this guards the setting, and the cascade it exists for.
func TestCatalogEnforcesForeignKeysAndUsesWAL(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fk int
	var mode string
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
	}
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO datasets(id,path,name,size_bytes,modified_at,added_at,header) VALUES(1,'p','n',0,0,0,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO interpretations(dataset_id,kind,unit,declared_at) VALUES(1,'scalar','K',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM datasets WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM interpretations`).Scan(&n)
	if n != 0 {
		t.Fatalf("deleting a dataset left %d orphan interpretation rows: foreign keys are not enforced", n)
	}
}

func TestWithPragmasHandlesEveryDSNShape(t *testing.T) {
	for in, want := range map[string]string{
		"c.db":                   "file:c.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		"file:c.db?mode=rwc":     "file:c.db?mode=rwc&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		":memory:":               "file::memory:?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		"file:x?mode=memory&a=b": "file:x?mode=memory&a=b&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
	} {
		if got := WithPragmas(in); got != want {
			t.Errorf("WithPragmas(%q) = %q, want %q", in, got, want)
		}
	}
}
