# ADR 0003: One primary design rulebook

- Status: accepted
- Date: 2026-11-03

## Context
Several overlapping design skills exist. Mixing them yields contradictory guidance.

## Decision
Impeccable is the primary rulebook (`PRODUCT.md`, later `DESIGN.md`, critique, audit, polish). Scientific correctness and permissions outrank aesthetic rules. Playwright is used for rendered verification. `PRODUCT.md` and `DESIGN.md` summarize and link to `docs/product/` and `docs/design/`, never contradict them.

## Alternatives
Using several rulebooks at once (rejected).

## Consequences
Keep the active tool set small.

## Validation
Design reviews cite the rendered screenshots they relied on.

## Rollback / Migration
Swap the rulebook by superseding this ADR.
