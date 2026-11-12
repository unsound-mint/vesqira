package schema

import "database/sql"

// RelayTables are the group server's side of relayed run requests (ADR 0066 section 3, ADR 0050 slice 5). relay_inboxes is what
// a key holder's linked instance published: the cluster labels it takes requests for (free text to the server, which keeps no
// clusters table), the templates and typed bounds under each, and whether it is paused, with the last time that instance polled.
// run_requests is one request addressed to one key holder: the template, typed parameter values, an optional grid, an optional
// base (a pushed run item), a plain-text note and an expiry; never a path, script, host or dataset id. `change` is the cursor the
// key holder's instance pulls with: every transition sets it to one more than the workspace's highest, so a pull answers exactly
// the requests that changed since. The approval record (who approved, the script hash or sweep hash, the final parameters, the
// label, whether the base was chosen and how many lines were edited by hand) and the reported status sit on the row; the server
// holds no script, folder or Slurm id. run_request_events is the record of every transition with its actor and the way it came
// (session or token, the token id and the instance).
const RelayTables = `CREATE TABLE IF NOT EXISTS relay_inboxes(
	workspace_id INTEGER NOT NULL,
	account_id TEXT NOT NULL,
	inbox TEXT NOT NULL,
	paused INTEGER NOT NULL DEFAULT 0,
	instance TEXT NOT NULL DEFAULT '',
	token_id TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL,
	polled_at INTEGER,
	PRIMARY KEY(workspace_id, account_id));
CREATE TABLE IF NOT EXISTS run_requests(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL,
	created_by TEXT NOT NULL,
	addressee TEXT NOT NULL,
	label TEXT NOT NULL,
	template TEXT NOT NULL CHECK(template IN ('vgt','rohsa')),
	params TEXT NOT NULL,
	resources TEXT NOT NULL,
	grid TEXT NOT NULL DEFAULT '[]',
	base_item_id INTEGER,
	note TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL CHECK(state IN ('requested','waiting_review','approved','submitted','running','finished','failed','unknown','declined','withdrawn','expired')),
	change INTEGER NOT NULL,
	seq INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	claim_instance TEXT NOT NULL DEFAULT '',
	script_hash TEXT NOT NULL DEFAULT '',
	sweep_hash TEXT NOT NULL DEFAULT '',
	approved TEXT NOT NULL DEFAULT '',
	approved_at INTEGER,
	code TEXT NOT NULL DEFAULT '',
	exit_code TEXT NOT NULL DEFAULT '',
	counts TEXT NOT NULL DEFAULT '{}',
	run_hash TEXT NOT NULL DEFAULT '',
	submitted_at INTEGER,
	running_at INTEGER,
	ended_at INTEGER,
	reported_at INTEGER,
	reported_instance TEXT NOT NULL DEFAULT '',
	decline_note TEXT NOT NULL DEFAULT '',
	closed_note TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS run_requests_change ON run_requests(workspace_id, addressee, change);
CREATE INDEX IF NOT EXISTS run_requests_requester ON run_requests(workspace_id, created_by, addressee, state);
CREATE TABLE IF NOT EXISTS run_request_events(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id INTEGER NOT NULL REFERENCES run_requests(id) ON DELETE CASCADE,
	at INTEGER NOT NULL,
	actor TEXT NOT NULL,
	via TEXT NOT NULL,
	token_id TEXT NOT NULL DEFAULT '',
	instance TEXT NOT NULL DEFAULT '',
	event TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS run_request_events_request ON run_request_events(request_id)`

// RelayedTables are the key holder's local side (ADR 0066 section 3). relay_local is the one inbox this computer publishes: the
// labels with their templates and bounds, and, only here, the host each label stands for (a label never names a host on the
// server); the pull cursor and the poll's backoff, for the linked server's origin (another origin starts again from zero).
// relayed_requests mirrors each request the server answered, as strictly decoded, with what this computer made of it: refused by
// its own validation, drafted (the job or sweep), approved, declined or detached; the status sequence it last reported. The job
// a request became also carries the request's reference in jobs.request_ref.
const RelayedTables = `CREATE TABLE IF NOT EXISTS relay_local(
	id INTEGER PRIMARY KEY CHECK(id = 1),
	origin TEXT NOT NULL,
	inbox TEXT NOT NULL,
	hosts TEXT NOT NULL DEFAULT '{}',
	published INTEGER NOT NULL DEFAULT 0,
	cursor INTEGER NOT NULL DEFAULT 0,
	polled_at INTEGER,
	answered_at INTEGER,
	failures INTEGER NOT NULL DEFAULT 0,
	message TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS relayed_requests(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id INTEGER NOT NULL,
	origin TEXT NOT NULL,
	server_id INTEGER NOT NULL,
	request TEXT NOT NULL,
	server_state TEXT NOT NULL,
	local_state TEXT NOT NULL CHECK(local_state IN ('received','refused','draft','approved','declined','detached','closed')),
	refusal TEXT NOT NULL DEFAULT '',
	job_id INTEGER,
	sweep_id INTEGER,
	base_run_id INTEGER,
	base_chosen INTEGER NOT NULL DEFAULT 0,
	script_hash TEXT NOT NULL DEFAULT '',
	auto_push INTEGER NOT NULL DEFAULT 1,
	run_hash TEXT NOT NULL DEFAULT '',
	seq INTEGER NOT NULL DEFAULT 0,
	reported TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(origin, server_id))`

// relayTables adds both sides of relayed run requests, and jobs.request_ref on a catalog that has jobs (a fresh catalog gets the
// column from the jobs package).
func relayTables(tx *sql.Tx) error {
	if _, err := tx.Exec(RelayTables); err != nil {
		return err
	}
	if _, err := tx.Exec(RelayedTables); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return AddColumn(tx, "jobs", "request_ref", "TEXT NOT NULL DEFAULT ''")
}
