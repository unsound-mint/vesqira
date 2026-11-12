package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before schema 27 upgrades with every location kept and its file identity recorded as 0 (not recorded): the server
// compares an identity only once it has one, and records it the first time the file still matches its size and time.
func TestLocationFileIDMigrationKeepsLocationsWithNoIdentityRecorded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/d/a.fits','a.fits',10,1,1,'{}')`,
		`CREATE TABLE dataset_locations(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, host_id INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'registered', is_primary INTEGER NOT NULL DEFAULT 0, link TEXT NOT NULL DEFAULT 'registered', size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, sampled_fp TEXT NOT NULL DEFAULT '', sampled_at INTEGER NOT NULL DEFAULT 0, sampled_by TEXT NOT NULL DEFAULT '', full_sha256 TEXT NOT NULL DEFAULT '', full_at INTEGER NOT NULL DEFAULT 0, full_by TEXT NOT NULL DEFAULT '', UNIQUE(host_id, path))`,
		`INSERT INTO dataset_locations(dataset_id,path,is_primary,size_bytes,modified_at,added_at) VALUES (1,'/d/a.fits',1,10,1,1)`,
		`PRAGMA user_version = 26`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 27 {
		t.Fatalf("version %d", version(t, db))
	}
	var dev, ino int64
	if err := db.QueryRow(`SELECT file_dev, file_ino FROM dataset_locations WHERE id=1`).Scan(&dev, &ino); err != nil || dev != 0 || ino != 0 {
		t.Fatalf("the existing location: %d %d %v", dev, ino, err)
	}
}
