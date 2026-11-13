package schema

import (
	"path/filepath"
	"testing"
)

// Schema 36 lets a figure be a sky view (ADR 0072). The kind list is a CHECK, so the table is rebuilt: every figure keeps its id,
// revision, picture, text, kind and workspace, an id once handed out is not handed out again, and the table still keeps the kinds
// apart.
func TestSkyFiguresMigrationKeepsEveryFigureAndItsIds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE figures(
			id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER, title TEXT NOT NULL, caption TEXT NOT NULL,
			created_at INTEGER NOT NULL, revision TEXT NOT NULL, preview_sha TEXT NOT NULL, width INTEGER NOT NULL,
			height INTEGER NOT NULL, provenance TEXT NOT NULL, scene TEXT NOT NULL DEFAULT '', identity TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'dataset' CHECK(kind IN ('dataset','crossrun')),
			workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
			CHECK((kind='dataset') = (dataset_id IS NOT NULL)),
			UNIQUE(workspace_id, revision))`,
		`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity,workspace_id,created_by) VALUES (1,'dataset','Peak','c',10,'r1','s1',8,6,'{"view":{"kind":"plane"}}','{"schema":"scene/1"}','{"x":1}',2,'ana')`,
		`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene) VALUES (NULL,'crossrun','Plot','',11,'r2','s2',9,7,'{}','{"schema":"cross-run/1"}')`,
		`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (3,'dataset','Gone','',12,'r3','s3',9,7,'{}')`,
		`DELETE FROM figures WHERE id=3`, // the newest id was used and deleted: the counter stays at 3
		`PRAGMA user_version = 35`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 36 {
		t.Fatalf("version %d", version(t, db))
	}
	var title, scene, identity, kind, rev, by string
	var ws int64
	if err := db.QueryRow(`SELECT title, scene, identity, kind, revision, workspace_id, created_by FROM figures WHERE id=1`).Scan(&title, &scene, &identity, &kind, &rev, &ws, &by); err != nil {
		t.Fatal(err)
	}
	if title != "Peak" || scene != `{"schema":"scene/1"}` || identity != `{"x":1}` || kind != "dataset" || rev != "r1" || ws != 2 || by != "ana" {
		t.Fatalf("figure changed: %q %q %q %q %q %d %q", title, scene, identity, kind, rev, ws, by)
	}
	if err := db.QueryRow(`SELECT kind FROM figures WHERE id=2`).Scan(&kind); err != nil || kind != "crossrun" {
		t.Fatalf("the cross-run plot is %q, %v", kind, err)
	}
	res, err := db.Exec(`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene) VALUES (NULL,'sky','View','',13,'r4','s4',9,7,'{}','{"schema":"sky-view/1"}')`)
	if err != nil {
		t.Fatalf("a sky figure is refused: %v", err)
	}
	if id, _ := res.LastInsertId(); id != 4 {
		t.Errorf("new id %d, want 4: an id handed out before the migration must not be reused", id)
	}
	for label, q := range map[string]string{
		"a sky figure with a dataset":        `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (5,'sky','x','',1,'r5','s',1,1,'{}')`,
		"a dataset figure without a dataset": `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (NULL,'dataset','x','',1,'r6','s',1,1,'{}')`,
		"an unknown kind":                    `INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES (NULL,'other','x','',1,'r7','s',1,1,'{}')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s was accepted", label)
		}
	}
}
