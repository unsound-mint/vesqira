package schema

import "database/sql"

// PushedTables are what the group server keeps of the work linked local instances push to a workspace (ADR 0066 section 2, ADR
// 0050 slice 4). A pushed item is an immutable frozen document (a figure, a run line, a board snapshot or a report revision),
// identified within its workspace by the SHA-256 of its canonical JSON; the same hash pushed again is the same row. (account,
// instance, kind, origin) is one lineage: a new hash in it becomes the head and the previous head records superseded_by, and is
// kept. Pushing an older version again makes that same row the head again (nothing is duplicated); pushed_history records each
// time an item became the head, so a lineage reads in the order of its pushes (A, B, A). Pictures are assets, stored once per workspace by the hash of their cleaned bytes in a folder beside the catalog, never in
// a row; pushed_item_assets says which item shows which, so a picture is served only through an item that shows it.
// pushed_references are the datasets a figure or a run was made from, as references: name and size, the fingerprints the
// instance sent in the push-only identity field (never part of the document, never forwarded to a review link or the publish
// server), and what the server found among its own datasets of the workspace (ADR 0053 tiers: verified, probable, none).
// The pushed package creates the tables for a fresh install and the migration for an upgraded one, from this text.
const PushedTables = `CREATE TABLE IF NOT EXISTS pushed_items(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL,
	created_by TEXT NOT NULL,
	token_id TEXT NOT NULL,
	instance TEXT NOT NULL,
	kind TEXT NOT NULL CHECK(kind IN ('figure','run','board','report')),
	origin TEXT NOT NULL,
	hash TEXT NOT NULL,
	title TEXT NOT NULL,
	doc TEXT NOT NULL,
	bytes INTEGER NOT NULL,
	pushed_at INTEGER NOT NULL,
	superseded_by INTEGER,
	superseded_at INTEGER,
	archived_at INTEGER,
	archived_by TEXT,
	UNIQUE(workspace_id, hash));
CREATE INDEX IF NOT EXISTS pushed_items_lineage ON pushed_items(workspace_id, created_by, instance, kind, origin);
CREATE INDEX IF NOT EXISTS pushed_items_day ON pushed_items(created_by, pushed_at);
CREATE TABLE IF NOT EXISTS pushed_history(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE,
	pushed_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS pushed_history_item ON pushed_history(item_id);
CREATE TABLE IF NOT EXISTS pushed_assets(
	workspace_id INTEGER NOT NULL,
	sha TEXT NOT NULL,
	mime TEXT NOT NULL,
	size INTEGER NOT NULL,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(workspace_id, sha));
CREATE TABLE IF NOT EXISTS pushed_item_assets(
	item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE,
	sha TEXT NOT NULL,
	PRIMARY KEY(item_id, sha));
CREATE TABLE IF NOT EXISTS pushed_references(
	item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE,
	n INTEGER NOT NULL,
	role TEXT NOT NULL,
	name TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	sampled_fp TEXT NOT NULL DEFAULT '',
	full_sha256 TEXT NOT NULL DEFAULT '',
	match TEXT NOT NULL DEFAULT 'none' CHECK(match IN ('verified','probable','none')),
	match_dataset_id INTEGER,
	matched_at INTEGER,
	PRIMARY KEY(item_id, n))`

// OutboxTables are the local instance's side of the link (ADR 0066 sections 1 and 2). group_link is the one linked server: its
// https origin, the random instance id sent with every call, the per-instance secret the opaque origin keys are made with (an
// HMAC of kind:local id, so the server never learns a local id; it is not a credential, and it is kept with the catalog so a
// restored catalog keeps its lineages), and the workspace name the server reported. The token itself is never here: it lives in
// a 0600 file beside the catalog. push_queue is the durable outbox: each row keeps the exact frozen bytes it will send until the
// server confirms them, so a retry resends identical bytes; its pictures wait in a folder beside the catalog. push_lineage maps
// an origin key back to the local object it stands for (slice 5 resolves a relayed request's base run through it).
const OutboxTables = `CREATE TABLE IF NOT EXISTS group_link(
	id INTEGER PRIMARY KEY CHECK(id = 1),
	origin TEXT NOT NULL,
	instance TEXT NOT NULL,
	secret TEXT NOT NULL,
	workspace TEXT NOT NULL DEFAULT '',
	linked_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS push_queue(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL,
	kind TEXT NOT NULL CHECK(kind IN ('figure','run','board','report')),
	local_id INTEGER NOT NULL,
	origin TEXT NOT NULL,
	hash TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	assets TEXT NOT NULL DEFAULT '[]',
	state TEXT NOT NULL CHECK(state IN ('waiting','sent','refused')),
	message TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	sent_at INTEGER,
	server_id INTEGER);
CREATE INDEX IF NOT EXISTS push_queue_state ON push_queue(state, next_at);
CREATE TABLE IF NOT EXISTS push_lineage(
	origin TEXT PRIMARY KEY,
	workspace_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	local_id INTEGER NOT NULL,
	head_hash TEXT NOT NULL DEFAULT '')`

// pushTables adds the group server's pushed items, the local outbox and the workspace a push or relay token is bound to (NULL:
// an account-wide token, which every token minted before is; a push or relay token among them allows nothing, see authz).
func pushTables(tx *sql.Tx) error {
	if _, err := tx.Exec(PushedTables); err != nil {
		return err
	}
	if _, err := tx.Exec(OutboxTables); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='access_tokens'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "access_tokens", "workspace_id", "INTEGER")
}
