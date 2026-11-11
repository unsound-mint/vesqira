package schema

import (
	"path/filepath"
	"testing"
)

// A schema 19 catalog upgrades to the offline store with its other tables kept; no copy is invented and the size setting starts unset.
func TestOfflineMigrationAddsEmptyTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE hosts(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO hosts(id,name) VALUES (1, 'login1')`,
		`PRAGMA user_version = 19`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var hosts, entries, settings int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM hosts), (SELECT COUNT(*) FROM offline_entries), (SELECT COUNT(*) FROM offline_settings)`).Scan(&hosts, &entries, &settings); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 22 || hosts != 1 || entries != 0 || settings != 0 {
		t.Fatalf("version %d, %d hosts, %d entries, %d settings", version(t, db), hosts, entries, settings)
	}
	ins := `INSERT INTO offline_entries(dataset_id,host_id,size,mtime,op,channel,step,x,y,file,bytes,sha256,taken_at) VALUES (1,1,10,20,'plane',3,1,0,0,'1/a.bin',5,'ab',1)`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("the same reply of the same file signature was kept twice")
	}
	if _, err := db.Exec(`INSERT INTO offline_settings(id,cap_bytes) VALUES (2, 1)`); err == nil {
		t.Fatal("a second settings row was accepted")
	}
}
