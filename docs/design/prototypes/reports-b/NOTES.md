# Reports, direction B: Linked pages

Disposable prototype. All figures, numbers, names and dates are fictional (flag "ILLUSTRATIVE MOCK DATA" on every page).
Open `01-owner-editor.html`, `02-reader.html`, `03-graph.html` directly in a browser (no build). Query: `?theme=dark|light`; `02-reader.html?state=default|empty|revoked|offline`; `01-owner-editor.html?state=preflight`. Extras: `?open=comments`, `?open=pop`, `?open=sheet`, `?drawer=1`.

## Concept
A report is one live-rendered page in a space of linked pages (Obsidian/Notion feel). Blocks render as you type; only the focused block shows its Markdown. The space is a page tree with search plus a "Linked" section (links to / linked from); the Graph toggle swaps the page for a link graph of reports, runs and figures, with a keyboard list beside it. Comments are highlighted ranges with a gutter count; the same page, read-only, is what the supervisor sees. Honesty is carried by labels in the page itself: frozen previews, provenance lines, "not shared with you", agent attribution, "on revision 3 . this paragraph changed since", and the sync chip.

## Layout per breakpoint
- 1440 / 1280: sidebar 280 + page (max 800, text 68ch, 44px comment gutter). Comments: popover from mark/gutter; "All comments" drawer 380 slides over the right edge.
- 390: sidebar becomes a drawer (menu button), header strip wraps to 2-3 rows, comment gutter 36px, popover replaced by a full-height sheet (with "All comments" link), preflight dialog full screen, graph/list switch via a Graph | List toggle with shortened node labels.

## Interactions
Click or Enter on a block to edit its source (Esc or blur renders; editing a commented block flags its threads "paragraph changed since"). Agent proposals: per-block diff with Accept / Keep rev 4, banner Review / Accept all. Gutter "+" / count, mark and figure pin open a thread (Esc closes, focus returns). Ctrl+Enter sends. Owner: Resolve/Reopen, Send/Edit/Discard agent draft reply. Publish... opens the preflight dialog. Reader: history menu, Page | Graph, All comments with Open/Resolved/All. Graph: type filters, click/Enter select node, arrow keys move between nodes, list mirrors it. Theme button. Prototype-only: links to unbuilt pages show a toast.

## Strengths
Reads like a document first; linking and backlinks are native, so the "mind map" is a view of what the writing already declares. Review state sits in the text (highlights, diff blocks), so the owner never switches modes. Same page for owner and reader keeps trust simple: the preview is what they get.

## Weaknesses and risks
- Block-level Markdown/WYSIWYG editing is the costliest part to build well; the prototype fakes it with textarea swap.
- Whole-block anchors are robust; text-range anchors will drift after edits (here: marked "changed since", never silently re-attached).
- The graph is manually laid out for 12 nodes; a real space needs layout and clutter control.
- Gutter counts need hover/focus affordance; on touch the "+" is always faintly visible.
- Popover on desktop covers the text it discusses.
- Two comment surfaces (popover, drawer) must stay consistent.

## Open decisions
- Is the graph owner-only value, or shown to readers at all (reader shows shared reports only)?
- Should the reader see which linked pages exist but are not shared (currently: only the plain "not shared with you" text, and unshared pages are omitted from tree, backlinks and graph)?
- Per-comment delivery state wording; offline posting limits.
- Block-level vs range anchors for v1; whether an owner can re-attach a stale comment.
- Whether unaccepted agent proposals can ever be visible to the supervisor (currently never).
- Deviation from tokens: reading text is 16px/26px with 68ch measure (`--text-read`, `--measure`); everything else uses the workbench token set verbatim. Headings stay within the token scale (20px / 16px).
