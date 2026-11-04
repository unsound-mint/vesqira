# AGENTS.md — Vesqira

> Instructions for contributors and coding agents. **Status:** pre-release. The v0.1 workflow exists (`apps/web`, `services/api`, `services/scientific-worker`, `fixtures`, `e2e`; verify with `make check` and `make e2e`); rules below that mention things not yet built say so.

## Mission

Build **Vesqira**, an open-source research workspace for scientific data: browse and compare datasets, build research boards, and share a safe, stable review with a supervisor.

**v0.1 (astronomy only):** a researcher registers H I FITS spectral cubes and existing VGT outputs, browses spectral planes and WCS coordinates, compares scalar and axial-orientation maps with reliability information, builds source-linked figures and a board, and shares a scoped review with no SSH/HPC credentials.

Scope is astronomy only. Do not add other disciplines, services, languages or databases without an ADR in `docs/adr/`. If a referenced command or document does not exist yet, say so; do not invent verification results.

**Design gate:** the workbench direction is approved (ADR 0005); new screens still need human approval before implementation. Verify rendered UI with screenshots, keyboard testing and scientific fixtures; a screenshot or passing axe scan is not a substitute for either.

## Governing priorities

When considerations conflict, prioritize in this order:

1. **Scientific correctness and source-data integrity.** Never silently alter scientific meaning.
2. **Security and permission boundaries.** A reviewer's access to a preview does not grant raw data, compute, or secret access.
3. **A working end-to-end user workflow.** Prefer completing a thin vertical slice to constructing unused abstractions.
4. **Measured performance and operational simplicity.** Avoid unnecessary copies and heavyweight services.
5. **Maintainability and extensibility.** Prefer concise contracts and existing libraries over custom infrastructure.

Avoid speculative generalization: implement what the current slice needs.

## Architecture boundaries

The intended baseline is a modular Go control plane, React/TypeScript web application, and isolated Python/Julia scientific workers. Local mode starts with SQLite; multi-user deployment may use PostgreSQL. Scientific binary data stays in files/object storage, not ordinary database rows. These are *starting choices*, not mandates to disregard evidence.

- `apps/web`: UI, viewer interactions, board and review surfaces. No trusted interpretation of arbitrary science metadata based only on UI heuristics.
- `services/api`: auth, project access, dataset catalog, provenance, lifecycle, durable operations, review and audit.
- `services/scientific-worker`: bounded scientific data decoding and specialized numerical operations. Do not burden the API with bulk arrays.
- `packages/contracts`: shared types, schemas, OpenAPI and generated clients. Keep transport contracts versioned and independently testable.
- `packages/science-types`: scientific descriptors; *small, semantic, format-neutral* core only.
- `extensions/astronomy`: FITS/WCS, spectral coordinates and appropriate orientation-field semantics.
- `integrations`: external connectors. Providers are outside the domain model.

**Discipline-specific behavior lives in extensions.** Don't scatter per-discipline conditionals through core code.

**Plugins:** In v0.1, use internal first-party adapters and narrow capability interfaces. Do not build a plugin marketplace, untrusted in-process plugin runtime, or broad public plugin API until the needed compatibility and trust model is specified.

## Scientific correctness: non-negotiable invariants

1. **Never invent calibration, units or coordinate frames.** Unavailable pixel/voxel spacing is `unknown`, never `1`, `0`, `None` silently converted to a physical value, or inferred from image dimensions. Display image pixels if physically calibrated units are unavailable, with a clear warning.
2. **Preserve native axes and semantic types.** Do not assume `XYZ`, `ZYX`, `TCZYX`, or FITS axis ordering without reading format metadata. Dimensional metadata and buffer strides are explicit.
3. **Do not silently merge incompatible measurements.** Validate frames, units, resolution, spatial sampling, masks, signal semantics, reference conventions and applicable calibrations. Reprojection is not identical to resolution matching.
4. **Axial angles require axial statistics.** For VGT magnetic-field orientations, 0° and 180° represent the same orientation; naive arithmetic interpolation/averaging is not valid. Preserve uncertainty and reliability where available.
5. **Never treat zero as missing.** Missing values, invalid pixels, low-SNR flags, saturated measurements and user exclusions have distinct meanings; preserve source data and operation-specific mask policies.
6. **Scientific QC is not file validity.** Successfully reading a file or completing a job is not evidence that a scientific result has passed validation.
7. **Preserve provenance truthfully.** Distinguish `verified`, `declared`, `inferred`, and `unresolved`. Do not claim to know a program's RNG, calibration version, or external reference table unless actually established.
8. **No silent changes to originals.** Masks, calibrations, normalizations, metadata overlays and processing outputs are distinct derived resources or explicitly mutable reviewed state.
9. **No universal scientific 'success' label.** Computational completion, scientific validity, and interpretation of evidence are separate.
10. **Scientific controls are not universal.** Domain methods for time scales, coordinate transformations, uncertainties and statistics must use validated providers; do not add generic math by intuition.

### Acceptance fixtures
- FITS 2D/3D, multiple channels, WCS with known test positions and values, masked/NaN samples, spectral units and reference-frame cases.
- Verify plane selection and spatial/spectral coordinate readouts against an independent trusted implementation.
- Preserve scalar, directional and **axial orientation** distinctions; verify the 1°/179° axial-mean case and degenerate resultant behavior.
- Test incompatible WCS, missing spectral reference data, mismatched resolution and remote partial-read failure.

## Data lifecycle and versioning

Do **not** create bespoke version systems for every entity. Apply four common policies:

