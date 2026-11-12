package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before masks on a host upgrades with every mask kept and recorded as on this computer (masks were made only for files
// read here until ADR 0064); a new row may name a host.
func TestMasksHostMigrationKeepsMasksOnThisComputer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/d/a.fits','a.fits',10,1,1,'{}')`,
		`CREATE TABLE masks(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, type TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
			width INTEGER NOT NULL, height INTEGER NOT NULL, channels INTEGER NOT NULL, flagged INTEGER NOT NULL, total INTEGER NOT NULL, tier TEXT NOT NULL, mapping_version TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL, rule TEXT NOT NULL, meaning TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL, source_size INTEGER NOT NULL, source_mtime INTEGER NOT NULL,
			source_identity TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL, file_mtime INTEGER NOT NULL, sha256 TEXT NOT NULL)`,
		`INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,method,rule,author,created_at,source_size,source_mtime,file_size,file_mtime,sha256)
			VALUES (1,'missing','No value','/derived/mask-1-missing-0a.fits',4,3,0,1,12,'verified','computed','{}','analysis',1,10,1,100,1,'ab')`,
		`PRAGMA user_version = 25`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 26 {
		t.Fatalf("version %d", version(t, db))
	}
	var host, n int
	if err := db.QueryRow(`SELECT host_id FROM masks WHERE id=1`).Scan(&host); err != nil || host != 0 {
		t.Fatalf("the existing mask: host %d %v", host, err)
	}
	if _, err := db.Exec(`INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,method,rule,author,created_at,source_size,source_mtime,file_size,file_mtime,sha256,host_id)
		VALUES (1,'saturated','Sat','/cluster/data/vesqira_derived/mask-1-saturated-0b.fits',4,3,0,1,12,'declared','computed','{}','user',1,10,1,100,1,'cd',7)`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM masks WHERE host_id=7`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("a mask on a host: %d %v", n, err)
	}
}
