# ADR 0019: Audit log and assessment history

Status: accepted under delegated merge; owner review pending.

## Context
Run assessments were overwritten in place, and nothing recorded who did what: hosts added, jobs submitted, boards published. A supervisor-facing workspace needs both to be answerable ("what did you think of this run last week", "when was this link created").

## Decision
- `run_review_history` gets a row on every change to a run's assessment, written in the same transaction as the current row. `GET /api/runs/{id}/history` lists them newest first. The run panel shows earlier ones; reviewers see only the current assessment.
- An append-only `audit` table records state-changing and data-exposing actions: host added/removed, job submitted/found/reopened/cancel requested, assessment recorded, review published/revoked, managed copy made. Each entry has a time, action, object kind and id, and one short line (names, hashes and Slurm ids; never paths, hosts' addresses, tokens or file contents). `GET /api/audit?kind=&target=&limit=` serves it to the owner; the review listener does not mount it.
- There is one principal today, so entries carry no actor. When accounts exist (roadmap stage 7) an actor column is added.

## Alternatives
Event sourcing for every entity (heavy, not asked for); logging to a file (not queryable from the UI, lost with the log rotation).

## Consequences
Implemented. No screen yet: the audit log is served by the API; an activity view is part of the review-workflow stage and needs a design. Entries are never edited or deleted by the application.

## Validation
Server tests: two assessments leave two history rows in order; the reviewer response shows only the current one; the audit log lists the assessment; the review listener answers 404 for `/api/audit`.

## Rollback/Migration
Both tables are created if missing; removing the feature leaves unused tables.
