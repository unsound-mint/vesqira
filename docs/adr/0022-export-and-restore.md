# ADR 0022: Export and restore

Status: accepted under delegated merge; owner review pending.

## Context
The catalog, figure previews, derived results and managed copies live in one data directory with no way to move or back it up as a unit, and the catalog only refers to the researcher's own FITS files.

## Decision
`vesqira-api export` and `restore` (and `vesqira export|restore` from the launcher) work on the data directory without the server. An export is one `.tar.gz`: `manifest.json` first (format version, schema version, each file with size, mode and SHA-256, and the datasets that refer to files outside the data directory), then `vesqira.db` (a consistent copy made with `VACUUM INTO`, safe while the server runs) and the files under `figures/`, `derived/`, `managed/` and `attachments/` (the images of reports, hashed in the manifest like every other file). External files are listed, never copied: a path is not the file. Restore only fills an empty data directory, checks every file against the manifest, refuses entries outside the four folders or with unsafe names, and removes what it wrote if anything fails. The next start runs the schema check of ADR 0020 on the restored catalog.

## Alternatives
Copying the directory by hand (inconsistent while the server writes); including the referenced FITS cubes (gigabytes, and the originals stay the reference).

## Consequences
Implemented as commands; no screen. Secrets are not in the catalog (ssh keys are never handled; review tokens are stored as hashes), but host aliases, paths and notes are, so treat an archive as private. Report attachments joined the archive later: an older archive has none, still restores and imports, and its reports show broken images (the import says so). `vesqira-api multiuser import` reads this format into a group server (ADR 0050, slice 4).

## Validation
Tests: round trip into an empty directory with contents, permissions (managed copies stay read-only) and the catalog compared; a modified file is caught by its checksum and leaves nothing behind; a manifest entry that climbs out of the directory and non-archives are refused; restoring over a catalog is refused. A manual run of export, restore and a second restore against a running server's data directory.

## Rollback/Migration
Nothing to migrate (the format version stays 1: a new folder is additive, and an archive without `attachments/` is valid); archives are self-describing by format version.
