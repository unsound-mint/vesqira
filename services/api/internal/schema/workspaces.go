package schema

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// WorkspaceTables are the access unit of ADR 0050 (section 3, slice 3). A workspace holds rows of user data; a membership gives an
// account one role in it (the matrix of section 3 lives in internal/authz); an invitation is a single-use token, stored as its
// SHA-256, that turns into an account with a role when someone accepts it before it expires. workspace_roots are the folders of
// the server an admin declared readable for the workspace (canonical paths, the ADR 0030 rules); on the group server every path
// route resolves against them and nothing else. managed_quota_bytes bounds the managed copies and derived outputs a workspace
// keeps (section 8). member_comments are comments by signed-in members on a figure or a report of their workspace (the matrix's
// "comment inside the workspace"); outsiders keep the token-based review comments. report_seen and notification_reads are what
// each account has read, in place of the single reports.seen_revision and notifications.read columns (section 6); local-owner
// is the account of local mode. Workspace 1 always exists: it is local mode's one workspace and holds every row written before
// schema 31. The catalog package creates the tables for a fresh install and the migration for an upgraded one, from this text.
const WorkspaceTables = `CREATE TABLE IF NOT EXISTS workspaces(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	created_by TEXT NOT NULL DEFAULT 'local-owner',
	managed_quota_bytes INTEGER NOT NULL DEFAULT 214748364800 CHECK(managed_quota_bytes >= 0),
	blocked_labels TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS memberships(
	workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	role TEXT NOT NULL CHECK(role IN ('viewer','researcher','maintainer','owner')),
	created_at INTEGER NOT NULL,
	PRIMARY KEY(workspace_id, account_id));
CREATE INDEX IF NOT EXISTS memberships_account ON memberships(account_id);
CREATE TABLE IF NOT EXISTS invitations(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	role TEXT NOT NULL CHECK(role IN ('viewer','researcher','maintainer','owner')),
	note TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL UNIQUE,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER,
	used_by TEXT,
	revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS invitations_workspace ON invitations(workspace_id);
CREATE TABLE IF NOT EXISTS workspace_roots(
	workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	path TEXT NOT NULL,
	added_at INTEGER NOT NULL,
	added_by TEXT NOT NULL,
	PRIMARY KEY(workspace_id, path));
CREATE TABLE IF NOT EXISTS member_comments(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL,
	created_by TEXT NOT NULL,
	kind TEXT NOT NULL CHECK(kind IN ('figure','report')),
	target_id INTEGER NOT NULL,
	body TEXT NOT NULL,
	created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS member_comments_target ON member_comments(workspace_id, kind, target_id);
CREATE TABLE IF NOT EXISTS report_seen(
	account_id TEXT NOT NULL,
	report_id INTEGER NOT NULL,
	revision INTEGER NOT NULL,
	PRIMARY KEY(account_id, report_id));
CREATE TABLE IF NOT EXISTS notification_reads(
	account_id TEXT NOT NULL,
	notification_id INTEGER NOT NULL,
	PRIMARY KEY(account_id, notification_id))`

