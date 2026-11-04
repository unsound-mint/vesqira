# ADR 0025: Presenting, printing and downloading a board

Status: accepted under delegated merge; owner review pending.

## Context
A board could be reviewed on a link but not shown in a meeting, printed, or taken out as a file.

## Decision
- **Present.** A full-screen view steps through the board's figures in board order (arrows, Page keys, Home, End; Escape leaves). The author's notes and the provenance are hidden until asked (N, P) because the room may be watching the screen.
- **Download.** `GET /api/figures/{id}/download` serves the published picture as a PNG with its title, caption and provenance JSON stored inside the file (iTXt chunks), so it stays attributed away from the board. Pixels are unchanged. Owner API only.
- **Print.** `#/print/<board id>` is a print layout (light theme forced) of the figures with captions, notes and provenance; the browser's Print or Save as PDF produces the PDF. No PDF library is added.

## Alternatives
A server-side PDF writer (a new dependency, and two renderings of one board to keep consistent); presenting live viewers (needs the data on screen and the HPC connection during a talk, a larger design).

## Consequences
Implemented. Pictures are the browser-drawn previews, not re-renderings of the data, and every export says so. A reviewer can print their own review page the same way; a reviewer has no download route.

## Validation
Go tests: text chunks keep the picture's pixels and decode; the download carries provenance and is absent on the review listener. Component tests for presenter navigation and hidden notes. A browser walkthrough presents, leaves, downloads (checks the embedded text) and opens the print layout; the presenter was inspected in a screenshot.

## Rollback/Migration
None.
