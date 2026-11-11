package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	want := []time.Duration{20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 320 * time.Second, 600 * time.Second, 600 * time.Second}
	for i, w := range want {
		if got := transferBackoff(i + 1); got != w {
			t.Errorf("after %d failures: %v, want %v", i+1, got, w)
		}
	}
	if transferBackoff(0) != 20*time.Second || transferBackoff(1000) != 10*time.Minute {
		t.Errorf("edges: %v %v", transferBackoff(0), transferBackoff(1000))
	}
}

// ---- conflicts: nothing is overwritten unless the owner said replace ----

func TestAnExistingDifferentFileWaitsForADecisionAndIsNeverTouched(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "cube.fits", 5000)
	mine := e.file(e.local, "cube.fits", 7000) // different size and content
	tr := e.download("cube.fits", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateConflict || !strings.Contains(got.Detail, "Nothing was changed") {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(mine) {
		t.Fatal("the existing file was changed")
	}
	if e.sshCalls("rsync --server") != 0 {
		t.Fatal("data moved before the owner decided")
	}
	if ns := e.notifications(); len(ns) != 1 || ns[0].Kind != notify.KindTransferConflict {
		t.Fatalf("%+v", ns)
	}
	// skip: nothing is transferred and the record says so
	if code, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/resolve"), map[string]any{"policy": "skip"}); code.StatusCode != 200 {
		t.Fatal(code.StatusCode)
	}
	e.run()
	if g := e.get(tr.ID); g.State != transfers.StateSkipped || !strings.Contains(g.Detail, "left as it is") {
		t.Fatalf("%+v", g)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(mine) {
		t.Fatal("the existing file was changed by skip")
	}
	// resolving a transfer that is not waiting is refused; "ask" is not a decision
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/resolve"), map[string]any{"policy": "replace"}); r.StatusCode != 409 {
		t.Errorf("resolve on a finished transfer: %d", r.StatusCode)
	}
	again := e.download("cube.fits", nil)
	e.run()
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(again.ID))+"/resolve"), map[string]any{"policy": "ask"}); r.StatusCode != 422 {
		t.Errorf("ask as a decision: %d", r.StatusCode)
	}
}

func TestKeepBothUsesANewNameAndKeepsItAcrossRetries(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 5000)
	mine := e.file(e.local, "cube.fits", 7000)
	e.file(e.local, "cube (2).fits", 10) // taken too
	tr := e.download("cube.fits", map[string]any{"policy": "keep_both"})
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateDone || got.FinalName != "cube (3).fits" || got.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube (3).fits")); string(b) != string(want) {
		t.Fatal("kept copy differs")
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(mine) {
		t.Fatal("the original was changed")
	}
	// the chosen name is stored, so a connection loss later resumes the same file rather than choosing again
	e.file(e.remote, "big.fits", 6<<20)
	e.file(e.local, "big.fits", 1)
	b := e.download("big.fits", map[string]any{"policy": "keep_both", "bwlimitKBps": 3000})
	e.flag("DROP", true, e.local+" 1000000")
	e.pass(time.Now())
	w := e.get(b.ID)
	if w.State != transfers.StateWaiting || w.FinalName != "big (2).fits" {
		t.Fatalf("%+v", w)
	}
	e.file(e.local, "big (2).fits", 3) // someone creates the name meanwhile: the retry must neither overwrite it nor pick a name by itself
	e.pass(time.Now().Add(time.Hour))
	w = e.get(b.ID)
	if w.State != transfers.StateConflict || w.FinalName != "" || !strings.Contains(w.Detail, "left alone") {
		t.Fatalf("a name that was taken meanwhile must put the transfer back to asking: %+v", w)
	}
	if st, _ := os.Stat(filepath.Join(e.local, "big (2).fits")); st.Size() != 3 {
		t.Fatalf("a file that appeared meanwhile was overwritten (%d bytes)", st.Size())
	}
	if _, err := os.Stat(filepath.Join(e.local, hosts_PartialDir, "big (2).fits")); err == nil {
		t.Errorf("the partial file of the abandoned name was left behind")
	}
	// the owner decides again; keep both now picks the next free name
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(b.ID))+"/resolve"), map[string]any{"policy": "keep_both"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	w = e.get(b.ID)
	if w.State != transfers.StateDone || w.FinalName != "big (3).fits" || w.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", w)
	}
	if st, _ := os.Stat(filepath.Join(e.local, "big (2).fits")); st.Size() != 3 {
		t.Fatal("the file that appeared meanwhile changed")
	}
}

