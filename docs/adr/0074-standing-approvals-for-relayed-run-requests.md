# ADR 0074: Standing approvals for relayed run requests

- Status: implemented (schema 37); decided under the owner's delegation, the owner reviews it afterwards
- Date: 2026-11-13
- Related: ADR 0016 (approval binds to the script hash), 0048 (sweeps), 0050 (group server; deferred "preapproved templates and budgets"), 0054 (strict rerun pins the commit), 0066 section 3 (relayed run requests), 0068 slice 5 (run templates, declared keys, the commit pin)

## Context
Since ADR 0066 slice 5 a collaborator without cluster access files a run request on the group server, the key holder's own computer pulls it, renders it into a draft with the shipped template, and the key holder reads the exact script and approves it. Every request needs that click. For a trusted labmate who runs the same configuration with a few settings changed, week after week, the click is friction without information: the key holder approves the same kind of script each time. ADR 0050 deferred "preapproved templates and budgets for trusted collaborators" and roadmap Stage 7 lists it as the last open item.

What must not change: the SSH key and the cluster account stay on the key holder's computer; the group server never sees a script, path, host or Slurm id; the script that runs is the one the shipped template renders; the key holder's computer validates every request itself and never trusts the server's claim that it is acceptable.

## Decision
**A standing approval (a grant) is a record on the key holder's computer that lets one named collaborator's requests be submitted without a per-request click, inside narrow terms and a budget.** The computer re-checks every term for every request; the group server only carries the request and the grant's id.

