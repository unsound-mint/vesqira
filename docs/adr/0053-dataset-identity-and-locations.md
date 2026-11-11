# ADR 0053: Dataset identity and locations

- Status: proposed; implemented, awaiting owner review.
- Date: 2026-11-11
- Builds the Stage 5 item "Dataset identity independent of the path: several locations per dataset, content fingerprint, 'newer result available' notice" (Q83, Q84, Q136), after its first part (changed or vanished local files flagged by size and modification time). ADR 0042 said a figure stores each dataset's identity "and the fingerprint once Stage 5 adds it"; this is that.

## Context

A path, a file name, a size and a modification time do not prove which bytes a dataset is (AGENTS.md invariant 7). Until now a dataset was one `datasets.path`: the same file copied to a laptop, downloaded from a host or re-run by the pipeline was a second, unrelated dataset, and a file edited in place was only noticed through size or time. A VGT re-run writes `psi_<stem>_1.fits` beside the old output instead of overwriting it, so the old dataset and the figures drawn from it silently go on describing an earlier result.

Constraint: hashing a multi-GB cube reads all of it, which does not belong on the interactive path (AGENTS.md, performance).

## Decision

**Two honest fingerprints, stored per location with when and by what they were computed.**

- **Sampled fingerprint, `sampled/1`.** SHA-256 over: the file size, the primary FITS header (the 2880-byte blocks through the one holding `END`, at most 1 MiB; none when the file does not start like FITS), and 16 blocks of 64 KiB at fixed offsets spread evenly from the end of the header to the end of the file (the first and last block always included). It reads about 1 MiB however large the file is, so it is taken at every registration. It is a function of the bytes it reads: a **different value proves the files differ**; an **equal value does not prove they are the same**. **Blind spot, by design:** a change between the sampled blocks, in the data of an extension HDU, or anywhere not listed above is not seen (a test pins this). It is never called a hash of the file; the UI says what it covers.
- **Full SHA-256** of every byte. Taken at registration only for files up to 16 MiB (as cheap as the header read already done); for anything larger only on request, in the background, one at a time, with a size cap (16 GiB local, 4 GiB through a host's worker; `Server.FullHashCap` lowers the local cap) and cancellable. It runs in the API process for local files (streamed, cancellation checked every MiB, no request timeout) and in the host's scientific worker on the heavy lane for remote files (`fingerprint_full`, `max_bytes` enforced there as well; the API kills that lane's process when the caller cancels). A managed copy records the SHA-256 it computed while copying, on the copy and on the original it read.
- **Where they are computed.** Local files by `services/api/internal/identity`; files on a host by the host's worker (`services/scientific-worker/fingerprint.py`, ops `fingerprint` on the registration lane and `fingerprint_full` on the heavy lane). One definition, two implementations: a test hashes the same files with both and compares.
- **Never on stale bytes.** A value is stored only if the file still has the size and modification time recorded for the location when it was computed (`SetFingerprint` refuses otherwise), and registering a changed file drops that location's old values before the new ones are taken.

**Locations.** `dataset_locations` (schema 15): every dataset has one primary row (the file it was registered from; `datasets.path` stays the unique key for it) and may have more, each with `kind` (registered, managed, downloaded, copy), `link`, size, modification time and the fingerprint columns above. A file is a location of at most one dataset. A location never changes what the dataset is (header, declarations and provenance stay the dataset's).

**Linking rule.** A file becomes another location of a dataset only on evidence (`identity.Relate`):

| Evidence | link | Label |
|---|---|---|
| equal full SHA-256 | `verified` | "verified same bytes" |
| equal size and equal sampled fingerprint, no full hash on both sides | `probable` | "probably the same (sampled); not verified" |
| nothing computed can tell (a fingerprint is missing on a side) and the researcher says so | `declared` | "declared the same by you; not checked" |
| any computed value differs (size, sampled, full) | refused, 409 `bytes_differ` | proof beats a declaration |

Only the registered file and `verified` locations count as evidence of what the dataset's bytes are: a new file is compared with those, never with a `probable` or `declared` location (so a declaration cannot make another file "verified"), and the same-content search at registration looks only at them. When the registered file is read again with a changed size or time, every `verified` or `probable` location goes `stale` (it was compared with the earlier file) and is not read from; once the registered file has its fingerprints again the server compares again and restores `verified` (a file only touched) or `probable`, otherwise it stays `stale`. A `declared` location stays declared.

Registering a file (`POST /api/datasets`) whose content a registered dataset already holds answers 409 `same_content` with the candidates and registers nothing; the researcher adds it as a location (`POST /api/datasets/{id}/locations`) or registers it separately (`asNew: true`). Project scans and run imports register without the offer (they have no one to ask). Registering on a host does not offer it either (the full hash is not taken there on registration); linking later is by the locations route.

**Reading.** The viewer reads from the best available location: files on this computer before files on a host, the registered file before the others, and **only the registered file or a `verified` location is eligible**. A `probable` or `declared` location may hold other bytes than the header that was catalogued and is never read silently. The reply carries `X-Vesqira-Location`, and `GET /api/datasets/{id}/locations` says `readsFrom` and why. This applies to everything that goes through `source()` (planes, spectra, coordinates, overlays' base, channel maps, PV, conversions, regions); analysis jobs, relative orientation, catalog rows and figure saving still use the registered file.

**"A newer result is available"** (`GET /api/datasets/newer`), a notice and never an action:

- `newer_file`: for a dataset that is a run output, a file in the same folder named like it with a higher `_<n>` suffix and the same extension (`psi_x.fits` then `psi_x_1.fits`), the highest `n`; names what differs (size, time, fingerprint) and the dataset if that file is already registered.
- `changed_in_place`: the registered file's size or time differs from the record; says what differs, including whether the sampled fingerprint changed. A touched file with the same bytes says only "time".
- `figure_data_changed`: the dataset behind a figure no longer matches the identity the figure recorded.

Availability (the "missing" / "changed on disk" flags) treats a registered file that is gone as fine while a verified copy on this computer can be read; a file that changed is still flagged, so it can be read again. "fingerprint differs" is said only when both values exist; a missing value is never a match or a difference. Only files on this computer are checked (as for availability). Datasets, figures and boards built on the older file show the notice; nothing is swapped or redrawn.

**Figures.** `figures.identity` (owner-side, like `scene`) stores `figure-identity/1`: for the source and each overlay dataset its id, name, size, modification time, the sampled fingerprint (with algorithm and what it covers) and the full SHA-256 when known, each with when and by what. Reviewer-safe provenance and snapshots do not carry it (a test checks the review API, the snapshot list and that the frozen and published figure types have no such field). The one change to what a reviewer reads: the source line used to say "its bytes were not hashed", which is no longer true for a file with a recorded SHA-256, so it now says what was recorded (the SHA-256 of its bytes, or a sampled fingerprint that is not a hash of the whole file, or still "not hashed"); the same for overlay statements. Figures saved before this keep their text. "Draw again" (a later slice) will use it.

**Lifecycle (ADR 0041).** Locations go with their dataset (`deleteSteps`), the impact preview lists the extra locations ("forgotten; the files are never deleted"), and files are never deleted. No allowlist entry was needed.

**Schema.** Version 15 adds `dataset_locations` (a primary row per existing dataset, no fingerprint: none was computed) and `figures.identity`. Version 14 is the transfer queue (ADR 0051).

## Alternatives

- **Hash every file fully at registration.** Honest and simple, but reads whole multi-GB cubes on the registration path. Rejected; the full hash is offered on request and taken for small files.
- **Treat the sampled fingerprint as identity.** Cheap, but a change between samples would pass as the same bytes. Rejected: equal sampled values only ever give "probable".
- **Merge duplicates silently by content.** Rejected: the researcher decides, and a probable match is not proof.
- **Replace `datasets.path` with the locations table.** The unique path key and every reader of `datasets.path` would change at once. Rejected for this slice: the registered file stays on the dataset and the location table mirrors it.
- **Swap the dataset to the newer file.** Rejected: results are never replaced behind a figure's back; the researcher registers the new file and decides.

## Consequences

- Registration reads about 1 MiB more per file (and up to 16 MiB hashed for small ones). A project scan of a thousand files does that a thousand times.
- Files whose bytes are equal under two names now meet an offer where they used to register twice. The API has an `asNew` escape; tests that register equal files use it.
- A file on a host gets only a sampled fingerprint at registration; a full hash is on request, through the host's connection and its request timeout, and fails honestly when too slow.
- Datasets registered before this have no fingerprint until one is computed (on request, when a location is added, or when a figure is saved); the UI says "not computed".
- Reading from a copy on this computer makes a pairing with a file on the registered file's host (an overlay, a reliability map) refuse as "another host", as it would for any two machines.
- Not built: "newer result" notices for files on hosts, locations on a host through the web form (the API takes `hostId`), reading a verified location in analysis jobs, relative orientation and catalog rows, and offering the same-content choice for project scans and host registrations.

## Validation

`identity_test.go`: stable for an unchanged file; changes with the header and with a change in the first, middle or last sampled block; a change between sampled blocks is **not** seen by `sampled/1` and is by the full hash; the full hash is capped (refused before reading) and cancellable; Go and Python produce equal sampled values for FITS-like, small, empty and odd-size files. Server tests: registration records both fingerprints for a small file and only the sampled one for a large file; the same-content offer (verified, and probable for a large file) and `asNew`; linking rules including refusal of a proven difference and of a declaration over proof; background full hash cap, status, cancellation and result; a host's worker computes the same sampled value and a bounded full hash (fake ssh); best-location reading with the real worker; `_1`/`_2` newer-file detection ignoring other names and extensions, in-place change with exact "what differs", figure identity stored and absent from provenance, figure notice; lifecycle impact and cascade; spec conformance. Schema: the migration test from 13, and the fresh-install snapshot for 15. Web tests for the location list labels, the notices and the offer. `make check` and `make e2e-isolated`.

## Rollback / Migration

Schema 15 is additive (a table and a column); an older program refuses a newer catalog, and the backup taken before the upgrade is the way back. Removing the feature means dropping `dataset_locations` and `figures.identity`; datasets, figures and files do not depend on them.

**Transfers (ADR 0051).** When a download finishes `verified` and its source is the registered file or a verified location of a dataset on that host, whose recorded full SHA-256 equals the one compared, the copy becomes a `downloaded` location linked `verified` (full hash stamped from the transfer). A source with no full hash recorded gets one from the transfer (method "full sha256 computed by transfer N", time of verification) only if the transfer's stat-and-hash step saw the registered size and modification time (seconds, as the host reports them); then the equal hashes link the copy as `verified`. A source whose size or time differ from the registered values records nothing and links nothing (audited as "downloaded copy not linked": the registered dataset may be stale); so does a recorded full hash that differs. A value is never stored for bytes it was not computed from. Registering a verified download whose bytes a dataset already holds answers 409 `same_content` as local registration does (`asNew` keeps it separate).
