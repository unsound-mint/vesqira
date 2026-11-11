package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before figure scenes (schema 8 or earlier) upgrades with every figure kept and no scene: an old figure stays a PNG
// with its provenance, exactly as it was saved.
func TestFigureSceneMigrationKeepsEveryFigureWithoutAScene(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE figures(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, title TEXT NOT NULL, caption TEXT NOT NULL,
			created_at INTEGER NOT NULL, revision TEXT NOT NULL UNIQUE, preview_sha TEXT NOT NULL, width INTEGER NOT NULL,
			height INTEGER NOT NULL, provenance TEXT NOT NULL)`,
		`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (1,'Peak','',10,'r1','s1',8,6,'{"view":{"kind":"plane"}}')`,
		`PRAGMA user_version = 7`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 9 {
		t.Fatalf("version %d", version(t, db))
	}
	var title, prov, scene string
	if err := db.QueryRow(`SELECT title, provenance, scene FROM figures WHERE revision='r1'`).Scan(&title, &prov, &scene); err != nil {
		t.Fatalf("the column is missing: %v", err)
	}
	if title != "Peak" || prov != `{"view":{"kind":"plane"}}` || scene != "" {
		t.Fatalf("figure changed: %q %q %q", title, prov, scene)
	}
}
