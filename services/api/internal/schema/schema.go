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
// Version 15: a dataset may have several locations, each with its own fingerprints (dataset_locations); a figure records the
// identity of the datasets it was drawn from (figures.identity; ADR 0053).
// Version 16: a run keeps what its job observed about the environment and the controls stated for it (runs.environment,
// runs.controls; ADR 0054).
// Version 17: a host may carry the argv of the user's VPN client (hosts.vpn_command, hosts.vpn_disconnect; ADR 0029 step 3).
// Version 18: interpretation tiers (tier, mapping_version on interpretations, dataset_units and catalog_declarations, existing
// rows migrated from declared_by) and typed masks (masks; ADR 0055).
// Version 19: the exploration history, a bounded list of viewer scenes per project (exploration_steps; ADR 0057).
// Version 20: a project keeps the researcher's import rules and a record of what each was applied to (import_rules, import_applied;
// ADR 0058).
// Version 21: a board panel may carry its place on the board's grid (board_items.layout_x, layout_y, layout_w, layout_h; ADR 0060).
// Version 22: offline copies of bounded, derived replies (offline_entries) and the size limit for them (offline_settings; ADR 0059).
// Version 23: a researcher's WCS and unit corrections, an append-only history per dataset (dataset_corrections; ADR 0061).
// Version 24: saved views of the runs table (run_views; ADR 0062).
// Version 25: virtual subsets, a saved window onto a dataset with an append-only history of versions (subsets, subset_versions; ADR 0063).
const Version = 25

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
	{To: 15, Name: "dataset locations and figure identity", Up: datasetIdentity},
	{To: 16, Name: "run environment and controls", Up: runEnvironmentAndControls},
	{To: 17, Name: "host VPN commands", Up: hostVPNCommands},
	{To: 18, Name: "interpretation tiers and typed masks", Up: tiersAndMasks},
	{To: 19, Name: "exploration history", Up: explorationHistory},
	{To: 20, Name: "import rules", Up: importRules},
	{To: 21, Name: "board layout grid", Up: boardLayoutGrid},
	{To: 22, Name: "offline copies", Up: offlineCopies},
	{To: 23, Name: "WCS and unit corrections", Up: correctionsTable},
	{To: 24, Name: "saved run views", Up: savedRunViews},
	{To: 25, Name: "virtual subsets", Up: subsetsTables},
}

// SubsetsTables are the virtual subsets of a dataset (ADR 0063): `subsets` is the identity (which source it is a window onto) and
// `subset_versions` the append-only history of its definitions. A version holds the name, the reason, the definition (box, step,
// channel range or world interval, as JSON), who and when, and the identity of the source file it was pinned to (size, modification
// time, recorded content identity). An edit or a re-pin is a new row; rows are never updated (the trigger refuses it). They go with
// their source (catalog delete, ADR 0041); a file materialised from a version is an ordinary dataset and stays. The subsets
// package creates them for a fresh install and the migration for an upgraded one, from this one text.
const SubsetsTables = `CREATE TABLE IF NOT EXISTS subsets(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS subsets_source ON subsets(source_id);
CREATE TABLE IF NOT EXISTS subset_versions(
	subset_id INTEGER NOT NULL REFERENCES subsets(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	kind TEXT NOT NULL, name TEXT NOT NULL, reason TEXT NOT NULL, definition TEXT NOT NULL,
	source_size INTEGER NOT NULL, source_mtime_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL, created_at INTEGER NOT NULL,
	PRIMARY KEY(subset_id, version));
CREATE TRIGGER IF NOT EXISTS subset_versions_append_only BEFORE UPDATE ON subset_versions
BEGIN SELECT RAISE(ABORT, 'subset_versions is append-only: an edit or a re-pin is a new version'); END`

// subsetsTables adds the tables. No row to carry over: no subset existed before.
func subsetsTables(tx *sql.Tx) error {
	_, err := tx.Exec(SubsetsTables)
	return err
}

