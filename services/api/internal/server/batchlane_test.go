package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// One running batch item per account, ten waiting, the next refused at once with the count; another account is not held up.
func TestBatchLaneIsPerAccountAndBounded(t *testing.T) {
	var l batchLane
	leave, err := l.enter(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan error, batchWaiting)
	for i := 0; i < batchWaiting; i++ {
		go func() { _, err := l.enter(ctx, "a"); started <- err }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		n := l.accs["a"].waiting
		l.mu.Unlock()
		if n == batchWaiting || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var full errBatchFull
	if _, err := l.enter(ctx, "a"); !errors.As(err, &full) || full.waiting != batchWaiting {
		t.Fatalf("the twelfth item of one account: %v", err)
	}
	other, err := l.enter(context.Background(), "b")
	if err != nil {
		t.Fatalf("another account waited behind the first: %v", err)
	}
	other()
	leave()
	if err := <-started; err != nil { // one waiter gets the slot
		t.Fatal(err)
	}
	cancel()
}

func TestManagedQuotaShowsTheCurrentUse(t *testing.T) {
	g, _ := scopedGroup(t)
	g.setupAdmin("ada")
	if err := g.acc.SetQuota(workspace.Local, 1000); err != nil {
		t.Fatal(err)
	}
	dir := g.s.managedDirOf(workspace.Local)
	if !strings.HasSuffix(dir, "ws-1") {
		t.Fatalf("managed folder of workspace 1: %s", dir)
	}
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "a.fits"), make([]byte, 900), 0o444)
	if msg := g.s.quotaRefusal(workspace.Local, 50); msg != "" {
		t.Fatalf("a copy that fits was refused: %s", msg)
	}
	msg := g.s.quotaRefusal(workspace.Local, 200)
	if !strings.Contains(msg, "900 B") || !strings.Contains(msg, "1000 B") {
		t.Fatalf("the refusal does not show the use: %q", msg)
	}
}
