package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The watcher's backoff state shows as "not reachable"; a successful VPN connect makes it ask again at once, inside the
// backoff, and a success brings the state back.
func TestWatcherRetriesAtOnceAfterAVPNConnectAndReportsReachability(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	j := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(j.SlurmID, "RUNNING")
	w := newJobWatcher(MinWatchInterval)
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	base := e.attempts()
	t0 := time.Now()
	e.srv.watchOnce(ctx, w, t0)
	e.srv.watchOnce(ctx, w, t0.Add(2*MinWatchInterval+time.Second))
	if e.attempts()-base != 2 {
		t.Fatalf("setup: %d attempts", e.attempts()-base)
	}
	if st, _ := e.srv.reach.get(e.host.ID); st != "no" {
		t.Fatalf("reachability while the watcher backs off: %q", st)
	}
	inside := t0.Add(2*MinWatchInterval + 2*time.Second)
	e.srv.watchOnce(ctx, w, inside)
	if e.attempts()-base != 2 {
		t.Fatal("asked again inside the backoff without a kick")
	}
	os.Remove(filepath.Join(e.home, "DOWN"))
	e.srv.reach.kick(e.host.ID)
	e.srv.watchOnce(ctx, w, inside)
	if e.attempts()-base <= 2 {
		t.Fatalf("no retry after the kick: %d attempts", e.attempts()-base)
	}
	if st, _ := e.srv.reach.get(e.host.ID); st != "yes" || w.host(e.host.ID).fails != 0 {
		t.Fatalf("after the retry: %q, %d failures", st, w.host(e.host.ID).fails)
	}
}
