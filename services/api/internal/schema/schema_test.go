package schema

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFreshCatalogIsStampedWithoutABackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.db")
	db := open(t, p)
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version {
		t.Fatal("a new catalog must be stamped with the current version")
	}
	if m, _ := filepath.Glob(p + ".before-schema-*"); len(m) != 0 {
		t.Fatalf("a new catalog has nothing to back up: %v", m)
	}
}

func TestNewerCatalogIsRefusedAndLeftAlone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	db.Exec(`CREATE TABLE datasets(id INTEGER)`)
	db.Exec(`PRAGMA user_version = 99`)
	err := Guard(db, "file:"+p)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("got %v", err)
	}
	if version(t, db) != 99 {
		t.Fatal("a refused catalog must not be restamped")
	}
}

func TestUpgradeBacksUpFirstAndRollsBackAFailedStep(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.db")
	db := open(t, p)
	db.Exec(`CREATE TABLE datasets(id INTEGER, name TEXT)`)
	db.Exec(`INSERT INTO datasets VALUES (1, 'a.fits')`)
	db.Exec(`PRAGMA user_version = 1`)
	steps := []Migration{
		{To: 2, Name: "adds a column", Up: func(tx *sql.Tx) error { _, err := tx.Exec(`ALTER TABLE datasets ADD COLUMN extra TEXT`); return err }},
		{To: 3, Name: "fails", Up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM datasets`); err != nil {
				return err
			}
			return errors.New("boom")
		}},
	}
	err := guard(db, "file:"+p, 3, steps)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("got %v", err)
	}
	if v := version(t, db); v != 2 {
		t.Fatalf("the completed step stays, the failed one does not: version %d", v)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n)
	if n != 1 {
		t.Fatal("the failed step must not have deleted anything")
	}
	m, _ := filepath.Glob(p + ".before-schema-3-*")
	if len(m) != 1 {
		t.Fatalf("expected one backup, got %v", m)
	}
	bk := open(t, m[0])
	var name string
	if err := bk.QueryRow(`SELECT name FROM datasets`).Scan(&name); err != nil || name != "a.fits" {
		t.Fatalf("the backup must hold the catalog as it was: %v %q", err, name)
	}
	if st, _ := os.Stat(m[0]); st.Mode().Perm() != 0o600 {
		t.Errorf("backup permissions %v", st.Mode().Perm())
	}
}

func TestAddColumnIsIdempotentAndReportsRealErrors(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "c.db"))
	db.Exec(`CREATE TABLE t(a INTEGER)`)
	for i := 0; i < 2; i++ {
		if err := AddColumn(db, "t", "b", "TEXT NOT NULL DEFAULT ''"); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddColumn(db, "t", "c", "NOT A TYPE ((("); err == nil {
		t.Fatal("a failing ALTER must not be swallowed")
	}
}
