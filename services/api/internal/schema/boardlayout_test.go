package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before the board layout grid upgrades with every panel kept and no place on the grid: the board has no layout
// until the researcher arranges it.
func TestBoardLayoutMigrationKeepsPanelsWithoutAPlace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE board_items(board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '', PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)))`,
		`INSERT INTO board_items(board_id,position,figure_id,note) VALUES (1,0,7,'first'),(1,1,9,'')`,
		`PRAGMA user_version = 20`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 21 {
		t.Fatalf("version %d", version(t, db))
	}
	var note string
	var x, y, w, h *int
	if err := db.QueryRow(`SELECT note, layout_x, layout_y, layout_w, layout_h FROM board_items WHERE position=0`).Scan(&note, &x, &y, &w, &h); err != nil {
		t.Fatalf("the columns are missing: %v", err)
	}
	if note != "first" || x != nil || y != nil || w != nil || h != nil {
		t.Fatalf("panel changed: %q %v %v %v %v", note, x, y, w, h)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM board_items`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows %d %v", n, err)
	}
}
