# Vesqira

**An open-source, extensible workspace for scientific research.** *Your research, connected.* (tagline provisional)

**Status: pre-release.** The v0.1 workflow is implemented end to end and covered by an automated browser walk-through on synthetic fixtures (`docs/demo.md`, `make e2e`): register cubes, browse planes with astropy-computed coordinates, regions and spectra, axial orientation maps, side-by-side comparison, figures, boards, and a scoped review link. Nothing has been validated against a published scientific benchmark and nothing is released. The name is not yet cleared (`docs/product/brand.md`). Architecture decisions are in `docs/adr/`.

## v0.1: astronomy

A researcher registers H I FITS spectral cubes and existing VGT outputs, browses spectral planes and WCS coordinates, compares scalar and axial-orientation maps with reliability information, builds source-linked figures and a research board, and shares a scoped review with a supervisor who needs no SSH/HPC credentials.

Try it on generated data: `tools/demo.sh` and `docs/demo.md`.

## Principles

Scientific honesty is a product feature: no invented calibration, units or coordinate frames; axial orientations (0° ≡ 180°) get axial statistics; missing is never zero; provenance is labelled verified, declared, inferred or unresolved. See `AGENTS.md`.

## Design

The approved workbench layout and design tokens are described in `docs/design/design-directions.md` and `docs/adr/0005-workbench-layout-and-tokens.md`. All prototypes use synthetic data.

## Contributing

See `CONTRIBUTING.md`. Run `make check` before pushing.

## License

GNU Affero General Public License v3 only (`AGPL-3.0-only`), see `LICENSE`.

Copyright (C) 2026 Denis Andreyev
