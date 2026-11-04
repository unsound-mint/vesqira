// Package audit keeps an append-only record of the actions that change state or let someone see data: which host was
// added, which job was submitted, which board was published or revoked. It never stores secrets or file contents; the
// detail is a short, human-readable line.
package audit

import (
	"database/sql"
	"strings"
	"time"
)

type Entry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Kind   string    `json:"kind"`   // host | job | run | board | snapshot | dataset
	Target int64     `json:"target"` // the id of that object, 0 when none
	Detail string    `json:"detail"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_target ON audit(kind, target)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	return &Store{db}, nil
}

// Record appends one entry. A nil store records nothing, so callers need no checks.
func (s *Store) Record(action, kind string, target int64, detail string) {
	if s == nil {
		return
	}
	detail = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, detail)
	if len(detail) > 300 {
		detail = detail[:300]
	}
	_, _ = s.db.Exec(`INSERT INTO audit(at,action,kind,target,detail) VALUES(?,?,?,?,?)`, time.Now().Unix(), action, kind, target, detail)
}

// List returns the newest entries first; kind and target narrow it when set.
func (s *Store) List(kind string, target int64, limit int) ([]Entry, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,at,action,kind,target,detail FROM audit`
	var args []any
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
		if target != 0 {
			q += ` AND target=?`
			args = append(args, target)
		}
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Action, &e.Kind, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
