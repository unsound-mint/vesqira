package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before point catalogs (schema 9) upgrades with every dataset and declaration kept, gets the overlay table,
// and is backed up first; the table cascades with its dataset.
func TestCatalogDeclarationsMigrationKeepsEveryDatasetAndAddsTheOverlay(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	db.Exec(`PRAGMA foreign_keys = ON`)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, path TEXT, name TEXT)`,
		`INSERT INTO datasets VALUES (1, '/d/a.fits', 'a.fits'), (2, '/d/b.fits', 'b.fits')`,
		`CREATE TABLE interpretations(dataset_id INTEGER PRIMARY KEY, kind TEXT, unit TEXT, frame TEXT, quantity TEXT)`,
		`INSERT INTO interpretations VALUES (1, 'axial_orientation', 'rad', 'pixel', 'b_field')`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != toyVersion || Version < 10 {
		t.Fatalf("version %d", version(t, db))
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n)
	if n != 2 {
		t.Fatalf("datasets were lost: %d", n)
	}
	var frame string
	db.QueryRow(`SELECT frame FROM interpretations WHERE dataset_id=1`).Scan(&frame)
	if frame != "pixel" {
		t.Fatal("an existing declaration was changed")
	}
	if _, err := db.Exec(`INSERT INTO catalog_declarations(dataset_id,declaration,declared_at) VALUES (1,'{}',1)`); err != nil {
		t.Fatalf("the overlay table was not created: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM datasets WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM catalog_declarations`).Scan(&n)
	if n != 0 {
		t.Fatal("a declaration outlived its dataset")
	}
	if m, _ := filepath.Glob(p + ".before-schema-*"); len(m) != 1 {
		t.Fatalf("the catalog was not backed up first: %v", m)
	}
}

func TestCatalogDeclarationsMigrationSkipsACatalogWithoutDatasets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	db.Exec(`CREATE TABLE other(id INTEGER)`)
	db.Exec(`PRAGMA user_version = 9`)
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='catalog_declarations'`).Scan(&n)
	if n != 0 || version(t, db) != toyVersion {
		t.Fatalf("created for a catalog with no datasets, or not upgraded: %d, %d", n, version(t, db))
	}
}
