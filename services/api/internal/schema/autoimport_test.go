package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before automatic import (schema 5) upgrades with its jobs kept, none of them set to import by themselves
// and none marked as already tried.
func TestAutoImportMigrationKeepsJobsAndImportsNothingByItself(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE jobs(id INTEGER PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL, record_owed INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO jobs(id,name,state) VALUES (1, 'vgt-a', 'completed'), (2, 'vgt-b', 'running')`,
		`PRAGMA user_version = 5`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var n, on, tried int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(auto_import),0), COALESCE(SUM(auto_import_tried),0) FROM jobs`).Scan(&n, &on, &tried); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || n != 2 || on != 0 || tried != 0 {
		t.Fatalf("version %d, %d jobs, %d importing by themselves, %d tried", version(t, db), n, on, tried)
	}
	// a catalog without a jobs table needs nothing
	p2 := filepath.Join(t.TempDir(), "d.db")
	db2 := open(t, p2)
	db2.Exec(`CREATE TABLE other(id INTEGER)`)
	db2.Exec(`PRAGMA user_version = 5`)
	if err := Guard(db2, "file:"+p2); err != nil {
		t.Fatal(err)
	}
}
