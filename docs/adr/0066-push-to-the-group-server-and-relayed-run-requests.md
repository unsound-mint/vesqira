# ADR 0066: Push to the group server and relayed run requests

- Status: proposed; decided under the owner's delegation, awaiting owner review. Sections 1 and 2 (link tokens and push) are **implemented** (slice 4, schema 32; see "Implementation" below); section 3 (relayed run requests) is specified, not implemented.
- Date: 2026-11-12
- Required by ADR 0050 (section 5 and the owner answers of 2026-11-12) before slices 4 and 5. Amends ADR 0050 where it says so below.

## Context

After the owner answers to ADR 0050, no ssh key is ever held on the group server: it is a cloud VM that never reaches a cluster. Compute stays on each researcher's own **local** Vesqira. Two protocols must connect the two kinds of instance:

1. **Push (slice 4).** A local instance needs to put work in front of the workspace: figures, run lines, board snapshots and report revisions.
2. **Relayed run requests (revised slice 5).** A member without cluster access needs to ask a key holder for a run, and to see what happened to it.

Constraints from earlier decisions:

- **Direction.** The local instance reaches the server over HTTPS. The server never connects to a local instance (ADR 0050, owner answer 3).
- **What crosses.** Frozen, reviewer-safe copies only: the ADR 0011 figure provenance, the ADR 0014 run line, the ADR 0011 `FrozenItem` whitelist, and the ADR 0036 published revision, with no path, host, dataset id, script or token. Pictures have their metadata stripped (`internal/imgmeta`).
- **Scripts.** The script is the unit of trust and approval binds to its hash (ADR 0016). Sweeps are approved once over `sweepHash`, and their drafts never carry hand edits (ADR 0048).
- **Unanswered status.** Not answering is not a verdict (ADR 0043).
- **Interview (shape.md, Q29–Q31).** Approval covers the executable, parameters, inputs, limits and destination, never a free command. The lifecycle is visible, so nobody has to guess whether a run waits on approval, ssh or Slurm. Whether the cluster permits delegation is unverified.

The task names "the identity evidence of ADR 0017/0050" for deciding whether a pushed dataset is the same file as one on the server. ADR 0017 records no file identity. The device and inode of ADR 0050 section 8 are local to one machine and mean nothing on another. The portable evidence is ADR 0053's fingerprints and its `identity.Relate` tiers, so that is what this ADR reuses.

## Decision

### 1. Linking a local instance (amends ADR 0050 section 2)

- **Creating the link token.** On the group server, a member chooses "Link a computer". This needs a recent sign-in. It mints a personal access token (ADR 0050 section 2: 256-bit, shown once, hashed, default 30 days, at most 1 year) with two new properties:
  - It is bound to **one workspace**. ADR 0050 tokens were account-wide.
  - It carries one or both of two new **narrow scopes**, `push` and `relay`. Neither implies `read` or `write`.
- **What each scope allows:**
  - A `push` token may upload frozen items and ask which hashes the server already holds.
  - A `relay` token may publish the holder's inbox, read requests addressed to the token's account, and report on them.
  - Neither may list or read other workspace content, file a request, change membership, or act in another workspace.
- **Instance identity.** The local instance generates a random 128-bit `instance` id when it is first linked. It sends the id with every call, and the server records it with each push and status report. The id serves supersession and the "last heard from" display. It is not a credential.
- **Local storage.** The token is stored in a 0600 file beside the catalog, written and replaced like the publisher key (ADR 0036 hardening). It is never stored in the catalog, logs or exports. A changed server address never inherits it. The client sends it only to the configured `https` origin, with normal certificate verification.
- **Revoking.** Revoking the token on the server cuts the instance off at once. Unlinking locally deletes the file.

### 2. Push

**Items.** Four kinds, each an immutable document built on the local instance by the code that already freezes it for reviewers:

