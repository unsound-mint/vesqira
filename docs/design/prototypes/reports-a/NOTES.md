# Reports, direction A: document in the workbench

Disposable prototype. All numbers, names and runs are fictional; every page carries "ILLUSTRATIVE MOCK DATA".

## Concept
- Owner side is the approved workbench: icon rail, Reports/Runs/Figures explorer, split Markdown source | preview, stacked inspector (Publish, Backlinks, Comments, History).
- The report is one document; the agent's edits are visible as marked changes (gutter bar in source, tinted insert in preview) that Denis accepts or reverts before publishing.
- Publishing is an explicit preflight dialog listing what goes out (text, figures as pictures, run lines, link status) and what does not (files, paths, hosts, agent prompts, unsent drafts), plus the diff from rev 4.
- Reader side is a calm column with comments in a right margin aligned to their paragraphs; space index on the left. Honesty lines: per-comment "seen by the author / not yet seen", workspace sync age, "not shared with you" plain text, "on revision 3 · this paragraph changed since".

## Layout per breakpoint
- 1440: owner rail 48 / explorer 280 / editor / inspector 360; editor splits source and preview. 1280: explorer 232, inspector 340, view defaults to Preview (split is too narrow, still selectable). 390: owner becomes Report / Publish-and-comments tabs, source hidden (read-only mock anyway), explorer hidden.
- Reader 1440: index 240, article 640, margin 320. 1180 and below: article flexible, margin 280. 860 and below: index collapses behind "Reports", margin hidden, each paragraph gets a Comment/threads button that opens a bottom sheet with that paragraph's threads and composer.

## Interactions
Source/Split/Preview toggle; collapsible inspector sections; accept/revert agent changes and Accept all; insert buttons (toast only); publish dialog with required acknowledgement checkbox, then a mock "pushed" toast; owner replies, Resolve/Reopen, agent draft reply with Send as Denis / Edit / Discard; reader: add comment per paragraph, reply, resolved threads collapsed, figure pin focuses its thread, revision history popover, theme toggle. Keyboard: composers use Ctrl+Enter / Esc; all controls have a 2px focus ring (checked by capture.py).
Variants: `02-reader.html?state=empty|revoked|offline`; `01-owner-editor.html?state=preflight` (`03-publish-preflight.html` just redirects there); `?theme=dark|light`.

## Deviations from the design system
Long-form reading text only: body 15.5/26, h2 18, h1 24, 68ch measure. Everything else uses workbench tokens unchanged. Pills `ok`/`acc` use `--text` with a coloured dot because the token tint failed 4.5:1 on its own background (token issue worth a design-system fix).

## Strengths
Fits the existing mental model (one workbench, report is another document). Agent changes and publish exposure are reviewed in place. Margin comments keep the reader's attention on the text and make anchoring obvious.

## Weaknesses and risks
- At 1280 the editor is cramped; split view is effectively a 1440+ feature. Source pane is a read-only mock (a real editor such as CodeMirror is needed; caret/selection to comment anchors untested).
- Margin layout is JS-measured; long comment stacks push cards far below their paragraph (alignment degrades, no connector lines).
- A comment button under every paragraph on mobile is noisy.
- Three places show threads (margin, sheet, owner inspector); owner cannot comment in context in the preview, only jump to the inspector.
- Fonts were not loaded in the capture sandbox (offline); screenshots use the fallback sans, widths differ slightly from Geist.
- MathML rendering depends on the browser math font; real build should use KaTeX or similar.

## Open decisions
Can the professor resolve threads, or only the owner? Is "seen by the author" shown per comment or only the sync age? Should an unshared link show the title or only "not shared"? Do figure pins survive a figure being replaced in a later revision? Publish gating: is the acknowledgement checkbox too heavy for routine publishes?
