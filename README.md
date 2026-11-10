# Vesqira

**An open-source, extensible workspace for scientific research.** *Your research, connected.* (tagline provisional)

**Status: pre-release.** The v0.1 workflow is implemented end to end and covered by an automated browser walk-through on synthetic fixtures (`docs/demo.md`, `make e2e`): register cubes, browse planes with astropy-computed coordinates, regions and spectra, axial orientation maps, side-by-side comparison, figures, boards, and a scoped review link. Nothing has been validated against a published scientific benchmark and nothing is released. The name is not yet cleared (`docs/product/brand.md`). Architecture decisions are in `docs/adr/`.

## v0.1: astronomy

A researcher registers H I FITS spectral cubes and existing VGT outputs, browses spectral planes and WCS coordinates, compares scalar and axial-orientation maps with reliability information, builds source-linked figures and a research board, and shares a scoped review with a supervisor who needs no SSH/HPC credentials.

## Install (Fedora)

```
sudo dnf install ./vesqira-<version>.fc44.x86_64.rpm     # from the release page, or: tools/build-rpm.sh
```

Then start **Vesqira** from the applications menu (or run `vesqira`): it starts a local server and opens it in your browser. Choose which folders it may read with `vesqira add-root /path/to/fits` (the first run creates `~/Vesqira`); `vesqira stop`, `vesqira status` and `vesqira logs` do what they say. Only files inside those folders are ever read, and the server listens on 127.0.0.1 only.

## Start the app from the source tree

One-time setup (Linux/macOS): Python 3 with astropy (`pip install -r services/scientific-worker/requirements.txt`), Go, Node, then `cd apps/web && npm ci`.

- **On your own data:** `tools/start.sh --root /path/to/your/fits/folder` (repeat `--root` for more folders), then open <http://127.0.0.1:5173>. Only files inside the given folders are ever read. Your catalog and derived files live in `~/.local/share/vesqira` (`VESQIRA_DATA` changes it).
- **Try it on generated data first:** `tools/demo.sh`, then follow `docs/demo.md`.
- Other machines (cluster, another laptop): bring your VPN up, make sure `ssh <alias>` works without a prompt, then add the host under *Hosts* in the app.
- Automated check of the whole workflow: `make e2e`.

## Principles

Scientific honesty is a product feature: no invented calibration, units or coordinate frames; axial orientations (0° ≡ 180°) get axial statistics; missing is never zero; provenance is labelled verified, declared, inferred or unresolved. See `AGENTS.md`.

## Design

The approved workbench layout and design tokens are described in `docs/design/design-directions.md` and `docs/adr/0005-workbench-layout-and-tokens.md`. All prototypes use synthetic data.

## Where things are

- What the product is meant to be: `docs/product/shape.md`. What is built and what is next: `docs/roadmap.md`. Decisions: `docs/adr/`.
- How fast it is and how that is measured: `docs/performance.md` (`make bench`).

## Contributing

See `CONTRIBUTING.md`. Run `make check` before pushing.

## License

GNU Affero General Public License v3 only (`AGPL-3.0-only`), see `LICENSE`.

Copyright (C) 2026 Denis Andreyev