func TestAnIdenticalFileIsAlreadyThereAndNothingIsTransferred(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "cube.fits", 5000)
	e.file(e.local, "cube.fits", 5000) // the same bytes (same seed)
	for _, policy := range []string{"ask", "keep_both"} {
		tr := e.download("cube.fits", map[string]any{"policy": policy})
		e.run()
		got := e.get(tr.ID)
		if got.State != transfers.StateDone || got.Verif.Status != transfers.Verified || !strings.Contains(got.Detail, "already") || got.FinalName != "" {
			t.Fatalf("%s: %+v", policy, got)
		}
	}
	if e.sshCalls("rsync --server") != 0 {
		t.Fatal("an identical file was transferred")
	}
	if es, _ := os.ReadDir(e.local); len(es) != 1 {
		t.Fatalf("a copy was made: %v", es)
	}
	// the same on the host side for an upload
	e.file(e.local, "maps.fits", 4000)
	e.file(e.remote, "maps.fits", 4000)
	up := e.upload("maps.fits", nil)
	e.run()
	if g := e.get(up.ID); g.State != transfers.StateDone || !strings.Contains(g.Detail, "already on Cluster") {
		t.Fatalf("%+v", g)
	}
}

func TestReplaceOverwritesOnlyWhenTheOwnerChoseIt(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.local, "maps.fits", 4000)
	e.file(e.remote, "maps.fits", 9000)
	up := e.upload("maps.fits", nil)
	e.run()
	if g := e.get(up.ID); g.State != transfers.StateConflict {
		t.Fatalf("%+v", g)
	}
	if st, _ := os.Stat(filepath.Join(e.remote, "maps.fits")); st.Size() != 9000 {
		t.Fatal("the host file changed before the owner decided")
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(up.ID))+"/resolve"), map[string]any{"policy": "replace"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	g := e.get(up.ID)
	b, _ := os.ReadFile(filepath.Join(e.remote, "maps.fits"))
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(b) != string(want) || g.Policy != transfers.PolicyReplace {
		t.Fatalf("%+v", g)
	}
	// replacing a folder with a file is refused, whatever the policy
	e.file(filepath.Join(e.remote, "thing"), "x", 10)
	e.file(e.local, "thing", 10)
	k := e.upload("thing", map[string]any{"policy": "replace"})
	e.run()
	if g := e.get(k.ID); g.State != transfers.StateFailed || !strings.Contains(g.Detail, "will not replace one kind with another") {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(e.remote, "thing", "x")); err != nil {
		t.Fatal("the host folder was damaged")
	}
}

func TestAFileThatAppearsAfterTheCheckIsNeverOverwritten(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "cube.fits", 5<<20)
	tr := e.download("cube.fits", map[string]any{"bwlimitKBps": 2000})
	// the conflict check passes (nothing there); the connection drops; a file with the name appears before the retry
	e.flag("DROP", true, e.local+" 1000")
	e.pass(time.Now())
	if g := e.get(tr.ID); g.State != transfers.StateWaiting {
		t.Fatalf("%+v", g)
	}
	other := e.file(e.local, "cube.fits", 20)
	e.pass(time.Now().Add(time.Hour))
	g := e.get(tr.ID)
	if g.State != transfers.StateConflict || g.Verif.Status != "" {
		t.Fatalf("a changed destination must put the transfer back to asking, nothing verified: %+v", g)
	}
	// pressing Try again or Copy again cannot overwrite it either: they are not offered for a transfer that is asking
	for _, op := range []string{"retry"} {
		if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/"+op), map[string]any{}); r.StatusCode != 409 {
			t.Errorf("%s on a transfer waiting for a decision: %d", op, r.StatusCode)
		}
	}
	for i := 0; i < 3; i++ {
		e.pass(time.Now().Add(time.Duration(i+2) * time.Hour))
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(other) {
		t.Fatal("a file that appeared meanwhile was overwritten")
	}
}

