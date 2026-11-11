package schema

import (
	"path/filepath"
	"testing"
)

// A schema 13 catalog upgrades to the transfer queue with every existing table kept; no transfer is invented.
func TestTransfersMigrationAddsAnEmptyQueue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE hosts(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO hosts(id,name) VALUES (1, 'login1')`,
		`PRAGMA user_version = 13`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var hosts, transfers int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM hosts), (SELECT COUNT(*) FROM transfers)`).Scan(&hosts, &transfers); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 14 || hosts != 1 || transfers != 0 {
		t.Fatalf("version %d, %d hosts, %d transfers", version(t, db), hosts, transfers)
	}
	// the table is usable and refuses a row without its required columns
	if _, err := db.Exec(`INSERT INTO transfers(host_id,direction,source_path,dest_dir,name,policy,state,created_at) VALUES (1,'download','/a','/b','a','ask','queued',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transfers(host_id) VALUES (1)`); err == nil {
		t.Fatal("a transfer without a source was accepted")
	}
}
