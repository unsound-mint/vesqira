-- index audit_target
CREATE INDEX audit_target ON audit(kind, target);

-- index dataset_corrections_dataset
CREATE INDEX dataset_corrections_dataset ON dataset_corrections(dataset_id, id);

-- index exploration_steps_project
CREATE INDEX exploration_steps_project ON exploration_steps(project_id, id);

-- index project_entries_format
CREATE INDEX project_entries_format ON project_entries(project_id, format);

-- index publish_comments_report
CREATE INDEX publish_comments_report ON publish_comments(report);

-- index report_links_target
CREATE INDEX report_links_target ON report_links(kind, target);

-- index saved_regions_dataset
CREATE INDEX saved_regions_dataset ON saved_regions(dataset_id);

-- index subsets_source
CREATE INDEX subsets_source ON subsets(source_id);

-- index transfers_live_name
CREATE UNIQUE INDEX transfers_live_name ON transfers(CASE direction WHEN 'download' THEN 0 ELSE host_id END, dest_dir, final_name) WHERE final_name<>'' AND state IN ('queued','waiting','running','verifying');

-- table archived_entries
CREATE TABLE archived_entries( kind TEXT NOT NULL, entity_id INTEGER NOT NULL, archived_at INTEGER NOT NULL, PRIMARY KEY(kind, entity_id));

-- table attachments
CREATE TABLE attachments(sha TEXT PRIMARY KEY, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL);

-- table audit
CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL);

-- table board_items
CREATE TABLE board_items( board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '', layout_x INTEGER, layout_y INTEGER, layout_w INTEGER, layout_h INTEGER, PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)));

