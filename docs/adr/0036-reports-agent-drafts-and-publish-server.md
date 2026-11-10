# ADR 0036: Reports, agent drafts and a publish server

- Status: accepted (owner answers of 2026-11-10 recorded below; UI: reports mock direction B approved on 2026-11-10)
- Date: 2026-11-10

## Context
The owner keeps weekly results and method notes for the supervisor in Notion and has reached its block limit. What is wanted instead: the owner's VGT agent drafts readable reports from real runs and figures, the owner checks and sends them, and the professor reads and comments at any hour. Three things are missing today:
1. **No document.** A board holds figures and job panels with short notes; there is nowhere to write an argument with equations, a results table and links between notes (shape.md section 11: hybrid documents that reference artifacts with provenance, v1 "linked notes").
2. **No agent interface.** The owner API is loopback-only with no token; there is no MCP server (shape.md section 11: one provider-neutral MCP server over the same operations, agent work is a draft).
3. **No always-on reader surface.** The review listener (ADR 0011) runs inside the owner's instance, so a reader link dies when the laptop sleeps.

ADR 0028 chose a shared multi-user server (option B) as the long-term shape, with its own design ADR before code. This ADR does not implement B. It adds the narrower thing the owner needs now and keeps the security boundary where it is: the owner's instance keeps the data, the ssh agent and the VPN; the public machine holds only what was published.

Owner answers (2026-11-10): laptop plus a publish server (not the whole app on the server); the agent drafts and the owner publishes; import an existing Notion Markdown export.

## Decision

### Reports (owner instance, mutable working state)
- A **report** is a title and a Markdown body stored in the catalog, with an edit history (each save: author `owner` or `agent`, time, base revision). It is mutable working state, not pinned evidence.
- Body syntax is CommonMark plus GitHub tables, `$…$` / `$$…$$` math, and three reference forms that the server resolves:
  `![[figure:<revision>]]` (a pinned figure revision, ADR 0011), `[[run:<id>]]` (a run line), `[[Report title]]` (another report; unresolved titles render as plain text marked "no such report").
- **Backlinks** are computed from the reference forms on save (a link table), not by scanning bodies on read.
- **Raw HTML is never rendered** (owner or reader side). Markdown renders with `markdown-it` (`html: false`) and math with KaTeX (`trust: false`, no `\href` or `\url`). Both are MIT and new dependencies of `apps/web`.
- Attachments (images a report needs that are not Vesqira figures, mainly from the Notion import) are stored content-addressed under `--data-dir/attachments`, PNG, JPEG or GIF only (the Go standard library decodes them; WebP would need a new dependency), fully decoded on upload, at most 8 MiB and 4096 × 4096, like figure previews.
- **Concurrent edits:** an update names the revision it was based on; a stale base is refused with 409 and the current text, so an agent and the owner never overwrite each other silently.

### The agent interface
- `vesqira mcp` is a stdio MCP server in the same binary, built on the official Go SDK (`github.com/modelcontextprotocol/go-sdk`, a new dependency of `services/api`). It calls the owner API on loopback and holds no state of its own. It is started by the agent's own MCP client on the owner's machine; it opens no port.
- Tools: list and read reports, runs, figures and comments; create a report; update a report (with base revision); attach an image; draft a reply to a comment. **There is no publish tool and no resolve tool.** A drafted reply waits until the owner sends it.
- Every write by the agent is recorded with author `agent` in the report history and in the audit log, and the report shows which blocks the agent changed since the owner last looked.
- **What this boundary rests on (review of 2026-11-10).** The agent cannot publish, send, resolve or make reader links *through its MCP tools*: they are fixed routes, and a test pins the list. It is not a boundary against a process running as the owner: the owner API is unauthenticated on loopback by design, and the publisher key file is readable by the owner's user, so an agent that also has a shell or file access as the owner can do anything the owner can. The author field is likewise declared by the client. Run an agent that reads supervisor comments (untrusted text from a link holder) with the MCP tools only, or under a separate user without access to the Vesqira data directory, if this matters. A confirmation only the UI can produce would not change this for a same-user process.
- A plain CLI (`vesqira report get|put|list`) wraps the same calls for scripts that do not speak MCP.

