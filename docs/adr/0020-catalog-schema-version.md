# ADR 0020: Catalog schema version

Status: accepted under delegated merge; owner review pending.

## Context
The catalog grew by `CREATE TABLE IF NOT EXISTS` plus `ALTER TABLE` calls whose errors were ignored. A failed or half-applied change was invisible, an older program could open a newer catalog and misread it, and there was no copy to return to.

## Decision
- The version is SQLite's `user_version`. This release is version 1: the catalog as shipped through v0.2, still created idempotently by the packages that own the tables.
- On start, before anything else touches the file: a catalog with a higher version is refused with a message; an existing catalog with a lower version is copied first (`VACUUM INTO <db>.before-schema-<n>-<time>`, mode 0600) and only then migrated, one transaction per step, stopping at the first failure with the earlier steps kept; a new catalog is stamped without a copy. If the copy cannot be made, nothing is changed.
- Adding a column goes through `schema.AddColumn`, which checks the table and returns real errors instead of discarding them.
- Backups are never deleted by the program.

## Alternatives
A migration library (more machinery than the single SQLite file needs now); keeping ignored ALTERs (hides failures).

## Consequences
Implemented. A user who downgrades gets a refusal, not silent damage. Backups accumulate beside the database until the owner removes them.

## Validation
Unit tests: a new catalog is stamped without a backup; a newer one is refused and left alone; an upgrade writes a backup that holds the old data, keeps completed steps, rolls back the failed one; `AddColumn` is idempotent and reports a real failure.

## Rollback/Migration
Restore the `.before-schema-*` file over the database.
