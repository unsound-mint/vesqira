package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before sweeps (schema 12) upgrades with its jobs untouched and gains the two empty sweep tables.
func TestSweepsMigrationKeepsJobsAndAddsEmptyTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE jobs(id INTEGER PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL, usage TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO jobs(id,name,state) VALUES (1, 'vgt-a', 'completed'), (2, 'vgt-b', 'draft')`,
		`PRAGMA user_version = 12`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var jobsN, sweepsN, memberN int
	for q, dst := range map[string]*int{`SELECT COUNT(*) FROM jobs`: &jobsN, `SELECT COUNT(*) FROM sweeps`: &sweepsN, `SELECT COUNT(*) FROM sweep_jobs`: &memberN} {
		if err := db.QueryRow(q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if version(t, db) != Version || jobsN != 2 || sweepsN != 0 || memberN != 0 {
		t.Fatalf("version %d, %d jobs, %d sweeps, %d members", version(t, db), jobsN, sweepsN, memberN)
	}
}