- **`figure`.** The ADR 0011 frozen figure: picture asset, title, caption and reviewer-safe provenance.
- **`run`.** The ADR 0014 reviewer run line: name, short commit, dirty flag, finish time, parameters with location-like keys dropped, and declared metrics with units.
- **`board`.** The ADR 0011 `FrozenItem` list with its layout. Job panels are frozen to name and state.
- **`report`.** The ADR 0036 `publish.Revision`.

**Identity.** An item is identified by its **content hash**: SHA-256 of its canonical JSON with the id field empty. `publish.ComputeID` already does this, and the same function is used for all four kinds. Pictures and attachments are **assets**, addressed by the SHA-256 of their cleaned bytes. Pushing the same hash twice stores it once and answers with the existing item, so a retry cannot duplicate.

**Supersession, without a two-way merge.**

- **Origin key.** Each item carries an opaque origin key: the first 16 bytes, in hex, of HMAC-SHA256 under a per-instance secret over `kind:local id`. The key is stable across pushes, reveals no local id, and so passes the ADR 0036 reflection test.
- **Lineage.** On the server, (account, instance, kind, origin key) is one lineage. A new hash in a lineage becomes its head, and the previous head is marked `superseded`. Superseded items stay readable, immutable and commentable, like report revisions.
- **Editing.** Edits happen only locally. The server never edits a pushed item, and only its pusher's instance can supersede it.

**Dataset references, and the separate identity field.**

- **What a reference carries.** A figure's or run's datasets travel as references: file name, size, header digest, and the ADR 0053 fingerprints with their algorithm. That means the sampled fingerprint, plus the full SHA-256 when it was computed.
- **Where the field lives.** This is a push-only `identity` field beside the provenance, never inside it. ADR 0053 pins that reviewer-safe provenance carries no identity. The server never forwards this field to a review link, a snapshot or the publish server, and a test pins that.
- **Matching.** The server compares the reference with the datasets registered under the workspace's data roots, by the ADR 0053 rule:

| Server finds | Reference shows | Effect |
|---|---|---|
| equal full SHA-256 | "on this server: <name> (verified same bytes)" | linked; members open the server's dataset, read through the server's own files |
| equal size and sampled fingerprint, no full hash on both sides | "probably on this server as <name> (sampled; not verified)" | not linked for reading; a Researcher may request a full hash on the server copy, which upgrades the reference to verified or refuses the match |
| any computed value differs, or nothing comparable | "not on this server" | catalogued facts only |

- **Typical outcome.** Large cubes usually have only a sampled fingerprint, so most matches will be "probably". That is the honest answer, and only a verified match is ever read, as in ADR 0053.
- **What never counts.** A file name match never counts. Device and inode never cross machines.
- **Re-evaluation.** Matching is re-run when a server dataset gains or changes a fingerprint.

**What a push never carries.** The ADR 0036 reflection test extends to every pushed type and bans: paths (absolute or relative) and folder names; host names or ids, ssh aliases and cluster usernames; partition and account values, Slurm ids, job directories, scripts and configs; local dataset, run, figure or board ids; tokens and keys; `figures.identity` beyond the reference above; owner-only fields (the ADR 0016 `request`, sweep labels).

Before sending, the local instance runs the ADR 0036 **preflight**. It shows each item and asset going out, the parameters that look like a machine, account or address, and the references that will show "not on this server". Nothing is pushed without that confirmation, except the run line of a relayed request (section 3) that the key holder agreed to at approval.

**Order and partial failure.**

1. **Ask what is missing.** A push first sends every hash it would upload to `POST /api/push/have`, and the server answers which ones it lacks.
2. **Upload in dependency order.** Missing assets go first, then runs, then figures, then boards and reports.
3. **Each item is atomic.** Each item is one request and one server transaction. The server refuses an item whose referenced assets or items are absent, with 409 and the missing hashes, so a board never appears with holes.
4. **Failure midway.** A failure leaves the earlier items stored. They are valid on their own and harmless.
5. **The queue.** The local push queue is durable. It keeps the frozen bytes of each item until the server confirms them, so a retry resends identical bytes (ADR 0036 hardening), and it shows each item as `sent`, `waiting` or `refused: <reason>`.

