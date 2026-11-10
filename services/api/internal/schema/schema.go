// Package schema versions the catalog database. The version lives in SQLite's own user_version. A catalog written by
// a newer program is refused rather than half-understood, and an older one is copied aside before any migration runs.
package schema

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// Version is the schema this program writes. Version 1 is the catalog as shipped through v0.2; the tables are still
// created idempotently by the packages that own them, so a migration is only needed when an existing table changes.
// Version 2: a board item may follow a job instead of holding a figure.
// Version 3: reports sit in a tree of pages (reports.parent_id); a reader's comment may quote a phrase
// (publish_comments.quote).
const Version = 3

// A Migration upgrades a catalog from version From-1 to From. Steps run in order, each in its own transaction.
type Migration struct {
	To   int
	Name string
	Up   func(tx *sql.Tx) error
}

var migrations = []Migration{
	{To: 2, Name: "board items may follow a job", Up: boardItemsMayFollowAJob},
	{To: 3, Name: "report pages and quoted comments", Up: reportPagesAndQuotes},
}

// reportPagesAndQuotes adds two columns with defaults that keep every existing row meaning what it meant: a report at
// the top level, a comment that quotes nothing. Tables a catalog never created need nothing.
func reportPagesAndQuotes(tx *sql.Tx) error {
	for _, c := range []struct{ table, column, def string }{
		{"reports", "parent_id", "INTEGER NOT NULL DEFAULT 0"},
		{"publish_comments", "quote", "TEXT NOT NULL DEFAULT ''"},
	} {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, c.table).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := AddColumn(tx, c.table, c.column, c.def); err != nil {
			return err
		}
	}
	return nil
}

// boardItemsMayFollowAJob rebuilds board_items so figure_id is optional and job_id exists; exactly one of them is set.
// SQLite cannot relax a NOT NULL in place, so the table is copied. Every row keeps its position and note. A catalog
// without the table (never opened by the boards package) needs nothing.
func boardItemsMayFollowAJob(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='board_items'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS board_items_v2`,
		`CREATE TABLE board_items_v2(
			board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)))`,
		`INSERT INTO board_items_v2(board_id,position,figure_id,note) SELECT board_id,position,figure_id,note FROM board_items`,
		`DROP TABLE board_items`,
		`ALTER TABLE board_items_v2 RENAME TO board_items`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// Guard checks the catalog's version before anything else touches it. dsn is the database file, used to place the
// backup; an in-memory database has nothing to back up.
func Guard(db *sql.DB, dsn string) error {
	return guard(db, dsn, Version, migrations)
}

func guard(db *sql.DB, dsn string, target int, steps []Migration) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > target {
		return fmt.Errorf("this catalog uses schema %d, newer than this program (%d): start the newer version, or restore a backup made by this one", v, target)
	}
	if v == target {
		return nil
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 { // an existing catalog: keep a copy of it before changing anything
		if path := filePath(dsn); path != "" {
			backup := fmt.Sprintf("%s.before-schema-%d-%s", path, target, time.Now().UTC().Format("20060102T150405Z"))
			if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
				return fmt.Errorf("could not back up the catalog before upgrading it (%v); nothing was changed", err)
			}
			_ = os.Chmod(backup, 0o600)
		}
		for _, m := range steps {
			if m.To <= v || m.To > target {
				continue
			}
			if err := apply(db, m); err != nil {
				return fmt.Errorf("upgrading the catalog to schema %d (%s) failed and was rolled back: %v", m.To, m.Name, err)
			}
		}
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, target))
	return err
}

func apply(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.Up(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.To)); err != nil {
		return err
	}
	return tx.Commit()
}

func filePath(dsn string) string {
	p := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		if strings.Contains(p[i:], "mode=memory") {
			return ""
		}
		p = p[:i]
	}
	if p == "" || p == ":memory:" || strings.HasPrefix(p, ":memory:") {
		return ""
	}
	return p
}

// AddColumn adds a column unless the table already has it. Any other failure is returned, unlike an ignored ALTER.
func AddColumn(db interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}, table, column, definition string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	has := false
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		has = has || n == column
	}
	rows.Close()
	if has {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}
