# ADR 0073: Review rounds

- Status: implemented (no schema change); the owner reviews it afterwards
- Date: 2026-11-13
- Related: ADR 0011 (review surface), 0036 (report revisions), 0042 (draw again), 0069 (access labels), 0071 (share this view), GitHub issue 130. Number 0072 is taken by the sky-context branch.

## Context
A result such as a phase separation becomes convincing over several review rounds, and a reviewer with little time needs each round to say what changed. A board could already be published again, and an older link could be marked replaced, but nothing described the difference, and a comment made on round 1 was invisible in round 2 even on a figure that had not moved. Report revisions have a text diff in the owner's preflight (ADR 0036); board snapshots had none.

## Decision
**A round is one published snapshot of a board, and the history is the board's snapshots in id order.** Snapshots are never deleted (a board with snapshots cannot be deleted, ADR 0041) and never edited, so "the round before" is the snapshot with the next lower id and the same board and workspace. Snapshots with `board_id` 0 (a figure shared on its own, ADR 0071) are in no history: the lookup returns nothing for board 0, the list does not number them, and a board's next round skips them. There is no schema change.

**The comparison is embedded in the newer snapshot at publish time.** `Frozen.Round` holds the round number and, from round 2, `Since`: the previous round's date and label, one `ItemChange` per item (new, redrawn or unchanged, the fields that changed, the earlier text and picture), the removed titles, a flat list of change lines, and the carried comments. It is computed once, in `internal/boards/rounds.go`, from the two frozen documents, and the same function builds the preflight, so the author sees before publishing what the reviewer will read. Reasons for embedding instead of comparing on the reviewer listener: the older document and its comments are not needed to render the page, the reviewer listener needs no second lookup to describe a change, and what the reviewer reads stays what the author published. The one cross-snapshot read at review time is a status check of the earlier snapshot (revoked, expired), described below. The embedded type is not `FrozenItem`: that type is also the push wire format (ADR 0066), and a round field on it would have reached the group server. A reflection test lists the allowed fields of every new type.

**Identity of a figure across rounds.** First the same revision (unchanged picture: its caption, title or note may still differ and is reported). Then the same drawing redrawn: the owner's "draw again" links a new figure to its original in `figures.identity.redrawnFrom` (ADR 0042), and following that chain to the first drawing gives a root revision. Two figures with the same root are the same figure redrawn. That link is owner-side and ADR 0042 says it never reaches a snapshot, so it is read from the catalog while the round is built (the published picture is pinned, the link is the one fact that is not) and only its result, "redrawn", is frozen. Figures are never matched by title or by what they look like: a different figure put in the same place is removed and added. A run panel matches the earlier panel of the same name. Duplicates match one to one in order.

**What the comparison reports.** Added, removed, redrawn, title, caption and note changes, a changed access label, a run panel's state. Corrections and masks changes are read only from the two frozen provenance documents (`corrections` by keyword and value, and the `tiers` whose name mentions a mask), for a redrawn figure; the catalog's current state is never consulted for a past round, and an unchanged revision has identical provenance by construction. Detail sentences name keywords and mask lines that the reviewer already sees in the provenance.

**Comments carry over as text.** For each matched figure, the comments made on the previous round, and those that round carried from earlier ones, are copied into `Since.Carried` with the round they were made in and re-keyed to the figure's revision in the new snapshot. They are read-only: the reviewer replies on the figure in the current round. A mark on the picture is kept only when the picture is unchanged; on a redrawn figure it is dropped and the comment says it was made on the earlier drawing, since a place on one drawing means nothing on another. Replies and the author's resolved state come along.

**Revocation and expiry of the earlier round.** The new link must never open a revoked round.
- Revoked at publish time: nothing is copied from it; the stored comparison keeps only what the new round holds (states and the flags of what changed).
- Revoked later: the reviewer-facing methods read the earlier snapshot's status and withhold, from the reader's copy, the previous round's label, texts and pictures, the removed titles, the detail sentences and the carried comments, and the picture route refuses the earlier picture. A reviewer sees "no longer available". The date of the earlier round stays, since it is the heading's date and holds no content.
- Expired but not revoked: the text and comments frozen into the newer snapshot stay (the author published them into it), and the earlier picture is withheld, as the issue required. With the default expiry this means the picture of a redrawn figure is often gone by round 2; the text diff is not.
- The earlier picture is served only if its hash is listed in this snapshot's own comparison and the earlier link is valid, so a token never opens a removed figure's picture or any other.
- The earlier link is never needed to open the newer one, and the reverse is not true: neither ever reaches live data.