**Retries.** A network error, 5xx or 429 is retried with a doubling wait (1, 2, 4 … up to 15 minutes), reset by any success. Any other 4xx (size, scope, validation) stops that item, shows the sentence, and is never retried. After a lost answer, `GET /api/push/heads` returns this instance's lineage heads, so the reply is reconciled without a second upload.

**Limits.** An asset is at most 8 MiB and 4096 × 4096, decoded and re-checked on the server; a document at most 4 MiB (the `publish` constants); a board at most 200 items. At most 600 items and 2 GiB per account per day. Pushed assets count towards the workspace's ADR 0050 section 8 byte quota, and a refusal shows the current use. Pushing needs Researcher or higher in the token's workspace; a Viewer's token cannot get the `push` scope.

**Audit.** Every server row records the actor (account, via token id and instance), the workspace, the kind and the hash. Events: `push.item` (with the superseded hash), `push.refused` (with the reason), `push.match` (a reference becoming verified or probable) and archive. The local log records each confirmed or refused item and the server's answer.

**Who removes pushed items.** Members archive pushed items under the ADR 0050 ownership rule: their own as Researcher, any as Maintainer or Owner. Archiving hides an item. Deleting removes its lineage and its assets once nothing else references them.

### 3. Relayed run requests (replaces ADR 0050 section 5 "Delegated execution" and slice 5)

**Inbox.** A key holder's instance, through `relay`, publishes an **inbox**: the cluster **labels** it accepts requests for, and for each label the request templates and their bounds.

- **Templates.** Only `vgt` and `rohsa`. There is never a custom script.
- **Bounds.** The allowed config keys are a subset of the ADR 0016 and ADR 0046 whitelists, never `output_dir`, `input_file` or `input`. Resources are capped at a maximum of CPUs, memory and time.
- **Labels.** Labels are typed by the key holder: at most 40 letters, digits, spaces and `-`. They map to a host only on the key holder's machine. A label containing a dot, `@` or the host's destination is refused locally, so no host name becomes a label.
- **On the server.** The server keeps no clusters table, and labels are free text to it.
- **Pausing and changes.** Pausing an inbox stops new requests. A bound that is lowered leaves open requests to the key holder's judgement.

**Filing.** Any workspace member, from Viewer up (ADR 0050 matrix), files a request addressed to one key holder and one of their labels. It carries a template and typed parameter values within the inbox bounds, optionally a **base** (a pushed `run` item) and a **grid** (sweep, below), a plain-text note of at most 4000 characters, and an expiry of 1 to 30 days (default 14). It names no path, script, host or dataset id. The server validates it against the inbox as a first filter. Rate limits: 10 open requests per requester per addressee, and 30 filed per requester per day.

**How a pathless request becomes a script.**

- **Resolving the base.** The key holder's instance resolves the base through its own push records: the base's origin key maps to a local run, and through that run to the job's stored request (the ADR 0016 rerun prefill). That supplies the repository, the config text and the input.
- **No resolvable base.** If the base was pushed by another instance, or its run kept no request, or no base was given, the key holder **chooses** the base on their machine. The record then says "base chosen by <name>".
- **Output folder.** It is always the key holder's per-job folder (ADR 0046).
- **Inputs are never the requester's.** The requester can only change whitelisted values of a run the key holder already ran.

**Pull protocol.**

- **What is polled.** The instance calls `GET /api/relay/requests?after=<cursor>`. It answers the requests addressed to the token's account in that workspace that changed since the cursor: new, withdrawn or expired.
- **When.** At start, every 60 s while linked and online, at wake from sleep (detected as a jump between the wall clock and the monotonic clock), when the network changes, and on demand.
- **When the server does not answer** (flaky network, sleep, VPN changes), the wait doubles up to 15 minutes, as in ADR 0043, and any answer resets it. Missing a poll changes nothing on either side.
- **Bounding the reply.** Responses are size-capped. A page whose cursor does not strictly increase, or jumps more than 100 000, is refused whole, as in ADR 0036.
- **What the server shows.** It records each poll and shows requesters "<name>'s computer last checked at <time>", never "offline" or "ignored".

