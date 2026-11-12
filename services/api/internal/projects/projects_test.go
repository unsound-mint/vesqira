package projects

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTouchListAndRoots(t *testing.T) {
	s := newStore(t)
	a, err := s.Touch(workspace.Local, "local-owner", 0, "/p/a", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Touch(workspace.Local, "local-owner", 0, "/p/b", "b")
	time.Sleep(1100 * time.Millisecond) // opened_at has second resolution
	again, _ := s.Touch(workspace.Local, "local-owner", 0, "/p/a", "ignored")
	if again.ID != a.ID || again.Name != "a" {
		t.Fatalf("the same folder is the same project: %+v", again)
	}
	list, _ := s.List(workspace.Local, 10)
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("recent order: %+v", list)
	}
	if r, _ := s.Roots(workspace.Local, 0); len(r) != 2 {
		t.Fatalf("%v", r)
	}
	if _, err := s.Touch(workspace.Local, "local-owner", 3, "/p/a", "remote"); err != nil { // the same path on another host is another project
		t.Fatal(err)
	}
	if r, _ := s.Roots(workspace.Local, 0); len(r) != 2 {
		t.Fatalf("a remote project is not a local root: %v", r)
	}
	if _, err := s.Get(workspace.Local, 999); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
}

func TestScanRowsPageAndReplace(t *testing.T) {
	root := t.TempDir()
	for rel, c := range map[string]string{
		"runs/a/run.json": `{"params":{"x":1}}`, "runs/a/out.fits": "x", "runs/b/run.json": `{"params":{"x":2}}`,
		"data/raw/c.fits": "x", "data/raw/d.fits": "x", "configs/c.toml": "a=1",
	} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c), 0o644)
	}
	res := discover.Scan(context.Background(), discover.LocalFS{Root: root}, discover.Options{})
	s := newStore(t)
	p, _ := s.Touch(workspace.Local, "local-owner", 0, root, "p")
	if err := s.SaveScan(workspace.Local, p.ID, time.Now(), []byte(`{"ok":true}`), Rows(res)); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.Children(p.ID, "runs", "runs", 1, 0)
	if err != nil || total != 2 || len(rows) != 1 || rows[0].Name != "a" || rows[0].Kind != "run" {
		t.Fatalf("%v %d %+v", err, total, rows)
	}
	rows, _, _ = s.Children(p.ID, "runs", "runs", 10, 1)
	if len(rows) != 1 || rows[0].Name != "b" {
		t.Fatalf("second page: %+v", rows)
	}
	top, _, _ := s.Children(p.ID, "runs", "", 10, 0)
	if len(top) != 1 || top[0].Kind != "dir" || top[0].Name != "runs" {
		t.Fatalf("%+v", top)
	}
	out, _, _ := s.Children(p.ID, "runs", "runs/a", 10, 0)
	if len(out) != 1 || out[0].Kind != "output" || out[0].Format != "FITS" {
		t.Fatalf("run outputs are entries of the run: %+v", out)
	}
	fits, _ := s.ByFormat(p.ID, "FITS")
	if len(fits) != 3 {
		t.Fatalf("%+v", fits)
	}
	// a rescan replaces the entries; the answers stay
	s.SetAnswer(p.ID, Answer{Need: "n", Answer: "unknown", DeclaredBy: "you", Date: "2026-11-05"})
	if err := s.SaveScan(workspace.Local, p.ID, time.Now(), []byte(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if rows, total, _ := s.Children(p.ID, "runs", "", 10, 0); len(rows) != 0 || total != 0 {
		t.Fatal("entries survived a rescan")
	}
	if a, _ := s.Answers(p.ID); a["n"].Answer != "unknown" {
		t.Fatalf("%+v", a)
	}
}
