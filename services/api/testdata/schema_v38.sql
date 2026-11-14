-- index access_tokens_account
CREATE INDEX access_tokens_account ON access_tokens(account_id);

-- index audit_target
CREATE INDEX audit_target ON audit(kind, target);

-- index audit_workspace
CREATE INDEX audit_workspace ON audit(workspace_id);

-- index boards_workspace
CREATE INDEX boards_workspace ON boards(workspace_id);

-- index dataset_access_dataset
CREATE INDEX dataset_access_dataset ON dataset_access(workspace_id, dataset_id, id);

-- index dataset_corrections_dataset
CREATE INDEX dataset_corrections_dataset ON dataset_corrections(dataset_id, id);

-- index exploration_steps_project
CREATE INDEX exploration_steps_project ON exploration_steps(project_id, id);

-- index exploration_steps_workspace
CREATE INDEX exploration_steps_workspace ON exploration_steps(workspace_id);

-- index hosts_workspace
CREATE INDEX hosts_workspace ON hosts(workspace_id);

-- index identities_account
CREATE INDEX identities_account ON identities(account_id);

-- index invitations_workspace
CREATE INDEX invitations_workspace ON invitations(workspace_id);

-- index jobs_workspace
CREATE INDEX jobs_workspace ON jobs(workspace_id);

-- index masks_workspace
CREATE INDEX masks_workspace ON masks(workspace_id);

-- index member_comments_target
CREATE INDEX member_comments_target ON member_comments(workspace_id, kind, target_id);

-- index memberships_account
CREATE INDEX memberships_account ON memberships(account_id);

-- index notifications_workspace
CREATE INDEX notifications_workspace ON notifications(workspace_id);

-- index offline_entries_workspace
CREATE INDEX offline_entries_workspace ON offline_entries(workspace_id);

-- index project_entries_format
CREATE INDEX project_entries_format ON project_entries(project_id, format);

-- index publish_comments_report
CREATE INDEX publish_comments_report ON publish_comments(report);

-- index publish_comments_workspace
CREATE INDEX publish_comments_workspace ON publish_comments(workspace_id);

-- index push_queue_state
CREATE INDEX push_queue_state ON push_queue(state, next_at);

-- index pushed_history_item
CREATE INDEX pushed_history_item ON pushed_history(item_id);

-- index pushed_items_day
CREATE INDEX pushed_items_day ON pushed_items(created_by, pushed_at);

-- index pushed_items_lineage
CREATE INDEX pushed_items_lineage ON pushed_items(workspace_id, created_by, instance, kind, origin);

-- index relay_grant_uses_grant
CREATE INDEX relay_grant_uses_grant ON relay_grant_uses(grant_id);

-- index reply_drafts_workspace
CREATE INDEX reply_drafts_workspace ON reply_drafts(workspace_id);

-- index report_links_target
CREATE INDEX report_links_target ON report_links(kind, target);

-- index run_request_events_request
CREATE INDEX run_request_events_request ON run_request_events(request_id);

-- index run_requests_change
CREATE INDEX run_requests_change ON run_requests(workspace_id, addressee, change);

-- index run_requests_requester
CREATE INDEX run_requests_requester ON run_requests(workspace_id, created_by, addressee, state);

-- index saved_regions_dataset
CREATE INDEX saved_regions_dataset ON saved_regions(dataset_id);

-- index saved_regions_workspace
CREATE INDEX saved_regions_workspace ON saved_regions(workspace_id);

-- index sessions_account
CREATE INDEX sessions_account ON sessions(account_id);

-- index snapshots_workspace
CREATE INDEX snapshots_workspace ON snapshots(workspace_id);

-- index subsets_source
CREATE INDEX subsets_source ON subsets(source_id);

-- index subsets_workspace
CREATE INDEX subsets_workspace ON subsets(workspace_id);

-- index sweeps_workspace
CREATE INDEX sweeps_workspace ON sweeps(workspace_id);

