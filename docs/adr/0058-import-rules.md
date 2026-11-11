# ADR 0058: Import rules: scan preview, researcher rules, explicit apply

- Status: proposed; implemented, awaiting owner review. No design review and no owner approval of the new screen (same position as ADR 0046).
- Date: 2026-11-12
- Builds the Stage 5 item "Generic import: scan a folder, review the proposed mapping, save the rules" (Q17, Q82) on ADR 0030 (projects and discovery), 0039 (writing on a host), 0044 (point catalogs), 0046 (ROHSA runs), 0053 (dataset identity).

## Context

Discovery applies built-in rules: run records, FITS, catalogs in declared data folders. Researchers' folders vary (`validation_output/rohsa/<label>/{maps,cubes}`, `data/{exp_raw,interim,sims}`, `vda/*/vgt psi maps`, text star tables). The built-in rules cannot know that `vda/*/vgt/psi_*.fits` are B-field orientations in the pixel frame, that `*_kms.fits` have unit km/s, or that `stars/*.dat` is a catalog with declared columns; the researcher does, and Vesqira must not guess it (AGENTS.md invariants 1, 7, 8).

## Decision

**Scan preview (read-only).** `POST /api/projects/{id}/import/preview` walks a chosen folder inside the project (this computer, or a host through the worker's `walk`; the project folder is already a source root, so no new authorisation) with the scanner's own rules, a depth cap of 24 on top of the entry and time budgets, and an inventory of every file nothing recognises (at most 50 000, `othersTruncated` says when there were more). Every file gets a built-in role and the rule behind it (`discover.Items`): dataset, catalog, run record, VGT output with the role the run record gives it (`psi`, `reliability`, `support`, `fit_rbar2`), file inside a run, ROHSA fit and ROHSA product, config, job template, archive, other format without viewer, file in a declared data folder, not recognised. The reply groups files by role, extension and author with counts, sizes and a bounded sample (a group opens to 500). Truncation (entries, time, depth, inventory) is reported, never silent. The preview registers nothing, writes nothing and changes no row; the walk is cached ten minutes per project so that editing a rule is answered without walking again.

**Researcher rules.** A rule has a unique name, a kind (`glob` or `regex`), a pattern on the path relative to the project folder, a role and flat declaration fields (`discover.Rule`).
- Glob: no `/` means the file name at any depth; with `/` it is anchored at the project folder; `*` stays in one segment, `**` stands for any number of folders; case-sensitive. Regex: Go RE2 (no backtracking), searched in the whole path. Pattern length 300, 100 rules per project.
- Roles: `dataset` (optional `unit`), `orientation` (`angleUnit` deg|rad **required**, optional `angleFrame` pixel|sky and `quantity`), `catalog` (longitude and latitude columns, coordinate format, coordinate frame **required**, equinox for fk4/fk5, optional angle column with unit, quantity and uncertainty column, up to eight value columns) and `ignore`. A declaration field a role does not take is refused. Nothing has a default: the example `vda/*/vgt/psi_*.fits` needs its angle unit stated. There is no `mask` role: masks are not in the tree (ADR 0055 is unmerged); a file named like a mask is "not recognised" until it lands.
- **First match wins**; the order is editable and is the priority. Files matched by several rules are listed as conflicts with the winner and whether the rules would say different things.
- Rules see only what the walk listed: never-read names (`*DO_NOT_OPEN*`, `blind*key*`), skipped folders, links, archives and run records are decided before any rule (tested with a counting filesystem and the pattern `.*`). Rules cannot create runs: run records are found by the built-in rules only.
- **Live preview.** The draft list is sent with the preview request and matched on the server against the cached walk (one matcher; JavaScript regular expressions differ from RE2). A rule with a broken pattern matches nothing and says why. Saving refuses invalid rules with every reason.

**Storage.** Rules live in the catalog (`import_rules`, ordered per project, schema 18) and, only after a separate confirmation (`POST .../import/rules/write` with `write: true`; without it 400 and the file is untouched), in `.vesqira/project.toml` as a `[[import.rules]]` section between `# vesqira:import-rules begin/end` markers. Writing replaces only that section; the rest of the file stays byte for byte. `[[import.rules]]` outside the markers are the researcher's own: they are read, and never rewritten (409). On a host the write follows ADR 0039 (a file Vesqira did not write is not replaced; the rules stay in the catalog and the reply says so). When a scan finds rules in the file and the catalog has none (a project opened on another computer) it loads them; otherwise the file is not read back over saved edits and the page says whether the file holds the same rules.

**Apply.** `POST .../import/apply` with `confirm: true` registers the files the **saved** rules select, through the existing paths (`registerPath` for FITS, `registerCatalogPath` for a catalog rule whatever the name, `registerRemoteFile` on a host, all on the shared heavy lane), then applies the rule's declarations through the existing setters (`SetUnitDeclaration`, `SetInterpretation`, `ValidateCatalogDeclaration` then `SetCatalogDeclaration`) with author `rule:<name>`. At most 200 files per request (`remaining` says how many are left), 150 s.
- A declaration the researcher made (`declaredBy = user`) is never replaced; one by an adapter or an earlier rule may be. A file's own BUNIT always wins over a rule's unit. A reliability companion an adapter paired stays; a rule never pairs one. Catalog columns are validated against the file's columns first.
- Duplicates follow ADR 0053: a file whose content a dataset holds is offered (`duplicate`, with the matches), not registered; "Register separately" is a click that sends the path in `asNew`. On a host there is no offer (ADR 0053 takes no full hash on registration there).
- A file inside a run folder is registered by that run's import; a rule only adds its declarations once it is registered, otherwise the file is skipped with that reason. A point catalog on a host is skipped (ADR 0044).
- The reply names every file registered, declared, offered, skipped (with why) or failed. An application record (`import_applied`: path, rule, size, modification time, outcome) settles a file at that size and time; a changed file, or a failure, is pending again. Applying twice does nothing twice.

**Provenance labels.** ADR 0055's tiers are not merged, so a rule's declaration carries `declaredBy = rule:<name>`; the tier follows when 0055 lands (that tier would be `declared`, the rule name the source). Everywhere a declaration is labelled it reads "declared by your rule <name>" (figure declaration comparison, the needs list, the viewer's orientation panels, the reviewer-facing board provenance, which says "an import rule of the author's project" without the name). An orientation declared by a rule stays in "Needs your input" as unconfirmed until the researcher confirms it, like a run import's proposal; a VGT re-import does not replace it.

**Rescans.** Every scan counts what the saved rules select (`importRules` in the project summary: rules, matched, pending, new). "N new files match your rules" is the pending files no earlier scan counted (a `pending` application record remembers them); the explorer links to the page. **A scan never registers a file for a rule and never applies a declaration**; registration of what rules select happens only on the Register click. (The scan still registers FITS files by the built-in rules, as before.)

**Contracts.** The five routes are in `bodies.json` (no growth of `undescribed.txt`); a contract test validates their replies against the spec.

**Schema.** Version 18 adds `import_rules` and `import_applied` (DDL shared by the migration and `projects.Open`). **Numbering:** other branches claim 18 (masks) and 19 (history); this branch takes the next number after main, 18, because the migration test requires versions 2..N without a gap. When those merge this one becomes 20: renumber `schema.Version`, the migration's `To`, `testdata/schema_v18.sql` and the comments.

## Alternatives

- **Register what rules select during every scan.** One less click, and a stray pattern would register and declare files unseen. Rejected: the scan counts, the researcher confirms.
- **Match drafts in the browser.** Instant, but a second glob and regex implementation that drifts from the server's (JavaScript regex is not RE2). Rejected; a short debounce and the cached walk keep it fast.
- **Rewrite project.toml from the catalog on every save, or append superseding blocks.** The first loses the researcher's edits to the file, the second cannot express reordering. A marked section Vesqira owns, replaced only on confirmation, avoids both.
- **A `mask` role now.** Nothing could apply it. Deferred to ADR 0055.
- **Defaults for the angle unit or the coordinate frame.** The example rule from the roadmap has none; filling it in is exactly the silent guess invariant 1 forbids.

## Consequences

- Researchers can describe a folder's conventions once and re-apply them; declarations stay labelled as theirs and are never promoted to the file's.
- Every scan stats each unrecognised file (up to 50 000) for the inventory.
- A scan on a host lists the whole project (the worker's walk has no subtree option); the folder only narrows what is classified.
- `internal/projects` now imports `internal/schema` (architecture table updated).

## Validation

Table tests for glob and regex matching, validation, first match and conflicts, grouping, built-in roles per run kind, depth and entry bounds, the never-read and archive exclusions (counting filesystem), and the project.toml section (bytes kept, foreign rules refused, quoting round trip); server tests for a read-only preview, live draft preview, save refusal, apply with provenance, the researcher's declaration and a file's BUNIT winning, duplicates offered and registered separately, run files, the confirmed write, rescan counts and "a scan never registers", a catalog rule with the real worker, a host folder through the fake host harness, and a contract test; a migration test from schema 17; web tests for the rule editor, the preview, conflicts, save and write confirmation, and apply with duplicates.

## Rollback / Migration

Two new tables, no change to existing ones. To roll back, drop `import_rules` and `import_applied` and set `user_version` back; declarations already applied stay (each is labelled `rule:<name>` and can be withdrawn like any declaration). `[[import.rules]]` sections stay in researchers' project files and are inert to older programs.
