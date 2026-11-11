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
// Version 4: a person may declare the unit of a file that has no BUNIT (dataset_units; ADR 0038).
// Version 5: a job remembers that its run record on the host is still owed (jobs.record_owed; ADR 0039).
// Version 6: entries may be archived (archived_entries; ADR 0041).
// Version 7: an orientation declaration also records the angle's frame and quantity (interpretations.frame, .quantity;
// ADR 0042).
// Version 8: a job remembers whether its results are imported automatically when it finishes, and whether that was
// tried (jobs.auto_import, jobs.auto_import_tried); finished jobs leave notifications (ADR 0043).
// Version 9: a figure may record the scene it was drawn from (figures.scene; ADR 0042).
// Version 10: a point catalog's declared columns (catalog_declarations; ADR 0044).
// Version 11: a finished job keeps what sacct reported it used (jobs.usage, JSON; ADR 0045).
// Version 12: a ROHSA run keeps its decomposition record (rohsa_runs; ADR 0046).
// Version 13: parameter sweeps, a record of a grid of linked draft jobs (sweeps, sweep_jobs; ADR 0048).
// Version 14: the transfer queue (transfers; ADR 0051).
const Version = 14

// A Migration upgrades a catalog from version From-1 to From. Steps run in order, each in its own transaction.
type Migration struct {
	To   int
	Name string
	Up   func(tx *sql.Tx) error
}

var migrations = []Migration{
	{To: 2, Name: "board items may follow a job", Up: boardItemsMayFollowAJob},
	{To: 3, Name: "report pages and quoted comments", Up: reportPagesAndQuotes},
	{To: 4, Name: "declared units", Up: declaredUnits},
	{To: 5, Name: "run record owed", Up: runRecordOwed},
	{To: 6, Name: "archived entries", Up: archivedEntries},
	{To: 7, Name: "orientation frame and quantity", Up: orientationFrameAndQuantity},
	{To: 8, Name: "automatic import and notifications", Up: autoImportAndNotifications},
	{To: 9, Name: "figure scene", Up: figureScene},
	{To: 10, Name: "catalog declarations", Up: catalogDeclarations},
	{To: 11, Name: "job usage", Up: jobUsage},
	{To: 12, Name: "ROHSA runs", Up: rohsaRuns},
	{To: 13, Name: "parameter sweeps", Up: parameterSweeps},
	{To: 14, Name: "transfers", Up: transfersTable},
}

// SweepsTables is the DDL of both sweep tables (ADR 0048), shared by the sweeps package (a fresh catalog) and the migration
// (an upgraded one).
const SweepsTables = `CREATE TABLE IF NOT EXISTS sweeps(
	id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, base_job_id INTEGER NOT NULL,
	base_run_id INTEGER, params TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
	concurrency INTEGER NOT NULL DEFAULT 1, stop_on_failure INTEGER NOT NULL DEFAULT 0, board_id INTEGER,
	created_at INTEGER NOT NULL, started_at INTEGER);
CREATE TABLE IF NOT EXISTS sweep_jobs(
	sweep_id INTEGER NOT NULL REFERENCES sweeps(id) ON DELETE CASCADE, job_id INTEGER NOT NULL UNIQUE, idx INTEGER NOT NULL,
	vals TEXT NOT NULL, approved_hash TEXT NOT NULL DEFAULT '', PRIMARY KEY(sweep_id, idx));`

// parameterSweeps adds the two sweep tables. No row to carry over: no sweep existed before.
func parameterSweeps(tx *sql.Tx) error {
	_, err := tx.Exec(SweepsTables)
	return err
}

// TransfersTable is the queue of transfers between this computer and a host (ADR 0051). The transfers package creates it for a
// fresh install and the migration for an upgraded one, from this one text.
const TransfersTable = `CREATE TABLE IF NOT EXISTS transfers(
	id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, direction TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '',
	source_path TEXT NOT NULL, dest_dir TEXT NOT NULL, name TEXT NOT NULL, final_name TEXT NOT NULL DEFAULT '',
	policy TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
	total_bytes INTEGER NOT NULL DEFAULT 0, done_bytes INTEGER NOT NULL DEFAULT 0, rate_bps INTEGER NOT NULL DEFAULT 0, eta_s INTEGER NOT NULL DEFAULT -1,
	bwlimit_kbps INTEGER NOT NULL DEFAULT 0, compress INTEGER NOT NULL DEFAULT 1, checksum INTEGER NOT NULL DEFAULT 0,
	attempts INTEGER NOT NULL DEFAULT 0, fails INTEGER NOT NULL DEFAULT 0, next_at INTEGER, warning TEXT NOT NULL DEFAULT '',
	verif_status TEXT NOT NULL DEFAULT '', verif_method TEXT NOT NULL DEFAULT '', verif_note TEXT NOT NULL DEFAULT '',
	src_sha TEXT NOT NULL DEFAULT '', dst_sha TEXT NOT NULL DEFAULT '', verif_bytes INTEGER NOT NULL DEFAULT 0,
	copied INTEGER NOT NULL DEFAULT 0, written_sha TEXT NOT NULL DEFAULT '',
	dataset_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER)`

