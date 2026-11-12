package schema

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoardItemsMigrationKeepsEveryRowAndAllowsJobs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.db")
	db := open(t, p)
	// the table exactly as schema 1 created it
	db.Exec(`CREATE TABLE board_items(board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '', PRIMARY KEY(board_id, position))`)
	db.Exec(`INSERT INTO board_items VALUES (1,0,7,'first'),(1,1,9,''),(2,0,7,'other board')`)
	db.Exec(`PRAGMA user_version = 1`)
	if err := toyGuard(db, p); err != nil {
		t.Fatal(err)
	}
	if v := version(t, db); v != toyVersion {
		t.Fatalf("version %d", v)
	}
	rows, err := db.Query(`SELECT board_id,position,figure_id,job_id,note FROM board_items ORDER BY board_id,position`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var b, pos int
		var f, j sql.NullInt64
		var note string
		rows.Scan(&b, &pos, &f, &j, &note)
		got = append(got, fmt.Sprintf("%d|%d|%d|%v|%s", b, pos, f.Int64, j.Valid, note))
	}
	rows.Close()
	if want := "1|0|7|false|first,1|1|9|false|,2|0|7|false|other board"; strings.Join(got, ",") != want {
		t.Fatalf("got %v", got)
	}
	if _, err := db.Exec(`INSERT INTO board_items(board_id,position,job_id) VALUES (1,2,5)`); err != nil {
		t.Fatalf("a job item must be allowed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO board_items(board_id,position) VALUES (1,3)`); err == nil {
		t.Fatal("an item with neither figure nor job must be refused")
	}
	if _, err := db.Exec(`INSERT INTO board_items(board_id,position,figure_id,job_id) VALUES (1,4,1,1)`); err == nil {
		t.Fatal("an item with both must be refused")
	}
	if m, _ := filepath.Glob(p + ".before-schema-*"); len(m) != 1 {
		t.Fatalf("the catalog must be backed up first: %v", m)
	}
}

func TestBoardItemsMigrationSkipsACatalogWithoutBoards(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "c.db"))
	db.Exec(`CREATE TABLE datasets(id INTEGER)`)
	db.Exec(`PRAGMA user_version = 1`)
	if err := guard(db, "file:"+filepath.Join(t.TempDir(), "x.db"), toyVersion, migrations); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != toyVersion {
		t.Fatal("not upgraded")
	}
}