// Copy again replaces the file this transfer wrote, and only that file: if something else has taken its place since, the
// transfer asks instead.
func TestCopyAgainNeverOverwritesAFileThisTransferDidNotWrite(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 5000)
	e.flag("CORRUPT", true, "")
	tr := e.download("cube.fits", nil)
	e.run()
	g := e.get(tr.ID)
	if g.Verif.Status != transfers.Unverified || g.Verif.DestSHA256 == "" {
		t.Fatalf("%+v", g)
	}
	e.flag("CORRUPT", false, "")
	// someone replaces the copy with their own file, then presses Copy again
	mine := []byte("my own notes, not a cube")
	os.WriteFile(filepath.Join(e.local, "cube.fits"), mine, 0o644)
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/retry"), map[string]any{}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	g = e.get(tr.ID)
	if g.State != transfers.StateConflict || !strings.Contains(g.Detail, "not the one this transfer wrote") {
		t.Fatalf("%+v", g)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(mine) {
		t.Fatal("Copy again overwrote a file this transfer did not write")
	}
	// when the file is still the one it wrote, Copy again does replace it and compares contents
	os.WriteFile(filepath.Join(e.local, "cube.fits"), []byte("x"), 0o644)
	e.flag("CORRUPT", true, "")
	os.Remove(filepath.Join(e.local, "cube.fits"))
	tr2 := e.download("cube.fits", map[string]any{"name": "again.fits"})
	e.run()
	e.flag("CORRUPT", false, "")
	if g := e.get(tr2.ID); g.Verif.Status != transfers.Unverified {
		t.Fatalf("%+v", g)
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr2.ID))+"/retry"), map[string]any{}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	g = e.get(tr2.ID)
	got, _ := os.ReadFile(filepath.Join(e.local, "again.fits"))
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(got) != string(want) {
		t.Fatalf("%+v", g)
	}
}

// After rsync has finished for a name, a retry (a network error while checking) only checks again: it does not run rsync a
// second time and so cannot be fooled by, or overwrite, what is there.
func TestARetryAfterTheCopyOnlyChecksAgain(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 5000)
	tr := e.download("cube.fits", nil)
	e.pass(time.Now()) // a full, verified run first, to see how many rsync runs one transfer takes
	runs := e.sshCalls("rsync --server")
	if runs != 1 || e.get(tr.ID).Verif.Status != transfers.Verified {
		t.Fatalf("%d rsync runs, %+v", runs, e.get(tr.ID))
	}
	// now the same state as a network error while checking: copied, running again
	if _, err := e.cat.DB().Exec(`UPDATE transfers SET state='queued', verif_status='', verif_method='', verif_note='' WHERE id=?`, tr.ID); err != nil {
		t.Fatal(err)
	}
	e.pass(time.Now())
	g := e.get(tr.ID)
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || e.sshCalls("rsync --server") != runs {
		t.Fatalf("rsync ran again after it had finished: %d runs, %+v", e.sshCalls("rsync --server"), g)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); string(b) != string(want) {
		t.Fatal("the file changed")
	}
}

// Replacing a folder that already holds other files merges; the content check then lists the extras instead of calling the
// result verified.
func TestAFolderReplacedIntoAnExistingFolderIsUnverifiedWhenExtraFilesWereThere(t *testing.T) {
	e := newXferEnv(t)
	e.file(filepath.Join(e.remote, "run1"), "a.fits", 3000)
	e.file(filepath.Join(e.local, "run1"), "mine.txt", 10)
	tr := e.download("run1", map[string]any{"policy": "replace"})
	e.run()
	g := e.get(tr.ID)
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Unverified || !strings.Contains(g.Verif.Note, "mine.txt") {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(e.local, "run1", "mine.txt")); err != nil {
		t.Fatal("replace deleted a file that was already there")
	}
}