// CorrectionsTable is the append-only history of a researcher's WCS and unit corrections (ADR 0061): one row per record (a
// correction set, a revert, a confirmation for a changed file, an import, a withdrawal), each carrying the whole correction
// state in force after it (`state`, JSON), what it changed (`changes`, JSON), the reason and optional source, who and when,
// and the identity of the file it was made for (size, modification time, recorded content identity). Rows are never updated
// (the trigger refuses it); a revert is a new row. They go with their dataset (catalog delete, ADR 0041). The catalog package
// creates it for a fresh install and the migration for an upgraded one, from this one text.
const CorrectionsTable = `CREATE TABLE IF NOT EXISTS dataset_corrections(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
	action TEXT NOT NULL, state TEXT NOT NULL, changes TEXT NOT NULL,
	reason TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, tier TEXT NOT NULL DEFAULT 'declared',
	created_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL, modified_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '',
	reverts_id INTEGER);
CREATE INDEX IF NOT EXISTS dataset_corrections_dataset ON dataset_corrections(dataset_id, id);
CREATE TRIGGER IF NOT EXISTS dataset_corrections_append_only BEFORE UPDATE ON dataset_corrections
BEGIN SELECT RAISE(ABORT, 'dataset_corrections is append-only: a revert is a new record'); END`

// correctionsTable adds the history. No row to carry over: no correction existed before (a unit declared for a file with no
// BUNIT stays in dataset_units, ADR 0038).
func correctionsTable(tx *sql.Tx) error {
	_, err := tx.Exec(CorrectionsTable)
	return err
}

// boardLayoutGrid adds the four nullable columns that place a panel on the grid. NULL is what every existing panel gets: the board
// has no layout and the client derives one from the order, without rewriting anything until the researcher changes it. A catalog
// without a board_items table needs nothing.
func boardLayoutGrid(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='board_items'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, c := range []string{"layout_x", "layout_y", "layout_w", "layout_h"} {
		if err := AddColumn(tx, "board_items", c, "INTEGER"); err != nil {
			return err
		}
	}
	return nil
}

// MasksTable is the table of typed masks (ADR 0055): one row per derived mask file, with what the mask is for (the dataset), what
// it means (type, rule, meaning), how it was made (method, author, tier) and which bytes it was made from and is (source and own
// identity). The masks package creates it for a fresh install and the migration for an upgraded one, from this one text.
const MasksTable = `CREATE TABLE IF NOT EXISTS masks(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
	type TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
	width INTEGER NOT NULL, height INTEGER NOT NULL, channels INTEGER NOT NULL,
	flagged INTEGER NOT NULL, total INTEGER NOT NULL,
	tier TEXT NOT NULL, mapping_version TEXT NOT NULL DEFAULT '',
	method TEXT NOT NULL, rule TEXT NOT NULL, meaning TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL,
	source_size INTEGER NOT NULL, source_mtime INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '',
	file_size INTEGER NOT NULL, file_mtime INTEGER NOT NULL, sha256 TEXT NOT NULL)`

// AddTierColumns adds the tier and mapping_version columns of ADR 0055 to a declaration table (a default of "declared" suits
// every row the researcher wrote; tiersAndMasks corrects the rows a rule wrote).
func AddTierColumns(db interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}, table string) error {
	if err := AddColumn(db, table, "tier", "TEXT NOT NULL DEFAULT 'declared'"); err != nil {
		return err
	}
	return AddColumn(db, table, "mapping_version", "TEXT NOT NULL DEFAULT ''")
}

