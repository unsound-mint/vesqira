package server

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The batch workload class on the group server (ADR 0050 section 8, the part slice 3 needs): one running batch item per
// account (local analyses, managed copies), at most ten more waiting, and the workspace's quota of managed-copy bytes. The
// server-wide limit stays the heavy class of the workload governor (worker.Load), which every analysis already takes; this
// lane only stops one person from filling it. Local mode has one person and no lane.

const (
	batchRunning = 1  // per account
	batchWaiting = 10 // per account, beyond the running one
)

type batchLane struct {
	mu   sync.Mutex
	accs map[string]*batchAccount
}

type batchAccount struct {
	slot    chan struct{}
	waiting int
}

// errBatchFull is the refusal when an account already has its running item and ten waiting.
type errBatchFull struct{ waiting int }

func (e errBatchFull) Error() string {
	return fmt.Sprintf("you already have an analysis or copy running and %d waiting; wait for one to finish", e.waiting)
}

// enter waits for the account's batch slot (or ctx), and returns its release. With more than batchWaiting already waiting it
// refuses at once (errBatchFull), so a burst never queues without limit.
func (l *batchLane) enter(ctx context.Context, account string) (func(), error) {
	l.mu.Lock()
	if l.accs == nil {
		l.accs = map[string]*batchAccount{}
	}
	a := l.accs[account]
	if a == nil {
		a = &batchAccount{slot: make(chan struct{}, batchRunning)}
		l.accs[account] = a
	}
	select {
	case a.slot <- struct{}{}:
		l.mu.Unlock()
		return func() { <-a.slot }, nil
	default:
	}
	if a.waiting >= batchWaiting {
		n := a.waiting
		l.mu.Unlock()
		return nil, errBatchFull{n}
	}
	a.waiting++
	l.mu.Unlock()
	defer func() { l.mu.Lock(); a.waiting--; l.mu.Unlock() }()
	select {
	case a.slot <- struct{}{}:
		return func() { <-a.slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// batchSlot is enter for the request's actor on the group server; local mode passes through.
func (s *Server) batchSlot(ctx context.Context) (func(), error) {
	if !s.multiUser() {
		return func() {}, nil
	}
	return s.batch.enter(ctx, actorID(ctx))
}

// managedDirOf is where a workspace's managed copies go: the managed folder itself in local mode, a folder per workspace on
// the group server (ADR 0050 section 3), so its use can be measured against its quota and nothing of one workspace sits in
// another's folder.
func (s *Server) managedDirOf(ws workspace.ID) string {
	if !s.multiUser() || s.ManagedDir == "" {
		return s.ManagedDir
	}
	return filepath.Join(s.ManagedDir, fmt.Sprintf("ws-%d", ws))
}

// quotaRefusal says why adding size bytes to a workspace's managed folder is refused, with its current use, or "" when it fits.
// Local mode has no quota.
func (s *Server) quotaRefusal(ws workspace.ID, size int64) string {
	if !s.multiUser() {
		return ""
	}
	wsp, err := s.MultiUser.Accounts.Workspace(ws)
	if err != nil {
		return "this workspace is not available"
	}
	var used int64
	_ = filepath.WalkDir(s.managedDirOf(ws), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				used += fi.Size()
			}
		}
		return nil
	})
	if s.Pushed != nil {
		used += s.Pushed.AssetBytes(ws) // pictures pushed from linked instances count too (ADR 0066 limits)
	}
	if used+size <= wsp.QuotaBytes {
		return ""
	}
	return fmt.Sprintf("this workspace keeps %s of managed copies and may keep %s; this copy needs %s more: delete copies it no longer needs, or ask the admin to raise the quota",
		humanBytes(used), humanBytes(wsp.QuotaBytes), humanBytes(size))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// derivedDirOf is where analysis results, masks and subsets written as files go: the derived folder in local mode, a folder
// per workspace on the group server, which is one of that workspace's readable roots and no other's.
func (s *Server) derivedDirOf(ws workspace.ID) string {
	if s.Analysis == nil {
		return ""
	}
	if !s.multiUser() || s.Analysis.DerivedDir == "" {
		return s.Analysis.DerivedDir
	}
	d := filepath.Join(s.Analysis.DerivedDir, fmt.Sprintf("ws-%d", ws))
	_ = os.MkdirAll(d, 0o700)
	return d
}
