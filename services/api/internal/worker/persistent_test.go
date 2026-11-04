package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A tiny stand-in worker speaking the same line protocol. The path field selects its behaviour.
const fake = `
import sys, json, time
for line in sys.stdin.buffer:
    r = json.loads(line)
    m = r.get("path")
    if m == "hang":
        time.sleep(60)
    if m == "crash":
        sys.exit(3)
    if m == "huge":
        sys.stdout.write('{"ok":true,"nbytes":999999999999}\n'); sys.stdout.flush(); continue
    if m == "garbage":
        sys.stdout.write('not json\n'); sys.stdout.flush(); continue
    if m == "refuse":
        sys.stdout.write('{"ok":false,"code":"bad_request","error":"channel out of range","nbytes":0}\n'); sys.stdout.flush(); continue
    if m == "big":
        sys.stdout.buffer.write(b'{"ok":true,"marker":"intact","nbytes":300000}\n' + b"z" * 300000); sys.stdout.flush(); continue
    if m == "slow":
        time.sleep(0.3)
    sys.stdout.buffer.write(b'{"ok":true,"nbytes":4}\n' + b"abcd"); sys.stdout.flush()
`

func newFake(t *testing.T, timeout time.Duration) *Persistent {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join(t.TempDir(), "fake.py")
	if err := os.WriteFile(script, []byte(fake), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Persistent{Command: []string{"python3", "-P", script}, Timeout: timeout}
	t.Cleanup(p.Close)
	return p
}

func do(p *Persistent, mode string) (Reply, error) {
	return p.Do(context.Background(), map[string]any{"op": "plane", "path": mode})
}

func TestOneProcessServesManyRequests(t *testing.T) {
	p := newFake(t, 5*time.Second)
	for i := 0; i < 5; i++ {
		r, err := do(p, "ok")
		if err != nil || string(r.Payload) != "abcd" {
			t.Fatalf("%v %q", err, r.Payload)
		}
	}
	if p.Starts.Load() != 1 {
		t.Fatalf("started %d processes for 5 requests, want 1", p.Starts.Load())
	}
}

func TestTimeoutKillsAndNextRequestRestarts(t *testing.T) {
	p := newFake(t, 300*time.Millisecond)
	start := time.Now()
	if _, err := do(p, "hang"); !errors.Is(err, ErrTimeout) || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	if _, err := do(p, "ok"); err != nil {
		t.Fatalf("after timeout: %v", err)
	}
	if p.Starts.Load() != 2 {
		t.Fatalf("starts=%d, want 2", p.Starts.Load())
	}
}

func TestBrokenRepliesNeverReuseTheProcess(t *testing.T) {
	for _, mode := range []string{"crash", "huge", "garbage"} {
		p := newFake(t, 5*time.Second)
		if _, err := do(p, mode); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err=%v", mode, err)
		}
		if _, err := do(p, "ok"); err != nil || p.Starts.Load() != 2 {
			t.Errorf("%s: follow-up err=%v starts=%d", mode, err, p.Starts.Load())
		}
	}
}

func TestWorkerRefusalKeepsTheProcessAndCode(t *testing.T) {
	p := newFake(t, 5*time.Second)
	_, err := do(p, "refuse")
	var re *RequestError
	if !errors.As(err, &re) || re.Code != "bad_request" || !strings.Contains(re.Message, "out of range") {
		t.Fatalf("%v", err)
	}
	if _, err := do(p, "ok"); err != nil || p.Starts.Load() != 1 {
		t.Fatalf("refusal must not restart the worker: %v starts=%d", err, p.Starts.Load())
	}
}

func TestCancelledWhileQueuedDoesNotKillTheRunningRequest(t *testing.T) {
	p := newFake(t, 5*time.Second)
	var wg sync.WaitGroup
	wg.Add(1)
	var first error
	go func() { defer wg.Done(); _, first = do(p, "slow") }()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Do(ctx, map[string]any{"op": "plane", "path": "ok"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued call: %v", err)
	}
	wg.Wait()
	if first != nil || p.Starts.Load() != 1 {
		t.Fatalf("running request disturbed: err=%v starts=%d", first, p.Starts.Load())
	}
}

func TestMissingInterpreterIsUnavailable(t *testing.T) {
	p := &Persistent{Command: []string{"/nonexistent/python"}, Timeout: time.Second}
	if _, err := do(p, "ok"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestHeaderSurvivesALargePayload(t *testing.T) {
	p := newFake(t, 5*time.Second)
	r, err := do(p, "big")
	if err != nil || len(r.Payload) != 300000 || !strings.Contains(string(r.Head), `"marker":"intact"`) {
		t.Fatalf("head=%q payload=%d err=%v", r.Head, len(r.Payload), err)
	}
}
