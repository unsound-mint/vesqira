package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// Transfers between this computer and a host (ADR 0051): a persistent queue, one transfer per host at a time, retried with
// backoff and resumed from the partial file, verified by sha256 on both ends. The routes exist only on the owner's loopback
// listener; the reviewer handler has no transfer route.

const (
	transferTick        = 3 * time.Second
	transferBackoffBase = 20 * time.Second
	transferBackoffMax  = 10 * time.Minute
	// maxTransferFails is how many attempts in a row may fail without moving a single byte before the queue gives up
	// (about five hours at the longest backoff): a VPN that is down for an evening must not fail a night's transfer.
	maxTransferFails = 30
	probeFresh       = 30 * time.Minute
	progressEvery    = time.Second
	// recoverWait is how long a transfer that a crash left running waits before it is started again. An orphaned rsync dies on
	// its own within a second or two, because its progress output goes to a pipe nobody reads any more (SIGPIPE).
	recoverWait = 15 * time.Second
)

// transferBackoff is the wait after fails failures in a row: the base doubled once per failure, up to ten minutes. The first
// success of any kind resets the count.
func transferBackoff(fails int) time.Duration {
	d := transferBackoffBase
	for i := 1; i < fails && d < transferBackoffMax; i++ {
		d *= 2
	}
	if d > transferBackoffMax {
		d = transferBackoffMax
	}
	return d
}

type probeEntry struct {
	info hosts.HostRsync
	at   time.Time
}

// transferRunner holds what only the running process knows: the cancel function of each running transfer and what the
// hosts' rsync looked like recently.
type transferRunner struct {
	mu       sync.Mutex
	cancels  map[int64]context.CancelFunc
	userStop map[int64]stopRequest
	probes   map[int64]probeEntry
	local    *hosts.RsyncInfo
	localAt  time.Time
	wg       sync.WaitGroup
}

type stopRequest struct{ discard bool }

// ---- the routes ----

type transferRequest struct {
	Direction   string `json:"direction"`
	HostID      int64  `json:"hostId"`
	SourcePath  string `json:"sourcePath"`
	DestDir     string `json:"destDir"`
	Name        string `json:"name"`
	Policy      string `json:"policy"`
	BwlimitKBps int    `json:"bwlimitKBps"`
	Compress    *bool  `json:"compress"`
}

func (s *Server) transferHost(w http.ResponseWriter, id int64) (hosts.Host, bool) {
	h, err := s.HostDB.Get(id)
	if err != nil {
		fail(w, 404, "host not found")
		return h, false
	}
	return h, true
}

func validPolicy(p string, allowAsk bool) bool {
	switch p {
	case transfers.PolicySkip, transfers.PolicyKeepBoth, transfers.PolicyReplace:
		return true
	case transfers.PolicyAsk:
		return allowAsk
	}
	return false
}

// dirSize adds up the regular files below a local folder, stopping after maxEntries so a huge tree cannot stall the request.
func dirSize(root string, maxEntries int) int64 {
	var total int64
	n := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > maxEntries {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (s *Server) createTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferRequest
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	h, ok := s.transferHost(w, in.HostID)
	if !ok {
		return
	}
	if in.Policy == "" {
		in.Policy = transfers.PolicyAsk
	}
	if !validPolicy(in.Policy, true) {
		fail(w, 422, "policy must be ask, skip, keep_both or replace")
		return
	}
	if in.BwlimitKBps < 0 || in.BwlimitKBps > 10_000_000 {
		fail(w, 422, "the bandwidth limit must be between 0 and 10000000 KiB/s (0 means none)")
		return
	}
	compress := in.Compress == nil || *in.Compress
	if _, err := s.localRsync(r.Context()); err != nil {
		fail(w, 422, err.Error())
		return
	}
	if path.IsAbs(in.DestDir) {
		in.DestDir = path.Clean(in.DestDir) // a trailing slash is not a different folder
	}
	if path.IsAbs(in.SourcePath) {
		in.SourcePath = path.Clean(in.SourcePath)
	}
	t := transfers.Transfer{HostID: h.ID, Direction: in.Direction, DestDir: in.DestDir, Policy: in.Policy, BwlimitKBps: in.BwlimitKBps, Compress: compress}
	switch in.Direction {
	case hosts.Download:
		if err := h.CheckRemoteSource(in.SourcePath); err != nil {
			fail(w, 422, err.Error())
			return
		}
		real, err := s.roots().ResolveDir(in.DestDir)
		if err != nil {
			fail(w, 403, "the destination must be an existing folder inside the source roots on this computer")
			return
		}
		t.SourcePath, t.DestDir = in.SourcePath, real
		t.Name = in.Name
		if t.Name == "" {
			t.Name = path.Base(in.SourcePath)
		}
	case hosts.Upload:
		real, err := s.roots().Resolve(in.SourcePath)
		kind := transfers.KindFile
		if err != nil {
			if real, err = s.roots().ResolveDir(in.SourcePath); err != nil {
				fail(w, 403, "only a file or folder inside the source roots on this computer can be uploaded")
				return
			}
			kind = transfers.KindDir
		}
		if err := h.CheckRemoteDest(in.DestDir); err != nil {
			fail(w, 422, err.Error())
			return
		}
		t.SourcePath, t.Kind = real, kind
		t.Name = in.Name
		if t.Name == "" {
			t.Name = filepath.Base(real)
		}
		if kind == transfers.KindFile {
			if st, err := os.Stat(real); err == nil {
				t.TotalBytes = st.Size()
			}
		} else {
			t.TotalBytes = dirSize(real, 200_000)
		}
		t.Warning = s.transferWarning(h, in.DestDir, t.TotalBytes)
	default:
		fail(w, 422, "direction must be download or upload")
		return
	}
	if err := hosts.CheckName(t.Name); err != nil {
		fail(w, 422, err.Error())
		return
	}
	out, err := s.Transfers.Create(t)
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("transfer queued", "transfer", out.ID, fmt.Sprintf("%s %s: %s -> %s", out.Direction, h.Name, out.SourcePath, out.DestDir))
	writeJSON(w, 201, map[string]any{"transfer": out})
}

func (s *Server) listTransfers(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Transfers.List(0)
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"transfers": ts})
}

