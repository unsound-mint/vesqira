package catalog

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// OpenReadPool opens a read-only pool on the catalog file for the group server (ADR 0050 section 6): up to conns connections
// opened with mode=ro, so a read never waits for the single writer connection, and in WAL mode never for a write in progress.
// Open the writer (Open) first: a read-only connection cannot create the WAL files. An in-memory catalog has no file to share.
func OpenReadPool(dsn string, conns int) (*sql.DB, error) {
	p := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// WriterWaits reports, every interval, how often requests waited for the single writer connection and for how long in
// total (database/sql's pool wait counters, which with one connection are the queue for the write lock inside this process).
// It calls report only for an interval with at least one wait. This is the lock-wait log ADR 0050 section 6 measures its
// PostgreSQL trigger against; it gives a count and a mean per interval, not a percentile.
func WriterWaits(ctx context.Context, db *sql.DB, every time.Duration, report func(waits int64, total time.Duration)) {
	last := db.Stats()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := db.Stats()
			if n := now.WaitCount - last.WaitCount; n > 0 {
				report(n, now.WaitDuration-last.WaitDuration)
			}
			last = now
		}
	}
}