// An unfinished transfer is neither a dataset nor shown in the file tree.
func TestAnUnfinishedTransferCannotBeRegisteredOrBrowsed(t *testing.T) {
	e := newXferEnv(t)
	e.file(filepath.Join(e.local, hosts_PartialDir), "cube.fits", 3000)
	r, b := postJSON(t, e.api("/api/datasets"), map[string]any{"path": filepath.Join(e.local, hosts_PartialDir, "cube.fits")})
	if r.StatusCode != 422 && r.StatusCode != 503 && r.StatusCode != 500 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if r.StatusCode == 422 && !strings.Contains(string(b), "unfinished transfer") {
		t.Fatalf("%s", b)
	}
	e.file(e.local, "ok.fits", 10)
	_, tb := get(t, e.api("/api/files/tree?path="+e.local))
	if strings.Contains(string(tb), hosts_PartialDir) || !strings.Contains(string(tb), "ok.fits") {
		t.Fatalf("%s", tb)
	}
}

// ---- verification ----

func TestAMismatchOnTheHostIsUnverifiedWithTheReasonAndCanBeCopiedAgain(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 5000)
	e.flag("CORRUPT", true, "")
	tr := e.download("cube.fits", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateDone || got.Verif.Status != transfers.Unverified || !strings.Contains(got.Verif.Note, "differs") {
		t.Fatalf("%+v", got)
	}
	if got.Verif.SourceSHA256 == got.Verif.DestSHA256 || got.Verif.SourceSHA256 == "" {
		t.Fatalf("both hashes must be recorded: %+v", got.Verif)
	}
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindTransferFailed || !strings.Contains(ns[0].Title, "NOT verified") {
		t.Fatalf("an unverified copy must not be announced as success: %+v", ns)
	}
	// it cannot be registered as a dataset
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/register"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("registered an unverified copy: %d", r.StatusCode)
	}
	// after the host is fixed, Copy again compares contents and ends verified
	e.flag("CORRUPT", false, "")
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/retry"), map[string]any{}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	g := e.get(tr.ID)
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || g.Verif.DestSHA256 != sha(want) {
		t.Fatalf("%+v", g)
	}
}

func TestAHostWithoutAHashToolIsUnverifiedNotVerified(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "cube.fits", 5000)
	e.flag("NOSHA", true, "")
	tr := e.download("cube.fits", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateDone || got.Verif.Status != transfers.Unverified || !strings.Contains(got.Verif.Note, "no sha256sum or shasum") || !strings.Contains(got.Verif.Note, "sizes are equal") {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits")); len(b) != 5000 {
		t.Fatal("the file did not arrive")
	}
}

// ---- retries, drops, restarts ----

func TestAnUnreachableHostIsRetriedWithGrowingWaitsAndThenSucceeds(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 5000)
	e.flag("DOWN", true, "")
	tr := e.download("cube.fits", nil)
	t0 := time.Now()
	e.pass(t0)
	g := e.get(tr.ID)
	if g.State != transfers.StateWaiting || g.Fails != 1 || g.NextAt == nil || !strings.Contains(g.Detail, "Trying again in 20s") {
		t.Fatalf("%+v", g)
	}
	calls := e.sshLines()
	e.pass(t0.Add(5 * time.Second)) // too early: the host is not called
	if e.get(tr.ID).Fails != 1 || e.sshLines() != calls {
		t.Fatalf("a retry before its time: %+v", e.get(tr.ID))
	}
	e.pass(g.NextAt.Add(time.Second))
	g = e.get(tr.ID)
	if g.Fails != 2 || !strings.Contains(g.Detail, "40s") {
		t.Fatalf("the wait must double: %+v", g)
	}
	e.flag("DOWN", false, "")
	e.pass(g.NextAt.Add(time.Second))
	g = e.get(tr.ID)
	b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits"))
	if g.State != transfers.StateDone || g.Fails != 0 || string(b) != string(want) || g.Attempts != 3 {
		t.Fatalf("%+v", g)
	}
	if ns := e.notifications(); len(ns) != 1 {
		t.Fatalf("a retry must not announce: %+v", ns)
	}
}

