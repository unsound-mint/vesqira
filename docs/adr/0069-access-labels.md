# ADR 0069: Access labels on datasets

- Status: accepted, implemented (schema 34); the owner reviews it afterwards
- Date: 2026-11-13
- Related: ADR 0011 (review surface), 0050 (group server), 0066 (push to the group server), GitHub issue 135

## Context
A researcher often holds data that may not be shown yet: a collaboration's proprietary cube, an observation whose proposal period is still running, a result nobody has validated. Vesqira let a board or a report be published to a reviewer or a group with no question asked about that. A reviewer never gets the data, only frozen pictures, but a picture of unreleased data shows it. The author is the only person who knows the status of the data, and nothing in the system recorded it.

## Decision
**A label on a dataset.** Each dataset has one label: `none` (released, the default and the absence of a row), `collaboration` (collaboration only) or `unreleased`. The order is unreleased over collaboration over none. The label is **mutable working state with an audited history**, not a version system: every change is an append-only row (who, when, previous label, a reason of up to 500 characters) in `dataset_access`, and an audit entry. Setting the label a dataset already has writes nothing.

**A declaration, not a check.** The label is what the researcher says. Vesqira cannot see whether a file is proprietary, and the UI says so. Nothing about it is "verified".

**Inheritance, computed when asked.** A figure, a board and a report carry the most restrictive label among the datasets they depend on. It is computed from the recorded dependency edges (`derived.sources`, the figure's dataset, scene and identity references, run outputs) at the moment it is needed, never copied, so relabelling a source shows at once everywhere. A dataset that a run made also takes the labels of the run's input cube (below). The dependency walk is cycle-safe and loads the derived edges only when some label exists in the workspace. The effective label comes with the names of the labelled datasets that bring it in (`via`).

**Publishing asks.** Publishing a board or a report that carries a label needs `confirmAccess: true` in the request. Without it the server answers 409 `access_confirmation_required` with a sentence naming the figures and the datasets. For a board, the check runs inside the publication, after the figures are frozen, so a label set between the preflight and the publication cannot be missed. Report publishing is a local-instance path (the group server leaves it out), so it has the same confirmation and the label on the revision, and no workspace policy; reports reach a group server only by push, where the policy applies.

**What the reviewer gets.** The frozen snapshot and the review page state the label's name (and each figure's). The label adds no dataset name, id or list of labelled sources: `via` is the owner's alone. A figure's own provenance still names its source file, as it always did (ADR 0011 and 0036). The label stored in a snapshot is frozen with it; relabelling later does not rewrite what was published. A reviewer never receives data (ADR 0011 unchanged).

**Workspace policy on the group server.** An admin (`workspace.admin`, which needs a recent sign-in) chooses which restricted labels **block** publishing in the workspace (`workspaces.blocked_labels`). A blocked label refuses a board publication with 403 `publishing_blocked`, and `confirmAccess` does not override it. If the policy cannot be read the server fails closed and blocks both restricted labels. A local instance has no policy; it only asks for confirmation.

**Who may change a label.** Raising a label needs the right to write the dataset (Researcher and above). **Lowering needs Maintainer.** Otherwise a Researcher could undo the admin's block by relabelling and publishing. A Viewer sets nothing.

**Pushing counts as publishing on the group server.** Pushing work into a workspace makes it visible to its members (ADR 0066), so the server applies its policy to pushed figures, boards and reports. A local instance puts the label it computed into the pushed document (`access`, omitted when released, and `none` is refused so one meaning has one encoding and one hash), and adds a warning in the push preview. The server refuses a pushed item that declares a blocked label with 403 and stores nothing. It enforces what is declared; it cannot see the data.

**The label outlives its dataset.** Rows are keyed by dataset id with no foreign key and stay after a dataset is deleted, so deleting and re-registering a file does not quietly release it. The lifecycle guard lists this as intentional.

**UI.** An Access section in the dataset inspector (the control, the inherited label and its sources, the history), a text-and-icon badge (never colour alone) on the dataset rows, on figures and board panels, on the snapshot list, in the publish confirmation and on the review page, a confirmation in the report publish dialog, and a publishing-policy control for admins on the members page.

## Alternatives
- **Copy the label onto each figure when it is made.** Rejected: it goes stale when a source is relabelled, and the stale copy is the dangerous direction.
- **A label per figure or board.** Rejected: the author would have to remember it each time; the fact belongs to the data.
- **More levels, or free-text labels.** Rejected for now: three levels cover the cases in the issue, and a policy needs a fixed vocabulary to enforce.
- **Block in the client only.** Rejected: AGENTS.md requires server-side enforcement.
- **Let any writer lower a label.** Rejected for the relabel bypass above.

## Consequences
- A figure from labelled data can be published only after one explicit yes that names the data. Released data behaves as before.
- **Run outputs inherit their input cube's label by inference.** A run records its input file only by name and size, not a dataset id. A run's outputs therefore inherit the most restrictive label of every dataset in the workspace whose file name (base name) and size equal the run's `input_name` and `input_size`. The match is inferred, not known: the entry in `via` says "input of run X, matched by file name and size", and the inference can only add restriction, never remove it (two files of the same name and size in different folders both count, and the stricter label wins). Where the job that made the run recorded the exact dataset it was drafted on (a rerun on another cube, `inputRerun.input.datasetId`), that edge is used instead of the guess. A run whose input matches no labelled dataset stays released; a run whose input file was never registered, or was renamed, is not protected. The walk stays cycle-safe.
- The label is trusted as declared. A researcher who never labels gets no protection; the workspace policy applies to labels that exist.
- The review surface and every frozen type gained a whitelisted `access` field that holds a label name only (the frozen-type reflection test covers it).
- Schema 34 adds `dataset_access` and `workspaces.blocked_labels`.

## Validation
- Go: inheritance through derived edges and cycles, through a run's input cube by name and size (a labelled cube, a run imported with that input, its psi output and the figure of it, the 409 and the policy 403), a run matching nothing staying released, two matching files taking the stricter label, the job's exact edge preferred, ordering pinned, idempotent append-only history, the 409 without confirmation and the 201 with it, the snapshot and review JSON holding the label name and no `via`, `restricted` or dataset id, report confirmation and the label on the published revision, a Researcher unable to set the policy or to lower a label, an admin block returning 403 even with confirmation and leaving no snapshot, an admin of one workspace refused when the path names another, push refusal with nothing stored, the label carried by a local push, and the scope and lifecycle guards for the new table.
- Web: the badge, the control, the inherited label, the 409 and 403 flows in the publish panel, and the review page.
- One Playwright spec: set a label, make a figure, see the confirmation, read the label on the review page at 390 px, with axe scans in light and dark on the review page and the board, and screenshots.
- Not covered: the report publish dialog in a browser (Go and the component only), a job-recorded input edge through the HTTP API (unit-tested only), and a v33 to v34 migration test beyond the recorded schema snapshot.

## Rollback/Migration
The migration only adds a table and a column, with no backfill, so every existing dataset stays released. To roll back, drop `dataset_access` and the column and remove the routes. Snapshots and pushed documents that carry `access` stay readable, because the field is optional everywhere.
