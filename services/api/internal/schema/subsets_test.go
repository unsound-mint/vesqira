package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

// A catalog from before virtual subsets upgrades with every dataset kept and no subsets: a version row can be added and removed with
// their subset (the delete steps do it; cascades are off here), never edited (an edit or a re-pin is a new version), and subsets go with their source dataset.
func TestSubsetsMigrationAddsAnAppendOnlyHistory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/d/a.fits','a.fits',10,1,1,'{}')`,
		`PRAGMA user_version = 23`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 24 {
		t.Fatalf("version %d", version(t, db))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("datasets %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM subsets`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the subsets table is missing or not empty: %d %v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO subsets(id,source_id,created_at) VALUES (1,1,1)`,
		`INSERT INTO subset_versions(subset_id,version,kind,name,reason,definition,source_size,source_mtime_ns,author,created_at) VALUES (1,1,'create','n','r','{}',10,1,'user',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE subset_versions SET reason='rewritten'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("a version could be edited: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM subset_versions WHERE subset_id=1`); err != nil {
		t.Fatalf("versions must go with their subset: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM subsets WHERE id=1`); err != nil {
		t.Fatal(err)
	}
}
