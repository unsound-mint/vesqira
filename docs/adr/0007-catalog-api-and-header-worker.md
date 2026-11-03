# ADR 0007: Catalog API and header worker

- Status: proposed (accepted on merge by the owner)
- Date: 2026-11-04

## Context
The first backend slice registers existing FITS files by reference and shows what their headers declare. File paths and file contents are untrusted, and a later reviewer role must never gain access to originals.

## Decision
- **Go control plane** in `services/api`, local mode, SQLite through the pure-Go `modernc.org/sqlite` driver (no cgo, so CI and contributors need only Go). It binds to loopback only and refuses other addresses; Host and cross-site browser writes are rejected, so a web page cannot drive it by DNS rebinding or a forged form.
- **Source roots:** a path is registered only if it is absolute, resolves (symlinks included) to a regular file, and lies inside a configured `--root`. Containment uses `filepath.Rel` on resolved paths, not string prefixes. The worker only ever receives an already-authorized path.
- **Header worker:** `services/scientific-worker/fits_header.py`, one subprocess per request in its own process group (killed as a group on timeout; POSIX only), JSON on stdout, 20 s timeout, 1 MiB output cap, minimal environment, interpreter started with `-P`. It reads at most 64 header blocks, never touches the data unit, and rejects absurd `NAXISn` products before anything is allocated. Its stderr and any non-JSON output are never forwarded to clients.
- **Provenance:** every value is `declared` (present in the header) or `unresolved` (absent). Nothing is `verified`. Unresolved values are `null`, never 0 or a default.
- **Catalog:** stores the reference (canonical path, size, mtime) and the declared header facts. Scientific data stays in the original files; the path does not prove bytewise identity.

## Alternatives
cgo SQLite (needs a C toolchain); reading headers inside the API process (decoder bugs would take the control plane with them); a long-lived worker pool (more state, not needed for header-only reads).

## Consequences
Every request starts Python, which is acceptable for header reads and will be revisited when pixel reads need a persistent worker. The API has no authentication yet; it is a single-user local service, and ADR work on auth precedes any non-loopback use or the reviewer role.

## Validation
Go tests cover symlink escape, `..`, the `/data` vs `/data2` prefix case, non-regular files, foreign Host, cross-site POST, wrong content type, worker timeout, output limit and non-forwarding of stderr. Worker tests run on the generated fixtures (missing spectral reference and missing celestial WCS stay unresolved) and on malformed, truncated and oversized headers. A real-worker end-to-end test registers a fixture through the API.

## Rollback / Migration
The catalog is a single SQLite file holding no scientific data; delete it to reset.
