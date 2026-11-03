# ADR 0002: License is AGPL-3.0-only

- Status: accepted
- Date: 2026-11-03

## Context
Vesqira is a network-served collaborative application. Modifications served over a network should stay open.

## Decision
License under the GNU Affero General Public License v3 only (SPDX `AGPL-3.0-only`). `LICENSE` is the unmodified official text from gnu.org. Third-party notices and the licenses of any external reference projects stay separate and intact. Copyright (C) 2026 Denis Andreyev.

## Alternatives
Apache-2.0 or MIT (no network copyleft); AGPL-3.0-or-later (rejected: "only" was chosen).

## Consequences
Dependencies must be AGPL-compatible.

## Validation
`tools/check_docs.py` checks the LICENSE title and version line.

## Rollback / Migration
Relicensing needs agreement of all copyright holders; keep contributor records.
