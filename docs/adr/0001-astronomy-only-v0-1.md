# ADR 0001: v0.1 is astronomy only

- Status: accepted
- Date: 2026-11-03

## Context
Proving one complete workflow matters more than breadth. Supporting several disciplines at once multiplies design, fixture and viewer-generality work before any workflow is trusted.

## Decision
v0.1 covers astronomy only: H I FITS cubes and VGT outputs.

## Alternatives
Support several disciplines simultaneously (rejected: scope and risk).

## Consequences
No generic N-D viewer or other format decoders in v0.1. Adapter boundaries stay honest about axes, units and field semantics so other disciplines can be added later without rewriting core services.

## Validation
`tools/check_docs.py`; review of `AGENTS.md` and design docs for scope creep.

## Rollback / Migration
Widening scope requires a new ADR.
