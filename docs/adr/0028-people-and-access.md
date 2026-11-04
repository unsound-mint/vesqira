# ADR 0028: People and access

Status: **proposed**. Needs the owner's decision before any code (it changes the security boundary).

## Context
Today there is one principal, the owner, on a loopback address. Others reach the app only through a review link: read-only, revocable, no data, no paths, no credentials. The requirements ask for colleagues and a supervisor to take part (comment, request runs, see results per cluster) without ever being given the owner's cluster access (AGENTS.md: SSH credentials are user-scoped and never shared).

## Options
**A. One Vesqira per person, collaboration by links and requests (recommended).**
Everyone runs their own copy with their own ssh agent and cluster account. A person is a *name on a link*, as now. New: **run requests**. A reviewer, from the review page, can propose a job (template, parameters, a note) for something on the board. The request is data: it lands in the owner's queue; the owner reads the exact script it would produce, edits or rejects it, and submits it from their own host. Nothing a reviewer sends can run by itself, and a request names no path or host. Approval is therefore structural, not a permission flag.
- No accounts, passwords or server to operate; the security boundary does not move.
- Colleagues who want to run on the cluster use their own Vesqira and their own login.
- Limit: no shared live project; people see what the owner publishes.

**B. A shared multi-user server.**
Accounts (OIDC or passwords), roles per project and per cluster, PostgreSQL, TLS, an audit trail with actors, server-side brokering of ssh with per-user keys. This is the long-term shape in the architecture notes, and also the largest change: a deployment to run and secure, key custody to design, and every existing route to gain an actor and a permission check.
- Gives a shared project and per-cluster roles.
- Costs weeks, and a mistake here exposes cluster access.

**C. Accounts for review only.** Named, authenticated reviewers (email magic link) but still no shared compute. Fixes "comments are anonymous" without running a server of your own: it needs a small hosted identity step, which is an external service.

## Recommendation
Do A now (run requests, queue, approval by reading the script). Treat C as the answer to authenticated comments only if a supervisor needs it. Defer B until a group actually wants a shared deployment; write its own ADR then.

## What A would add (if approved)
- `POST /review-api/{token}/requests` (reviewer): a template name, bounded typed parameters, a note; rate- and size-limited like comments.
- `GET /api/requests`, `POST /api/requests/{id}/draft` (owner): turns a request into an ordinary draft job on a chosen host, to be read and submitted as today; `POST /api/requests/{id}/decline`.
- A requests list in the board screen and an audit entry for each decision. Reviewers see the state (open, drafted, declined) and the owner's note, never the script, host or paths.

## Validation (when built)
Requests carry only validated values; a request cannot reference hosts, paths or datasets by id; the review listener cannot list, edit or approve; drafting never submits.

## Rollback
Additive tables and routes.
