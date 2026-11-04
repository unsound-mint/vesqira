# contracts

`openapi.yaml` lists every route of the local API and the review API (path, method, listener). It is **generated** from the route registrations in `services/api` by `tools/contracts.py`; `make docs` fails when it is out of date or when the web client (`apps/web/src/api`) calls a route that does not exist.

Status: routes are covered. Request and response bodies are **not yet described**; the TypeScript types in `apps/web/src/api` are still written by hand. Describing bodies and generating those types from them is the next step, and is deferred until the shapes settle.