-- index transfers_live_name
CREATE UNIQUE INDEX transfers_live_name ON transfers(CASE direction WHEN 'download' THEN 0 ELSE host_id END, dest_dir, final_name) WHERE final_name<>'' AND state IN ('queued','waiting','running','verifying');

-- index transfers_workspace
CREATE INDEX transfers_workspace ON transfers(workspace_id);

-- table access_tokens
CREATE TABLE access_tokens( id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, scopes TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_used_at INTEGER, revoked_at INTEGER, workspace_id INTEGER);

-- table accounts
CREATE TABLE accounts( id TEXT PRIMARY KEY, name TEXT NOT NULL, admin INTEGER NOT NULL DEFAULT 0 CHECK(admin IN (0,1)), created_at INTEGER NOT NULL, disabled_at INTEGER);

-- table archived_entries
CREATE TABLE archived_entries( kind TEXT NOT NULL, entity_id INTEGER NOT NULL, archived_at INTEGER NOT NULL, PRIMARY KEY(kind, entity_id));

-- table attachments
CREATE TABLE attachments( sha TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', PRIMARY KEY(workspace_id, sha));

-- table audit
CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL, actor TEXT NOT NULL DEFAULT 'local-owner', on_behalf_of TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT 'local', workspace_id INTEGER NOT NULL DEFAULT 1);

-- table board_items
CREATE TABLE board_items( board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '', layout_x INTEGER, layout_y INTEGER, layout_w INTEGER, layout_h INTEGER, PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)));

