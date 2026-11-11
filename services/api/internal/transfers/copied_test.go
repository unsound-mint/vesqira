package transfers

import (
	"path/filepath"
	"testing"
	"time"
)

// What decides whether a retry may touch the destination: whether rsync finished for the chosen name, and the hash of what
// it wrote.
func TestCopiedAndWrittenHashDecideWhatARetryMayTouch(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	now := time.Now()
	s.Claim(a.ID, now)
	s.Plan(a.ID, "file", 100, "a (2)")
	if got, _ := s.Get(a.ID); got.Copied || got.WrittenSHA != "" {
		t.Fatalf("%+v", got)
	}
	if ok, _ := s.Verifying(a.ID); !ok {
		t.Fatal("not verifying")
	}
	if ok, _ := s.Verifying(a.ID); ok {
		t.Fatal("verifying twice")
	}
	s.Finish(a.ID, []string{StateVerifying}, StateDone, "", Verif{Status: Unverified, Method: MethodSHA256, DestSHA256: "bb", SourceSHA256: "aa"})
	got, _ := s.Get(a.ID)
	if !got.Copied || got.WrittenSHA != "bb" {
		t.Fatalf("%+v", got)
	}
	// copy again keeps both, and sets the flag; the attempt that finishes spends the flag
	if ok, _ := s.Requeue(a.ID, true); !ok {
		t.Fatal("requeue")
	}
	got, _ = s.Get(a.ID)
	if !got.Copied || got.WrittenSHA != "bb" || !got.Checksum || got.Verif.Status != "" {
		t.Fatalf("%+v", got)
	}
	s.Claim(a.ID, now)
	s.Verifying(a.ID)
	if got, _ = s.Get(a.ID); got.Checksum || !got.Copied {
		t.Fatalf("%+v", got)
	}
	// a hash that could not be computed never erases the one that was
	s.Finish(a.ID, []string{StateVerifying}, StateDone, "", Verif{Status: Unverified, Method: MethodSHA256})
	if got, _ = s.Get(a.ID); got.WrittenSHA != "bb" {
		t.Fatalf("%+v", got)
	}
	// forgetting the name forgets what it owned
	if err := s.ClearFinalName(a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(a.ID)
	if got.FinalName != "" || got.Copied || got.WrittenSHA != "" || got.DestPath != "/l/a" {
		t.Fatalf("%+v", got)
	}
}

func TestRecoveredTransfersWaitBeforeTheyStartAgain(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	now := time.Now()
	s.Claim(a.ID, now)
	if n, _ := s.Recover(now, 15*time.Second); n != 1 {
		t.Fatalf("recovered %d", n)
	}
	if due, _ := s.Due(now.Add(5 * time.Second)); len(due) != 0 {
		t.Fatal("started again before the wait")
	}
	if due, _ := s.Due(now.Add(20 * time.Second)); len(due) != 1 {
		t.Fatal("not started after the wait")
	}
}