func (s *Server) transferFrom(w http.ResponseWriter, r *http.Request) (transfers.Transfer, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return transfers.Transfer{}, false
	}
	t, err := s.Transfers.Get(id)
	if errors.Is(err, transfers.ErrNotFound) {
		fail(w, 404, "transfer not found")
		return t, false
	}
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return t, false
	}
	return t, true
}

func (s *Server) cancelTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transferFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		DiscardPartial bool `json:"discardPartial"`
	}
	if r.ContentLength != 0 && !body(w, r, 1<<10, &in, true) {
		return
	}
	if transfers.Terminal(t.State) {
		fail(w, 409, "this transfer has already finished")
		return
	}
	if t.State == transfers.StateRunning || t.State == transfers.StateVerifying {
		s.xfer.mu.Lock()
		cancel := s.xfer.cancels[t.ID]
		if cancel != nil {
			s.xfer.userStop[t.ID] = stopRequest{discard: in.DiscardPartial}
		}
		s.xfer.mu.Unlock()
		if cancel != nil {
			cancel() // the runner records the cancellation when rsync has stopped
			out, _ := s.Transfers.Get(t.ID)
			writeJSON(w, 202, map[string]any{"transfer": out})
			return
		}
	}
	won, err := s.Transfers.Finish(t.ID, []string{transfers.StateQueued, transfers.StateWaiting, transfers.StateConflict, transfers.StateRunning, transfers.StateVerifying},
		transfers.StateCancelled, "cancelled by you", transfers.Verif{})
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if won && in.DiscardPartial {
		s.discardLocalPartial(t)
	}
	s.Audit.Record("transfer cancelled", "transfer", t.ID, t.SourcePath)
	out, _ := s.Transfers.Get(t.ID)
	writeJSON(w, 200, map[string]any{"transfer": out})
}

// discardLocalPartial deletes the unfinished file of a cancelled download. A partial file on a host is never deleted by
// Vesqira (it would be a delete on the researcher's account that the ADR does not authorise); it stays in PartialDir there.
func (s *Server) discardLocalPartial(t transfers.Transfer) {
	if t.Direction != hosts.Download || t.Kind == transfers.KindDir || t.FinalName == "" {
		return
	}
	dir, err := s.roots().ResolveDir(t.DestDir)
	if err != nil {
		return
	}
	pd := filepath.Join(dir, hosts.PartialDir)
	if st, err := os.Lstat(filepath.Join(pd, t.FinalName)); err == nil && st.Mode().IsRegular() {
		_ = os.Remove(filepath.Join(pd, t.FinalName))
		_ = os.Remove(pd) // only succeeds when it is empty
	}
}

func (s *Server) retryTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transferFrom(w, r)
	if !ok {
		return
	}
	// Copying again after a mismatch compares contents (a same-size, same-time file would otherwise look finished) and may
	// replace the copy this transfer itself made: that is what the owner asked for by pressing it.
	won, err := s.Transfers.Requeue(t.ID, t.Verif.Status == transfers.Unverified)
	if errors.Is(err, transfers.ErrNameTaken) {
		fail(w, 409, "another transfer to the same folder is now writing "+tName(t)+"; wait for it or remove one of them")
		return
	}
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if !won {
		fail(w, 409, "only a failed or cancelled transfer, or one that could not be verified, can be tried again")
		return
	}
	s.Audit.Record("transfer retried", "transfer", t.ID, t.SourcePath)
	out, _ := s.Transfers.Get(t.ID)
	writeJSON(w, 200, map[string]any{"transfer": out})
}