// tiersAndMasks gives every declaration a tier and creates the masks table. A declaration the researcher made (declared_by
// 'user') is `declared`; one a rule or adapter made is `inferred`, named by declared_by, with mapping version "1" (the mapping
// every such row was made by: versions did not exist yet). Nothing is migrated to `verified`: no existing row was ever checked.
// A catalog without a table (never opened by its package) needs nothing for it.
func tiersAndMasks(tx *sql.Tx) error {
	for _, t := range []string{"interpretations", "dataset_units", "catalog_declarations"} {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, t).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := AddTierColumns(tx, t); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='declared_by'`, t).Scan(&n); err != nil {
			return err
		}
		if n == 0 { // not the catalog's own table (a test's stand-in): nobody is recorded as the author, so nothing to read a rule from
			continue
		}
		if _, err := tx.Exec(`UPDATE ` + t + ` SET tier='inferred', mapping_version='1' WHERE declared_by<>'user'`); err != nil {
			return err
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='datasets'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := tx.Exec(MasksTable)
	return err
}

// DatasetLocationsTable is where a dataset's files are (ADR 0053). Every dataset has one primary row (the file it was
// registered from; datasets.path is the same file under a unique key) and may have more. link says why a location belongs to
// the dataset: registered (the primary: no claim), verified (equal full SHA-256), probable (equal size and sampled
// fingerprint: NOT proof) or declared (the researcher's word). The fingerprint columns say what was computed, when and by
// whom (” means not computed). The catalog package creates it for a fresh install and the migration for an upgraded one.
const DatasetLocationsTable = `CREATE TABLE IF NOT EXISTS dataset_locations(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
	host_id INTEGER NOT NULL DEFAULT 0,
	path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'registered',
	is_primary INTEGER NOT NULL DEFAULT 0,
	link TEXT NOT NULL DEFAULT 'registered',
	size_bytes INTEGER NOT NULL,
	modified_at INTEGER NOT NULL,
	added_at INTEGER NOT NULL,
	sampled_fp TEXT NOT NULL DEFAULT '', sampled_at INTEGER NOT NULL DEFAULT 0, sampled_by TEXT NOT NULL DEFAULT '',
	full_sha256 TEXT NOT NULL DEFAULT '', full_at INTEGER NOT NULL DEFAULT 0, full_by TEXT NOT NULL DEFAULT '',
	UNIQUE(host_id, path))`

// datasetIdentity adds dataset_locations and gives every existing dataset its primary location (no fingerprint yet: none was
// computed, which the empty columns say), and adds figures.identity (” for every existing figure: it recorded none). A
// catalog without those tables (never opened by their packages) needs nothing for them.
func datasetIdentity(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='datasets'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.Exec(DatasetLocationsTable); err != nil {
			return err
		}
		var have int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('datasets') WHERE name IN ('path','size_bytes','modified_at','added_at')`).Scan(&have); err != nil {
			return err
		}
		if have < 4 { // a table that is not the catalog's own (a test's stand-in) has nothing to carry over
			n = 0
		}
	}
	if n > 0 {
		if err := AddColumn(tx, "datasets", "host_id", "INTEGER NOT NULL DEFAULT 0"); err != nil { // the first catalogs had none
			return err
		}
		// datasets.path of a file on a host is stored as "h<host>:<path>"; the location keeps the plain path
		if _, err := tx.Exec(`INSERT OR IGNORE INTO dataset_locations(dataset_id,host_id,path,kind,is_primary,link,size_bytes,modified_at,added_at)
			SELECT id, host_id, CASE WHEN host_id=0 THEN path ELSE substr(path, length('h'||host_id||':')+1) END, 'registered', 1, 'registered', size_bytes, modified_at, added_at FROM datasets`); err != nil {
			return err
		}
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='figures'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "figures", "identity", "TEXT NOT NULL DEFAULT ''")
}

