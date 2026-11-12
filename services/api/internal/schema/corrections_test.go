package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

// A catalog from before corrections upgrades with every dataset kept and an empty, append-only history: a row can be added and
// deleted with its dataset, never edited (a revert is a new row).
func TestCorrectionsMigrationAddsAnAppendOnlyHistory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/d/a.fits','a.fits',10,1,1,'{}')`,
		`PRAGMA user_version = 21`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 23 {
		t.Fatalf("version %d", version(t, db))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("datasets %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM dataset_corrections`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the history is missing or not empty: %d %v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_corrections(dataset_id,action,state,changes,reason,author,created_at,size_bytes,modified_ns) VALUES (1,'set','{}','[]','a reason','user',1,10,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE dataset_corrections SET reason='rewritten'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("a record could be edited: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM dataset_corrections WHERE dataset_id=1`); err != nil {
		t.Fatalf("records must go with their dataset: %v", err)
	}
}
