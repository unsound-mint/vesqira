package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before run environments (schema 13) upgrades with its runs untouched and nothing invented for them: an
// empty value means "not observed" and "not stated".
func TestRunEnvironmentMigrationKeepsRunsAndInventsNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE runs(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO runs(id,name) VALUES (1, 'vgt-a'), (2, 'vgt-b')`,
		`PRAGMA user_version = 13`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var n, withEnv, withControls int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(environment<>''),0), COALESCE(SUM(controls<>''),0) FROM runs`).Scan(&n, &withEnv, &withControls); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || n != 2 || withEnv != 0 || withControls != 0 {
		t.Fatalf("version %d, %d runs, %d with an environment, %d with controls", version(t, db), n, withEnv, withControls)
	}
	// a catalog without a runs table needs nothing
	p2 := filepath.Join(t.TempDir(), "d.db")
	db2 := open(t, p2)
	db2.Exec(`CREATE TABLE other(id INTEGER)`)
	db2.Exec(`PRAGMA user_version = 13`)
	if err := Guard(db2, "file:"+p2); err != nil {
		t.Fatal(err)
	}
}