**Local re-validation is the real defence.** The local instance validates every parameter with the same template validators it applies to its own drafts. It refuses keys outside its current inbox bounds and its whitelist, and it never trusts the server's filter. The note is shown as plain text, never as Markdown or HTML.

**Review and approval.**

1. **Draft.** Rendering a draft (with `buildVGT` or `buildROHSA`) reports `waiting for review`. The key holder sees the request, its note, and the exact script.
2. **Edits.** The key holder may lower resources or change whitelisted values, which go into the approved parameters. They may also edit the script, which is recorded as "edited by hand: N lines". The script text never leaves their machine.
3. **Claim on the server.** Approving first claims the request: `POST /api/relay/requests/{id}/approve` with the script hash, the final typed parameters and the label. The claim is one winner, guarded by state: it succeeds only while the request is `waiting for review`, not withdrawn and not expired. It is idempotent for the same (request, hash, instance).
4. **Submit locally.** Only after a successful claim does the instance submit through the existing `submitDraft`, which re-checks the hash (ADR 0016).
5. **Offline at approval time.** If the server cannot be reached, there is no claim and no submission. The dialog says why, and offers to keep the draft as the key holder's own job, detached from the request.

This settles the races: a withdrawal or expiry made while the laptop was offline makes the claim fail, so the draft is never submitted on the request's behalf; of two instances of one key holder only one claim wins; a key holder who knows the cluster is down may still approve, and the request shows "approved, not yet submitted".

**Declining.** Declining, with a note, is allowed through `relay` or from the key holder's browser session.

**Status flows back as declared facts.** The instance reports transitions with a per-request sequence number. The server applies them monotonically, guarded by state as `Store.ApplyStatus` is, so a late `running` never overwrites an end state. Every status is labelled "reported by <name>'s computer at <time>".

| Server status | Set by | Meaning shown |
|---|---|---|
| `requested` | filing | waiting for <name>'s computer to fetch it |
| `waiting for review` | relay, after the draft is rendered | waiting for <name> to review |
| `approved` | relay claim | approved with script hash <short>; "submission not confirmed" while the local job is `submitting` (ADR 0016 amendment), "submission failed: <sentence>" if `sbatch` refused |
| `submitted`, `running` | relay | as Slurm reported to the key holder |
| `finished`, `failed`, `unknown` | relay | ADR 0043 words: "finished (exit 0:0), results not yet checked", never "success"; `unknown` keeps its ADR 0043 meaning |
| `declined` | relay or session | with the key holder's note |
| `withdrawn`, `expired` | requester or Maintainer; the server clock | only from `requested` or `waiting for review` |

**After approval.** Withdrawal is refused once a request is approved. The requester can add a note asking the key holder to cancel, and cancelling stays the key holder's act on their machine. Approved requests do not expire.

