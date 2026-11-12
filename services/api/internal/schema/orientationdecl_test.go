package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

// A catalog from before the angle frame and quantity (schema 4) upgrades with every interpretation kept and both new
// fields unknown (NULL), so existing declarations keep working for single-map statistics and are refused for cross-dataset
// use until someone declares what the angle is.
func TestOrientationDeclarationMigrationKeepsEveryRowAndLeavesTheNewFieldsUnknown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, path TEXT, name TEXT)`,
		`CREATE TABLE interpretations(dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, kind TEXT NOT NULL, unit TEXT NOT NULL,
			reliability_id INTEGER, declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0,
			declared_by TEXT NOT NULL DEFAULT 'user')`,
		`INSERT INTO datasets VALUES (1,'/d/a.fits','a.fits'),(2,'/d/b.fits','b.fits')`,
		`INSERT INTO interpretations(dataset_id,kind,unit,reliability_id,declared_at,declared_by) VALUES (1,'axial_orientation','rad',2,10,'vgt-run-adapter'),(2,'axial_orientation','deg',NULL,11,'user')`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != toyVersion || Version < 7 {
		t.Fatalf("version %d", version(t, db))
	}
	rows, err := db.Query(`SELECT dataset_id,kind,unit,reliability_id,declared_by,frame,quantity FROM interpretations ORDER BY dataset_id`)
	if err != nil {
		t.Fatalf("the new columns are missing: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id int
		var kind, unit, by string
		var frame, quantity *string
		var relInt *int
		if err := rows.Scan(&id, &kind, &unit, &relInt, &by, &frame, &quantity); err != nil {
			t.Fatal(err)
		}
		if frame != nil || quantity != nil {
			t.Errorf("row %d: an existing declaration must have an unknown frame and quantity, got %v %v", id, frame, quantity)
		}
		got = append(got, strings.Join([]string{kind, unit, by}, "|"))
	}
	if want := "axial_orientation|rad|vgt-run-adapter,axial_orientation|deg|user"; strings.Join(got, ",") != want {
		t.Fatalf("rows changed: %v", got)
	}
	if m, _ := filepath.Glob(p + ".before-schema-*"); len(m) != 1 {
		t.Fatalf("the catalog was not backed up first: %v", m)
	}
	if !strings.Contains(migrations[5].Name, "frame and quantity") || migrations[5].To != 7 {
		t.Fatal("migration 7 is not the orientation one")
	}
}

func TestOrientationDeclarationMigrationSkipsACatalogWithoutInterpretations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	db.Exec(`CREATE TABLE other(id INTEGER)`)
	db.Exec(`PRAGMA user_version = 4`)
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='interpretations'`).Scan(&n)
	if n != 0 {
		t.Fatal("created a table the catalog package owns")
	}
}
