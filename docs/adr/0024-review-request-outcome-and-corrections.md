# ADR 0024: Review request, reviewer outcome, and corrections

Status: accepted under delegated merge; owner review pending.

## Context
Publishing a board only froze it with a label and an expiry. The reviewer was not told what to look at, the author could not see what exactly was about to be exposed, there was no way to say a review had been looked at, and a corrected version could not announce that it replaced an earlier one.

## Decision
- **Question.** Publishing takes an optional question (up to 1000 characters), frozen into the snapshot and shown to the reviewer first.
- **Preflight.** `GET /api/boards/{id}/preflight` returns exactly the frozen content a publish would produce (titles, captions, notes, provenance) and warnings: no question, no caption, a figure from a run made from uncommitted code, a run that is not assessed. It creates nothing. The board screen shows it before the link is created.
- **Outcome.** A reviewer sends one of `commented`, `changes_requested`, `reviewed` with an optional note (`POST /review-api/{token}/outcome`). Every call is kept; the latest counts and is shown to the author per snapshot ("Not yet reviewed" until then). It is a statement about the review of this version, never about the science of a run: run QC and interpretation stay the author's, kept apart.
- **Correction.** Publishing may name the earlier snapshot of the same board it replaces, with a reason. The old link keeps working and shows the reason and that a newer version exists; it does not point to the new link, which the author sends.

## Alternatives
A reviewer-controlled "approved" state (reads as a scientific verdict); deleting or redirecting the old link on replacement (loses what was reviewed).

## Consequences
Implemented. The `snapshots` table gains `superseded_by`, `supersede_reason`, `supersede_at`; outcomes are an append-only `snapshot_outcomes` table. Suggested related runs, a review queue and an activity feed are not built (roadmap stage 2). Outcomes are self-declared by name until accounts exist.

## Validation
Store tests for preflight (no snapshot created, warnings), question frozen, outcome validation and history, supersede visible on the old link only and only within one board; component tests for the question and replaced banners and for sending an outcome.

## Rollback/Migration
Additive columns and table.