// SeedLocalWorkspace makes sure workspace 1 exists. Run by the catalog package on every open, since a fresh catalog skips the
// migrations.
func SeedLocalWorkspace(db interface {
	Exec(string, ...any) (sql.Result, error)
}) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO workspaces(id, name, created_at, created_by) VALUES(1, 'Main workspace', ?, 'local-owner')`, time.Now().Unix())
	return err
}

// The tables rebuilt by schema 31 so that what was unique in the whole catalog is unique per workspace: the same file may be
// registered in two workspaces (ADR 0050 section 3), and a report title, a run folder, a saved view name, an opened folder, an
// attachment or a figure revision in one workspace must neither block nor reveal one in another. Each text is the table's
// whole definition; the owning package creates it for a fresh install, the migration rebuilds an existing one from it.
const (
	// DatasetsTable: path is the host-qualified key (catalog.pathKey), unique per workspace and host.
	DatasetsTable = `CREATE TABLE IF NOT EXISTS datasets(
	id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL, name TEXT NOT NULL, size_bytes INTEGER NOT NULL,
	modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0,
	workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
	UNIQUE(workspace_id, host_id, path))`
	ReportsTable = `CREATE TABLE IF NOT EXISTS reports(
	id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL, title TEXT NOT NULL COLLATE NOCASE,
	body TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
	parent_id INTEGER NOT NULL DEFAULT 0,
	workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
	UNIQUE(workspace_id, key), UNIQUE(workspace_id, title))`
	RunsTable = `CREATE TABLE IF NOT EXISTS runs(
	id INTEGER PRIMARY KEY AUTOINCREMENT, dir TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL,
	imported_at INTEGER NOT NULL, generated_at TEXT NOT NULL, git_commit TEXT NOT NULL, elapsed REAL,
	input_name TEXT NOT NULL, input_size INTEGER NOT NULL, params TEXT NOT NULL, diagnostics TEXT NOT NULL,
	metrics TEXT NOT NULL DEFAULT '[]', environment TEXT NOT NULL DEFAULT '', controls TEXT NOT NULL DEFAULT '',
	workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
	UNIQUE(workspace_id, dir))`
	ProjectsTable = `CREATE TABLE IF NOT EXISTS projects(
	id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, root TEXT NOT NULL, name TEXT NOT NULL,
	opened_at INTEGER NOT NULL, last_scan_at INTEGER, summary TEXT NOT NULL DEFAULT '{}',
	workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
	UNIQUE(workspace_id, host_id, root))`
	// AttachmentsTable: the picture's bytes stay content-addressed on disk (deduplication is harmless); the row is what makes it
	// reachable, and only from its own workspace (ADR 0050 section 3: looked up by workspace and sha, never sha alone).
	AttachmentsTable = `CREATE TABLE IF NOT EXISTS attachments(
	sha TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL,
	workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner',
	PRIMARY KEY(workspace_id, sha))`
)

// rebuiltTables pairs each rebuilt table with its definition, in the order the migration rebuilds them.
var rebuiltTables = []struct{ name, create string }{
	{"datasets", DatasetsTable},
	{"dataset_locations", DatasetLocationsTable},
	{"figures", FiguresTable},
	{"reports", ReportsTable},
	{"runs", RunsTable},
	{"run_views", RunViewsTable},
	{"projects", ProjectsTable},
	{"attachments", AttachmentsTable},
}

// ScopedTables are the tables of user data that gain workspace_id (and, unless noted, created_by) by ALTER, without a rebuild:
// nothing in them was unique across the catalog. The value says whether the table records who created a row (audit already
// records its actor).
var ScopedTables = map[string]bool{
	"boards": true, "snapshots": true, "saved_regions": true, "masks": true, "subsets": true, "exploration_steps": true,
	"notifications": true, "audit": false, "hosts": true, "jobs": true, "sweeps": true, "transfers": true, "offline_entries": false,
	"publish_comments": false, "reply_drafts": true,
}

// Scope adds workspace_id (existing rows: workspace 1) and, when createdBy, created_by (existing rows: local-owner) to a table,
// with an index on workspace_id. Idempotent: the owning package runs it after creating the table (a fresh install skips the
// migrations), and migration 31 runs it on an existing catalog.
func Scope(db interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}, table string) error {
	createdBy, ok := ScopedTables[table]
	if !ok {
		return fmt.Errorf("schema.Scope: %s is not a scoped table", table)
	}
	if err := AddColumn(db, table, "workspace_id", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if createdBy {
		if err := AddColumn(db, table, "created_by", "TEXT NOT NULL DEFAULT 'local-owner'"); err != nil {
			return err
		}
	}
	_, err := db.Exec(fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_workspace ON %s(workspace_id)`, table, table))
	return err
}

func tableExists(tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}