- **Ephemeral:** viewer cursor, transient object handles, temporary plots; leases/expiration, not immutable versions.
- **Mutable working state:** projects, drafts, board layouts and tasks; normal persistence and targeted edit history.
- **Pinned scientific evidence:** finalized datasets, figures, experiment outputs, and review snapshots; stable revision identities and meaningful dependency edges.
- **External reference:** archive product, Git SHA, remote HPC path, DOI/PDF version; record exact provider identity and validity/availability. Copy only when needed or explicitly requested.

The existence of a path, remote URL or filename does **not** prove bytewise identity or long-term availability. No automatic Git commits, pushes or changes to research repositories without explicit instruction.

## Performance and resource discipline

- Do not transfer an entire multi-GB FITS cube to the browser to display one plane or ROI.
- Prefer selective reads, well-bounded tiles, incremental previews and reused remote/source-side computations.
- Bound dimensions, decompression expansion, memory, CPU time, network transfer and number of concurrent scientific tasks. Validate headers before expensive allocation.
- Choose structured binary transport for numerical arrays; include dtype, byte order, shape, axis/order descriptors, masks and precision semantics.
- Avoid costly per-pixel unit/metadata abstraction. Validate scientific contracts at operation boundaries and use optimized kernels.
- Benchmark cold and warm operations, peak memory, bytes transferred and scientific equivalence; don't optimize on intuition alone.
- GPU and Rust acceleration are optional tools. Profile before introducing or rewriting native components.
- Check cancellation, retry, invalid cache data, partial reads, expired handles and network disconnection.

## Security and isolation

- Treat imported FITS files, metadata, archives and any third-party output as untrusted input.
- Avoid arbitrary local-path reads via unvalidated request parameters; canonicalize and authorize source roots before access. Defend remote connectors against SSRF, path traversal and overbroad network privileges.
- Never embed secrets in source, logs, provenance documents, generated client bundles or reviews.
- SSH credentials are user-scoped, brokered securely, and never exposed to supervisors or AI agents through document metadata.
- A reviewer receives only explicitly published previews/artifacts for which they have permissions; original files or privileged HPC sessions do not become readable through indirect URLs.
- All irreversible/destructive, external-write, high-cost HPC or instrument-control actions require policy checks and documented authorization.
- Use separate process isolation for scientific decoders and any executable workload.
- Plugins and connectors must advertise capabilities; calls enforce scopes server-side, not solely through UI gating.

## Agent working procedure

For every assigned task:

1. **Inspect** relevant code, tests and docs. Do not claim modules, commands or migrations exist until verified.
2. **Define the slice**: user-visible behavior, interfaces affected, source-data and security impact, acceptance criteria; avoid unrelated changes.
3. **Implement the smallest complete path**, including schema and client changes where required. Prefer adapting established libraries rather than reimplementing them.
4. **Test scientifically meaningful behavior**, not just type-checking. Add regression fixtures and negative/error cases for assumptions changed.
5. **Validate boundaries**: auth scopes, source integrity, invalid inputs, resource budgets, browser memory, cancellation and recovery.
6. **Run available project checks**: format/lint/type-check/unit/integration tests. Use the commands actually defined in the repo; don't invent success or suppress errors.
7. **Report outcome precisely**: changes made, tests executed and observed results, tests not run with reasons, scientific assumptions, remaining risks and any migrations or compatibility changes.

**For multi-agent work:** Assign non-overlapping file ownership where possible. Agree on shared contract changes first; avoid parallel agents making incompatible edits to the same API/schema or UI components. A separate reviewing agent should inspect correctness, security and scientific semantics before merge when practical.

## Tests and definition of done

A feature is done only if:
- The stated user workflow works end-to-end using real or representative fixtures.
- Scientific units, axes, masks, calibration and interpretation status are preserved and independently checked when relevant.
- The original scientific input is unchanged unless mutation was an explicit, authorized operation.
- Errors are actionable; malformed and untrusted inputs fail in controlled ways.
- Permission checks are enforced on the server and tested at trust boundaries.
- Normal and failure/retry/cancel flows are exercised where relevant.
- Tests and measurements are recorded; unsupported claims and unrun tests are disclosed.
- Documentation and relevant schema/ADR are updated for user-visible semantics or architectural changes.

Do not write "all tests pass" unless all intended tests were actually run and passed. Do not call a screenshot or a mock a verified scientific integration test.

## Decisions, documentation and ADRs

- Update a short ADR when introducing a new technology, storage mechanism, external service, security boundary or scientific semantics contract.
- ADR format: **Context**, **Decision**, **Alternatives**, **Consequences**, **Validation**, **Rollback/Migration**.
- State whether a capability is **implemented**, **specified**, **prototype**, or **deferred**. Don't equate specification with delivery.
- Use one source of truth for transport and semantic schemas; generate clients where it reduces drift.
- Preserve scientific provenance and metadata meaning when migrating schemas.
- Keep instructions short in this root file; add deeper `AGENTS.md` instructions in subdirectories only when concrete local rules emerge.

## Explicit anti-goals

- Don't implement everything at once; ship the first workflow first.
- Don't build an in-house scientific statistics, FITS or statistics library when robust existing libraries can be integrated.
- Don't create a generic microservice fleet or custom distributed object store without a measured need.
- Don't turn every change into an immutable version or copy all scientific data into managed storage by default.
- Don't introduce scientific assumptions, AI-derived facts, reference data or inferred calibration silently.
- Don't offer collaborators implicit access to research machines, credentials or private files.
- Don't automatically apply methods, re-run costly analyses, or alter external accounts without authorized scope.

**Optimize for a researcher being able to use and trust one complete workflow, not for the number of architectural concepts implemented.**
