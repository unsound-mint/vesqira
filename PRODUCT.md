# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

- **Astrophysics researcher (primary).** Runs the Julia-based VGT workflow on large FITS spectral cubes, often with data on remote machines or HPC. Job: find the corresponding spectral region, compare two runs, annotate a difference, and share a reproducible board.
- **Research-group collaborators (confirmed for v0.1).** Other researchers in the same group who edit boards alongside the primary researcher, not only review them.
- **Scientific supervisor / reviewer.** Opens a shared board or figure in a browser without HPC or SSH access, understands context and limitations, leaves annotations and change requests.

## Product Purpose

Vesqira is an open-source, discipline-extensible research workspace that combines scientific datasets, visualization, experiments, collaboration and research evidence. Immediate goal: a researcher registers existing VGT/FITS outputs, browses and compares them, builds a research board, and shares a safe, stable review with a supervisor. Success is one complete workflow a researcher can use and trust, not the number of architectural concepts implemented.

## Positioning

A scientific instrument plus thoughtful workspace, not a SaaS KPI dashboard. Its differentiator is scientific honesty: it never invents calibration, units or coordinate frames, keeps axial orientation, masks and missing values semantically distinct, and labels provenance as verified, declared, inferred or unresolved. Reviewers see only explicitly published, version-pinned artifacts and never receive infrastructure credentials.

## Operating Context

- Local/self-hosted project that registers or references existing local files; optional managed copy.
- Large FITS cubes and VGT outputs; selective reads, never a whole multi-GB cube sent to the browser.
- Remote data and HPC: v0.1 reads and registers outputs; it does not execute jobs.
- Long working sessions: keyboard-friendly, comfortable, dense where the work needs it.
- Baseline stack per AGENTS.md: Go control plane, React/TypeScript web app, isolated Python/Julia scientific workers, SQLite locally (PostgreSQL for multi-user).

## Capabilities and Constraints

- v0.1: FITS cube and VGT output import/reference; truthful FITS/WCS/spectral metadata; spectral/region viewer with ROIs, layers and masks; VGT run comparison with axial-angle semantics and reliability/validity masks; figures and version-pinned research boards; permission-scoped supervisor review with comments.
- Astronomy-only MVP.
- Non-negotiable invariants live in `AGENTS.md` (no invented calibration, axial statistics for orientations, zero is not missing, QC is not file validity, originals never silently altered).
- Production UI requires an approved visual direction first (`docs/design/design-directions.md`).
- Licence: AGPL-3.0-only (see `LICENSE`, `docs/adr/0002-license-agpl-3-0-only.md`). Copyright holder and name/trademark clearance are unresolved (`docs/product/brand.md`).

## Brand Commitments

Canonical docs: `docs/product/vision.md`, `brand.md`; this file only summarizes them.


Name: Vesqira. Design intent: "instrument-grade editorial" (see `docs/design/design-directions.md`; no direction approved yet). No visual direction is binding yet; avoid copying any commercial product's identity.

## Evidence on Hand

- Product and design docs under `docs/product/` and `docs/design/`.
- Reference scientific source: the VGT Julia pipeline (authoritative when accessible).
- No real user testimonials, benchmarks or production data are in the repo; do not fabricate any.

## Product Principles

1. Scientific correctness and source-data integrity outrank every other consideration, including polish.
2. The dataset viewer is the primary work surface; context is secondary and never blocks work.
3. Warnings about metadata, compatibility and provenance are visible and honest, never color-only.
4. A reviewer gets exactly what was published and nothing more.
5. Complete one thin end-to-end workflow before generalizing.

## Accessibility & Inclusion

WCAG 2.2 AA. Keyboard-complete viewer and board, axe scan plus manual keyboard review, light and dark themes, and no scientific meaning conveyed by color alone.
