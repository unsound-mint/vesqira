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
const Version = 1

// A Migration upgrades a catalog from version From-1 to From. Steps run in order, each in its own transaction.
type Migration struct {
	To   int
	Name string
	Up   func(tx *sql.Tx) error
}

var migrations = []Migration{}

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