// transfersTable adds the queue table. There is nothing to carry over: no transfer existed before.
func transfersTable(tx *sql.Tx) error {
	_, err := tx.Exec(TransfersTable)
	return err
}

// jobUsage adds jobs.usage. Every existing job gets ” (nothing was recorded; its use is not known, not zero). A catalog
// without a jobs table needs nothing.
func jobUsage(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "jobs", "usage", "TEXT NOT NULL DEFAULT ''")
}

// rohsaRuns adds the side table of ROHSA decomposition runs. The runs package creates it for a fresh install; creating it
// here keeps an upgraded catalog identical, with no row to carry over.
func rohsaRuns(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS rohsa_runs(run_id INTEGER PRIMARY KEY, dat_path TEXT NOT NULL, info TEXT NOT NULL)`)
	return err
}

// autoImportAndNotifications adds the two job columns. Every existing job gets 0 for both: a job submitted before the
// option existed is never imported behind its owner's back. The notifications table is created by its own package
// (idempotent, nothing to carry over). A catalog without a jobs table needs nothing.
func autoImportAndNotifications(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, c := range []string{"auto_import", "auto_import_tried"} {
		if err := AddColumn(tx, "jobs", c, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

// figureScene adds figures.scene, the scene/1 a figure was drawn from (ADR 0042). Every existing figure gets the empty text (no
// scene): it stays a PNG with provenance, as it was saved. A catalog without a figures table needs nothing.
func figureScene(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='figures'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "figures", "scene", "TEXT NOT NULL DEFAULT ''")
}

// CatalogDeclarationsTable is the overlay table for what a person declared about a point catalog's columns (ADR 0044). The
// catalog package creates it for a fresh install and the migration for an upgraded one, from this one text.
const CatalogDeclarationsTable = `CREATE TABLE IF NOT EXISTS catalog_declarations(
	dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE,
	declaration TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0)`

// catalogDeclarations adds the overlay table. No row to carry over: no dataset was a catalog before. A catalog without a
// datasets table (never opened by the catalog package) needs nothing.
func catalogDeclarations(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='datasets'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := tx.Exec(CatalogDeclarationsTable)
	return err
}

// runRecordOwed adds jobs.record_owed. Every existing job gets 0: no record is owed for a job submitted before records
// existed, which is what they are. A catalog without a jobs table needs nothing.
func runRecordOwed(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "jobs", "record_owed", "INTEGER NOT NULL DEFAULT 0")
}

// archivedEntries adds the side table that marks a dataset, run, figure or board as archived. No row to carry over.
func archivedEntries(tx *sql.Tx) error {
	_, err := tx.Exec(archiveTable)
	return err
}

const archiveTable = `CREATE TABLE IF NOT EXISTS archived_entries(
	kind TEXT NOT NULL, entity_id INTEGER NOT NULL, archived_at INTEGER NOT NULL, PRIMARY KEY(kind, entity_id))`

// EnsureArchive creates the archive table for a fresh install; every store that reads ArchivedExpr calls it from Open.
func EnsureArchive(db *sql.DB) error {
	_, err := db.Exec(archiveTable)
	return err
}

// ArchivedExpr is the SQL that tells whether the row of a table (idCol such as "datasets.id") is archived, for a select
// list. kind is one of "dataset", "run", "figure", "board".
func ArchivedExpr(kind, idCol string) string {
	switch kind {
	case "dataset", "run", "figure", "board":
	default: // kind is spliced into SQL text: only the four constants may get there
		panic("schema.ArchivedExpr: unknown kind " + kind)
	}
	return `EXISTS(SELECT 1 FROM archived_entries WHERE kind='` + kind + `' AND entity_id=` + idCol + `)`
}

// orientationFrameAndQuantity adds the two nullable columns that say what an orientation angle is (ADR 0042). NULL means
// not declared, which is what every existing row is: it keeps working for single-map statistics and is refused for
// cross-dataset use until someone declares it. A catalog without the table (never opened by the catalog package) needs
// nothing.
func orientationFrameAndQuantity(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='interpretations'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, c := range []string{"frame", "quantity"} {
		if err := AddColumn(tx, "interpretations", c, "TEXT"); err != nil {
			return err
		}
	}
	return nil
}

// declaredUnits adds the overlay table for a unit a person declared for a dataset with no BUNIT (ADR 0038). The catalog
// package creates it for a fresh install; creating it here keeps an upgraded catalog identical, with no row to carry
// over. A catalog without a datasets table (never opened by the catalog package) needs nothing.
func declaredUnits(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='datasets'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS dataset_units(
				dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE,
				unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL,
				size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0)`)
	return err
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
