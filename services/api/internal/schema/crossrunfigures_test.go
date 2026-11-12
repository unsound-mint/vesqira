package schema

import (
	"path/filepath"
	"testing"
)

// Schema 27 lets a figure have no dataset (a cross-run plot, ADR 0065). SQLite cannot relax a NOT NULL in place, so the table is
// rebuilt: every figure keeps its id, revision, picture and text, becomes kind 'dataset', and an id that was once handed out is
// not handed out again. The table's CHECK keeps the two kinds apart.
func TestCrossRunFiguresMigrationKeepsEveryFigureAndItsIds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE figures(id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, title TEXT NOT NULL, caption TEXT NOT NULL,
			created_at INTEGER NOT NULL, revision TEXT NOT NULL UNIQUE, preview_sha TEXT NOT NULL, width INTEGER NOT NULL,
			height INTEGER NOT NULL, provenance TEXT NOT NULL, scene TEXT NOT NULL DEFAULT '', identity TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity) VALUES (1,'Peak','c',10,'r1','s1',8,6,'{"view":{"kind":"plane"}}','{"schema":"scene/1"}','{"x":1}')`,
		`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (2,'Spectrum','',11,'r2','s2',9,7,'{}')`,
		`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (2,'Gone','',12,'r3','s3',9,7,'{}')`,
		`DELETE FROM figures WHERE id=3`, // the newest id was used and deleted: the counter stays at 3
		`PRAGMA user_version = 27`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 28 {
		t.Fatalf("version %d", version(t, db))
	}
	var title, scene, identity, kind, rev string
	var ds int64
	if err := db.QueryRow(`SELECT id, title, scene, identity, kind, revision FROM figures WHERE id=1`).Scan(&ds, &title, &scene, &identity, &kind, &rev); err != nil {
		t.Fatal(err)
	}
	if ds != 1 || title != "Peak" || scene != `{"schema":"scene/1"}` || identity != `{"x":1}` || kind != "dataset" || rev != "r1" {
		t.Fatalf("figure changed: %d %q %q %q %q %q", ds, title, scene, identity, kind, rev)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM figures WHERE kind='dataset' AND dataset_id IS NOT NULL`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d figures kept, %v", n, err)
	}
	// a cross-run plot has no dataset, and gets an id that was never used
	res, err := db.Exec(`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene) VALUES (NULL,'crossrun','Plot','',13,'r4','s4',9,7,'{}','{"schema":"cross-run/1"}')`)
	if err != nil {
		t.Fatalf("a figure with no dataset is refused: %v", err)
	}
	if id, _ := res.LastInsertId(); id != 4 {
		t.Errorf("new id %d, want 4: an id handed out before the migration must not be reused", id)
	}
	// the two kinds are kept apart by the table itself
	for label, q := range map[string]string{
		"a dataset figure without a dataset": `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (NULL,'dataset','x','',1,'r5','s',1,1,'{}')`,
		"a cross-run plot with a dataset":    `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (5,'crossrun','x','',1,'r6','s',1,1,'{}')`,
		"an unknown kind":                    `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (5,'other','x','',1,'r7','s',1,1,'{}')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s was accepted", label)
		}
	}
}

// A catalog with no figures table yet (never opened by the boards package) migrates without one; the boards package creates it.
func TestCrossRunFiguresMigrationNeedsNoTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	if _, err := db.Exec(`PRAGMA user_version = 27`); err != nil {
		t.Fatal(err)
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version {
		t.Fatalf("version %d", version(t, db))
	}
}
