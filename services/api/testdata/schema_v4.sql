-- index audit_target
CREATE INDEX audit_target ON audit(kind, target);

-- index publish_comments_report
CREATE INDEX publish_comments_report ON publish_comments(report);

-- index report_links_target
CREATE INDEX report_links_target ON report_links(kind, target);

-- index saved_regions_dataset
CREATE INDEX saved_regions_dataset ON saved_regions(dataset_id);

-- table attachments
CREATE TABLE attachments(sha TEXT PRIMARY KEY, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL);

-- table audit
CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL);

-- table board_items
CREATE TABLE board_items( board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '', PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)));

-- table boards
CREATE TABLE boards( id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

-- table comments
CREATE TABLE comments( id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, figure_revision TEXT NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, parent_id INTEGER NOT NULL DEFAULT 0, anchor TEXT NOT NULL DEFAULT '', from_owner INTEGER NOT NULL DEFAULT 0, resolved_at INTEGER NOT NULL DEFAULT 0);

-- table dataset_nature
CREATE TABLE dataset_nature(dataset_id INTEGER PRIMARY KEY, nature TEXT NOT NULL, declared_at INTEGER NOT NULL);

-- table dataset_units
CREATE TABLE dataset_units( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0);

-- table datasets
CREATE TABLE datasets( id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, -- Unix nanoseconds added_at INTEGER NOT NULL, header TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0);

-- table derived
CREATE TABLE derived( dataset_id INTEGER PRIMARY KEY, op TEXT NOT NULL, params TEXT NOT NULL, sources TEXT NOT NULL, summary TEXT NOT NULL, software TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table figures
CREATE TABLE figures( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, title TEXT NOT NULL, caption TEXT NOT NULL, created_at INTEGER NOT NULL, revision TEXT NOT NULL UNIQUE, preview_sha TEXT NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, provenance TEXT NOT NULL);

-- table hosts
CREATE TABLE hosts( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL, roots TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table interpretations
CREATE TABLE interpretations( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, kind TEXT NOT NULL, unit TEXT NOT NULL, reliability_id INTEGER, declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, declared_by TEXT NOT NULL DEFAULT 'user');

-- table jobs
CREATE TABLE jobs( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, name TEXT NOT NULL, template TEXT NOT NULL, script TEXT NOT NULL, script_hash TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL, slurm_id TEXT NOT NULL, work_dir TEXT NOT NULL, output_dir TEXT NOT NULL, created_at INTEGER NOT NULL, submitted_at INTEGER, finished_at INTEGER, run_id INTEGER, params TEXT NOT NULL DEFAULT '', request TEXT NOT NULL DEFAULT '', parent_run_id INTEGER, note TEXT NOT NULL DEFAULT '');

-- table publish_comments
CREATE TABLE publish_comments( id INTEGER PRIMARY KEY, space TEXT NOT NULL, report TEXT NOT NULL, revision TEXT NOT NULL, block TEXT NOT NULL, anchor TEXT NOT NULL DEFAULT '', parent_id INTEGER NOT NULL, author TEXT NOT NULL, from_owner INTEGER NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, resolved_at INTEGER NOT NULL DEFAULT 0, quote TEXT NOT NULL DEFAULT '');

-- table publish_settings
CREATE TABLE publish_settings(k TEXT PRIMARY KEY, v TEXT NOT NULL);

-- table reply_drafts
CREATE TABLE reply_drafts( id INTEGER PRIMARY KEY AUTOINCREMENT, comment_id INTEGER NOT NULL, body TEXT NOT NULL, author TEXT NOT NULL, created_at INTEGER NOT NULL, sent_at INTEGER NOT NULL DEFAULT 0, discarded INTEGER NOT NULL DEFAULT 0);

-- table report_history
CREATE TABLE report_history( report_id INTEGER NOT NULL, revision INTEGER NOT NULL, author TEXT NOT NULL, at INTEGER NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(report_id, revision));

-- table report_links
CREATE TABLE report_links( report_id INTEGER NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL, PRIMARY KEY(report_id, kind, target));

-- table report_publications
CREATE TABLE report_publications( report_id INTEGER NOT NULL, number INTEGER NOT NULL, revision_id TEXT NOT NULL, working_revision INTEGER NOT NULL, published_at INTEGER NOT NULL, space TEXT NOT NULL, doc TEXT NOT NULL, PRIMARY KEY(report_id, number));

-- table reports
CREATE TABLE reports( id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, title TEXT NOT NULL UNIQUE COLLATE NOCASE, body TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, seen_revision INTEGER NOT NULL DEFAULT 0, parent_id INTEGER NOT NULL DEFAULT 0);

-- table run_outputs
CREATE TABLE run_outputs( run_id INTEGER NOT NULL, role TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, role));

-- table run_review
CREATE TABLE run_review(run_id INTEGER PRIMARY KEY, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table run_review_history
CREATE TABLE run_review_history(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, at INTEGER NOT NULL, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table runs
CREATE TABLE runs( id INTEGER PRIMARY KEY AUTOINCREMENT, dir TEXT NOT NULL UNIQUE, name TEXT NOT NULL, kind TEXT NOT NULL, imported_at INTEGER NOT NULL, generated_at TEXT NOT NULL, git_commit TEXT NOT NULL, elapsed REAL, input_name TEXT NOT NULL, input_size INTEGER NOT NULL, params TEXT NOT NULL, diagnostics TEXT NOT NULL, metrics TEXT NOT NULL DEFAULT '[]');

-- table saved_regions
CREATE TABLE saved_regions( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, shape TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

-- table snapshot_outcomes
CREATE TABLE snapshot_outcomes(id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, at INTEGER NOT NULL, outcome TEXT NOT NULL, note TEXT NOT NULL, author TEXT NOT NULL);

-- table snapshots
CREATE TABLE snapshots( id INTEGER PRIMARY KEY AUTOINCREMENT, board_id INTEGER NOT NULL, label TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, token_hash TEXT NOT NULL UNIQUE, frozen TEXT NOT NULL, superseded_by INTEGER NOT NULL DEFAULT 0, supersede_reason TEXT NOT NULL DEFAULT '', supersede_at INTEGER NOT NULL DEFAULT 0);