func TestTheQueueGivesUpAfterManyAttemptsWithoutAnyData(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "cube.fits", 5000)
	e.flag("DOWN", true, "")
	tr := e.download("cube.fits", nil)
	now := time.Now()
	for i := 0; i < maxTransferFails+2; i++ {
		e.pass(now)
		g := e.get(tr.ID)
		if g.State == transfers.StateFailed {
			if !strings.Contains(g.Detail, "gave up") || i != maxTransferFails {
				t.Fatalf("gave up at pass %d: %+v", i, g)
			}
			if ns := e.notifications(); len(ns) != 1 || ns[0].Kind != notify.KindTransferFailed {
				t.Fatalf("%+v", ns)
			}
			return
		}
		if g.NextAt != nil {
			now = g.NextAt.Add(time.Second)
		}
	}
	t.Fatal("never gave up")
}

// The connection drops in the middle of a file: the bytes so far wait in the partial folder, the queue retries, rsync resumes
// from them, and the result is verified by hash. Both directions.
func TestAConnectionDropMidFileIsResumedAndVerified(t *testing.T) {
	for _, dir := range []string{"download", "upload"} {
		t.Run(dir, func(t *testing.T) {
			e := newXferEnv(t)
			var tr transfers.Transfer
			var want []byte
			dest := e.local
			if dir == "download" {
				want = e.file(e.remote, "cube.fits", 12<<20)
				tr = e.download("cube.fits", map[string]any{"bwlimitKBps": 6000})
			} else {
				want = e.file(e.local, "cube.fits", 12<<20)
				tr = e.upload("cube.fits", map[string]any{"bwlimitKBps": 6000})
				dest = e.remote
			}
			e.flag("DROP", true, dest+" 1500000")
			e.pass(time.Now())
			g := e.get(tr.ID)
			if g.State != transfers.StateWaiting || !strings.Contains(g.Detail, "resuming from the partial file") {
				t.Fatalf("%+v", g)
			}
			if _, err := os.Stat(filepath.Join(dest, "cube.fits")); err == nil {
				t.Fatal("a half file sits under the final name")
			}
			part, err := os.Stat(filepath.Join(dest, hosts_PartialDir, "cube.fits"))
			if err != nil || part.Size() == 0 || part.Size() >= int64(len(want)) {
				t.Fatalf("no partial file: %v %v", part, err)
			}
			e.pass(g.NextAt.Add(time.Second))
			g = e.get(tr.ID)
			b, _ := os.ReadFile(filepath.Join(dest, "cube.fits"))
			if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(b) != string(want) || g.Verif.DestSHA256 != sha(want) || g.Attempts != 2 {
				t.Fatalf("%+v", g)
			}
			if _, err := os.Stat(filepath.Join(dest, hosts_PartialDir, "cube.fits")); err == nil {
				t.Error("the partial file outlived the transfer")
			}
			if g.DoneBytes != int64(len(want)) {
				t.Errorf("progress ended at %d of %d", g.DoneBytes, len(want))
			}
		})
	}
}

const hosts_PartialDir = ".vesqira-partial"

// Closing the app stops rsync with SIGTERM (the partial file stays) and puts the transfer back in the queue; the next start
// finishes it from the partial file.
func TestAClosedAppResumesTheTransferAtTheNextStart(t *testing.T) {
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 16<<20)
	tr := e.download("cube.fits", map[string]any{"bwlimitKBps": 6000})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.srv.RunTransfers(ctx); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		g := e.get(tr.ID)
		if g.State == transfers.StateRunning && g.DoneBytes > 1<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never got going: %+v", g)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	g := e.get(tr.ID)
	if g.State != transfers.StateQueued || !strings.Contains(g.Detail, "closed") {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(e.local, "cube.fits")); err == nil {
		t.Fatal("a half file under the final name")
	}
	part, err := os.Stat(filepath.Join(e.local, hosts_PartialDir, "cube.fits"))
	if err != nil || part.Size() == 0 {
		t.Fatalf("SIGTERM must keep the partial file: %v %v", part, err)
	}
	if ns := e.notifications(); len(ns) != 0 {
		t.Fatalf("a shutdown is not announced: %+v", ns)
	}
	// a crash instead of a clean stop leaves the row "running"; the next start queues it again
	e.store.Claim(tr.ID, time.Now())
	if g := e.get(tr.ID); g.State != transfers.StateRunning {
		t.Fatal("setup")
	}
	e.srv.xfer = transferRunner{} // a new process: nothing in memory
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	e.mgr.Close()
	n, _ := e.store.Recover(time.Now(), 0)
	if n != 1 {
		t.Fatalf("recovered %d", n)
	}
	for i := 0; i < 400 && e.get(tr.ID).State != transfers.StateDone; i++ {
		e.srv.transferOnce(ctx2, time.Now())
		e.srv.waitTransfers()
	}
	g = e.get(tr.ID)
	b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits"))
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(b) != string(want) {
		t.Fatalf("%+v", g)
	}
	if g.Attempts < 3 {
		t.Errorf("attempts %d: the transfer should have been resumed, not restarted", g.Attempts)
	}
}

