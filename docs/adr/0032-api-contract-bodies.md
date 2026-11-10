# ADR 0032: Request and response bodies in the API contract

- Status: accepted; first slice implemented (datasets list, dataset, view, plane header). The rest is specified, not implemented
- Date: 2026-11-10

## Context
`packages/contracts/openapi.yaml` lists routes only. The TypeScript types in `apps/web/src/api` are written by hand next to Go structs, so a renamed or retyped JSON field breaks the browser at run time and no check notices. Route extraction is guarded (`tools/contracts.py --check` fails on registrations it cannot read); bodies are the remaining gap. The README deferred this until shapes settle; the API now has about 150 operations and an installable app, so the drift risk is real.

## Decision
Describe bodies spec-first, one route group at a time, starting with the groups the viewer and review depend on (planes, catalog, figures, boards, review). The OpenAPI file stays the single source; `openapi-typescript` generates `apps/web/src/api/schema.d.ts` from it and the hand-written modules import those types. Each described operation gets a Go contract test that decodes a real handler response into the spec's schema (kin-openapi), so Go and the spec cannot diverge either. Undescribed operations stay allowed, but `contracts.py --check` reports how many are still undescribed so the number only goes down.

Implementation notes (first slice): the descriptions live in `packages/contracts/bodies.json` and `tools/contracts.py` merges them into the generated `openapi.yaml` (the generator stays standard-library only). The type generator runs from `packages/contracts`, not `apps/web`, because `openapi-typescript` needs the TypeScript 5 compiler API and the web app uses TypeScript 7.

## Alternatives
- Generate the spec from Go structs (swaggo): the handlers are plain `net/http` with inline maps, so annotations would restate every shape and drift as the hand-written types do.
- Generate Go and TS from a schema language (protobuf, TypeSpec): a new toolchain for a one-developer project.
- Keep hand-written types and add only a Go-side golden JSON: catches Go changes, not the TS side.

## Consequences
Adds one dev dependency on each side and a generation step to `make check`. Handlers returning `map[string]any` must get named structs before they can be described.

## Validation
Per group: generated types compile, the contract test passes for each described operation, and `make e2e` still passes.

## Rollback / Migration
Generated types are additive; delete `schema.d.ts` and the imports to revert one group. No data or wire format changes.