### What a grant holds (local table `relay_grants`)
- **Who.** One collaborator, by the group server's account id (never the display name, which can change or collide). The key holder chooses among people who have already sent them a request, so the id is one the server has shown. The requester's account id travels on the pulled request only when the request names a grant (`requesterId`, omitted otherwise, so a page without grants is byte for byte what an older computer decodes).
- **Where and what.** One inbox label (and the host it stands for when the grant is made; if the label is later pointed at another host the grant no longer applies), one template (`vgt` or `rohsa`), the profile label at creation, and one code commit: the profile's pinned commit, or a 40-character commit the key holder types (for example the commit of their own recent runs). The grant does not change how relayed drafts pin code: a relayed draft already insists on the base run's commit (ADR 0054, ADR 0066 slice 5). A granted draft is submitted only when the commit its script insists on equals the grant's commit. A draft that insists on no commit, or another one, waits for the key holder.
- **Which settings.** A list of keys, each narrowed to a numeric range (both ends given) or a choice list. Every key must be settable by a request (`relay.Settable`), must be in the label's inbox for that template, and its range must lie inside the template's own declared range and inside the inbox bound; every choice must pass the template's validator and the inbox bound. Never wider. A key the grant does not list is outside it. The terms are checked against the template as loaded now both when the grant is made and when it is used.
- **Inputs.** A request never names an input; the input comes from the base run's job (ADR 0066). The grant lists input folders and exact files on the host (at most 16), each an absolute, clean path (`jobs.ValidPath`, `path.Clean(p) == p`, not `/`) inside one of the host's source roots. The granted draft's input (`jobs.InputPathFor` on the config the draft was rendered from) must equal a listed file or lie under a listed folder. Paths never leave the computer.
- **Budget.** Max jobs in total, max total requested core-hours (CPUs times the time limit, from the rendered draft, because a zero resource in a request means the base's value), per-job maximum CPUs, memory and wall time, max jobs queued or running at once, and an expiry date (at most 180 days ahead).
- **Revocation.** `revoked_at`. Revoking is immediate and final; a revoked grant is kept for the record.

### When a request goes through a grant
In each relay pass, after the pull, the computer looks at drafted requests that name a grant and have not been decided. A request is submitted under the grant only when all of these hold, in this order:
1. The grant exists on this computer for this group server, is not revoked and has not expired; its collaborator is the request's `requesterId`; its label and template are the request's; the label still stands for the grant's host.
2. The request was filed after the grant was made and has not expired by this computer's clock; it is in `requested` or `waiting_review`.
3. The request has no grid (a sweep waits for the key holder: reserving and approving a whole grid at once is a later decision), and every setting it changes is a key of the grant, inside the narrowed range or choices.
4. The base resolved by itself through this computer's own push records (a base the key holder must choose waits for them), and the draft was rendered from it with no change and no hand edit by the key holder.
5. The draft's input is inside the grant's folders or files; its CPUs, memory and wall time are within the per-job caps; its script insists on the grant's commit.
6. The budget takes it: one conditional `INSERT … SELECT … WHERE` statement checks revocation, expiry, jobs used, core-hours used plus this job, and the jobs not yet ended, and writes a use row. `UNIQUE(relayed_id)` on the use rows means one request takes at most one slot, whatever replays or retries. SQLite runs the statement atomically, so two requests at once cannot both take the last slot.
7. The claim on the group server, bound to the rendered script's hash and carrying the grant id, succeeds (the same one-winner claim as a manual approval). A claim that is refused (withdrawn, expired, gone) gives the slot back. A server that does not answer gives the slot back and the next pass tries again.
8. `submitDraft` submits the exact script, as a manual approval does.

The claim-and-submit core is shared with the manual approval (`approveRelayed`), so both submit the same way. The pass, the manual approval and revocation hold the same lock, so a revocation cannot land between the reservation and `sbatch`.

**Anything outside the grant falls back to the normal manual approval and says why.** The draft stays a draft, the key holder sees "Waiting for you: outside the standing approval: <reason>", and the reason is kept on the mirror. Toward the group server only an enumerated code crosses with the review (`outside`, `budget`, `expired`, `revoked`, `unknown`, `input`, `commit`, `base`, `grid`), shown to the collaborator as a fixed sentence, never the computer's own sentence (which could name a path). A request whose jobs-at-once limit is full waits for a free slot and is tried again each pass; the key holder can still approve it by hand.

**After the fact.** Every auto-submission writes an audit row ("run request submitted under a standing approval", with the request, the grant id, the job and the script hash); these rows are not subject to the refusal audit budget. The job's note says which grant allowed it. The key holder's inbox lists the request as "submitted under standing approval <id>", and its detail shows the exact script that ran, as for a manual approval. The group server's approval record names the grant.

**Revoking.** Revoking stops further submissions at once: the next request naming it falls back with "revoked". Jobs already queued or running under it are listed in the answer and on the grant, never cancelled by Vesqira; cancelling stays the key holder's act.

### The group server
- The key holder's computer publishes, with its inbox, an **offer** for each live grant: id, collaborator id, label, template, the narrowed keys, the per-job caps, the budget and what was used when it was published, the expiry. No path, host, commit or root. A use or a revocation marks the inbox for publishing again.
- `GET /api/run-inboxes` shows each member only the offers made to them.
- Filing with a grant id is refused (422) unless the addressee's published inbox offers that grant to the filer for that label and template, and it has not expired. The server stores the grant id on the request.
- `POST /api/run-requests/preview` tells the collaborator before sending whether the request fits an offer ("will go through standing approval <id>, unless the key holder's computer finds the base run's input, its code commit or the budget outside it") or will wait for review, and why. The preview is advisory: the server sees neither inputs nor the live budget.
- A claim that names a grant is accepted only when it is the grant the request was filed under.

### Limit stated, not solved
**The key holder's computer cannot verify who filed a request.** It trusts the group server for the requester's identity, as every relayed request does. A compromised group server can forge requests in the grantee's name that fit the grant, and the computer will run them. The grant bounds that: only the grant's keys and ranges, only its inputs, only its commit, and at most its budget before it expires. That is why a grant has a budget and an expiry, and why it is narrow by construction. Forged, expired, revoked and another person's grant ids are refused by the computer.

## Alternatives
- **The computer picks a matching grant itself and the request names none.** Smaller wire, but the collaborator would not know before sending whether a request goes through, and the audit could not say what the requester asked for. Rejected.
- **Grants held by the group server.** Breaks the rule that the server's word is never trusted; a compromised server could widen a grant. Rejected.
- **Budget as a counter column updated after submission.** A counter decremented after `sbatch` races; the conditional insert of a use row is one statement and leaves a record of each use. Chosen.
- **Grids under a grant.** Reserving N slots and approving a sweep without a click is possible, but doubles the paths to test. Deferred; a grid waits for the key holder.
- **Cancelling queued jobs on revocation.** Rejected: a revocation is about future requests, and cancelling someone's running work silently is the kind of irreversible act Vesqira leaves to a person.

## Consequences
- A trusted collaborator's routine request runs without the key holder at their computer, on the key holder's account, within terms the key holder set.
- The key holder must look afterwards. The inbox shows every auto-submitted request with its script; the audit log has the row.
- A compromised group server can spend a grant's budget. It cannot exceed it, change the code commit, choose inputs outside it, or set keys outside it.
- One more local table pair and three columns; two server columns.

## Validation
- `internal/relay/grants_test.go`: grant terms validation (a range wider than the template's or the inbox's, a missing end, a choice the template refuses, an unsettable or unlisted key, a path that is not clean or outside the host's roots, `/`, budget and expiry bounds); the fit of an ask (a key outside the grant, a value outside the narrowed range, a grid); the budget under concurrency, run with `-race`: many goroutines reserving against one remaining job, and against core-hours that admit exactly two, exactly that many win; a second reservation of the same request takes nothing; a revoked or expired grant reserves nothing.
- `internal/server/relay_grant_test.go` on the relay end-to-end setup of ADR 0066 (TLS group server, local instance on the stand-in Slurm host, every ssh command logged): a granted request is submitted exactly once with no click, the claim carries the rendered hash and the grant id, the audit row and the job note name the grant, the server's record names it; a request outside the grant (key, range, resources) waits as a draft with the reason; a forged grant id, an expired grant, another person's grant and a revoked grant fall back; an input outside the grant's folders (including a path with `..` in the base config) falls back; a commit other than the grant's falls back; the budget stops the next request; a replayed page does not submit again; a request filed before the grant was made falls back; a compromised server's forged in-grant requests stop at the budget; revocation lists the running job and cancels nothing; filing with a grant not offered is refused and the preview says which way a request goes; the wire carries no path, host or commit in the offer.
- `e2e/tests/zzzzzzzzzzzz-standing-approvals.spec.ts`: the "Standing approvals" section and the collaborator's preview, rendered against stubbed API answers (the Playwright stack has no TLS group server), with axe, keyboard order and screenshots at 1440x900 and 390x844 in light and dark. The security claims are carried by the Go tests above.

## Rollback / Migration
Schema 37 adds `relay_grants` and `relay_grant_uses` on the key holder's computer, `grant_id`, `grant_state` and `grant_note` on `relayed_requests`, and `grant_id` on the server's `run_requests`. Nothing is rewritten. Rolling back the code leaves the tables unused; an older computer ignores them and every request needs a click again. A server that runs the older code refuses filings that name a grant only if its strict body reader is reached with the field, which the older web app never sends.