**Access labels.** Unchanged: the round's access gate is the board's gate (`publishItems`), applied to the figures being published. A change of label is a change line.

**Owner side.** `GET /api/boards/{id}/snapshots` returns for each snapshot its round number and a `RoundChanges` (the change lines, how many comments were carried, the removed count). Snapshots made before rounds have no stored comparison, so they are compared with the snapshot before them by revision only; redraw links were not recorded then, so a redrawing of that time shows as removed and added. The Boards screen shows "Round N", a "Changed since round N-1" disclosure per snapshot, and the round a publish would make in "Check what will be shared".

**Reviewer side.** "Changed since <date>" with the summary and the list, a marker in words on every figure (New, Redrawn, Unchanged picture, with a glyph that is not the only cue), the earlier title or caption where it changed, the earlier drawing in a disclosure while available, and the carried comments labelled "Round N" in a read-only section above the comment box.

## Alternatives
- **Compute the diff on the reviewer listener from two snapshots.** Always current about the earlier link's status, but the listener would read and parse a second document for every page view, and its output would depend on a snapshot the author may have revoked. Rejected; the status check is the only cross-snapshot read.
- **A table of rounds, or a `previous_id` column.** Rejected: ids already order a board's snapshots, snapshots are never deleted, and a column would need a migration for something derived from existing data. If snapshots ever become deletable, the lookup must become a stored link (a migration then).
- **Matching figures by title.** Rejected as a guess; the owner-side redraw link is the only recorded statement that two figures are the same.
- **Freezing the redraw link into the snapshot.** Rejected: ADR 0042 keeps it away from reviewers.
- **Copying carried comments with live reads on each view.** Would show comments that arrive on the earlier link after the new round was published and would need the earlier link at read time. Rejected for the same reason as the diff.

## Consequences
- A snapshot grows by the carried comments (at most 500, the per-snapshot comment limit) and the earlier text of changed figures.
- Comments made on an earlier link after a later round was published do not appear in the later round. The author sees them in the history.
- Carried comments may come from a reviewer other than the one holding the new link, if the author gave the two rounds' links to different people. They are shown with the author name each wrote under, as in the first round. Publishing a round is the author's act of sharing them. An author who does not want this can revoke the earlier round before publishing the next, which copies nothing.
- Two publishes of one board at the same moment could both take the same round number. The owner API serves one author at a time in practice; not guarded.
- A figure replaced by a different figure counts as removed and added.

## Validation
- Go (`internal/boards/rounds_test.go`): added, removed, redrawn and unchanged with a caption change; no match by title; a redraw chain followed to its first drawing and not into another workspace; corrections and masks compared from provenance (table); access label and run state; duplicate panels; the history list with round numbers and an old snapshot compared by revision only; comments carried to the same figure with their round, anchors kept or dropped, replies kept, a third round carrying round 1 on; the earlier picture served only while valid (expired and revoked refused, a removed figure's picture never); a revoked round absent from the new link's whole document; nothing copied from a round revoked before publishing; figure shares excluded from numbering and history; another workspace's boards have their own history; the preflight shows the round and creates nothing; a reflection test on every new type and the absence of the redraw link. The reviewer-response contract test validates the new fields against the OpenAPI document.
- Web: the reviewer page with the list, markers, the earlier drawing, the unavailable wording, carried comments read-only, nothing on round 1; the owner's list and preflight.
- Playwright (`zzzzzzzzzzz-review-rounds.spec.ts`): publish round 1 and a reviewer comment, change a caption and add and remove figures, publish round 2, the reviewer sees the list, markers and the carried comment, axe in both themes, state cleaned through the API.
- Not covered: the group server through a browser (the routes are the same and the scope guard test covers them); two simultaneous publishes.

## Rollback/Migration
No migration. Remove `Round`, the comparison and its two read paths; snapshots made with rounds keep an extra `round` key in their JSON that older code ignores.