-- table boards
CREATE TABLE boards( id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

-- table catalog_declarations
CREATE TABLE catalog_declarations( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, declaration TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '');

-- table comments
CREATE TABLE comments( id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, figure_revision TEXT NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, parent_id INTEGER NOT NULL DEFAULT 0, anchor TEXT NOT NULL DEFAULT '', from_owner INTEGER NOT NULL DEFAULT 0, resolved_at INTEGER NOT NULL DEFAULT 0);

-- table dataset_corrections
CREATE TABLE dataset_corrections( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, action TEXT NOT NULL, state TEXT NOT NULL, changes TEXT NOT NULL, reason TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, tier TEXT NOT NULL DEFAULT 'declared', created_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL, modified_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', reverts_id INTEGER);

-- table dataset_locations
CREATE TABLE dataset_locations( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, host_id INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'registered', is_primary INTEGER NOT NULL DEFAULT 0, link TEXT NOT NULL DEFAULT 'registered', size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, sampled_fp TEXT NOT NULL DEFAULT '', sampled_at INTEGER NOT NULL DEFAULT 0, sampled_by TEXT NOT NULL DEFAULT '', full_sha256 TEXT NOT NULL DEFAULT '', full_at INTEGER NOT NULL DEFAULT 0, full_by TEXT NOT NULL DEFAULT '', UNIQUE(host_id, path));

-- table dataset_nature
CREATE TABLE dataset_nature(dataset_id INTEGER PRIMARY KEY, nature TEXT NOT NULL, declared_at INTEGER NOT NULL);

-- table dataset_units
CREATE TABLE dataset_units( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '');

-- table datasets
CREATE TABLE datasets( id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, -- Unix nanoseconds added_at INTEGER NOT NULL, header TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0);

-- table derived
CREATE TABLE derived( dataset_id INTEGER PRIMARY KEY, op TEXT NOT NULL, params TEXT NOT NULL, sources TEXT NOT NULL, summary TEXT NOT NULL, software TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table exploration_steps
CREATE TABLE exploration_steps( id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL DEFAULT 0, parent_id INTEGER, dataset_id INTEGER NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL, scene TEXT NOT NULL, at INTEGER NOT NULL);

-- table figures
CREATE TABLE figures( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, title TEXT NOT NULL, caption TEXT NOT NULL, created_at INTEGER NOT NULL, revision TEXT NOT NULL UNIQUE, preview_sha TEXT NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, provenance TEXT NOT NULL, scene TEXT NOT NULL DEFAULT '', identity TEXT NOT NULL DEFAULT '');

-- table hosts
CREATE TABLE hosts( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL, roots TEXT NOT NULL, created_at INTEGER NOT NULL, vpn_command TEXT NOT NULL DEFAULT '', vpn_disconnect TEXT NOT NULL DEFAULT '');

-- table import_applied
CREATE TABLE import_applied( project_id INTEGER NOT NULL, path TEXT NOT NULL, rule TEXT NOT NULL, size_bytes INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, outcome TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', applied_at INTEGER NOT NULL, PRIMARY KEY(project_id, path));

-- table import_lists
CREATE TABLE import_lists(project_id INTEGER PRIMARY KEY, saved_at INTEGER NOT NULL);

-- table import_rules
CREATE TABLE import_rules( project_id INTEGER NOT NULL, position INTEGER NOT NULL, name TEXT NOT NULL, rule TEXT NOT NULL, saved_at INTEGER NOT NULL, PRIMARY KEY(project_id, position));

-- table interpretations
CREATE TABLE interpretations( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, kind TEXT NOT NULL, unit TEXT NOT NULL, reliability_id INTEGER, declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, declared_by TEXT NOT NULL DEFAULT 'user', tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '', frame TEXT, quantity TEXT);

-- table jobs
CREATE TABLE jobs( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, name TEXT NOT NULL, template TEXT NOT NULL, script TEXT NOT NULL, script_hash TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL, slurm_id TEXT NOT NULL, work_dir TEXT NOT NULL, output_dir TEXT NOT NULL, created_at INTEGER NOT NULL, submitted_at INTEGER, finished_at INTEGER, run_id INTEGER, params TEXT NOT NULL DEFAULT '', request TEXT NOT NULL DEFAULT '', parent_run_id INTEGER, note TEXT NOT NULL DEFAULT '', record_owed INTEGER NOT NULL DEFAULT 0, auto_import INTEGER NOT NULL DEFAULT 0, auto_import_tried INTEGER NOT NULL DEFAULT 0, usage TEXT NOT NULL DEFAULT '');

-- table masks
CREATE TABLE masks( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, type TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE, width INTEGER NOT NULL, height INTEGER NOT NULL, channels INTEGER NOT NULL, flagged INTEGER NOT NULL, total INTEGER NOT NULL, tier TEXT NOT NULL, mapping_version TEXT NOT NULL DEFAULT '', method TEXT NOT NULL, rule TEXT NOT NULL, meaning TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL, source_size INTEGER NOT NULL, source_mtime INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL, file_mtime INTEGER NOT NULL, sha256 TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0);

-- table notifications
CREATE TABLE notifications( id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, job_id INTEGER, run_id INTEGER, read INTEGER NOT NULL DEFAULT 0, dedupe TEXT NOT NULL UNIQUE);

-- table offline_entries
CREATE TABLE offline_entries( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, host_id INTEGER NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, identity TEXT NOT NULL DEFAULT '', op TEXT NOT NULL, channel INTEGER NOT NULL, step INTEGER NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL, q TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '', file TEXT NOT NULL, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, taken_at INTEGER NOT NULL, seen_size INTEGER NOT NULL DEFAULT 0, seen_mtime INTEGER NOT NULL DEFAULT 0, seen_at INTEGER NOT NULL DEFAULT 0, UNIQUE(dataset_id, host_id, size, mtime, op, channel, step, x, y, q, unit));

-- table offline_settings
CREATE TABLE offline_settings( id INTEGER PRIMARY KEY CHECK (id = 1), cap_bytes INTEGER NOT NULL CHECK (cap_bytes >= 0));

-- table project_answers
CREATE TABLE project_answers( project_id INTEGER NOT NULL, need TEXT NOT NULL, answer TEXT NOT NULL, value TEXT NOT NULL, declared_by TEXT NOT NULL, date TEXT NOT NULL, written INTEGER NOT NULL, PRIMARY KEY(project_id, need));

-- table project_entries
CREATE TABLE project_entries( project_id INTEGER NOT NULL, grp TEXT NOT NULL, parent TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, path TEXT NOT NULL, format TEXT NOT NULL DEFAULT '', data TEXT NOT NULL, PRIMARY KEY(project_id, grp, parent, name));

-- table projects
CREATE TABLE projects( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, root TEXT NOT NULL, name TEXT NOT NULL, opened_at INTEGER NOT NULL, last_scan_at INTEGER, summary TEXT NOT NULL DEFAULT '{}', UNIQUE(host_id, root));

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

-- table rohsa_runs
CREATE TABLE rohsa_runs(run_id INTEGER PRIMARY KEY, dat_path TEXT NOT NULL, info TEXT NOT NULL);

-- table run_outputs
CREATE TABLE run_outputs( run_id INTEGER NOT NULL, role TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, role));

-- table run_review
CREATE TABLE run_review(run_id INTEGER PRIMARY KEY, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table run_review_history
CREATE TABLE run_review_history(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, at INTEGER NOT NULL, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table run_views
CREATE TABLE run_views( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE COLLATE NOCASE, spec TEXT NOT NULL, updated_at INTEGER NOT NULL);

-- table runs
CREATE TABLE runs( id INTEGER PRIMARY KEY AUTOINCREMENT, dir TEXT NOT NULL UNIQUE, name TEXT NOT NULL, kind TEXT NOT NULL, imported_at INTEGER NOT NULL, generated_at TEXT NOT NULL, git_commit TEXT NOT NULL, elapsed REAL, input_name TEXT NOT NULL, input_size INTEGER NOT NULL, params TEXT NOT NULL, diagnostics TEXT NOT NULL, metrics TEXT NOT NULL DEFAULT '[]', environment TEXT NOT NULL DEFAULT '', controls TEXT NOT NULL DEFAULT '');

-- table saved_regions
CREATE TABLE saved_regions( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, shape TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

-- table snapshot_outcomes
CREATE TABLE snapshot_outcomes(id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, at INTEGER NOT NULL, outcome TEXT NOT NULL, note TEXT NOT NULL, author TEXT NOT NULL);

-- table snapshots
CREATE TABLE snapshots( id INTEGER PRIMARY KEY AUTOINCREMENT, board_id INTEGER NOT NULL, label TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, token_hash TEXT NOT NULL UNIQUE, frozen TEXT NOT NULL, superseded_by INTEGER NOT NULL DEFAULT 0, supersede_reason TEXT NOT NULL DEFAULT '', supersede_at INTEGER NOT NULL DEFAULT 0);

-- table subset_versions
CREATE TABLE subset_versions( subset_id INTEGER NOT NULL REFERENCES subsets(id) ON DELETE CASCADE, version INTEGER NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, reason TEXT NOT NULL, definition TEXT NOT NULL, source_size INTEGER NOT NULL, source_mtime_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(subset_id, version));

-- table subsets
CREATE TABLE subsets( id INTEGER PRIMARY KEY AUTOINCREMENT, source_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, created_at INTEGER NOT NULL);

-- table sweep_jobs
CREATE TABLE sweep_jobs( sweep_id INTEGER NOT NULL REFERENCES sweeps(id) ON DELETE CASCADE, job_id INTEGER NOT NULL UNIQUE, idx INTEGER NOT NULL, vals TEXT NOT NULL, approved_hash TEXT NOT NULL DEFAULT '', PRIMARY KEY(sweep_id, idx));

-- table sweeps
CREATE TABLE sweeps( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, base_job_id INTEGER NOT NULL, base_run_id INTEGER, params TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', concurrency INTEGER NOT NULL DEFAULT 1, stop_on_failure INTEGER NOT NULL DEFAULT 0, board_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER);

-- table transfers
CREATE TABLE transfers( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, direction TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '', source_path TEXT NOT NULL, dest_dir TEXT NOT NULL, name TEXT NOT NULL, final_name TEXT NOT NULL DEFAULT '', policy TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', total_bytes INTEGER NOT NULL DEFAULT 0, done_bytes INTEGER NOT NULL DEFAULT 0, rate_bps INTEGER NOT NULL DEFAULT 0, eta_s INTEGER NOT NULL DEFAULT -1, bwlimit_kbps INTEGER NOT NULL DEFAULT 0, compress INTEGER NOT NULL DEFAULT 1, checksum INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, fails INTEGER NOT NULL DEFAULT 0, next_at INTEGER, warning TEXT NOT NULL DEFAULT '', verif_status TEXT NOT NULL DEFAULT '', verif_method TEXT NOT NULL DEFAULT '', verif_note TEXT NOT NULL DEFAULT '', src_sha TEXT NOT NULL DEFAULT '', dst_sha TEXT NOT NULL DEFAULT '', verif_bytes INTEGER NOT NULL DEFAULT 0, copied INTEGER NOT NULL DEFAULT 0, written_sha TEXT NOT NULL DEFAULT '', replaced TEXT NOT NULL DEFAULT '', dataset_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER);

-- trigger dataset_corrections_append_only
CREATE TRIGGER dataset_corrections_append_only BEFORE UPDATE ON dataset_corrections BEGIN SELECT RAISE(ABORT, 'dataset_corrections is append-only: a revert is a new record'); END;

-- trigger subset_versions_append_only
CREATE TRIGGER subset_versions_append_only BEFORE UPDATE ON subset_versions BEGIN SELECT RAISE(ABORT, 'subset_versions is append-only: an edit or a re-pin is a new version'); END;

