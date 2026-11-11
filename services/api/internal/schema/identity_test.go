package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before dataset identity (schema 13) upgrades with every dataset and figure kept, every dataset gaining its
// registered file as the primary location with no fingerprint (none was computed, and the empty columns say so), and every
// figure gaining an empty identity (it recorded none).
func TestIdentityMigrationGivesEveryDatasetItsPrimaryLocationAndEveryFigureAnEmptyIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		// the first catalogs had no host_id; the migration adds it before reading it
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL,
			modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/data/a.fits','a.fits',100,1700000000123456789,1700000001,'{}')`,
		`ALTER TABLE datasets ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,host_id) VALUES ('h2:/scratch/b.fits','b.fits',200,1700000000000000000,1700000002,'{}',2)`,
		`CREATE TABLE figures(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, title TEXT NOT NULL, caption TEXT NOT NULL,
			created_at INTEGER NOT NULL, revision TEXT NOT NULL UNIQUE, preview_sha TEXT NOT NULL, width INTEGER NOT NULL,
			height INTEGER NOT NULL, provenance TEXT NOT NULL, scene TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (1,'Peak','',10,'r1','s1',8,6,'{}')`,
		`PRAGMA user_version = 13`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 15 {
		t.Fatalf("version %d", version(t, db))
	}
	type row struct {
		ds, host                         int64
		path, kind, link                 string
		primary                          int
		size, mod, added                 int64
		sampled, full, sampledBy, fullBy string
	}
	rows, err := db.Query(`SELECT dataset_id,host_id,path,kind,link,is_primary,size_bytes,modified_at,added_at,sampled_fp,full_sha256,sampled_by,full_by FROM dataset_locations ORDER BY dataset_id`)
	if err != nil {
		t.Fatalf("the table is missing: %v", err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ds, &r.host, &r.path, &r.kind, &r.link, &r.primary, &r.size, &r.mod, &r.added, &r.sampled, &r.full, &r.sampledBy, &r.fullBy); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []row{
		{1, 0, "/data/a.fits", "registered", "registered", 1, 100, 1700000000123456789, 1700000001, "", "", "", ""},
		{2, 2, "/scratch/b.fits", "registered", "registered", 1, 200, 1700000000000000000, 1700000002, "", "", "", ""},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("locations: %+v", got)
	}
	var title, ident string
	if err := db.QueryRow(`SELECT title, identity FROM figures WHERE revision='r1'`).Scan(&title, &ident); err != nil || title != "Peak" || ident != "" {
		t.Fatalf("figure: %q %q %v", title, ident, err)
	}
	// a file is one location of one dataset only
	if _, err := db.Exec(`INSERT INTO dataset_locations(dataset_id,host_id,path,size_bytes,modified_at,added_at) VALUES (2,0,'/data/a.fits',1,1,1)`); err == nil {
		t.Fatal("the same (host, path) must not be a location twice")
	}
}

// A catalog with neither table (never opened by the stores that own them) needs nothing from this migration.
func TestIdentityMigrationSkipsACatalogWithoutDatasetsOrFigures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{`CREATE TABLE unrelated(x INTEGER)`, `PRAGMA user_version = 13`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version {
		t.Fatalf("version %d", version(t, db))
	}
}
