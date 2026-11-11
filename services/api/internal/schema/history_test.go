package schema

import (
	"path/filepath"
	"testing"
)

// A schema 17 catalog upgrades to the exploration history with its other tables kept; no step is invented.
func TestExplorationHistoryMigrationAddsAnEmptyTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE hosts(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO hosts(id,name) VALUES (1, 'login1')`,
		`PRAGMA user_version = 17`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var hosts, steps int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM hosts), (SELECT COUNT(*) FROM exploration_steps)`).Scan(&hosts, &steps); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 18 || hosts != 1 || steps != 0 {
		t.Fatalf("version %d, %d hosts, %d steps", version(t, db), hosts, steps)
	}
	if _, err := db.Exec(`INSERT INTO exploration_steps(project_id,dataset_id,kind,label,scene,at) VALUES (0,1,'opened','x','{}',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO exploration_steps(project_id) VALUES (0)`); err == nil {
		t.Fatal("a step without a scene was accepted")
	}
}