**Approval record** (the interview's "who requested, who authorised, what ran"): who requested; who approved, with the script hash, the final parameters, the label and the base; whether the script was edited by hand, and how many lines; submitted, running and end times. The server holds no script, folder or Slurm id.

**Results return by push.** At approval the key holder chooses whether the resulting run line is pushed automatically when the run is imported (default on, its content shown in the dialog). The request then links to that `run` item. Figures are pushed by hand, through the preflight. Outputs stay where they are, shown as references "not on this server" unless they match a workspace data root.

**Sweep requests: allowed, approved once.**

- **The grid.** A request may carry a grid of one to three whitelisted keys within the inbox bounds, expanding to at most 64 combinations (ADR 0048), validated on both sides.
- **Drafts.** The key holder's instance builds an ordinary ADR 0048 sweep from it: drafts rendered from the base, never hand-edited.
- **Review.** The key holder reviews with the existing sweep review, which shows differing lines with reasons.
- **Approval.** The key holder may drop combinations before approving. The claim carries `sweepHash` and the list of kept combinations. Concurrency is the key holder's choice (default 1); the requester may only suggest it in the note.
- **Status.** Status is reported per combination, and the request shows the counts.

**Account changes.** When the addressee is disabled or leaves the workspace, their inbox closes and their tokens are deleted; open `requested` or `waiting for review` requests become `declined` with "<name> can no longer take requests in this workspace"; approved requests keep their last reported status, marked "no further reports expected". A disabled requester's open requests are withdrawn. Pushed items of a removed account stay, attributed to "former member: <name>" (ADR 0050).

### 4. Endpoints (group server, multi-user mode)

Auth: **B** means bearer token only, with the scope named; **S** means browser session only (ADR 0050 CSRF rules). A token route refuses a session, and the reverse. Roles are the token's or session's account in that workspace.

| Route | Auth | Viewer | Researcher | Maintainer | Owner | Notes |
|---|---|---|---|---|---|---|
| `POST /api/push/have` | B `push` | – | yes | yes | yes | Which of the given hashes (at most 1000) are absent |
| `PUT /api/push/assets/{sha}` | B `push` | – | yes | yes | yes | Body hashed and decoded on arrival; 8 MiB |
| `POST /api/push/items` | B `push` | – | yes | yes | yes | One frozen item; 409 with missing hashes; supersedes own lineage only |
| `GET /api/push/heads` | B `push` | – | own instance | own instance | own instance | Reconcile after a lost answer |
| `GET /api/pushed`, `GET /api/pushed/{id}`, `…/assets/{sha}` | S | yes | yes | yes | yes | Heads and superseded chain; assets looked up by (workspace, sha) |
| `POST /api/pushed/{id}/archive`, `…/delete` | S | – | own | any | any | Delete needs a recent sign-in |
| `POST /api/pushed/{id}/references/{n}/full-hash` | S | – | yes | yes | yes | Full SHA-256 of the matching server dataset (ADR 0053 caps) |
| `PUT /api/relay/inbox` | B `relay` | – | self | self | self | Labels, templates, bounds; pause |
| `GET /api/relay/requests?after=` | B `relay` | – | addressed to self | addressed to self | addressed to self | The pull |
| `POST /api/relay/requests/{id}/review`, `…/approve`, `…/status` | B `relay` | – | addressee | addressee | addressee | Approve is the one-winner claim; status is monotonic with a sequence number |
| `POST /api/run-requests/{id}/decline` | B `relay` or S | – | addressee | addressee | addressee | With a note |
| `GET /api/run-inboxes` | S | yes | yes | yes | yes | Who takes requests, for which labels and bounds |
| `POST /api/run-requests` | S | yes | yes | yes | yes | Validated against the inbox; rate-limited |
| `GET /api/run-requests`, `GET /api/run-requests/{id}` | S | yes | yes | yes | yes | Status, record and notes; never a script |
| `POST /api/run-requests/{id}/withdraw` | S | own | own | any | any | Only before approval |
| `POST /api/run-requests/{id}/note` | S | own | own | any | any | A follow-up note, for example "please cancel" |
| `POST /api/account/tokens` (scopes `push`, `relay`) | S | – | yes | yes | yes | Recent sign-in; one workspace; a Viewer gets neither scope. (Written as `POST /api/tokens` before slice 4; the existing token route was extended instead of adding a second one.) |

The local instance gains loopback routes for the link settings, the push queue and preflight, and the mirrored request list. These are the local-owner's only, as today, and their bodies are fixed in the contracts when built.

**Withdrawn from ADR 0050** by the owner answers, and confirmed here: the `clusters`, `cluster_access` and `script_reads` tables; the server rule refusing "a hash this account never fetched" (scripts never reach the server, so the hash check stays local, ADR 0016); the run-request row of the matrix, replaced by the table above.

**New server tables:** `pushed_items`, `pushed_assets`, `pushed_references`, `relay_inboxes`, `run_requests`, `run_request_events`. **New local tables:** `group_link`, `push_queue`, `push_lineage`, `relayed_requests`, plus `jobs.request_ref`. Numbering is assigned at merge.

### Not now

Preapproved templates and budgets (shape.md; ADR 0050 slice 6); pushing datasets or managed copies; comments flowing back from the group server to the local instance; a push server stream; requests from review-link holders (ADR 0028 option A).

## Implementation (slice 4, 2026-11-13)

Sections 1 and 2 are built. Where the code differs from the text above, it says so here.

- **Tokens** (`internal/accounts`, `internal/authz`, schema 32 `access_tokens.workspace_id`). A token carrying `push` or `relay` is bound to the workspace the minting session acts in and carries no other scope (`accounts.CreateTokenIn`, `ErrBoundScopes`); account-wide tokens cannot be bound. "Link a computer" on `#/account` mints a `push` token for the default 30 days through the existing `POST /api/account/tokens` (recent sign-in, Researcher or higher there, 403 for a Viewer). `authz` gained `push.upload` (R, token-only: a session never holds it), `pushed.read` (V) and `pushed.archive` (R, ownership rule), the last two session-only. A bound actor acts in its workspace whatever `X-Vesqira-Workspace` says, its role is read on every request (a demotion or removal stops it at once), and a path object of another workspace answers 404. A `push` token minted before binding allows nothing. Relay allows nothing until slice 5.
- **Wire** (`internal/pushwire`, imports nothing). One envelope for the four kinds: `kind`, `origin`, `title`, `doc`, sorted `assets`, `references` (role, file name, size); the content hash is the SHA-256 of that JSON with `hash` empty. The fingerprints travel in `identity`, a list beside the item in the request body, outside the hashed item and outside the document. `pushwire.Check` and `pushwire.Scan` (banned keys such as `path`, `host`, `token`, `datasetId`, `scene`, any key ending in path, dir, host, folder, token, secret or password; values that look like absolute, home-relative, drive or file-URL paths) run on both sides.
- **Server** (`internal/pushed`, `internal/server/push_group.go`). Tables `pushed_items`, `pushed_assets`, `pushed_item_assets`, `pushed_references`. The server recomputes the hash, decodes the document strictly into the type of its kind (`pushedFigure`, `runs.Summary`, `pushedBoard` of `boards.FrozenItem`, `publish.Revision`) and refuses it unless re-encoding gives the bytes that arrived, and unless the pictures it shows are exactly the item's assets. A picture is refused unless it hashes to its name, decodes completely as PNG, JPEG or GIF within 4096 × 4096 and 8 MiB, and `imgmeta.Clean` finds nothing to remove. Each item is one transaction. A hash already held answers the existing row (200); when the same account and instance push it again it may add a full hash to a reference that had none, never change one (anyone else replaying an item they can read changes nothing); a hash that was superseded is answered as it is and the head is not moved back. Lineage is (account, instance, kind, origin). Missing pictures answer 409 with their hashes. Daily limits (600 items, 2 GiB of documents and pictures per account across workspaces, checked for a picture before it is stored) answer 429 with `Retry-After`; pictures count towards the workspace quota (507 with the current use). Audit: `push.item` (with the superseded hash, the token id and the instance), `push.refused`, `push.match`, `push.archive`. Pictures are files under `pushed/<workspace>/` beside the catalog.
- **Matching** follows the table above through `identity.Relate` against the primary and other locations of the workspace's datasets; a name is never compared. It runs at push time and again on every `GET /api/pushed/{id}`, so a server dataset that gains a full hash upgrades "probably" to "verified" at the next read. Members never receive the fingerprints themselves.
- **Local side** (`internal/outbox`, `internal/server/push_local.go`). Tables `group_link` (origin, instance id, the origin-key secret, workspace name), `push_queue` (the frozen request body and its picture hashes; pictures wait under `outbox/`), `push_lineage` (origin key to local object, for slice 5). The token is in `group.key` beside the catalog, written as a fresh 0600 file only after `GET /api/push/heads` accepted it, so a refused or unreachable server leaves an existing link as it was; a changed origin drops the token, starts a new instance and empties the queue. Unlinking deletes the token file and empties the queue but keeps the instance id, the secret and the lineages, so linking again to the same server continues the same lineages instead of duplicating them. `https` only, certificate verification, no redirects. Routes (all `local.only`): `GET/POST /api/group-link`, `POST /api/group-link/unlink`, `GET/POST /api/outbox`, `POST /api/outbox/preflight`, `POST /api/outbox/retry`. Queueing names the hash the preflight answered and is refused (409) when the object changed since, so nothing is queued that was not previewed. A background pass sends due items in the order runs, figures, boards, reports: it asks `have` first, uploads missing pictures, then the item; a hash the server already holds is marked sent without a second upload (the lost-answer case). Network errors, 5xx and 429 wait 1, 2, 4 … 15 minutes, reset by any success; any other answer is final ("refused: <sentence>"). The push token is redacted from support bundles; exports never contained it.
- **What is frozen**: the reviewer code paths (`boards.Freeze`, the report `freeze`, `runs.SummaryOf`, the figure's stored provenance). Pictures are cleaned with `imgmeta` and addressed by the cleaned bytes. A pushed report is the working revision (its number is the working revision and its time the last save, so an unchanged report pushes to the same hash). **Run settings that name a machine, account or address are left out** of a pushed run line and of the run inside a figure's provenance or a report's run block (`dropMachineParams`: the key says partition, account, user, host, node, cluster, queue, login, mail or server, or the value is an e-mail address; a dotted value such as a version is kept). The preflight says how many, and the item carries the count (`omitted`), which `#/pushed` shows. A published report still only warns. A figure keeps its revision although its provenance lost those settings.
- **Endpoints not built in slice 4**: `POST /api/pushed/{id}/delete` and `POST /api/pushed/{id}/references/{n}/full-hash` (a member upgrades a reference by computing the full hash of the server's own dataset with the existing fingerprint route, which the next read picks up); the preflight is a confirmation dialog, not yet the full screen.
- **Tests**: `internal/server/push_test.go` runs a group server on an `httptest` TLS listener and a local instance in one test: link, preflight, the four kinds, members' reads, the payload-leak scan of every request and answer (temporary and root folder, run folder, host name, user name, distinctive local ids, the origin-key secret, picture metadata, partition and e-mail values, the token outside `Authorization`), the same hash twice, supersession, revocation; retries with identical bytes and the lost answer; scope and binding (another workspace, a session, a Viewer, demotion, removal, an unbound token); refusals (missing picture, size, pixels, metadata, wrong hash, whitelist, 201 panels, 4 MiB, daily limit, quota); fingerprint tiers; each side's routes absent on the other. `internal/pushed` and `internal/outbox` have store tests; guards 3 and 5 cover the new tables and routes; `push_reflect_test.go` walks every field name of the four pushed types through the scan.
- **Known limits.** The leak test plants no ssh host alias, cluster user name or job-following board panel (no host is set up in it); those paths are frozen by the review code, which has its own tests, and a job panel is frozen to name and state. A report, caption or note that contains a path is refused whole by the scan rather than cleaned. Reverting an object to content pushed earlier answers that earlier item as it is (superseded) while the local queue shows "pushed"; the server keeps the newer head. The plain screens were not looked at in a browser.

## Alternatives

- **Server-initiated delivery** (webhook or WebSocket into the laptop). Breaks "the server never connects", needs an inbound port behind NAT and a VPN, and a compromised server could then reach the laptop. Rejected.
- **Long-poll or SSE for the inbox.** Requests are rare and minutes are fine. The ADR 0043 reasons apply, and an open stream adds sleep and reconnect complexity. Rejected.
- **The server stores or renders the script.** The script carries usernames, folders and partitions, and a compromised server could then present a script other than the one that runs. Rejected: only the hash crosses.
- **Two-way sync or merge of boards and reports.** Needs conflict resolution for little gain when one person authors each item. Rejected: a newer push supersedes.
- **Reusing the publish server and its publisher key.** That server is for outside readers with link tokens, not workspace members. The freezing and asset code is shared; the credential and store are not.
- **Matching datasets by name or path.** A name proves nothing (AGENTS.md invariant 7). Rejected for ADR 0053 fingerprints.
- **Approval without a server claim**, letting a key holder submit offline. A withdrawal or second instance could then be overtaken. Rejected: the key holder may still run it as their own job.

## Consequences

- No cluster credential, script or path ever reaches the group server. Its compromise exposes pushed frozen copies, requests and notes, and lets it forge or hide requests. It cannot run anything: execution needs the key holder to read the script on their own machine.
- **A stolen token is limited by its scope.**
  - A `push` token can add or supersede that account's items in one workspace. This is visible, audited and revocable, and the token cannot read anything.
  - A `relay` token can read requests addressed to that account, and decline them or forge their status. Every status is labelled as reported, and nothing executes.
- **The residual risk is social.** A key holder may approve a script without reading it carefully, or trust a persuasive note. Both decisions are theirs and are recorded.
- Requesters wait on a person whose laptop may be asleep. The page says when that computer last checked, and nothing is guessed.
- Most pushed cubes will show "probably on this server" until someone requests a full hash.
- ADR 0050 section 2 gains workspace-bound tokens and two scopes. ADR 0050's matrix gains the routes above.

## Validation

- **Reflection.** The test of ADR 0011 and ADR 0036 is extended to the four pushed types and to every server response of the routes above. It bans path, host, dataset id, script, token and credential fields.
- **End-to-end privacy.** A Go test registers a dataset under a distinctively named folder on a host with a distinctive alias and username, runs a job, pushes the run, the figure, a board and a report to a group server on a test listener, and checks every byte on the wire for none of them.
- **Identity field.** The push-only `identity` field never appears in a review snapshot, a review response or a publish-server revision.
- **Push mechanics:** the same item twice stores once; a new hash supersedes only its own lineage, never another account's or instance's; missing references give 409 with the hashes; an interrupted batch resumes with identical bytes and no duplicate; size, rate and quota refusals carry the reason; a `push` token cannot read `/api/pushed` or any other workspace route, or act in another workspace; a Viewer cannot get the scope.
- **Matching.** Equal full hash links; equal sampled values only give "probably" and are never read; any difference gives "not on this server"; a name match alone never links.
- **Relay:** a request carrying a path, script, host, unknown key, out-of-bounds value or custom template is refused by the server, and again by a local instance fed the same request directly (as from a compromised server); the note renders as text; the claim has one winner across two instances and against a concurrent withdrawal; an expired request cannot be claimed; no claim means no `sbatch` call (stand-in ssh logs every command); a hash change after the claim is refused by `submitDraft`; statuses are monotonic, a late `running` ignored; an unconfirmed submission shows "submission not confirmed"; poll backoff and reset, and a wake jump triggering a poll; a disabled addressee's open requests are declined.
- **Sweep requests:** 64 cap, kept-combinations list in the claim, `sweepHash` stale refused, concurrency default 1.
- **Against the real cluster:** one relayed request by the owner from a second account, recorded as evidence and not as a CI test. Not before slice 5 ships.

## Rollback / Migration

- **Scope of the change.** All additive: new tables on both sides by versioned migration with the automatic backup (ADR 0020), and routes mounted only with `--multi-user` on the server and only when linked locally.
- **Stopping it.** Revoking a token stops an instance at once. Unlinking leaves local work untouched, and pushed items stay on the server until archived.
- **Removing the feature.** Drop the routes and tables. Local jobs keep their `request_ref` as unread text.
