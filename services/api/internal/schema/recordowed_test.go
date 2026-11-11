package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before run records (schema 4) upgrades with its jobs kept and none of them owing a record.
func TestRunRecordOwedMigrationKeepsJobsAndOwesNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE jobs(id INTEGER PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL)`,
		`INSERT INTO jobs VALUES (1, 'vgt-a', 'completed'), (2, 'vgt-b', 'running')`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var n, owed int
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(record_owed),0) FROM jobs`).Scan(&n, &owed)
	if version(t, db) != Version || n != 2 || owed != 0 {
		t.Fatalf("version %d, %d jobs, %d owing a record", version(t, db), n, owed)
	}
	// a catalog without a jobs table needs nothing, and a second run is a no-op
	p2 := filepath.Join(t.TempDir(), "d.db")
	db2 := open(t, p2)
	db2.Exec(`CREATE TABLE other(id INTEGER)`)
	db2.Exec(`PRAGMA user_version = 4`)
	if err := Guard(db2, "file:"+p2); err != nil {
		t.Fatal(err)
	}
}
