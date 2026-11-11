package transfers

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openAt(t *testing.T, p string) (*sql.DB, *Store) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, s
}

func newT(t *testing.T, s *Store, host int64, name string) Transfer {
	t.Helper()
	tr, err := s.Create(Transfer{HostID: host, Direction: "download", SourcePath: "/r/" + name, DestDir: "/l", Name: name, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestOnlyOneCallerClaimsATransferAndTheHostSlotIsTaken(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a, b := newT(t, s, 1, "a"), newT(t, s, 2, "b")
	now := time.Now()
	if ok, err := s.Claim(a.ID, now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := s.Claim(a.ID, now); ok {
		t.Fatal("a transfer was claimed twice")
	}
	busy, _ := s.Busy()
	if !busy[1] || busy[2] {
		t.Fatalf("busy %v", busy)
	}
	due, _ := s.Due(now)
	if len(due) != 1 || due[0].ID != b.ID {
		t.Fatalf("due %v", due)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateRunning || got.Attempts != 1 || got.StartedAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestRetryWaitsUntilItsTimeAndTheFinalNameIsWrittenOnce(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a.fits")
	now := time.Now()
	s.Claim(a.ID, now)
	if err := s.Plan(a.ID, "file", 5000, "a (2).fits"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Retry(a.ID, []string{StateRunning}, now.Add(time.Minute), 3, "the connection dropped"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if due, _ := s.Due(now); len(due) != 0 {
		t.Fatalf("a waiting transfer is due before its time: %v", due)
	}
	if due, _ := s.Due(now.Add(2 * time.Minute)); len(due) != 1 {
		t.Fatalf("a waiting transfer is not due after its time")
	}
	// a second plan (the next attempt) does not rename it: the partial file is keyed by the first name
	s.Plan(a.ID, "file", 5000, "a (3).fits")
	got, _ := s.Get(a.ID)
	if got.FinalName != "a (2).fits" || got.DestPath != "/l/a (2).fits" || got.Fails != 3 || got.Kind != "file" || got.TotalBytes != 5000 {
		t.Fatalf("%+v", got)
	}
	// a stale finisher cannot move it: it is not running now
	if ok, _ := s.Move(a.ID, []string{StateRunning}, StateFailed, "late"); ok {
		t.Fatal("moved from a state it was not in")
	}
}

func TestProgressOnlyTouchesARunningTransfer(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	s.Progress(a.ID, 10, 100, 5, 18)
	if got, _ := s.Get(a.ID); got.DoneBytes != 0 {
		t.Fatalf("progress written to a queued transfer: %+v", got)
	}
	s.Claim(a.ID, time.Now())
	s.Progress(a.ID, 10, 100, 5, 18)
	got, _ := s.Get(a.ID)
	if got.DoneBytes != 10 || got.TotalBytes != 100 || got.RateBps != 5 || got.EtaSeconds != 18 {
		t.Fatalf("%+v", got)
	}
	s.Progress(a.ID, 50, 0, 6, 9) // an unknown total never erases a known one
	if got, _ = s.Get(a.ID); got.TotalBytes != 100 || got.DoneBytes != 50 {
		t.Fatalf("%+v", got)
	}
}

func TestFinishRecordsTheVerificationAndOnlyFromTheExpectedState(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	s.Claim(a.ID, time.Now())
	v := Verif{Status: Verified, Method: MethodSHA256, SourceSHA256: "aa", DestSHA256: "aa", Bytes: 7}
	if ok, _ := s.Finish(a.ID, []string{StateQueued}, StateDone, "x", v); ok {
		t.Fatal("finished from the wrong state")
	}
	if ok, err := s.Finish(a.ID, []string{StateRunning, StateVerifying}, StateDone, "copied", v); !ok || err != nil {
		t.Fatal(ok, err)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateDone || got.Verif != v || got.FinishedAt == nil || got.Detail != "copied" {
		t.Fatalf("%+v", got)
	}
	// an unverified result can be tried again; a verified one cannot
	if ok, _ := s.Requeue(a.ID, true); ok {
		t.Fatal("a verified transfer was re-queued")
	}
	b := newT(t, s, 1, "b")
	s.Claim(b.ID, time.Now())
	s.Finish(b.ID, []string{StateRunning}, StateDone, "", Verif{Status: Unverified, Method: MethodSHA256, Note: "hashes differ"})
	if ok, _ := s.Requeue(b.ID, true); !ok {
		t.Fatal("an unverified transfer could not be re-queued")
	}
	got, _ = s.Get(b.ID)
	if got.State != StateQueued || !got.Checksum || got.Verif.Status != "" || got.FinishedAt != nil {
		t.Fatalf("%+v", got)
	}
}

func TestConflictWaitsForADecision(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	if ok, _ := s.SetPolicy(a.ID, PolicyReplace); ok {
		t.Fatal("a policy was set on a transfer that is not in conflict")
	}
	s.Claim(a.ID, time.Now())
	s.Move(a.ID, []string{StateRunning}, StateConflict, "a different file is there")
	if due, _ := s.Due(time.Now()); len(due) != 0 {
		t.Fatal("a conflict is runnable")
	}
	if ok, _ := s.SetPolicy(a.ID, PolicyKeepBoth); !ok {
		t.Fatal("the decision was refused")
	}
	if got, _ := s.Get(a.ID); got.State != StateQueued || got.Policy != PolicyKeepBoth {
		t.Fatalf("%+v", got)
	}
}

// The queue is in the catalog: a second store on the same file sees it, and what was running when the app closed is queued
// again, with its partial file to resume from.
func TestTheQueueSurvivesARestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db, s := openAt(t, p)
	run, wait, done := newT(t, s, 1, "run"), newT(t, s, 1, "wait"), newT(t, s, 2, "done")
	now := time.Now()
	s.Claim(run.ID, now)
	s.Plan(run.ID, "file", 1000, "run")
	s.Progress(run.ID, 400, 1000, 1, 1)
	s.Claim(wait.ID, now)
	s.Retry(wait.ID, []string{StateRunning}, now.Add(time.Hour), 2, "dropped")
	s.Claim(done.ID, now)
	s.Finish(done.ID, []string{StateRunning}, StateDone, "", Verif{Status: Verified, Method: MethodSHA256})
	db.Close()

	_, s2 := openAt(t, p)
	n, err := s2.Recover(time.Now(), 15*time.Second)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d: %v", n, err)
	}
	got, _ := s2.Get(run.ID)
	if got.State != StateQueued || got.FinalName != "run" || got.DoneBytes != 400 || got.Detail == "" {
		t.Fatalf("%+v", got)
	}
	if w, _ := s2.Get(wait.ID); w.State != StateWaiting || w.Fails != 2 || w.NextAt == nil {
		t.Fatalf("a waiting transfer lost its schedule: %+v", w)
	}
	if d, _ := s2.Get(done.ID); d.State != StateDone || d.Verif.Status != Verified {
		t.Fatalf("%+v", d)
	}
	if busy, _ := s2.Busy(); len(busy) != 0 {
		t.Fatalf("busy after recovery: %v", busy)
	}
	list, _ := s2.List(0)
	if len(list) != 3 || list[0].ID != done.ID {
		t.Fatalf("list %d, newest first expected", len(list))
	}
}

func TestRemoveOnlyForgetsFinishedTransfers(t *testing.T) {
	_, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	if err := s.Remove(a.ID); !errors.Is(err, ErrState) {
		t.Fatalf("a queued transfer was removed: %v", err)
	}
	s.Claim(a.ID, time.Now())
	s.Finish(a.ID, []string{StateRunning}, StateCancelled, "cancelled", Verif{})
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.Remove(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestLinkDatasetSurvivesAndIsClearedWhenTheDatasetIsDeleted(t *testing.T) {
	db, s := openAt(t, filepath.Join(t.TempDir(), "c.db"))
	a := newT(t, s, 1, "a")
	s.LinkDataset(a.ID, 42)
	if got, _ := s.Get(a.ID); got.DatasetID == nil || *got.DatasetID != 42 {
		t.Fatalf("%+v", got)
	}
	// the statement the dataset delete runs (lifecycle.deleteSteps)
	if _, err := db.Exec(`UPDATE transfers SET dataset_id=NULL WHERE dataset_id=?`, 42); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(a.ID); got.DatasetID != nil {
		t.Fatalf("%+v", got)
	}
}
