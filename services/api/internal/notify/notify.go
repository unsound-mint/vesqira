// Package notify keeps the events the owner should hear about while the app runs: a job reached an end state, its results
// were imported or could not be. It is a log with a read mark, not an audit trail (the audit log has no read state and
// is append-only for another purpose). Text says what Slurm or the importer reported and never calls completion
// "success": a finished job has not been checked scientifically.
package notify

import (
	"database/sql"
	"strings"
	"time"
)

// Kinds. The first five are how a job ended; the next two say what happened to its results; the transfer kinds follow.
const (
	KindCompleted    = "completed"
	KindFailed       = "failed"
	KindCancelled    = "cancelled"
	KindTimeout      = "timeout"
	KindUnknown      = "unknown"
	KindImported     = "imported"
	KindImportFailed = "import_failed"
	// A transfer between this computer and a host finished, did not (or arrived unverified), or needs a decision (ADR 0051).
	KindTransferDone     = "transfer_done"
	KindTransferFailed   = "transfer_failed"
	KindTransferConflict = "transfer_conflict"
)

// Keep is how many events are kept; older ones are dropped when a new one arrives.
const Keep = 500

type Notification struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	Body  string    `json:"body"`
	JobID *int64    `json:"jobId"`
	RunID *int64    `json:"runId"`
	Read  bool      `json:"read"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS notifications(
		id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
		job_id INTEGER, run_id INTEGER, read INTEGER NOT NULL DEFAULT 0, dedupe TEXT NOT NULL UNIQUE)`)
	if err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// Add records an event once per key: a second Add with the same key changes nothing and reports false. A nil store records
// nothing, so callers need no checks.
func (s *Store) Add(key string, n Notification) (bool, error) {
	if s == nil {
		return false, nil
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO notifications(at,kind,title,body,job_id,run_id,dedupe) VALUES(?,?,?,?,?,?,?)`,
		time.Now().Unix(), n.Kind, clean(n.Title, 200), clean(n.Body, 600), n.JobID, n.RunID, key)
	if err != nil {
		return false, err
	}
	added, _ := res.RowsAffected()
	if added == 1 {
		_, err = s.db.Exec(`DELETE FROM notifications WHERE id <= (SELECT MAX(id) FROM notifications) - ?`, Keep)
	}
	return added == 1, err
}

// List returns the newest events first and how many are unread in all (not only among those returned).
func (s *Store) List(limit int) ([]Notification, int, error) {
	if limit < 1 || limit > Keep {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,at,kind,title,body,job_id,run_id,read FROM notifications ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var at int64
		var job, run sql.NullInt64
		var read int
		if err := rows.Scan(&n.ID, &at, &n.Kind, &n.Title, &n.Body, &job, &run, &read); err != nil {
			return nil, 0, err
		}
		n.At, n.Read = time.Unix(at, 0).UTC(), read != 0
		if job.Valid {
			n.JobID = &job.Int64
		}
		if run.Valid {
			n.RunID = &run.Int64
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE read=0`).Scan(&unread)
	return out, unread, err
}

// MarkRead marks the given events read; with all set it marks every event.
func (s *Store) MarkRead(ids []int64, all bool) error {
	if all {
		_, err := s.db.Exec(`UPDATE notifications SET read=1 WHERE read=0`)
		return err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE notifications SET read=1 WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
