package schema

import "database/sql"

// AccessTable is the access label of each dataset (ADR 0069): an append-only history, one row per change, the newest row of a
// dataset being the label in force (no row means "none", released). A row names the label, the one it replaced, who set it, the
// reason and when, and the dataset's name at that time. It is keyed by dataset id without a foreign key on purpose: the
// label outlives its dataset, so a figure drawn from a dataset that was later removed from the catalog keeps the label it had.
// Dataset ids are never reused (AUTOINCREMENT). Rows are never updated (the trigger refuses it). The access package creates it
// for a fresh install and the migration for an upgraded one, from this one text.
const AccessTable = `CREATE TABLE IF NOT EXISTS dataset_access(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL DEFAULT 1,
	dataset_id INTEGER NOT NULL,
	dataset_name TEXT NOT NULL,
	label TEXT NOT NULL CHECK(label IN ('none','collaboration','unreleased')),
	previous TEXT NOT NULL CHECK(previous IN ('none','collaboration','unreleased')),
	reason TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL,
	created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS dataset_access_dataset ON dataset_access(workspace_id, dataset_id, id);
CREATE TRIGGER IF NOT EXISTS dataset_access_append_only BEFORE UPDATE ON dataset_access
BEGIN SELECT RAISE(ABORT, 'dataset_access is append-only: a change of label is a new record'); END`

// accessLabels adds the history, and the policy column on a catalog that has workspaces (a fresh one has it from WorkspaceTables).
// Nothing to carry over: every dataset starts at "none", and no workspace blocks a label.
func accessLabels(tx *sql.Tx) error {
	if _, err := tx.Exec(AccessTable); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspaces'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "workspaces", "blocked_labels", "TEXT NOT NULL DEFAULT ''")
}
