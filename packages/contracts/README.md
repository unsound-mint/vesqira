# contracts

`openapi.yaml` is **generated** by `tools/contracts.py` and never edited by hand. It combines two sources:

- the route registrations in `services/api` (every path, method and listener), and
- `bodies.json`, the hand-written request and response descriptions (ADR 0032). Keys of `operations` are `"METHOD /path"`; `schemas` become `components.schemas`.

`make docs` fails when the file is out of date, when `bodies.json` names a route that does not exist or a schema it does not define, or when the web client (`apps/web/src/api`) calls a route that does not exist. It also enforces a ratchet: `undescribed.txt` lists the operations that still have no described bodies (one `METHOD /path` per line, sorted).
A route missing from bodies.json and from that list fails the check (describe it), and so does a listed route that is now described (`tools/contracts.py` trims the file; it never adds to it).

Status: bodies are described for `GET /api/datasets`, `GET /api/datasets/{id}`, `GET /api/datasets/{id}/view` and the plane frame (`GET /api/datasets/{id}/plane`: content type, query parameters and the JSON header; the binary payload is documented in prose). `GET /api/capabilities` and every `/api/figures*` and `/api/boards*` operation (request bodies, JSON replies and the two PNG routes) are described too, and so are the two `/api/notifications` operations and the text editor (`GET`/`PUT /api/files/text`, 409 conflict body). The review surface is described end to end: the reviewer API (`/review-api/{token}` and its preview, comments and outcome routes) and the author's snapshot routes (comments, reply, revoke, resolve). All other operations are routes only.

## Types for the web client

`apps/web/src/api/schema.d.ts` is generated from `openapi.yaml` with `openapi-typescript`. It lives in its own npm project here (not in `apps/web`) because the generator needs the TypeScript 5 compiler API, which TypeScript 7 does not provide.

- `cd apps/web && npm run gen:api` regenerates it; `npm run check:api` fails if it is stale (part of `make web`).
- `catalog.ts`, `planes.ts`, `boards.ts` (including the review types) and `metrics.ts` re-export their types from it; add new described shapes there rather than writing them twice.

## Go side

`services/api/internal/server/contract_test.go` (and `internal/review/contract_test.go` for the reviewer listener) decodes real handler responses (real worker, generated fixtures) and validates them against `openapi.yaml` with kin-openapi. When a handler and the spec disagree, the spec is what describes the wire; change it to match the handler, not the other way round, unless the wire format is meant to change.
