package schema

import "database/sql"

// RelayGrantTables are standing approvals on the key holder's computer (ADR 0074). relay_grants is one grant: the collaborator's
// account id on the group server (and the name shown then), the label and the host it stood for when made, the template, the
// profile label and the code commit a granted draft must insist on, the narrowed keys (JSON list of bounds), the input folders and
// files on the host (JSON lists; never sent anywhere), the budget (jobs, core-minutes, per-job CPUs, GiB and minutes, jobs at
// once), when it was made, when it expires and when it was revoked. relay_grant_uses is one request submitted under a grant: the
// resources counted against the budget, the script hash, and when its job ended (NULL while it may still be queued or running,
// which is what the jobs-at-once limit counts). UNIQUE(relayed_id): one request takes at most one slot.
const RelayGrantTables = `CREATE TABLE IF NOT EXISTS relay_grants(
	id TEXT PRIMARY KEY,
	origin TEXT NOT NULL,
	requester_id TEXT NOT NULL,
	requester_name TEXT NOT NULL,
	label TEXT NOT NULL,
	host_id INTEGER NOT NULL,
	template TEXT NOT NULL CHECK(template IN ('vgt','rohsa')),
	profile TEXT NOT NULL DEFAULT '',
	commit_sha TEXT NOT NULL,
	commit_basis TEXT NOT NULL CHECK(commit_basis IN ('profile','typed')),
	keys TEXT NOT NULL,
	roots TEXT NOT NULL DEFAULT '[]',
	files TEXT NOT NULL DEFAULT '[]',
	max_jobs INTEGER NOT NULL,
	max_core_minutes INTEGER NOT NULL,
	max_cpus INTEGER NOT NULL,
	max_mem_gib INTEGER NOT NULL,
	max_minutes INTEGER NOT NULL,
	max_concurrent INTEGER NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	revoked_at INTEGER);
CREATE TABLE IF NOT EXISTS relay_grant_uses(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	grant_id TEXT NOT NULL REFERENCES relay_grants(id),
	relayed_id INTEGER NOT NULL UNIQUE,
	job_id INTEGER NOT NULL,
	cpus INTEGER NOT NULL,
	minutes INTEGER NOT NULL,
	core_minutes INTEGER NOT NULL,
	script_hash TEXT NOT NULL,
	at INTEGER NOT NULL,
	ended_at INTEGER);
CREATE INDEX IF NOT EXISTS relay_grant_uses_grant ON relay_grant_uses(grant_id)`

// RelayGrantColumns adds what the mirror and the server's request keep about a grant: on relayed_requests the grant a request
// named, what this computer decided (grant_state) and why it fell back (grant_note); on run_requests the grant id it was filed
// under. Each runs only on a catalog that has the table.
func RelayGrantColumns(db interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}) error {
	has := func(name string) bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
		return n > 0
	}
	if has("relayed_requests") {
		for _, c := range []string{"grant_id", "grant_state", "grant_note"} {
			if err := AddColumn(db, "relayed_requests", c, "TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	if has("run_requests") {
		if err := AddColumn(db, "run_requests", "grant_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

// relayGrants is schema 37: the grant tables on a catalog that has the relayed tables (every catalog since 33), and the columns.
func relayGrants(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='relayed_requests'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.Exec(RelayGrantTables); err != nil {
			return err
		}
	}
	return RelayGrantColumns(tx)
}
