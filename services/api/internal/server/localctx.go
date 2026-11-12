package server

import (
	"context"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
)

// localCtx is the context of work no request started: the job watcher, the automatic import of a finished job, the sweep
// scheduler, the transfer runner. They act as the local owner, in workspace 1, which is the only account and workspace they
// have: hosts, jobs, sweeps and transfers are never mounted on the group server (ADR 0050 owner answer 3). Work a request
// starts keeps the request's own actor (context.WithoutCancel), never this.
func localCtx(parent context.Context) context.Context {
	return authz.WithActor(parent, authz.LocalOwner())
}