-- table boards
CREATE TABLE boards( id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table catalog_declarations
CREATE TABLE catalog_declarations( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, declaration TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '');

-- table comments
CREATE TABLE comments( id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, figure_revision TEXT NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, parent_id INTEGER NOT NULL DEFAULT 0, anchor TEXT NOT NULL DEFAULT '', from_owner INTEGER NOT NULL DEFAULT 0, resolved_at INTEGER NOT NULL DEFAULT 0);

-- table dataset_access
CREATE TABLE dataset_access( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL DEFAULT 1, dataset_id INTEGER NOT NULL, dataset_name TEXT NOT NULL, label TEXT NOT NULL CHECK(label IN ('none','collaboration','unreleased')), previous TEXT NOT NULL CHECK(previous IN ('none','collaboration','unreleased')), reason TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table dataset_corrections
CREATE TABLE dataset_corrections( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, action TEXT NOT NULL, state TEXT NOT NULL, changes TEXT NOT NULL, reason TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, tier TEXT NOT NULL DEFAULT 'declared', created_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL, modified_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', reverts_id INTEGER);

-- table dataset_locations
CREATE TABLE dataset_locations( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, host_id INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'registered', is_primary INTEGER NOT NULL DEFAULT 0, link TEXT NOT NULL DEFAULT 'registered', size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, sampled_fp TEXT NOT NULL DEFAULT '', sampled_at INTEGER NOT NULL DEFAULT 0, sampled_by TEXT NOT NULL DEFAULT '', full_sha256 TEXT NOT NULL DEFAULT '', full_at INTEGER NOT NULL DEFAULT 0, full_by TEXT NOT NULL DEFAULT '', file_dev INTEGER NOT NULL DEFAULT 0, file_ino INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, UNIQUE(workspace_id, host_id, path));

-- table dataset_nature
CREATE TABLE dataset_nature(dataset_id INTEGER PRIMARY KEY, nature TEXT NOT NULL, declared_at INTEGER NOT NULL);

-- table dataset_units
CREATE TABLE dataset_units( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '');

-- table datasets
CREATE TABLE datasets( id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', UNIQUE(workspace_id, host_id, path));

-- table derived
CREATE TABLE derived( dataset_id INTEGER PRIMARY KEY, op TEXT NOT NULL, params TEXT NOT NULL, sources TEXT NOT NULL, summary TEXT NOT NULL, software TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table exploration_steps
CREATE TABLE exploration_steps( id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL DEFAULT 0, parent_id INTEGER, dataset_id INTEGER NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL, scene TEXT NOT NULL, at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table figures
CREATE TABLE figures( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER, title TEXT NOT NULL, caption TEXT NOT NULL, created_at INTEGER NOT NULL, revision TEXT NOT NULL, preview_sha TEXT NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, provenance TEXT NOT NULL, scene TEXT NOT NULL DEFAULT '', identity TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'dataset' CHECK(kind IN ('dataset','crossrun','sky')), workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', CHECK((kind='dataset') = (dataset_id IS NOT NULL)), UNIQUE(workspace_id, revision));

-- table group_link
CREATE TABLE group_link( id INTEGER PRIMARY KEY CHECK(id = 1), origin TEXT NOT NULL, instance TEXT NOT NULL, secret TEXT NOT NULL, workspace TEXT NOT NULL DEFAULT '', linked_at INTEGER NOT NULL);

-- table hosts
CREATE TABLE hosts( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL, roots TEXT NOT NULL, created_at INTEGER NOT NULL, vpn_command TEXT NOT NULL DEFAULT '', vpn_disconnect TEXT NOT NULL DEFAULT '', workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table identities
CREATE TABLE identities( issuer TEXT NOT NULL, sub TEXT NOT NULL, account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, created_at INTEGER NOT NULL, PRIMARY KEY(issuer, sub));

-- table import_applied
CREATE TABLE import_applied( project_id INTEGER NOT NULL, path TEXT NOT NULL, rule TEXT NOT NULL, size_bytes INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, outcome TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', applied_at INTEGER NOT NULL, PRIMARY KEY(project_id, path));

-- table import_lists
CREATE TABLE import_lists(project_id INTEGER PRIMARY KEY, saved_at INTEGER NOT NULL);

-- table import_rules
CREATE TABLE import_rules( project_id INTEGER NOT NULL, position INTEGER NOT NULL, name TEXT NOT NULL, rule TEXT NOT NULL, saved_at INTEGER NOT NULL, PRIMARY KEY(project_id, position));

-- table interpretations
CREATE TABLE interpretations( dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE, kind TEXT NOT NULL, unit TEXT NOT NULL, reliability_id INTEGER, declared_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, declared_by TEXT NOT NULL DEFAULT 'user', tier TEXT NOT NULL DEFAULT 'declared', mapping_version TEXT NOT NULL DEFAULT '', frame TEXT, quantity TEXT, apply_id TEXT, applied_from INTEGER, applied_by TEXT);

-- table invitations
CREATE TABLE invitations( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK(role IN ('viewer','researcher','maintainer','owner')), note TEXT NOT NULL DEFAULT '', token_hash TEXT NOT NULL UNIQUE, created_by TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER, used_by TEXT, revoked_at INTEGER);

-- table jobs
CREATE TABLE jobs( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, name TEXT NOT NULL, template TEXT NOT NULL, script TEXT NOT NULL, script_hash TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL, slurm_id TEXT NOT NULL, work_dir TEXT NOT NULL, output_dir TEXT NOT NULL, created_at INTEGER NOT NULL, submitted_at INTEGER, finished_at INTEGER, run_id INTEGER, params TEXT NOT NULL DEFAULT '', request TEXT NOT NULL DEFAULT '', parent_run_id INTEGER, note TEXT NOT NULL DEFAULT '', record_owed INTEGER NOT NULL DEFAULT 0, auto_import INTEGER NOT NULL DEFAULT 0, auto_import_tried INTEGER NOT NULL DEFAULT 0, usage TEXT NOT NULL DEFAULT '', workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', request_ref TEXT NOT NULL DEFAULT '');

-- table masks
CREATE TABLE masks( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, type TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE, width INTEGER NOT NULL, height INTEGER NOT NULL, channels INTEGER NOT NULL, flagged INTEGER NOT NULL, total INTEGER NOT NULL, tier TEXT NOT NULL, mapping_version TEXT NOT NULL DEFAULT '', method TEXT NOT NULL, rule TEXT NOT NULL, meaning TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL, source_size INTEGER NOT NULL, source_mtime INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL, file_mtime INTEGER NOT NULL, sha256 TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table member_comments
CREATE TABLE member_comments( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL, created_by TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('figure','report')), target_id INTEGER NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL);

-- table memberships
CREATE TABLE memberships( workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE, account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK(role IN ('viewer','researcher','maintainer','owner')), created_at INTEGER NOT NULL, PRIMARY KEY(workspace_id, account_id));

-- table notification_reads
CREATE TABLE notification_reads( account_id TEXT NOT NULL, notification_id INTEGER NOT NULL, PRIMARY KEY(account_id, notification_id));

-- table notifications
CREATE TABLE notifications( id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, job_id INTEGER, run_id INTEGER, read INTEGER NOT NULL DEFAULT 0, dedupe TEXT NOT NULL UNIQUE, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table offline_entries
CREATE TABLE offline_entries( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, host_id INTEGER NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, identity TEXT NOT NULL DEFAULT '', op TEXT NOT NULL, channel INTEGER NOT NULL, step INTEGER NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL, q TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '', file TEXT NOT NULL, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, taken_at INTEGER NOT NULL, seen_size INTEGER NOT NULL DEFAULT 0, seen_mtime INTEGER NOT NULL DEFAULT 0, seen_at INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, UNIQUE(dataset_id, host_id, size, mtime, op, channel, step, x, y, q, unit));

-- table offline_settings
CREATE TABLE offline_settings( id INTEGER PRIMARY KEY CHECK (id = 1), cap_bytes INTEGER NOT NULL CHECK (cap_bytes >= 0));

-- table password_credentials
CREATE TABLE password_credentials( account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE, hash TEXT NOT NULL, changed_at INTEGER NOT NULL);

-- table project_answers
CREATE TABLE project_answers( project_id INTEGER NOT NULL, need TEXT NOT NULL, answer TEXT NOT NULL, value TEXT NOT NULL, declared_by TEXT NOT NULL, date TEXT NOT NULL, written INTEGER NOT NULL, PRIMARY KEY(project_id, need));

-- table project_entries
CREATE TABLE project_entries( project_id INTEGER NOT NULL, grp TEXT NOT NULL, parent TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, path TEXT NOT NULL, format TEXT NOT NULL DEFAULT '', data TEXT NOT NULL, PRIMARY KEY(project_id, grp, parent, name));

-- table projects
CREATE TABLE projects( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, root TEXT NOT NULL, name TEXT NOT NULL, opened_at INTEGER NOT NULL, last_scan_at INTEGER, summary TEXT NOT NULL DEFAULT '{}', workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', UNIQUE(workspace_id, host_id, root));

-- table publish_comments
CREATE TABLE publish_comments( id INTEGER PRIMARY KEY, space TEXT NOT NULL, report TEXT NOT NULL, revision TEXT NOT NULL, block TEXT NOT NULL, anchor TEXT NOT NULL DEFAULT '', parent_id INTEGER NOT NULL, author TEXT NOT NULL, from_owner INTEGER NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, resolved_at INTEGER NOT NULL DEFAULT 0, quote TEXT NOT NULL DEFAULT '', workspace_id INTEGER NOT NULL DEFAULT 1);

-- table publish_settings
CREATE TABLE publish_settings(k TEXT PRIMARY KEY, v TEXT NOT NULL);

-- table push_lineage
CREATE TABLE push_lineage( origin TEXT PRIMARY KEY, workspace_id INTEGER NOT NULL, kind TEXT NOT NULL, local_id INTEGER NOT NULL, head_hash TEXT NOT NULL DEFAULT '');

-- table push_queue
CREATE TABLE push_queue( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('figure','run','board','report')), local_id INTEGER NOT NULL, origin TEXT NOT NULL, hash TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, assets TEXT NOT NULL DEFAULT '[]', state TEXT NOT NULL CHECK(state IN ('waiting','sent','refused')), message TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, sent_at INTEGER, server_id INTEGER);

-- table pushed_assets
CREATE TABLE pushed_assets( workspace_id INTEGER NOT NULL, sha TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, created_by TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(workspace_id, sha));

-- table pushed_history
CREATE TABLE pushed_history( id INTEGER PRIMARY KEY AUTOINCREMENT, item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE, pushed_at INTEGER NOT NULL);

-- table pushed_item_assets
CREATE TABLE pushed_item_assets( item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE, sha TEXT NOT NULL, PRIMARY KEY(item_id, sha));

-- table pushed_items
CREATE TABLE pushed_items( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL, created_by TEXT NOT NULL, token_id TEXT NOT NULL, instance TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('figure','run','board','report')), origin TEXT NOT NULL, hash TEXT NOT NULL, title TEXT NOT NULL, doc TEXT NOT NULL, bytes INTEGER NOT NULL, pushed_at INTEGER NOT NULL, superseded_by INTEGER, superseded_at INTEGER, archived_at INTEGER, archived_by TEXT, matched_marker TEXT NOT NULL DEFAULT '', UNIQUE(workspace_id, hash));

-- table pushed_references
CREATE TABLE pushed_references( item_id INTEGER NOT NULL REFERENCES pushed_items(id) ON DELETE CASCADE, n INTEGER NOT NULL, role TEXT NOT NULL, name TEXT NOT NULL, size_bytes INTEGER NOT NULL, sampled_fp TEXT NOT NULL DEFAULT '', full_sha256 TEXT NOT NULL DEFAULT '', match TEXT NOT NULL DEFAULT 'none' CHECK(match IN ('verified','probable','none')), match_dataset_id INTEGER, matched_at INTEGER, PRIMARY KEY(item_id, n));

-- table recovery_codes
CREATE TABLE recovery_codes( account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, code_hash TEXT NOT NULL, used_at INTEGER, PRIMARY KEY(account_id, code_hash));

-- table relay_grant_uses
CREATE TABLE relay_grant_uses( id INTEGER PRIMARY KEY AUTOINCREMENT, grant_id TEXT NOT NULL REFERENCES relay_grants(id), relayed_id INTEGER NOT NULL UNIQUE, job_id INTEGER NOT NULL, cpus INTEGER NOT NULL, minutes INTEGER NOT NULL, core_minutes INTEGER NOT NULL, script_hash TEXT NOT NULL, at INTEGER NOT NULL, ended_at INTEGER);

-- table relay_grants
CREATE TABLE relay_grants( id TEXT PRIMARY KEY, origin TEXT NOT NULL, requester_id TEXT NOT NULL, requester_name TEXT NOT NULL, label TEXT NOT NULL, host_id INTEGER NOT NULL, template TEXT NOT NULL CHECK(template IN ('vgt','rohsa')), profile TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL, commit_basis TEXT NOT NULL CHECK(commit_basis IN ('profile','typed')), keys TEXT NOT NULL, roots TEXT NOT NULL DEFAULT '[]', files TEXT NOT NULL DEFAULT '[]', max_jobs INTEGER NOT NULL, max_core_minutes INTEGER NOT NULL, max_cpus INTEGER NOT NULL, max_mem_gib INTEGER NOT NULL, max_minutes INTEGER NOT NULL, max_concurrent INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER);

-- table relay_inboxes
CREATE TABLE relay_inboxes( workspace_id INTEGER NOT NULL, account_id TEXT NOT NULL, inbox TEXT NOT NULL, paused INTEGER NOT NULL DEFAULT 0, instance TEXT NOT NULL DEFAULT '', token_id TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL, polled_at INTEGER, PRIMARY KEY(workspace_id, account_id));

-- table relay_local
CREATE TABLE relay_local( id INTEGER PRIMARY KEY CHECK(id = 1), origin TEXT NOT NULL, inbox TEXT NOT NULL, hosts TEXT NOT NULL DEFAULT '{}', published INTEGER NOT NULL DEFAULT 0, cursor INTEGER NOT NULL DEFAULT 0, polled_at INTEGER, answered_at INTEGER, failures INTEGER NOT NULL DEFAULT 0, message TEXT NOT NULL DEFAULT '');

-- table relayed_requests
CREATE TABLE relayed_requests( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL, origin TEXT NOT NULL, server_id INTEGER NOT NULL, request TEXT NOT NULL, server_state TEXT NOT NULL, local_state TEXT NOT NULL CHECK(local_state IN ('received','refused','draft','approved','declined','detached','closed')), refusal TEXT NOT NULL DEFAULT '', job_id INTEGER, sweep_id INTEGER, base_run_id INTEGER, base_chosen INTEGER NOT NULL DEFAULT 0, draft TEXT NOT NULL DEFAULT '{}', script_hash TEXT NOT NULL DEFAULT '', auto_push INTEGER NOT NULL DEFAULT 1, run_hash TEXT NOT NULL DEFAULT '', seq INTEGER NOT NULL DEFAULT 0, reported TEXT NOT NULL DEFAULT '', received_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, grant_id TEXT NOT NULL DEFAULT '', grant_state TEXT NOT NULL DEFAULT '', grant_note TEXT NOT NULL DEFAULT '', UNIQUE(origin, server_id));

-- table reply_drafts
CREATE TABLE reply_drafts( id INTEGER PRIMARY KEY AUTOINCREMENT, comment_id INTEGER NOT NULL, body TEXT NOT NULL, author TEXT NOT NULL, created_at INTEGER NOT NULL, sent_at INTEGER NOT NULL DEFAULT 0, discarded INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table report_history
CREATE TABLE report_history( report_id INTEGER NOT NULL, revision INTEGER NOT NULL, author TEXT NOT NULL, at INTEGER NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(report_id, revision));

-- table report_links
CREATE TABLE report_links( report_id INTEGER NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL, PRIMARY KEY(report_id, kind, target));

-- table report_publications
CREATE TABLE report_publications( report_id INTEGER NOT NULL, number INTEGER NOT NULL, revision_id TEXT NOT NULL, working_revision INTEGER NOT NULL, published_at INTEGER NOT NULL, space TEXT NOT NULL, doc TEXT NOT NULL, PRIMARY KEY(report_id, number));

-- table report_seen
CREATE TABLE report_seen( account_id TEXT NOT NULL, report_id INTEGER NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(account_id, report_id));

-- table reports
CREATE TABLE reports( id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL, title TEXT NOT NULL COLLATE NOCASE, body TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, parent_id INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', UNIQUE(workspace_id, key), UNIQUE(workspace_id, title));

-- table rohsa_runs
CREATE TABLE rohsa_runs(run_id INTEGER PRIMARY KEY, dat_path TEXT NOT NULL, info TEXT NOT NULL);

-- table run_outputs
CREATE TABLE run_outputs( run_id INTEGER NOT NULL, role TEXT NOT NULL, product TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, product));

-- table run_request_events
CREATE TABLE run_request_events( id INTEGER PRIMARY KEY AUTOINCREMENT, request_id INTEGER NOT NULL REFERENCES run_requests(id) ON DELETE CASCADE, at INTEGER NOT NULL, actor TEXT NOT NULL, via TEXT NOT NULL, token_id TEXT NOT NULL DEFAULT '', instance TEXT NOT NULL DEFAULT '', event TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '');

-- table run_requests
CREATE TABLE run_requests( id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id INTEGER NOT NULL, created_by TEXT NOT NULL, addressee TEXT NOT NULL, label TEXT NOT NULL, template TEXT NOT NULL CHECK(template IN ('vgt','rohsa')), params TEXT NOT NULL, resources TEXT NOT NULL, grid TEXT NOT NULL DEFAULT '[]', base_item_id INTEGER, note TEXT NOT NULL DEFAULT '', state TEXT NOT NULL CHECK(state IN ('requested','waiting_review','approved','submitted','running','finished','failed','unknown','declined','withdrawn','expired')), change INTEGER NOT NULL, seq INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, claim_instance TEXT NOT NULL DEFAULT '', script_hash TEXT NOT NULL DEFAULT '', sweep_hash TEXT NOT NULL DEFAULT '', approved TEXT NOT NULL DEFAULT '', approved_at INTEGER, code TEXT NOT NULL DEFAULT '', exit_code TEXT NOT NULL DEFAULT '', counts TEXT NOT NULL DEFAULT '{}', run_hash TEXT NOT NULL DEFAULT '', submitted_at INTEGER, running_at INTEGER, ended_at INTEGER, reported_at INTEGER, reported_instance TEXT NOT NULL DEFAULT '', decline_note TEXT NOT NULL DEFAULT '', closed_note TEXT NOT NULL DEFAULT '', grant_id TEXT NOT NULL DEFAULT '');

-- table run_review
CREATE TABLE run_review(run_id INTEGER PRIMARY KEY, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table run_review_history
CREATE TABLE run_review_history(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, at INTEGER NOT NULL, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL);

-- table run_views
CREATE TABLE run_views( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL COLLATE NOCASE, spec TEXT NOT NULL, updated_at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', UNIQUE(workspace_id, name));

-- table runs
CREATE TABLE runs( id INTEGER PRIMARY KEY AUTOINCREMENT, dir TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, imported_at INTEGER NOT NULL, generated_at TEXT NOT NULL, git_commit TEXT NOT NULL, elapsed REAL, input_name TEXT NOT NULL, input_size INTEGER NOT NULL, params TEXT NOT NULL, diagnostics TEXT NOT NULL, metrics TEXT NOT NULL DEFAULT '[]', environment TEXT NOT NULL DEFAULT '', controls TEXT NOT NULL DEFAULT '', workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner', profile TEXT NOT NULL DEFAULT '', UNIQUE(workspace_id, dir));

-- table saved_regions
CREATE TABLE saved_regions( id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, shape TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table sessions
CREATE TABLE sessions( id_hash TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, stage TEXT NOT NULL CHECK(stage IN ('second_factor','enrol','full')), created_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, signed_in_at INTEGER NOT NULL, client_addr TEXT NOT NULL, user_agent TEXT NOT NULL);

-- table setup_tokens
CREATE TABLE setup_tokens( token_hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);

-- table snapshot_outcomes
CREATE TABLE snapshot_outcomes(id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, at INTEGER NOT NULL, outcome TEXT NOT NULL, note TEXT NOT NULL, author TEXT NOT NULL);

-- table snapshots
CREATE TABLE snapshots( id INTEGER PRIMARY KEY AUTOINCREMENT, board_id INTEGER NOT NULL, label TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, token_hash TEXT NOT NULL UNIQUE, frozen TEXT NOT NULL, superseded_by INTEGER NOT NULL DEFAULT 0, supersede_reason TEXT NOT NULL DEFAULT '', supersede_at INTEGER NOT NULL DEFAULT 0, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table subset_versions
CREATE TABLE subset_versions( subset_id INTEGER NOT NULL REFERENCES subsets(id) ON DELETE CASCADE, version INTEGER NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, reason TEXT NOT NULL, definition TEXT NOT NULL, source_size INTEGER NOT NULL, source_mtime_ns INTEGER NOT NULL, source_identity TEXT NOT NULL DEFAULT '', author TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(subset_id, version));

-- table subsets
CREATE TABLE subsets( id INTEGER PRIMARY KEY AUTOINCREMENT, source_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE, created_at INTEGER NOT NULL, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table sweep_jobs
CREATE TABLE sweep_jobs( sweep_id INTEGER NOT NULL REFERENCES sweeps(id) ON DELETE CASCADE, job_id INTEGER NOT NULL UNIQUE, idx INTEGER NOT NULL, vals TEXT NOT NULL, approved_hash TEXT NOT NULL DEFAULT '', PRIMARY KEY(sweep_id, idx));

-- table sweeps
CREATE TABLE sweeps( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, base_job_id INTEGER NOT NULL, base_run_id INTEGER, params TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', concurrency INTEGER NOT NULL DEFAULT 1, stop_on_failure INTEGER NOT NULL DEFAULT 0, board_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table totp_credentials
CREATE TABLE totp_credentials( account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE, secret BLOB, pending BLOB, last_step INTEGER NOT NULL DEFAULT 0, confirmed_at INTEGER);

-- table transfers
CREATE TABLE transfers( id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, direction TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '', source_path TEXT NOT NULL, dest_dir TEXT NOT NULL, name TEXT NOT NULL, final_name TEXT NOT NULL DEFAULT '', policy TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', total_bytes INTEGER NOT NULL DEFAULT 0, done_bytes INTEGER NOT NULL DEFAULT 0, rate_bps INTEGER NOT NULL DEFAULT 0, eta_s INTEGER NOT NULL DEFAULT -1, bwlimit_kbps INTEGER NOT NULL DEFAULT 0, compress INTEGER NOT NULL DEFAULT 1, checksum INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, fails INTEGER NOT NULL DEFAULT 0, next_at INTEGER, warning TEXT NOT NULL DEFAULT '', verif_status TEXT NOT NULL DEFAULT '', verif_method TEXT NOT NULL DEFAULT '', verif_note TEXT NOT NULL DEFAULT '', src_sha TEXT NOT NULL DEFAULT '', dst_sha TEXT NOT NULL DEFAULT '', verif_bytes INTEGER NOT NULL DEFAULT 0, copied INTEGER NOT NULL DEFAULT 0, written_sha TEXT NOT NULL DEFAULT '', replaced TEXT NOT NULL DEFAULT '', dataset_id INTEGER, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER, workspace_id INTEGER NOT NULL DEFAULT 1, created_by TEXT NOT NULL DEFAULT 'local-owner');

-- table workspace_roots
CREATE TABLE workspace_roots( workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE, path TEXT NOT NULL, added_at INTEGER NOT NULL, added_by TEXT NOT NULL, PRIMARY KEY(workspace_id, path));

-- table workspaces
CREATE TABLE workspaces( id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, created_at INTEGER NOT NULL, created_by TEXT NOT NULL DEFAULT 'local-owner', managed_quota_bytes INTEGER NOT NULL DEFAULT 214748364800 CHECK(managed_quota_bytes >= 0), blocked_labels TEXT NOT NULL DEFAULT '');

-- trigger dataset_access_append_only
CREATE TRIGGER dataset_access_append_only BEFORE UPDATE ON dataset_access BEGIN SELECT RAISE(ABORT, 'dataset_access is append-only: a change of label is a new record'); END;

-- trigger dataset_corrections_append_only
CREATE TRIGGER dataset_corrections_append_only BEFORE UPDATE ON dataset_corrections BEGIN SELECT RAISE(ABORT, 'dataset_corrections is append-only: a revert is a new record'); END;

-- trigger subset_versions_append_only
CREATE TRIGGER subset_versions_append_only BEFORE UPDATE ON subset_versions BEGIN SELECT RAISE(ABORT, 'subset_versions is append-only: an edit or a re-pin is a new version'); END;

