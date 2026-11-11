# ADR 0060: Board layout grid: places, snapping, compaction, keyboard

- Status: proposed; implemented, awaiting owner review. No design review and no owner approval of the new interaction.
- Date: 2026-11-12
- Builds the Stage 6 item "board layout grid with snap and resize" (Q10, Q11, Q12, Q13, Q186) on ADR 0057 (undo), 0041 (archive) and the board, figure and snapshot model of ADR 0011 and 0025.

## Context

A board was an ordered list of panels (`board_items.position`), shown one per row. Q10 to Q13 ask for a flexible grid with resize, bulk-added results laid out by channel and method, and the same board in presentation and analysis modes without rebuilding the layout. A board is mutable working state (AGENTS.md lifecycle): its layout is not evidence until the board is published, and a published snapshot is frozen.

## Decision

**Model.** A 12-column grid with rows of one fixed height (48 px, 16 px gap). A panel's place is four integers: column `x`, row `y`, column span `w`, row span `h`. Layouts are vertically compact and never overlap. Minimum 2 columns by 2 rows, at most 40 rows.

**Storage (schema 21).** Four nullable columns on `board_items` (`layout_x`, `layout_y`, `layout_w`, `layout_h`). They live on the panel's row, so reordering, pinning a run's result over its panel and removing another panel keep the place with the panel. NULL means "never placed". A board saved before this has no places: the client derives them from the stored order (every panel full width, a run panel shorter), a pure function of stored data (order and kind), never of the screen or the pictures, so the owner, the printed page and the reviewer agree. Nothing is written until the researcher changes something; the first change writes every panel's place. A panel added to an arranged board has no place and is shown below the others until the next change.

**API.** `POST /api/boards/{id}/layout` replaces the places of every panel in one transaction: `layout` (a place or null per panel, in position order), `expect` (the panel at each position, covering the whole board) and optionally `from` (the places being replaced). The server checks bounds and overlap (422) and the guards (409 with the current board, nothing changed), as for the item edits of ADR 0057. It does not compact: compaction is the client's, so the stored layout is whatever the researcher saw. There is no such route on the review listener. A snapshot freezes each panel's place (`layout` on a frozen figure and on a frozen run); snapshots made before this have none and are read like a never-arranged board. The place is five small integers on a fixed grid, added deliberately to the reviewer whitelist test.

**Engine.** `apps/web/src/boards/layout.ts`: pure functions with no imports (`settle`, `resolve`, `moveStep`, `resizeStep`, `dragTarget`, `arrange`, `placed`). `settle` puts the moved panel at its wanted place, pushes the panels it overlaps down (or lets one that was passed take the vacated top), then lets everything fall upward until nothing floats and nothing overlaps; it is idempotent and tested as such.

**Interaction.** A slim handle above each panel is the drag handle and the keyboard focus stop; a corner mark resizes. Both snap to whole cells while the pointer moves, the other panels settle live, and one save happens when the pointer is released (Escape cancels). The 12 column guides and row lines appear only while dragging or resizing. On the focused handle, the arrow keys move the panel by one cell and Shift with the arrow keys resize it by one cell; a polite live region says the new place and size ("“Figure 2” moved to column 3, row 1; 6 columns wide, 8 rows tall."), or that the panel cannot go further. In a compact layout an empty row above a panel is closed at once, so an Up or Down press that would change nothing continues until the panel has passed the neighbour in the way; the sentence says where it landed. Handle and corner are shown on hover and focus, and always on devices without hover. A board narrower than 560 px drops the placement and stacks its panels in reading order, with no handles. A panel's content scrolls inside its cell rather than overlapping the next.

**Order.** Once any place is stored, the order of the panels everywhere (presentation, printed page, reviewer, narrow stack, reading by assistive technology) is reading order: top to bottom, then left to right. The older Move up and Move down buttons change `position`, which no longer says anything on an arranged board, so they are hidden there; "Back to reading order" stores no places again (one undoable step) and brings them back. The reviewer's page keeps the columns and the reading order of the saved layout, read-only; its rows size to their content because each panel carries the comment thread.

**Undo.** Every drag, key press, automatic arrangement and reset is one command on the board's own stack (ADR 0057): `setLayout(before, after)` with the same two guards. Undoing the first change of a never-arranged board stores no places again. Undoing the removal of a panel restores every panel's place, not only the removed one's. Saves are queued, so two quick key presses never act on a stale layout.

**Arrange automatically (Q12).** Channels as columns, methods as rows, only from what each figure records: its view's channel and the kind of run it was drawn from. Anything else falls back to reading order with a sentence that says what was missing, and nothing is guessed: a run that has no picture yet, a figure without a channel (channel 0 is a channel), a figure without a recorded run, two figures for one channel and method, a grid with a hole (a compact layout would let the next row rise into it), more than six channels. The action applies to the whole board; there is no bulk-add flow to attach it to yet. The titles and labels are never read.

**Library.** Not used. react-grid-layout (2.3.0, published 2026-10) brings react-draggable, react-resizable and a ResizeObserver polyfill, positions panels with CSS transforms, has no keyboard path and owns its own compaction; we need the engine regardless for keyboard moves, deriving, arrangement, the frozen copy and the reviewer's page, and the pointer code on top of it is about eighty lines. dnd-kit has no grid, snapping to cells or resizing and its last release is December 2024. The engine is pure and covered by unit tests; the pointer code is exercised in the end-to-end test.

## Alternatives

- A JSON layout blob on the board: needs its own identity for panels (positions change) and its own guard; per-row columns reuse the item identity and the ADR 0057 guard.
- Free placement with gaps (Figma-like): no compaction rules, but the reading order is then not derivable and a keyboard step is easy but an empty board region is meaningless on the reviewer's narrow stack. The brief asks for a compact layout.
- Hiding Move up and Move down never, and mapping them to a swap in layout space: panels differ in size, so a swap overlaps or leaves holes.
- Storing the compacted result server-side: would hide what the researcher placed; the server validates only.

## Consequences

- One new route, one schema version, one frozen field. The reviewer's surface gains read-only data and no write path.
- A board's `position` is no longer authoritative for display once it is arranged; it remains the identity the item edits address.
- Keyboard Up and Down can move a panel by more than one row in a compact layout; this is stated in the live region.
- A fixed row height means a panel's content may need scrolling inside a small cell; the researcher resizes to taste.
- Existing e2e and tests that use Move up and Move down are unaffected for boards that were never arranged.

## Validation

- Unit tests for the engine (`layout.test.ts`): compaction, overlap, bounds, idempotence, keyboard moves and resizes, deriving for an old board, pointer snapping, automatic arrangement and each fallback.
- Web tests (`boardGrid.test.tsx`, `review.test.tsx`): keyboard move and resize with the live sentence and the saved body, one undoable step per change, undo of the first change, a refused undo, undo of a removal, arrange and its fallback, the reviewer's layout.
- Go tests: round trip and layout travelling with the panel, refusals changing nothing, the guards, the frozen copy (including the run whitelist), the route and its 409, the reviewer listener having no route, the migration, the schema snapshot.
- End to end (`zzzzzz-board-grid.spec.ts`): drag and resize with the mouse, keyboard, undo, reload, presentation, print, the reviewer's page, 390 px stacking, axe.

## Rollback / Migration

Schema 21 adds four nullable columns; nothing is rewritten. A program that ignores them shows the board by `position`, which was never changed by layout edits. Dropping the columns is a table rebuild and loses only the layouts. A catalog written by this version is refused by an older program under the schema guard, as for every version.
