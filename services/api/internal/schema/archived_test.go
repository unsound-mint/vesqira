package schema

import (
	"path/filepath"
	"testing"
)

// A version 4 catalog upgrades to the archive table with every row kept; nothing is archived by the upgrade.
func TestArchivedEntriesMigrationAddsTheTableAndKeepsEveryRow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, path TEXT, name TEXT)`,
		`INSERT INTO datasets VALUES (1, '/d/a.fits', 'a.fits')`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != toyVersion || Version < 6 {
		t.Fatalf("version %d", version(t, db))
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n)
	if n != 1 {
		t.Fatalf("datasets were lost: %d", n)
	}
	db.QueryRow(`SELECT COUNT(*) FROM archived_entries`).Scan(&n)
	if n != 0 {
		t.Fatal("the upgrade archived something")
	}
	if err := EnsureArchive(db); err != nil { // a fresh install and an upgrade end with the same table
		t.Fatal(err)
	}
}
