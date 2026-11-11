package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before job usage (schema 10) upgrades with its jobs kept and no usage invented for them: an empty value
// means "not recorded".
func TestJobUsageMigrationKeepsJobsAndInventsNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE jobs(id INTEGER PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL)`,
		`INSERT INTO jobs(id,name,state) VALUES (1, 'vgt-a', 'completed'), (2, 'vgt-b', 'running')`,
		`PRAGMA user_version = 10`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var n, withUsage int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(usage<>''),0) FROM jobs`).Scan(&n, &withUsage); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || n != 2 || withUsage != 0 {
		t.Fatalf("version %d, %d jobs, %d with usage", version(t, db), n, withUsage)
	}
	// a catalog without a jobs table needs nothing
	p2 := filepath.Join(t.TempDir(), "d.db")
	db2 := open(t, p2)
	db2.Exec(`CREATE TABLE other(id INTEGER)`)
	db2.Exec(`PRAGMA user_version = 10`)
	if err := Guard(db2, "file:"+p2); err != nil {
		t.Fatal(err)
	}
}
