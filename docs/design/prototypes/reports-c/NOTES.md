# Reports, direction C: review inbox and changes first

## Concept
The professor has little time and the VGT agent produces a steady stream of drafts, so both sides are organised around what changed, not around browsing documents. The reader lands on an activity inbox ("Since your last visit") whose items open the report scrolled to the changed block. Changed blocks carry a change bar and a "changed in rev 5" label, with a toggle for the previous text. The report is figure-forward: wide figure plates with a provenance column, 68ch text, one right-hand Discussion column in document order. The owner works from a "desk": a queue of things needing Denis, the report with tracked agent suggestions (accept or reject per block), and a publish panel with preflight and readers. Same workbench tokens; structure and workflow differ, not the brand.

Deviation from the token scale: reading text is 16/28 with a 68ch measure (document body only), as allowed.

## Layout per breakpoint
- 1440 and 1280: reader report is text + figures (up to 860px) and a 380px Discussion column that stays in view; inbox is a feed plus a 340px "reports in this space" aside; owner desk is three columns (260-300 queue, centre report, 280-320 publish), each scrolling independently.
- Below 1100 (incl. 390): Discussion becomes a bottom sheet opened by a floating button or by a comment marker; owner desk becomes three panes switched by a bottom tab bar (Queue, Report, Publish); the preflight dialog is full screen. Inbox is a single column, reports list below.

## Interactions
- Reader: next/previous change stepper (1 of 4), "Changes since rev 4" and "Previous text" toggles, revision history popover, comment marker per block (jumps to thread), "+" per block (opens composer targeted at that block), pin on Figure 2 opens its thread, thread title jumps to the block, resolved thread collapsed, inline reply, Ctrl+Enter sends, honest sync strip and "not yet seen by the author" status. Deep links: `?focus=b2|t2`, `?sheet=1`.
- Inbox: filter chips, mark all as seen, states `empty`, `revoked`, `offline`.
- Owner: accept/reject per block (from queue or in the report), accept all, view switch (tracked, all accepted, rev 4), reply to professor, send/edit/discard agent-drafted reply, revoke reader (toast), preflight dialog computed from the current accept state (`?state=preflight` pre-accepts three of four), publish bumps to revision 5.

## Strengths
- Directly answers "what do I need to look at now" for both people; no hunting for what the agent changed.
- Attribution, unshared links, stale comments and unsynced comments are all visible where they matter.
- Suggestion model gives the owner a clear review gate before anything is published.

## Weaknesses and risks
- Change tracking depends on block-level diffs; reflowed or moved paragraphs will look like deletion plus insertion.
- The inbox assumes one dominant report; with many reports it needs grouping.
- Three-column desk is dense at 1280; the report column is only about 700px.
- The inline accept/reject UI doubles as a queue item and as a block control (two places for one action).
- Mock timeline simplification: the owner desk is pre-publish (rev 4 live), the reader screens show the world after rev 5 was published; "other" reports (Weekly notes, Spectral smoothing) are links to nothing. Fonts fall back to system faces when Google Fonts is unreachable.

## Open decisions
- Is a reply by the owner visible to the reader before publishing a new revision (assumed: yes, replies push independently)?
- Should resolved threads on changed blocks be re-opened automatically?
- Unit of change: block (assumed) versus sentence.
- Does "seen by the author" need to be per message or per thread?
- Should the reader be able to mark inbox items seen across devices (needs state on the publish server)?