// hostVPNCommands adds the two VPN command columns to hosts: empty text (no command) for every existing host. A catalog that never had
// the hosts table needs nothing.
func hostVPNCommands(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hosts'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, c := range []string{"vpn_command", "vpn_disconnect"} {
		if err := AddColumn(tx, "hosts", c, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

// ImportRulesTables is the DDL of the researcher's import rules and of the record of what they were applied to (ADR 0058),
// shared by the projects package (a fresh catalog) and the migration (an upgraded one). import_rules is the ordered list of one
// project (rule: JSON of discover.Rule; the first matching rule wins; import_lists says a list was saved). import_applied has one row per file a rule's application
// registered, skipped or failed on, with the size and modification time it had, so a rescan does not count it as new again.
const ImportRulesTables = `CREATE TABLE IF NOT EXISTS import_rules(
	project_id INTEGER NOT NULL, position INTEGER NOT NULL, name TEXT NOT NULL, rule TEXT NOT NULL, saved_at INTEGER NOT NULL,
	PRIMARY KEY(project_id, position));
-- one row once the researcher has saved a rule list, even an empty one: from then on project.toml is not read back over it
CREATE TABLE IF NOT EXISTS import_lists(project_id INTEGER PRIMARY KEY, saved_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS import_applied(
	project_id INTEGER NOT NULL, path TEXT NOT NULL, rule TEXT NOT NULL, size_bytes INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,
	outcome TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', applied_at INTEGER NOT NULL, PRIMARY KEY(project_id, path))`

// importRules adds the two tables. No row to carry over: no rule existed before.
func importRules(tx *sql.Tx) error {
	_, err := tx.Exec(ImportRulesTables)
	return err
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
	copied INTEGER NOT NULL DEFAULT 0, written_sha TEXT NOT NULL DEFAULT '', replaced TEXT NOT NULL DEFAULT '',
	dataset_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER);
-- A final name belongs to one live transfer per side (this computer, or one host) and folder: the file sits in .vesqira-partial
-- until the end, so the destination folder alone cannot say that a name is taken.
CREATE UNIQUE INDEX IF NOT EXISTS transfers_live_name ON transfers(CASE direction WHEN 'download' THEN 0 ELSE host_id END, dest_dir, final_name)
	WHERE final_name<>'' AND state IN ('queued','waiting','running','verifying')`

// ExplorationStepsTable is the viewer's exploration history (ADR 0057): per project (0 = none open), one row per meaningful
// scene state. parent_id is the step it was reached from (NULL for the first of a branch, or when that step was pruned); it is
// deliberately not a foreign key, because pruning the oldest rows must not rewrite or delete their descendants. scene is the
// scene/1 JSON, validated before it is stored. The history package creates it for a fresh install and the migration for an
// upgraded one.
const ExplorationStepsTable = `CREATE TABLE IF NOT EXISTS exploration_steps(
	id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL DEFAULT 0, parent_id INTEGER,
	dataset_id INTEGER NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL, scene TEXT NOT NULL, at INTEGER NOT NULL)`

// ExplorationStepsIndex serves "the newest steps of a project" and pruning.
const ExplorationStepsIndex = `CREATE INDEX IF NOT EXISTS exploration_steps_project ON exploration_steps(project_id, id)`

// OfflineEntriesTable is the index of the offline store (ADR 0059): one row per kept reply of the scientific worker (a view, the
// header cards, one plane at one step, one region spectrum), keyed exactly as the plane cache is, plus the file signature it was
// made from (host, size, mtime and the content identity when one was recorded). The bytes are a file under <data>/offline, with
// the SHA-256 they must still have. seen_* is what a live look last found on the host: a copy whose signature differs from it is
// stale and is never served. Keyed by dataset_id only, and deleted with the dataset (lifecycle). The offline package creates it
// for a fresh install and the migration for an upgraded one.
const OfflineEntriesTable = `CREATE TABLE IF NOT EXISTS offline_entries(
	id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL,
	host_id INTEGER NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, identity TEXT NOT NULL DEFAULT '',
	op TEXT NOT NULL, channel INTEGER NOT NULL, step INTEGER NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL,
	q TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '',
	file TEXT NOT NULL, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, taken_at INTEGER NOT NULL,
	seen_size INTEGER NOT NULL DEFAULT 0, seen_mtime INTEGER NOT NULL DEFAULT 0, seen_at INTEGER NOT NULL DEFAULT 0,
	UNIQUE(dataset_id, host_id, size, mtime, op, channel, step, x, y, q, unit))`

// OfflineSettingsTable holds the one setting of the offline store: the most it may keep, in bytes (one row, id 1).
const OfflineSettingsTable = `CREATE TABLE IF NOT EXISTS offline_settings(
	id INTEGER PRIMARY KEY CHECK (id = 1), cap_bytes INTEGER NOT NULL CHECK (cap_bytes >= 0))`

// RunViewsTable holds the researcher's saved views of the runs table (ADR 0062): a name, and a JSON spec of the filter query, the
// exposed columns, the sort and the plot's axes. Words and keys only; no run id or value is stored in it.
const RunViewsTable = `CREATE TABLE IF NOT EXISTS run_views(
	id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE COLLATE NOCASE, spec TEXT NOT NULL, updated_at INTEGER NOT NULL)`

// savedRunViews adds the table. Nothing to carry over: no view was kept before.
func savedRunViews(tx *sql.Tx) error {
	_, err := tx.Exec(RunViewsTable)
	return err
}

// offlineCopies adds the offline tables. Nothing to carry over: nothing was kept offline before.
func offlineCopies(tx *sql.Tx) error {
	if _, err := tx.Exec(OfflineEntriesTable); err != nil {
		return err
	}
	_, err := tx.Exec(OfflineSettingsTable)
	return err
}

// explorationHistory adds the history table. Nothing to carry over: no step was kept before.
func explorationHistory(tx *sql.Tx) error {
	if _, err := tx.Exec(ExplorationStepsTable); err != nil {
		return err
	}
	_, err := tx.Exec(ExplorationStepsIndex)
	return err
}

// transfersTable adds the queue table. There is nothing to carry over: no transfer existed before.
func transfersTable(tx *sql.Tx) error {
	_, err := tx.Exec(TransfersTable)
	return err
}

// runEnvironmentAndControls adds runs.environment and runs.controls. Every existing run gets ” for both: nothing was observed
// or stated, which is not the same as "none". A catalog without a runs table needs nothing.
func runEnvironmentAndControls(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, c := range []string{"environment", "controls"} {
		if err := AddColumn(tx, "runs", c, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
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