func columns(tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// rebuild replaces a table's definition with create, keeping every row and its id, the table's own indexes and triggers, and
// the AUTOINCREMENT counter (an id once handed out is never handed out again). Columns the old table has and the new one does
// not are dropped; columns only the new one has take their defaults. The caller runs it with foreign keys off (dropping the old
// table must not cascade into the tables that reference it). A catalog without the table needs nothing.
func rebuild(tx *sql.Tx, table, create string) error {
	if ok, err := tableExists(tx, table); err != nil || !ok {
		return err
	}
	tmp := table + "_v31"
	head := "IF NOT EXISTS " + table + "("
	if !strings.Contains(create, head) {
		return fmt.Errorf("rebuild %s: the definition does not create it", table)
	}
	old, err := columns(tx, table)
	if err != nil {
		return err
	}
	var extra []string // the table's indexes and triggers, which DROP TABLE removes
	rows, err := tx.Query(`SELECT sql FROM sqlite_master WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL`, table)
	if err != nil {
		return err
	}
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			rows.Close()
			return err
		}
		extra = append(extra, q)
	}
	rows.Close()
	var seq sql.NullInt64
	if err := tx.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name=?`, table).Scan(&seq); err != nil && err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + tmp); err != nil {
		return err
	}
	if _, err := tx.Exec(strings.Replace(create, head, tmp+"(", 1)); err != nil {
		return err
	}
	fresh, err := columns(tx, tmp)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, c := range fresh {
		keep[c] = true
	}
	var cols []string
	for _, c := range old {
		if keep[c] {
			cols = append(cols, `"`+c+`"`)
		}
	}
	list := strings.Join(cols, ",")
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO %s(%s) SELECT %s FROM %s`, tmp, list, list, table),
		`DROP TABLE ` + table,
		fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, tmp, table),
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	for _, q := range extra {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	if !seq.Valid {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM sqlite_sequence WHERE name=?`, table); err != nil {
		return err
	}
	var top int64
	if err := tx.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(rowid),0) FROM %s`, table)).Scan(&top); err != nil {
		return err
	}
	if top > seq.Int64 {
		seq.Int64 = top
	}
	_, err = tx.Exec(`INSERT INTO sqlite_sequence(name, seq) VALUES(?, ?)`, table, seq.Int64)
	return err
}

// workspacesMigration is schema 31. Every row that exists was written by the one owner of a local program (or, on a group server
// started by slice 2, by an admin into its one implicit workspace), so workspace 1 and local-owner are true of them, not a guess.
// On a catalog that already has accounts (a slice 2 group server), each account becomes an Owner of workspace 1: in slice 2 every
// account was an admin who could see everything, and taking that away silently would lock the group out of its own work. Read
// state moves to per-account rows before the reports table loses its seen_revision column.
func workspacesMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(WorkspaceTables); err != nil {
		return err
	}
	if err := SeedLocalWorkspace(tx); err != nil {
		return err
	}
	if ok, err := tableExists(tx, "reports"); err != nil {
		return err
	} else if ok {
		cols, err := columns(tx, "reports")
		if err != nil {
			return err
		}
		for _, c := range cols {
			if c == "seen_revision" {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO report_seen(account_id, report_id, revision) SELECT 'local-owner', id, seen_revision FROM reports WHERE seen_revision > 0`); err != nil {
					return err
				}
			}
		}
	}
	if ok, err := tableExists(tx, "notifications"); err != nil {
		return err
	} else if ok {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO notification_reads(account_id, notification_id) SELECT 'local-owner', id FROM notifications WHERE read = 1`); err != nil {
			return err
		}
	}
	for _, t := range rebuiltTables {
		if err := rebuild(tx, t.name, t.create); err != nil {
			return err
		}
	}
	for t := range ScopedTables {
		ok, err := tableExists(tx, t)
		if err != nil {
			return err
		}
		if ok {
			if err := Scope(tx, t); err != nil {
				return err
			}
		}
	}
	if ok, err := tableExists(tx, "accounts"); err != nil {
		return err
	} else if ok {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO memberships(workspace_id, account_id, role, created_at) SELECT 1, id, 'owner', ? FROM accounts`, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}