func (s *Server) resolveTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transferFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Policy string `json:"policy"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if !validPolicy(in.Policy, false) {
		fail(w, 422, "choose skip, keep_both or replace")
		return
	}
	won, err := s.Transfers.SetPolicy(t.ID, in.Policy)
	if err != nil {
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if !won {
		fail(w, 409, "this transfer is not waiting for a decision")
		return
	}
	s.Audit.Record("transfer conflict decided", "transfer", t.ID, in.Policy+": "+t.DestDir+"/"+t.Name)
	out, _ := s.Transfers.Get(t.ID)
	writeJSON(w, 200, map[string]any{"transfer": out})
}

func (s *Server) removeTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transferFrom(w, r)
	if !ok {
		return
	}
	if err := s.Transfers.Remove(t.ID); err != nil {
		if errors.Is(err, transfers.ErrState) {
			fail(w, 409, "only a finished transfer can be removed from the list; cancel it first")
			return
		}
		log.Printf("transfers: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]bool{"removed": true})
}

// transferWarning compares an upload's size with the host's last storage look (ADR 0045), if there is one: no call is made to
// the host. It only warns; the host's own quota is what decides. "" means nothing to say.
func (s *Server) transferWarning(h hosts.Host, destDir string, size int64) string {
	if size <= 0 {
		return ""
	}
	s.disk.mu.Lock()
	e := s.disk.look[h.ID]
	var rep *hosts.StorageReport
	if e != nil {
		rep = e.rep
	}
	s.disk.mu.Unlock()
	if rep == nil {
		return ""
	}
	when := rep.CheckedAt.Format("15:04")
	var best *hosts.Filesystem
	for i := range rep.Filesystems {
		f := &rep.Filesystems[i]
		if f.Error == "" && (f.Root == destDir || strings.HasPrefix(destDir, strings.TrimRight(f.Root, "/")+"/")) && (best == nil || len(f.Root) > len(best.Root)) {
			best = f
		}
	}
	if best != nil && size > best.AvailBytes {
		return fmt.Sprintf("This is %s and the file system holding the destination reports %s available (df, looked at %s). The upload will probably fail.", fmtBytes(size), fmtBytes(best.AvailBytes), when)
	}
	if rep.Quota != nil {
		for _, q := range rep.Quota.Entries {
			switch {
			case q.HardBytes != nil && q.UsedBytes+size > *q.HardBytes:
				return fmt.Sprintf("This is %s; your quota on %s is %s used of a hard limit of %s (%s, looked at %s). The upload would pass the hard limit.", fmtBytes(size), q.Scope, fmtBytes(q.UsedBytes), fmtBytes(*q.HardBytes), rep.Quota.Tool, when)
			case q.SoftBytes != nil && q.UsedBytes+size > *q.SoftBytes:
				return fmt.Sprintf("This is %s; your quota on %s is %s used of a soft limit of %s (%s, looked at %s). The upload would pass the soft limit.", fmtBytes(size), q.Scope, fmtBytes(q.UsedBytes), fmtBytes(*q.SoftBytes), rep.Quota.Tool, when)
			}
		}
	}
	return ""
}

func fmtBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if f < k || u == "TiB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= k
	}
	return ""
}

// ---- rsync on the two ends, remembered for a while ----

func (s *Server) localRsync(ctx context.Context) (hosts.RsyncInfo, error) {
	x := &s.xfer
	x.mu.Lock()
	if x.local != nil && time.Since(x.localAt) < probeFresh {
		info := *x.local
		x.mu.Unlock()
		return info, nil
	}
	x.mu.Unlock()
	info, err := s.Remote.LocalRsync(ctx)
	if err != nil {
		return info, err
	}
	x.mu.Lock()
	x.local, x.localAt = &info, time.Now()
	x.mu.Unlock()
	return info, nil
}

func (s *Server) hostRsync(ctx context.Context, h hosts.Host) (hosts.HostRsync, error) {
	x := &s.xfer
	x.mu.Lock()
	if e, ok := x.probes[h.ID]; ok && time.Since(e.at) < probeFresh {
		x.mu.Unlock()
		return e.info, nil
	}
	x.mu.Unlock()
	info, err := s.Remote.HostRsyncProbe(ctx, h)
	if err != nil {
		return info, err
	}
	x.mu.Lock()
	if x.probes == nil {
		x.probes = map[int64]probeEntry{}
	}
	x.probes[h.ID] = probeEntry{info, time.Now()}
	x.mu.Unlock()
	return info, nil
}

// ---- the queue ----

// RunTransfers starts the queue: transfers that were running when the app last closed are queued again, then a pass is
// made every few seconds until ctx ends. When ctx ends, running transfers are stopped with SIGTERM (their partial files
// stay) and put back in the queue.
func (s *Server) RunTransfers(ctx context.Context) {
	if s.Transfers == nil || s.HostDB == nil || s.Remote == nil {
		return
	}
	if n, err := s.Transfers.Recover(time.Now(), recoverWait); err != nil {
		log.Printf("transfers: recover: %v", err)
	} else if n > 0 {
		log.Printf("transfers: %d transfer(s) interrupted by the last shutdown are queued again", n)
	}
	t := time.NewTicker(transferTick)
	defer t.Stop()
	for {
		s.transferOnce(ctx, time.Now())
		select {
		case <-ctx.Done():
			s.waitTransfers()
			return
		case <-t.C:
		}
	}
}

// waitTransfers blocks until the transfers started so far have stopped (tests, shutdown).
func (s *Server) waitTransfers() { s.xfer.wg.Wait() }

// transferOnce is one pass at time now: every runnable transfer whose host is free is started, at most one per host.
func (s *Server) transferOnce(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	due, err := s.Transfers.Due(now)
	if err != nil {
		log.Printf("transfers: %v", err)
		return
	}
	busy, err := s.Transfers.Busy()
	if err != nil {
		log.Printf("transfers: %v", err)
		return
	}
	for _, t := range due {
		if busy[t.HostID] {
			continue
		}
		if won, err := s.Transfers.Claim(t.ID, now); err != nil || !won {
			continue
		}
		busy[t.HostID] = true
		rctx, cancel := context.WithCancel(ctx)
		x := &s.xfer
		x.mu.Lock()
		if x.cancels == nil {
			x.cancels, x.userStop = map[int64]context.CancelFunc{}, map[int64]stopRequest{}
		}
		x.cancels[t.ID] = cancel
		x.mu.Unlock()
		x.wg.Add(1)
		go func(id int64) {
			defer x.wg.Done()
			defer func() {
				cancel()
				x.mu.Lock()
				delete(x.cancels, id)
				delete(x.userStop, id)
				x.mu.Unlock()
			}()
			s.runTransfer(rctx, ctx, id)
		}(t.ID)
	}
}

// outcome is how one attempt ended.
type outcome struct {
	state  string // done | failed | skipped | conflict | retry | cancelled | interrupted
	detail string
	verif  transfers.Verif
}

func failed(format string, a ...any) outcome {
	return outcome{state: transfers.StateFailed, detail: fmt.Sprintf(format, a...)}
}

// classify turns an error from a host call into an outcome: a refusal repeats and fails the transfer, anything else is a
// lost connection and is retried.
func classify(err error) outcome {
	if errors.Is(err, hosts.ErrTransferRefused) {
		return outcome{state: transfers.StateFailed, detail: err.Error()}
	}
	return outcome{state: "retry", detail: err.Error()}
}

func (s *Server) runTransfer(ctx, parent context.Context, id int64) {
	t, err := s.Transfers.Get(id)
	if err != nil {
		return
	}
	h, herr := s.HostDB.Get(t.HostID)
	var out outcome
	if herr != nil {
		out = failed("the host was removed")
	} else {
		out = s.attempt(ctx, t, h)
	}
	if ctx.Err() != nil && out.state != transfers.StateDone && out.state != transfers.StateFailed {
		s.xfer.mu.Lock()
		stop, asked := s.xfer.userStop[id]
		s.xfer.mu.Unlock()
		switch {
		case asked:
			out = outcome{state: transfers.StateCancelled, detail: "cancelled by you; the partial file is kept, Try again resumes it"}
			if stop.discard {
				out.detail = "cancelled by you; the partial file was deleted"
				if cur, err := s.Transfers.Get(id); err == nil { // the name chosen by the first attempt
					s.discardLocalPartial(cur)
				}
			}
		case parent.Err() != nil:
			out = outcome{state: "interrupted"}
		default:
			out = outcome{state: transfers.StateCancelled, detail: "cancelled"}
		}
	}
	s.settle(t, h, out)
}

// settle writes an attempt's outcome and, once, tells the owner.
func (s *Server) settle(t transfers.Transfer, h hosts.Host, out outcome) {
	live := []string{transfers.StateRunning, transfers.StateVerifying}
	cur, _ := s.Transfers.Get(t.ID)
	switch out.state {
	case "interrupted":
		_, _ = s.Transfers.Move(t.ID, live, transfers.StateQueued, "Vesqira was closed during this transfer; it resumes from the partial file")
	case "retry":
		fails := cur.Fails + 1
		if cur.DoneBytes > t.DoneBytes { // bytes moved this time: the connection is working, start the backoff over
			fails = 1
		}
		if fails > maxTransferFails {
			s.settle(t, h, failed("gave up after %d attempts in a row moved no data: %s", maxTransferFails, out.detail))
			return
		}
		wait := transferBackoff(fails)
		_, _ = s.Transfers.Retry(t.ID, live, time.Now().Add(wait), fails, fmt.Sprintf("%s. Trying again in %s, resuming from the partial file.", strings.TrimRight(out.detail, ". "), wait.Round(time.Second)))
	case transfers.StateConflict:
		if ok, _ := s.Transfers.Move(t.ID, live, transfers.StateConflict, out.detail); ok {
			s.notifyTransfer(t, h, notify.KindTransferConflict, "needs a decision: "+tName(t), out.detail)
		}
	case transfers.StateCancelled:
		if ok, _ := s.Transfers.Finish(t.ID, live, transfers.StateCancelled, out.detail, transfers.Verif{}); ok {
			s.Audit.Record("transfer cancelled", "transfer", t.ID, t.SourcePath)
		}
	default: // done, failed, skipped
		if ok, _ := s.Transfers.Finish(t.ID, live, out.state, out.detail, out.verif); !ok {
			return
		}
		s.Audit.Record("transfer "+out.state, "transfer", t.ID, fmt.Sprintf("%s: %s", h.Name, out.detail))
		switch {
		case out.state == transfers.StateFailed:
			s.notifyTransfer(t, h, notify.KindTransferFailed, "failed: "+tName(t), out.detail)
		case out.state == transfers.StateDone && out.verif.Status == transfers.Unverified:
			s.notifyTransfer(t, h, notify.KindTransferFailed, "arrived but NOT verified: "+tName(t), out.verif.Note)
		case out.state == transfers.StateDone:
			if done, err := s.Transfers.Get(t.ID); err == nil { // the kind and the final name are settled by the attempt
				s.linkDownloadedLocation(context.Background(), done, out.verif)
			}
			s.notifyTransfer(t, h, notify.KindTransferDone, "finished and verified: "+tName(t), out.detail)
		}
	}
}

func tName(t transfers.Transfer) string {
	if t.FinalName != "" {
		return t.FinalName
	}
	return t.Name
}

func (s *Server) notifyTransfer(t transfers.Transfer, h hosts.Host, kind, title, body string) {
	if s.Notes == nil {
		return
	}
	way := "from " + h.Name
	if t.Direction == hosts.Upload {
		way = "to " + h.Name
	}
	key := fmt.Sprintf("transfer:%d:%s", t.ID, kind)
	if kind == notify.KindTransferConflict {
		key += fmt.Sprintf(":%d", time.Now().UnixNano()) // each conflict is a new decision
	}
	if _, err := s.Notes.Add(key, notify.Notification{Kind: kind, Title: "Transfer " + way + " " + title, Body: body}); err != nil {
		log.Printf("transfers: notification: %v", err)
	}
}

// ---- one attempt ----

// end is what one side of a transfer looks like.
type end struct {
	kind  string
	size  int64
	real  string
	sha   string
	mtime int64 // seconds; 0 when not known
}

// kindReserved is a name that no file holds yet but another live transfer to the same folder has reserved.
const kindReserved = "reserved"

// identity is what a replace noted about the entry it was going to overwrite. A retry that finds the same entry there knows
// nobody has touched the name since; any other entry means the name was lost.
func (e end) identity() string { return fmt.Sprintf("%s:%d:%d", e.kind, e.size, e.mtime) }

func (s *Server) localEnd(p string) end {
	st, err := os.Lstat(p)
	switch {
	case err != nil:
		return end{kind: hosts.KindAbsent}
	case st.Mode()&os.ModeSymlink != 0:
		return end{kind: hosts.KindLink}
	case st.IsDir():
		return end{kind: hosts.KindDir, real: p, mtime: st.ModTime().Unix()}
	case st.Mode().IsRegular():
		return end{kind: hosts.KindFile, size: st.Size(), real: p, mtime: st.ModTime().Unix()}
	}
	return end{kind: hosts.KindOther}
}

func fromRemote(st hosts.RemoteStat) end {
	return end{kind: st.Kind, size: st.Size, real: st.Real, sha: st.SHA, mtime: st.MTime}
}

// nameWithSuffix is "cube.fits" -> "cube (2).fits"; a folder or a name without an extension gets the suffix at the end.
func nameWithSuffix(name string, n int) string {
	ext := path.Ext(name)
	if ext == name { // ".hidden"
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}

func (s *Server) attempt(ctx context.Context, t transfers.Transfer, h hosts.Host) outcome {
	if _, err := s.localRsync(ctx); err != nil {
		return classify(err)
	}
	hr, err := s.hostRsync(ctx, h)
	if err != nil {
		return classify(err)
	}
	download := t.Direction == hosts.Download

	// the folder the data goes into, resolved on its own side and re-authorised now: roots and links may have changed
	var destReal string
	if download {
		if destReal, err = s.roots().ResolveDir(t.DestDir); err != nil {
			return failed("the destination folder is no longer an existing folder inside the source roots on this computer")
		}
	} else {
		st, err := s.Remote.StatPath(ctx, h, t.DestDir, "", false)
		if err != nil {
			return classify(err)
		}
		if st.Kind != hosts.KindDir {
			return failed("the destination folder on %s does not exist", h.Name)
		}
		destReal = st.Real
	}

	// the source
	var src end
	if download {
		st, err := s.Remote.StatPath(ctx, h, path.Dir(t.SourcePath), path.Base(t.SourcePath), false)
		if err != nil {
			return classify(err)
		}
		src = fromRemote(st)
	} else {
		if p, err := s.roots().Resolve(t.SourcePath); err == nil {
			src = s.localEnd(p)
		} else if p, err := s.roots().ResolveDir(t.SourcePath); err == nil {
			src = s.localEnd(p)
		} else {
			return failed("the source is no longer a file or folder inside the source roots on this computer")
		}
	}
	switch src.kind {
	case hosts.KindFile, hosts.KindDir:
	case hosts.KindAbsent:
		return failed("the source no longer exists")
	case hosts.KindLink:
		return failed("the source is a symbolic link; Vesqira does not follow links, name the file it points to")
	default:
		return failed("the source is neither a regular file nor a folder")
	}

	final := t.FinalName
	replaced := ""
	skipCopy := false
	if final == "" { // the first attempt: the conflict check, once; its answer is stored so a retry resumes the same file
		o, name, was := s.resolveConflict(ctx, t, h, destReal, src)
		if o != nil {
			return *o
		}
		final, replaced = name, was
	} else {
		// A retry. The partial file is in PartialDir, so nothing may be at the chosen name unless this transfer put it there.
		switch {
		case t.Copied && !t.Checksum: // rsync already finished for this name; only the check is left
			skipCopy = true
		case src.kind == hosts.KindFile && t.Copied && t.Checksum:
			// Copy again replaces only the file this transfer itself wrote and hashed, never one that took its place since.
			cur, err := s.lookDest(ctx, t, h, destReal, final, true)
			if err != nil {
				return classify(err)
			}
			if cur.kind != hosts.KindFile || t.WrittenSHA == "" || cur.sha != t.WrittenSHA {
				return s.nameLost(t, final, "the file there is not the one this transfer wrote (it changed since, or its content could not be hashed)")
			}
		case src.kind == hosts.KindFile && !t.Copied:
			cur, err := s.lookDest(ctx, t, h, destReal, final, false)
			if err != nil {
				return classify(err)
			}
			switch {
			case cur.kind == hosts.KindAbsent:
			case t.Policy == transfers.PolicyReplace && final == t.Name && t.Replaced != "" && cur.identity() == t.Replaced:
				// the file this replace was going to overwrite, unchanged since the first check: the partial file is resumed
			default:
				return s.nameLost(t, final, "something else was put there after this transfer was queued")
			}
		}
	}
	total := int64(0)
	if src.kind == hosts.KindFile {
		total = src.size
	}
	spec := hosts.RsyncSpec{Direction: t.Direction, Dir: src.kind == hosts.KindDir, BwlimitKBps: t.BwlimitKBps, Compress: t.Compress,
		IgnoreExisting: t.Policy != transfers.PolicyReplace && !t.Checksum, Checksum: t.Checksum, HostRsyncPath: hr.Path}
	if download {
		spec.Local, spec.Remote = filepath.Join(destReal, final), src.real
	} else {
		spec.Local, spec.Remote = src.real, path.Join(destReal, final)
	}
	if !skipCopy {
		if !download {
			if err := s.Remote.CheckUploadTarget(ctx, h, t.DestDir, final); err != nil {
				return classify(err)
			}
		}
		if src.kind == hosts.KindDir && t.Policy == transfers.PolicyReplace {
			if o := s.typeConflicts(ctx, t, h, spec, destReal, final); o != nil {
				return *o
			}
		}
	}
	if err := s.Transfers.Plan(t.ID, src.kind, total, final, replaced); err != nil {
		if errors.Is(err, transfers.ErrNameTaken) {
			return outcome{state: transfers.StateConflict, detail: fmt.Sprintf("%s was just taken by another transfer to the same folder. Nothing was changed; choose what to do.", final)}
		}
		return failed("could not record the transfer: %v", err)
	}

	// room on this computer, counting what an earlier attempt already holds
	if download && total > 0 {
		have := int64(0)
		if st, err := os.Stat(filepath.Join(destReal, hosts.PartialDir, final)); err == nil {
			have = st.Size()
		}
		if free, err := hosts.FreeBytes(destReal); err == nil && free < total-have {
			return failed("not enough free space on this computer: %s needed, %s available. Free some space and press Try again; the partial file is kept.", fmtBytes(total-have), fmtBytes(free))
		}
	}

	if !skipCopy {
		argv, err := s.Remote.RsyncArgv(h, spec)
		if err != nil {
			return classify(err)
		}
		var lastSaved time.Time
		res, err := s.Remote.RunRsync(ctx, argv, func(p hosts.Progress) {
			if time.Since(lastSaved) < progressEvery && p.Percent < 100 {
				return
			}
			lastSaved = time.Now()
			tot := total
			if tot == 0 && p.Percent > 0 {
				tot = p.Bytes * 100 / int64(p.Percent)
			}
			done := p.Bytes
			if tot > 0 && done > tot { // after a resume rsync may count the matched bytes again; never show more than the whole
				done = tot
			}
			_ = s.Transfers.Progress(t.ID, done, tot, p.RateBps, p.EtaSec)
		})
		if err != nil {
			return classify(err)
		}
		if ctx.Err() != nil {
			return outcome{state: transfers.StateCancelled}
		}
		if res.ExitCode != 0 {
			msg := fmt.Sprintf("rsync exited with code %d", res.ExitCode)
			if res.ExitCode == -1 {
				msg = "rsync was stopped by a signal"
			}
			if res.Stderr != "" {
				msg += ": " + clipText(res.Stderr, 300)
			}
			if hosts.RsyncExitRetryable(res.ExitCode) {
				return outcome{state: "retry", detail: msg}
			}
			if res.ExitCode == 11 {
				msg += " (the disk may be full; free some space and press Try again, the partial file is kept)"
			}
			return failed("%s", msg)
		}
	}
	if ok, _ := s.Transfers.Verifying(t.ID); !ok {
		return outcome{state: transfers.StateCancelled}
	}
	return s.verify(ctx, t, h, spec, src, destReal, final)
}

// folderDifferencesNote says in words what a folder's verifying dry run found. Entries the copy skipped on purpose (links that
// point outside the folder, special files) come first: nothing was transferred for them, so the folder is not a full copy.
func folderDifferencesNote(d hosts.Differences) string {
	names := func(a []string) string {
		shown := a[:min(len(a), 5)]
		out := strings.Join(shown, ", ")
		if len(a) > len(shown) {
			out += fmt.Sprintf(" and %d more", len(a)-len(shown))
		}
		return clipText(out, 300)
	}
	var parts []string
	if len(d.Links) > 0 {
		parts = append(parts, fmt.Sprintf("%d symbolic link(s) in the source were not copied, because a link that points outside the folder is never copied (or is missing in the copy): %s", len(d.Links), names(d.Links)))
	}
	if len(d.Specials) > 0 {
		parts = append(parts, fmt.Sprintf("%d special file(s) (device, pipe or socket) in the source were not copied: %s", len(d.Specials), names(d.Specials)))
	}
	if len(d.Other) > 0 {
		shown := strings.Join(d.Other[:min(len(d.Other), 5)], "; ")
		parts = append(parts, fmt.Sprintf("rsync's content comparison found %d other difference(s): %s", len(d.Other), clipText(shown, 300)))
	}
	return strings.Join(parts, ". ")
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// lookDest looks at a name in the destination folder, on whichever side that is, hashing a file when asked. On the host the
// folder is named the way the owner named it (a root may be a symbolic link there); the host resolves it again each time.
func (s *Server) lookDest(ctx context.Context, t transfers.Transfer, h hosts.Host, destReal, name string, hash bool) (end, error) {
	if t.Direction == hosts.Download {
		e := s.localEnd(filepath.Join(destReal, name))
		if hash && e.kind == hosts.KindFile {
			sum, _, err := hosts.HashFile(ctx, e.real)
			if err != nil {
				return e, err
			}
			e.sha = sum
		}
		return e, nil
	}
	st, err := s.Remote.StatPath(ctx, h, t.DestDir, name, hash)
	return fromRemote(st), err
}

// typeConflicts stops a folder replace that would delete something the owner has. rsync turns an entry of the other type into
// the source's by deleting it first: a symbolic link called sub at the destination, where the source has a folder sub, is removed
// and replaced by a real folder (and a file by a folder, a link by a file). A dry run (no --delete, so it lists only what the copy
// would create) names every entry that is new to rsync; one of those that already exists at the destination is exactly such a
// clash. It returns nil when there is none, otherwise the outcome that asks the owner, with nothing changed.
func (s *Server) typeConflicts(ctx context.Context, t transfers.Transfer, h hosts.Host, spec hosts.RsyncSpec, destReal, final string) *outcome {
	download := t.Direction == hosts.Download
	cur, err := s.lookDest(ctx, t, h, destReal, final, false)
	if err != nil {
		o := classify(err)
		return &o
	}
	if cur.kind != hosts.KindDir {
		return nil // nothing there yet (or the kind check refuses it elsewhere): there is nothing to delete
	}
	spec.Preflight = true
	argv, err := s.Remote.RsyncArgv(h, spec)
	if err != nil {
		o := classify(err)
		return &o
	}
	res, err := s.Remote.RunRsync(ctx, argv, nil)
	if err != nil {
		o := classify(err)
		return &o
	}
	switch {
	case ctx.Err() != nil:
		return &outcome{state: transfers.StateCancelled}
	case res.ExitCode != 0 && hosts.RsyncExitRetryable(res.ExitCode):
		return &outcome{state: "retry", detail: "the connection was lost while looking at the destination folder: " + clipText(res.Stderr, 200)}
	case res.ExitCode != 0:
		o := failed("looking at the destination folder before replacing it failed (rsync exit %d): %s. Nothing was changed.", res.ExitCode, clipText(res.Stderr, 300))
		return &o
	}
	rels := hosts.NewEntries(res.Listing)
	var exist []string
	if download {
		for _, r := range rels {
			if !filepath.IsLocal(r) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(destReal, final, r)); err == nil {
				exist = append(exist, r)
			}
		}
	} else if len(rels) > 0 {
		if exist, err = s.Remote.ExistingAmong(ctx, h, t.DestDir, final, rels); err != nil {
			o := classify(err)
			return &o
		}
	}
	// report the outermost entries only: a clash at sub makes everything below it one too
	var top []string
	for _, r := range exist {
		nested := false
		for _, o := range exist {
			if o != r && strings.HasPrefix(r, o+"/") {
				nested = true
				break
			}
		}
		if !nested {
			top = append(top, r)
		}
	}
	if len(top) == 0 {
		return nil
	}
	_ = s.Transfers.ClearFinalName(t.ID)
	shown := top[:min(len(top), 5)]
	more := ""
	if len(top) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(top)-len(shown))
	}
	where := "on this computer"
	if !download {
		where = "on " + h.Name
	}
	return &outcome{state: transfers.StateConflict, detail: fmt.Sprintf("Replacing %s would delete %d entr%s %s that %s of another type than the one in the source (a link, file or folder where the source has a different one): %s%s. Nothing was changed. Move or remove them yourself and choose Replace again, or choose Keep both.",
		final, len(top), map[bool]string{true: "y", false: "ies"}[len(top) == 1], where, map[bool]string{true: "is", false: "are"}[len(top) == 1], strings.Join(shown, ", "), more)}
}

// nameLost answers a retry that finds the destination changed under it: the transfer goes back to asking, nothing at the
// destination is touched, and a partial file of a download that was only going to be resumed under the abandoned name is deleted.
func (s *Server) nameLost(t transfers.Transfer, name, why string) outcome {
	if !t.Copied {
		s.discardLocalPartial(t)
	}
	_ = s.Transfers.ClearFinalName(t.ID)
	return outcome{state: transfers.StateConflict, detail: fmt.Sprintf("%s is now at the destination and %s. It was left alone and nothing was changed; choose what to do.", name, why)}
}

// resolveConflict looks at the name at the destination and applies the transfer's policy. It returns a finished outcome
// (conflict, skipped, already there, failed, retry) or nil with the name to use. Nothing at the destination is touched.
func (s *Server) resolveConflict(ctx context.Context, t transfers.Transfer, h hosts.Host, destReal string, src end) (*outcome, string, string) {
	download := t.Direction == hosts.Download
	// A name counts as taken when the folder holds it or another live transfer to the same side and folder has reserved it: that
	// transfer's file sits in the partial folder until it ends, so the folder alone would say the name is free.
	look := func(name string, hash bool) (end, error) {
		e, err := s.lookDest(ctx, t, h, destReal, name, hash)
		if err != nil || e.kind != hosts.KindAbsent || hash {
			return e, err
		}
		held, err := s.Transfers.NameHeld(t, name)
		if err == nil && held {
			return end{kind: kindReserved}, nil
		}
		return e, err
	}
	ret := func(o outcome) (*outcome, string, string) { return &o, "", "" }
	existing, err := look(t.Name, false)
	if err != nil {
		o := classify(err)
		return &o, "", ""
	}
	if existing.kind == hosts.KindAbsent {
		return nil, t.Name, ""
	}
	where := "on this computer"
	if !download {
		where = "on " + h.Name
	}
	// a file that is already there and identical needs nothing, whatever the policy but an explicit replace
	if src.kind == hosts.KindFile && existing.kind == hosts.KindFile && existing.size == src.size && t.Policy != transfers.PolicyReplace && t.Policy != transfers.PolicySkip {
		var a, b end
		var aerr, berr error
		if download {
			var st hosts.RemoteStat
			st, aerr = s.Remote.StatPath(ctx, h, path.Dir(t.SourcePath), path.Base(t.SourcePath), true)
			a = fromRemote(st)
			b, berr = look(t.Name, true)
		} else {
			var sum string
			sum, _, aerr = hosts.HashFile(ctx, src.real)
			a = end{sha: sum}
			b, berr = look(t.Name, true)
		}
		for _, e := range []error{aerr, berr} {
			if e != nil {
				o := classify(e)
				return &o, "", ""
			}
		}
		if a.sha != "" && a.sha == b.sha {
			return ret(outcome{state: transfers.StateDone, detail: fmt.Sprintf("A file with this name, size and SHA-256 is already %s; nothing was transferred.", where),
				verif: transfers.Verif{Status: transfers.Verified, Method: transfers.MethodSHA256, SourceSHA256: a.sha, DestSHA256: b.sha, Bytes: src.size, SourceMTime: a.mtime,
					Note: "The existing file and the source have the same SHA-256 (both hashed just now)."}})
		}
	}
	what := "a " + existing.kind
	if existing.kind == hosts.KindFile {
		what = fmt.Sprintf("a file of %s", fmtBytes(existing.size))
	}
	if existing.kind == kindReserved {
		what = "a name that another transfer in the queue is writing there"
		if t.Policy != transfers.PolicySkip && t.Policy != transfers.PolicyKeepBoth {
			return ret(outcome{state: transfers.StateConflict, detail: fmt.Sprintf("%s is being written %s by another transfer that has not finished. Nothing was changed; wait for it, or choose Keep both or Skip.", t.Name, where)})
		}
	}
	switch t.Policy {
	case transfers.PolicySkip:
		return ret(outcome{state: transfers.StateSkipped, detail: fmt.Sprintf("%s is already %s (%s); left as it is and nothing was transferred.", t.Name, where, what)})
	case transfers.PolicyKeepBoth:
		for n := 2; n < 100; n++ {
			name := nameWithSuffix(t.Name, n)
			e, err := look(name, false)
			if err != nil {
				o := classify(err)
				return &o, "", ""
			}
			if e.kind == hosts.KindAbsent {
				return nil, name, ""
			}
		}
		return ret(failed("there are already 98 copies named like %s; remove some or pick another name", t.Name))
	case transfers.PolicyReplace:
		if existing.kind != src.kind {
			return ret(failed("%s is already %s and is %s, while the source is a %s; Vesqira will not replace one kind with another", t.Name, where, what, src.kind))
		}
		return nil, t.Name, existing.identity()
	}
	return ret(outcome{state: transfers.StateConflict, detail: fmt.Sprintf("%s is already %s (%s) and differs from the source (%s). Nothing was changed; choose what to do.",
		t.Name, where, what, sourceWords(src))})
}

func sourceWords(e end) string {
	if e.kind == hosts.KindFile {
		return fmt.Sprintf("a file of %s", fmtBytes(e.size))
	}
	return "a folder"
}

// verify checks the arrived copy against the source and records the result. A file is hashed (sha256) on both ends, after the
// transfer, so a source that changed meanwhile shows as a mismatch; a folder is compared by rsync's checksums in a dry run.
// A connection lost while checking is a retry (not "unverified"); a transfer is unverified only for a reason the owner can read.
func (s *Server) verify(ctx context.Context, t transfers.Transfer, h hosts.Host, spec hosts.RsyncSpec, src end, destReal, final string) outcome {
	download := t.Direction == hosts.Download
	done := func(v transfers.Verif, what string) outcome {
		return outcome{state: transfers.StateDone, detail: what, verif: v}
	}
	if src.kind == hosts.KindDir {
		spec.VerifyOnly = true
		argv, err := s.Remote.RsyncArgv(h, spec)
		if err != nil {
			return classify(err)
		}
		res, err := s.Remote.RunRsync(ctx, argv, nil)
		if err != nil {
			return classify(err)
		}
		switch {
		case ctx.Err() != nil:
			return outcome{state: transfers.StateCancelled}
		case res.ExitCode != 0 && hosts.RsyncExitRetryable(res.ExitCode):
			return outcome{state: "retry", detail: "the connection was lost while checking the copy: " + clipText(res.Stderr, 200)}
		case res.ExitCode != 0:
			return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodChecksum, Note: "the check could not be completed: " + clipText(res.Stderr, 300)}, "The folder arrived but could not be checked.")
		case strings.TrimSpace(res.Listing) != "":
			return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodChecksum, Note: folderDifferencesNote(hosts.ParseDifferences(res.Listing))}, "The folder arrived but differs from the source.")
		}
		return done(transfers.Verif{Status: transfers.Verified, Method: transfers.MethodChecksum,
			Note: "Every file in the folder was compared by content with rsync's checksum in a dry run (rsync's checksum, not SHA-256), and the file lists are equal."},
			"Folder transferred; every file compared by content.")
	}

	// a file: size and sha256 on both ends
	var srcSum, dstSum string
	var srcSize, dstSize, srcMTime int64
	if download {
		st, err := s.Remote.StatPath(ctx, h, path.Dir(t.SourcePath), path.Base(t.SourcePath), true)
		if err != nil {
			if errors.Is(err, hosts.ErrTransferRefused) {
				return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodSHA256, Note: "the source could not be looked at again after the transfer: " + err.Error()}, "The file arrived but could not be checked.")
			}
			return classify(err)
		}
		srcSum, srcSize, srcMTime = st.SHA, st.Size, st.MTime
		sum, n, err := hosts.HashFile(ctx, filepath.Join(destReal, final))
		if err != nil {
			if ctx.Err() != nil {
				return outcome{state: transfers.StateCancelled}
			}
			return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodSHA256, Note: "the copy on this computer could not be read: " + err.Error()}, "The file arrived but could not be checked.")
		}
		dstSum, dstSize = sum, n
	} else {
		sum, n, err := hosts.HashFile(ctx, src.real)
		if err != nil {
			if ctx.Err() != nil {
				return outcome{state: transfers.StateCancelled}
			}
			return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodSHA256, Note: "the source on this computer could not be read again: " + err.Error()}, "The file arrived but could not be checked.")
		}
		srcSum, srcSize = sum, n
		st, err := s.Remote.StatPath(ctx, h, t.DestDir, final, true)
		if err != nil {
			if errors.Is(err, hosts.ErrTransferRefused) {
				return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodSHA256, Note: "the copy on the host could not be looked at: " + err.Error()}, "The file arrived but could not be checked.")
			}
			return classify(err)
		}
		if st.Kind != hosts.KindFile {
			return done(transfers.Verif{Status: transfers.Unverified, Method: transfers.MethodSHA256, Note: "there is no regular file at the destination on the host after the transfer"}, "The file did not arrive as a file.")
		}
		dstSum, dstSize = st.SHA, st.Size
	}
	v := transfers.Verif{Method: transfers.MethodSHA256, SourceSHA256: srcSum, DestSHA256: dstSum, Bytes: dstSize, SourceMTime: srcMTime}
	switch {
	case srcSum == "" || dstSum == "":
		v.Status = transfers.Unverified
		equal := "equal"
		if srcSize != dstSize {
			equal = "different"
		}
		v.Note = fmt.Sprintf("a SHA-256 could not be computed on %s (no sha256sum or shasum there); the sizes are %s (source %d bytes, copy %d bytes)", h.Name, equal, srcSize, dstSize)
		return done(v, "The file arrived; its content could not be compared.")
	case srcSize != dstSize:
		v.Status = transfers.Unverified
		v.Note = fmt.Sprintf("the sizes differ: source %d bytes, copy %d bytes", srcSize, dstSize)
		return done(v, "The file arrived with a different size than the source.")
	case srcSum != dstSum:
		v.Status = transfers.Unverified
		v.Note = "the SHA-256 of the copy differs from the source's (the source may have changed during the transfer). Use Copy again to compare contents and replace the copy."
		return done(v, "The file arrived but its content differs from the source.")
	}
	v.Status = transfers.Verified
	v.Note = fmt.Sprintf("The source and the copy have the same size (%d bytes) and the same SHA-256, each computed on its own machine after the transfer.", dstSize)
	return done(v, "Transferred and verified.")
}