### Publishing
- The owner publishes a report revision. Publishing **freezes** it into a published revision built on the owner's server, like a board snapshot: Markdown text, figure references replaced by the frozen preview and the reviewer-safe provenance of ADR 0011, run references replaced by the reviewer run line (ADR 0014), report links to reports not shared with that space replaced by "not shared", attachments copied. No path, host, dataset id, run directory, script or token reaches it; the reflection test of ADR 0011 extends to the published-report type.
- A **preflight** shows exactly what goes out (pictures, run lines, unshared links, the text diff from the last published revision) before the owner confirms.
- Unlike a board snapshot link, a reader sees the **latest published revision** of each report in their space, and can open earlier revisions. Revisions are immutable; publishing never edits one.

### The publish server
- `vesqira publish-server` is a separate mode of the same binary, run on the owner's server behind a TLS reverse proxy (Caddy example in the setup docs). It has its own SQLite file and data directory and nothing else: no catalog, source roots, worker, hosts, jobs, ssh or audit of the owner's instance. A new internal package `publish` holds its store and handlers and, like `review`, may not reach anything beyond its own store (enforced in `internal/arch`).
- **Publisher credential.** `vesqira publish-server add-publisher` prints a 256-bit key once; only its SHA-256 is stored. The owner's instance stores the key in a 0600 file beside the catalog (never in the catalog, logs, exports or reports) and sends it as a bearer token over TLS. Only a publisher can upload revisions, create or revoke reader links, post owner replies, resolve threads and fetch comments.
- **Readers and spaces.** A space is the set of reports shared with it. The owner creates a **reader link** for a named person on a space (for example "Supervisor"). It is a capability token like ADR 0011 (256-bit, hashed, shown once, identical 404 for bad, revoked or expired tokens, rate-limited failures). Comments made through a link carry the link's name, set by the owner, instead of a self-declared name. This is attribution, not authentication: anyone holding the link can comment as that person, and the setup docs and the link dialog say so. Reader links may have no expiry (a standing space is the point) and are revocable at once; the default expiry is none and the owner can set one.
- **Comments** anchor to a block of a revision (block ids are hashes of the block's normalised text, assigned at publish) or to a place on a figure (ADR 0023). On a later revision a thread is shown at the same block if a block with that hash still exists, otherwise under "on revision N, this paragraph changed since". Replies are one level, the owner resolves (ADR 0023 rules unchanged). Limits: 4000 characters, 2000 comments per space, JSON only.
- **Sync.** The owner's instance pushes revisions and owner replies, and pulls comments with a cursor whenever it is online (on start, every minute while open, and on demand). Pushes are idempotent (revision ids are content hashes), so a retry after a timeout cannot duplicate. The reader page shows when the author's instance last synced, so a professor knows whether their comment has been seen. If the laptop is off, reading and commenting keep working.
- The existing `--review-addr` board links are unchanged. Publishing boards to the publish server is a later step that reuses the same mechanism.

### Notion import
- `vesqira import notion <export.zip>` (owner instance only) turns a Notion "Markdown & CSV" export into draft reports: pages become reports, links between exported pages become `[[wikilinks]]`, images become attachments, database CSVs become Markdown tables. The zip is untrusted input: entries are canonicalised and confined to a temporary directory (no absolute paths, no `..`, no symlinks), total expansion is capped (default 2 GiB and 50 000 entries), HTML is converted to text, not kept. A dry run lists what would be created first. Imported reports are drafts; nothing is published by an import.

### Hardening after the review (2026-11-10)
- A new publish server address never inherits the stored key (the key file is removed until it is entered again), so saving an address cannot send the key elsewhere. The key file is written as a fresh 0600 temporary file and renamed.
- The frozen revision is kept until the server confirms it, and a retry of the same working revision resends those bytes, so a push whose answer was lost does not leave the report stuck on a conflict.
- A reader link may post at most 100 comments a day; only readers' comments count towards a space's 2000, so the owner can always answer.
- Pictures are looked up through an index of the assets each revision shows, not by scanning documents.
- The preflight names run settings that look like a machine, account or address (partition, user, host, an e-mail, a dotted host name). The run import only drops paths; it does not decide what is sensitive.
- Follow-ups not done yet: strip metadata from attachments when publishing (EXIF, PNG text), decode the derived-data `software` field into a fixed shape before it reaches provenance, bound the Markdown inline scans in the reader for adversarial 64 KiB blocks, and guard the comment cursor against an absurd id from a compromised server.

### Pages, front matter, changes and deletions (2026-11-10)

- **Pages.** Reports form a tree (`reports.parent_id`, schema 3). Moving a page under itself or its own child is refused; deleting a page moves its children up. The Notion import keeps the export's hierarchy, and a database view page absorbs its table instead of becoming a second page.
- **Front matter.** A body may start with Obsidian-style `key: value` lines between `---` fences (`internal/reports/front.go`, `apps/web/src/reports/front.ts`, one shared fixture). Only `date` (YYYY-MM-DD) and `summary` are shown and published, as `Revision.date` and `Revision.summary`; every other key, such as an imported Notion `status`, stays on the laptop.
- **What changed.** Block ids are content hashes, so the reader page marks the parts of the latest revision that are not in the revision this browser last read (kept in the browser's local storage) and says how many were removed. No server state is involved.
- **Quoted comments.** A thread's first comment on a text part may carry the words the reader selected (`quote`, at most 500 characters, plain text). Both pages highlight them where the browser supports the CSS Custom Highlight API.
- **Deleting and withdrawing.** The owner deletes any comment (a thread's first comment takes its replies). A reader deletes their own comment while nobody has answered it. Deletions leave tombstones that the owner's sync follows. Withdrawing a report deletes every published revision, its pictures and its comments on the publish server and the local record of them. The draft stays, and publishing it again starts at revision 1.

### Not now
Graph view of links (planned after backlinks, once there are enough reports to make it useful); live co-editing (CRDT); full accounts and roles (ADR 0028 B); LaTeX manuscripts (shape.md section 11); publishing boards to the publish server.

## Alternatives
- **Run the whole app on the server** (owner's second option): one place, no sync, but the server would need the VPN, the owner's ssh access to the clusters and data, and the unauthenticated owner API would have to be tunnelled or given auth now. A compromise would then expose cluster access. Rejected by the owner.
- **Keep reports in Notion or Obsidian and link figures out.** No provenance in the document, Notion's block limit remains, and Obsidian has no reader comments without a third-party publish service.
- **Let the agent publish.** Convenient, but an agent's mistake would reach the supervisor with no human in between. Rejected by the owner; can be revisited per space.
- **Reader accounts (ADR 0028 C).** Real authentication, but an identity provider or email delivery to run. Named reader links give attribution now; accounts remain stage 7.
- **Sync through a shared Git repository of Markdown files.** Familiar to agents, but comments, anchors and revocation would need another layer anyway, and Vesqira does not push to research repositories without explicit instruction.

## Consequences
- A second deployable mode with its own data and its own failure modes. If the server is lost, the owner republishes from the laptop (it holds every report and every revision), but comments that were never pulled are lost; the server's data directory must be backed up.
- The publish server is internet-facing. Its surface is small (read a space, read a revision, read a picture, post a comment; publisher routes behind the key) and it holds only published material, so a compromise exposes what was already shared, the reader names and the comments, never data, paths or cluster access.
- New catalog tables (reports, report history, report links, attachments, publish target, sync cursor) are created idempotently like the others, so the catalog schema version does not change (ADR 0020 needs a migration only when an existing table changes). Export and restore (ADR 0022) include reports and attachments.

## Validation
- Store tests: base-revision conflicts, history authorship, backlinks on save and rename, unresolved links, attachment decoding and caps.
- Rendering tests: raw HTML and `javascript:` links inert, KaTeX with `trust: false`, every reference form, unresolved and unshared links.
- Publish tests: a reflection test that bans path, host, dataset id, token and credential fields from every published type; an end-to-end Go test that registers a dataset under a distinctively named directory, writes a report with a figure and a run, publishes to a publish server on a test listener and checks on the wire that no response contains the path, the root or the host.
- Publish-server tests: publisher key required on every publisher route; reader tokens identical 404 and rate limited; a token opens only its own space; comment limits; idempotent pushes; anchors carried to the next revision or reported as changed.
- MCP tests: tool list has no publish or resolve; a write is recorded as `agent`; a stale base returns the conflict.
- Import tests: zip-slip, symlink, absolute path, oversized and over-count archives refused; a fixture Notion export imports to the expected reports and links.
- In a browser: the approved mock's screens at 1440, 1280 and 390, light and dark, with a keyboard pass and axe, against a real publish server on a second port.

## Rollback / Migration
Additive tables and a separate server database; dropping them and the `publish` package removes the feature without touching datasets, runs, figures or boards. Revoking every reader link or stopping the publish server withdraws all access immediately.