// ---- cancelling ----

func TestCancelStopsARunningTransferAndCanDiscardItsPartialFile(t *testing.T) {
	for _, discard := range []bool{false, true} {
		e := newXferEnv(t)
		e.file(e.remote, "cube.fits", 16<<20)
		tr := e.download("cube.fits", map[string]any{"bwlimitKBps": 4000})
		ctx, cancel := context.WithCancel(context.Background())
		e.srv.transferOnce(ctx, time.Now())
		deadline := time.Now().Add(20 * time.Second)
		for e.get(tr.ID).DoneBytes < 1<<20 {
			if time.Now().After(deadline) {
				t.Fatal("never got going")
			}
			time.Sleep(20 * time.Millisecond)
		}
		r, b := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/cancel"), map[string]any{"discardPartial": discard})
		if r.StatusCode != 202 {
			t.Fatalf("%d %s", r.StatusCode, b)
		}
		e.srv.waitTransfers()
		cancel()
		g := e.get(tr.ID)
		if g.State != transfers.StateCancelled || g.FinishedAt == nil {
			t.Fatalf("%+v", g)
		}
		if _, err := os.Stat(filepath.Join(e.local, "cube.fits")); err == nil {
			t.Fatal("a cancelled transfer left a file under the final name")
		}
		_, perr := os.Stat(filepath.Join(e.local, hosts_PartialDir, "cube.fits"))
		if discard == (perr == nil) {
			t.Fatalf("discard=%v but partial present=%v", discard, perr == nil)
		}
		if len(e.notifications()) != 0 {
			t.Fatalf("a cancel is not announced")
		}
		if !discard { // Try again resumes the kept partial file
			if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/retry"), map[string]any{}); r.StatusCode != 200 {
				t.Fatal(r.StatusCode)
			}
			if g := e.get(tr.ID); g.State != transfers.StateQueued {
				t.Fatalf("%+v", g)
			}
		}
	}
}

func TestCancelAQueuedAWaitingAndAConflictedTransferAndRemoveFinishedOnes(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "a.fits", 100)
	e.file(e.remote, "b.fits", 100)
	e.file(e.local, "b.fits", 5) // conflicts
	a := e.download("a.fits", nil)
	r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(a.ID))+"/cancel"), map[string]any{})
	if r.StatusCode != 200 || e.get(a.ID).State != transfers.StateCancelled {
		t.Fatalf("%d %+v", r.StatusCode, e.get(a.ID))
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(a.ID))+"/cancel"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("cancelling a cancelled transfer: %d", r.StatusCode)
	}
	b := e.download("b.fits", nil)
	e.run()
	if e.get(b.ID).State != transfers.StateConflict {
		t.Fatal("setup")
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(b.ID))+"/cancel"), map[string]any{}); r.StatusCode != 200 || e.get(b.ID).State != transfers.StateCancelled {
		t.Fatalf("%d", r.StatusCode)
	}
	if st, _ := os.Stat(filepath.Join(e.local, "b.fits")); st.Size() != 5 {
		t.Fatal("the existing file changed")
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(a.ID))+"/remove"), map[string]any{}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if _, err := e.store.Get(a.ID); err == nil {
		t.Fatal("not removed")
	}
	live := e.download("a.fits", nil)
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(live.ID))+"/remove"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("removed a live transfer: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, e.api("/api/transfers/999/cancel"), map[string]any{}); r.StatusCode != 404 {
		t.Errorf("%d", r.StatusCode)
	}
}
