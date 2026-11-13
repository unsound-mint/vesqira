# ADR 0071: Share this view, a figure shared on its own

- Status: accepted, implemented (no schema change); the owner reviews it afterwards
- Date: 2026-11-13
- Related: ADR 0011 (review surface), 0069 (access labels), GitHub issue 129. There is no ADR 0070 on main at the time of writing; 0071 is used as the issue asked.

## Context
In a video call, screen sharing often fails or the right figure cannot be found quickly, and people ask for a snapshot afterwards. A saved figure can go on a board and a board can be published as a scoped review link (ADR 0011), but there was no link from the viewer or from one figure without making a board first.

## Decision
**A figure share is a review snapshot with no board.** `snapshots.board_id` is 0 (no board has id 0), and the frozen document is the same `Frozen` a board makes: the figure's title as the heading, one `FrozenItem` (title, caption, picture hash, size, reviewer-safe provenance, the access label) with no note and no layout. The one code path that freezes, stamps access labels, creates the token, stores only its hash, and enforces the 90-day expiry is `publishItems`, which `PublishWith` (a board) and the new `PublishFigure` both call. Revocation, expiry, comments, replies and outcomes are the board snapshot's own, so the reviewer listener cannot tell the two apart and the reviewer-visible field set did not change (the frozen-type reflection test is untouched and still passes).

**The access gate is the board's gate.** `POST /api/figures/{id}/share` passes `publishGateFor(ctx, confirm, "figure")` as the `Access` hook. `publishGate` for a board is now `publishGateFor(..., "board")`: one function, the noun only changes the sentence and the audit target. A labelled figure answers 409 `access_confirmation_required` naming the datasets, `confirmAccess: true` publishes, a label the workspace blocks answers 403 `publishing_blocked` whatever the request says, and the label stated on the review page is frozen at that moment.

**Who may share.** The route needs `review.publish`, the permission of a board link: a Viewer cannot, a Researcher shares and revokes only what is theirs, a Maintainer and above any. With no board to name the owner, the owner of a snapshot for the scope check is the snapshot's own `created_by` (`COALESCE(board.created_by, snapshot.created_by)` in the `snapshots/id` and `comments/id` lookups); for board snapshots nothing changes.

**Where the owner finds it.** `GET /api/figures/{id}/shares` lists the links of one figure (matched by the figure's revision, so editing its title keeps them attached) and `GET /api/figure-shares` lists all of the workspace. Both return the same `Snapshot` shape as a board's list, with `revision` and `title` set. A new "Shared figures" section on the Boards screen lists them with status, comment count and a Revoke button, beside the board's own "Shared snapshots"; revoking uses the existing `POST /api/snapshots/{id}/revoke`, and comments and replies the existing snapshot routes. The link is shown once, as for a board.

**UI.** "Share this view" in the viewer's Figure section saves what is on screen (identical evidence is one figure, so saving again changes nothing) and then publishes; "Share this figure" does the same for a saved figure in the Figure section and on each board card. One dialog (lazy-loaded with the modal primitives): label and expiry with defaults (7 days), the review address under a disclosure, Enter confirms, the link is copied as it is made and shown with Copy and Revoke. Labelled data adds the confirmation sentence and button in the same dialog; a blocked label shows the 403 text and no way on.

## Alternatives
- **An implicit one-figure board kept out of the boards list.** Reuses `Publish` unchanged, and the snapshot would have an owner and a board to hang the existing lists on. Rejected: it needs a hidden flag on `boards` (a schema change) and every place that lists, archives, deletes, searches, pushes or counts boards would have to remember to skip it. A forgotten place shows the author a board they never made.
- **A new table for figure shares.** Rejected: it would duplicate tokens, expiry, comments and outcomes, and the reviewer listener would need a second lookup.
- **A "kind" field on `Frozen`.** Would let the review page say "figure" instead of "board" in its label note. Not added: it widens the reviewer-visible whitelist for wording only. The access note says "the data shown here" instead, which is true for both.

## Consequences
- No migration. Existing snapshots are untouched, and `board_id` 0 was never used.
- A figure share holds the figure's picture hash like a board snapshot does, so deleting the figure is subject to the same lifecycle rule (the share pins it; the impact preview lists it).
- Reviewers can comment on a shared figure, exactly as on a board; the link is read-only for data, not for comments (ADR 0011).
- The group server serves the same routes under the same workspace scoping; the cross-workspace route test requests the three new routes.

## Validation
- Go: a figure shared on its own gives the reviewer that figure with provenance and no path, dataset id or other figure; the token opens its own picture and no other, and a comment on another figure's revision is refused; no owner route is reachable on the reviewer listener; the share is listed under its figure and in the workspace list and no board lists it; expiry (past `expires_at`) and revocation give 404 on the document and the picture and the figure stays; an expiry beyond 90 days and an unknown field are refused. Through the group server: confirmation names the data, the label is frozen and the reviewer's copy has no `via`, an admin block refuses with and without confirmation and stores nothing, a Viewer and a Researcher on someone else's figure are refused, a Researcher shares and revokes their own, a Maintainer revokes any. The cross-workspace guard test (`TestNoRouteLeaksAcrossWorkspaces`) covers the new routes.
- Web: the dialog makes the link with defaults and copies it, revokes it, asks once more for labelled data without saving again, offers nothing when blocked, and reports a save that failed without making a link.
- One Playwright spec: viewer, share, a fresh browser context opens the link and sees the picture and provenance, `/api/*` data routes answer 404 on the review address, revoke from "Shared figures", the link is dead; axe in light and dark on the dialog, the review page and the list, and 390 px screenshots.
- Not covered: sharing from the channel-grid and region-spectrum figures through the browser (they use the same save path but are not exercised), and the group server through a browser.

## Rollback/Migration
Remove the three routes and `PublishFigure`. Snapshots with `board_id` 0 stay readable by their token until they expire; nothing else refers to them.
