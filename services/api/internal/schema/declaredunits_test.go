package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A catalog from before declared units (schema 3) upgrades with every dataset kept, gets the overlay table, and is backed
// up first; the new table cascades with its dataset, and a catalog that never had datasets needs nothing.
func TestDeclaredUnitsMigrationKeepsEveryDatasetAndAddsTheOverlay(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.db")
	db := open(t, p)
	db.Exec(`PRAGMA foreign_keys = ON`)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, path TEXT, name TEXT)`,
		`INSERT INTO datasets VALUES (1, '/d/a.fits', 'a.fits'), (2, '/d/b.fits', 'b.fits')`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 4 {
		t.Fatalf("version %d", version(t, db))
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n)
	if n != 2 {
		t.Fatalf("datasets were lost: %d", n)
	}
	if _, err := db.Exec(`INSERT INTO dataset_units(dataset_id,unit,declared_at) VALUES (1,'Jy/beam',1)`); err != nil {
		t.Fatalf("the overlay table was not created: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM datasets WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM dataset_units`).Scan(&n)
	if n != 0 {
		t.Fatal("a declaration outlived its dataset")
	}
	if m, _ := filepath.Glob(p + ".before-schema-*"); len(m) != 1 {
		t.Fatalf("the catalog was not backed up first: %v", m)
	} else if st, _ := os.Stat(m[0]); st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the backup is readable by others: %v", st.Mode())
	}
}

func TestDeclaredUnitsMigrationSkipsACatalogWithoutDatasets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	db.Exec(`CREATE TABLE other(id INTEGER)`)
	db.Exec(`PRAGMA user_version = 3`)
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='dataset_units'`).Scan(&n)
	if n != 0 {
		t.Fatal("created a table next to nothing it can refer to")
	}
	if !strings.Contains(migrations[2].Name, "declared units") || migrations[2].To != 4 {
		t.Fatal("migration 4 is not the declared-units one")
	}
}
