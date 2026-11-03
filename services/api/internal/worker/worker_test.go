package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func sh(script string, d time.Duration) Runner {
	return Runner{Command: []string{"sh", "-c", script, "sh"}, Timeout: d}
}

func TestSuccessReturnsJSON(t *testing.T) {
	got, err := sh(`echo '{"ok":true,"x":1}'`, 2*time.Second).Header(context.Background(), "/p")
	if err != nil || !strings.Contains(string(got), `"x":1`) {
		t.Fatalf("%s %v", got, err)
	}
}

func TestTimeoutKillsWorker(t *testing.T) {
	start := time.Now()
	_, err := sh(`sleep 5`, 150*time.Millisecond).Header(context.Background(), "/p")
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestOutputLimit(t *testing.T) {
	_, err := sh(`yes x | head -c 3000000`, 5*time.Second).Header(context.Background(), "/p")
	if err == nil {
		t.Fatal("expected error for oversized output")
	}
}

func TestStderrAndGarbageAreNotForwarded(t *testing.T) {
	_, err := sh(`echo secret-path-/etc/passwd >&2; echo not json`, 2*time.Second).Header(context.Background(), "/p")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkerReportedErrorIsForwardedShort(t *testing.T) {
	_, err := sh(`echo '{"ok":false,"error":"not a FITS primary header"}'; exit 2`, 2*time.Second).Header(context.Background(), "/p")
	if err == nil || !strings.Contains(err.Error(), "not a FITS primary header") {
		t.Fatalf("err=%v", err)
	}
}
