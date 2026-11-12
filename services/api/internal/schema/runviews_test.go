package schema

import (
	"path/filepath"
	"testing"
)

// A schema 22 catalog upgrades to the saved run views with its runs kept; no view is invented, and a name is unique without regard to case.
func TestRunViewsMigrationAddsAnEmptyTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE runs(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO runs(id,name) VALUES (1, 'r1')`,
		`PRAGMA user_version = 22`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	var runs, views int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM runs), (SELECT COUNT(*) FROM run_views)`).Scan(&runs, &views); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != toyVersion || Version < 24 || runs != 1 || views != 0 {
		t.Fatalf("version %d, %d runs, %d views", version(t, db), runs, views)
	}
	ins := `INSERT INTO run_views(name,spec,updated_at) VALUES (?, '{}', 1)`
	if _, err := db.Exec(ins, "Sweep"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins, "sweep"); err == nil {
		t.Fatal("two views of the same name (another case) were kept")
	}
}
