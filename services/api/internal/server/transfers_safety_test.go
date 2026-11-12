package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// secondHost adds a second host with its own folder, reached through the same stand-in ssh.
func (e *xferEnv) secondHost() (hosts.Host, string) {
	e.t.Helper()
	remote2 := filepath.Join(e.base, "remote2")
	if err := os.MkdirAll(remote2, 0o755); err != nil {
		e.t.Fatal(err)
	}
	h, err := e.srv.HostDB.Create(hosts.Host{Name: "Cluster2", Dest: "login2", Roots: []string{remote2}})
	if err != nil {
		e.t.Fatal(err)
	}
	return h, remote2
}

// ---- a folder is never "verified" when the copy skipped something ----

func TestAFolderWithASkippedLinkOrPipeIsNeverVerified(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"download", "upload"} {
		t.Run(dir, func(t *testing.T) {
			e := newXferEnv(t)
			src, other := e.remote, e.local
			if dir == "upload" {
				src, other = e.local, e.remote
			}
			outside := filepath.Join(e.base, "outside.txt")
			os.WriteFile(outside, []byte("secret"), 0o644)
			run := filepath.Join(src, "run")
			e.file(run, "a.fits", 3000)
			os.Symlink("a.fits", filepath.Join(run, "inside")) // stays inside the folder: copied as a link, fine
			os.Symlink(outside, filepath.Join(run, "unsafe"))  // points outside: rsync --safe-links drops it
			if err := syscall.Mkfifo(filepath.Join(run, "pipe"), 0o644); err != nil {
				t.Fatal(err)
			}
			var tr transfers.Transfer
			if dir == "download" {
				tr = e.download("run", nil)
			} else {
				tr = e.upload("run", nil)
			}
			e.run()
			g := e.get(tr.ID)
			if g.State != transfers.StateDone || g.Verif.Status != transfers.Unverified {
				t.Fatalf("a folder whose entries were skipped must not be verified: %+v", g)
			}
			if !strings.Contains(g.Verif.Note, "unsafe") || !strings.Contains(g.Verif.Note, "pipe") || strings.Contains(g.Verif.Note, "inside") {
				t.Errorf("the note must name what was skipped and only that: %q", g.Verif.Note)
			}
			if _, err := os.Lstat(filepath.Join(other, "run", "unsafe")); err == nil {
				t.Error("a link pointing outside the folder was copied")
			}
			if _, err := os.Lstat(filepath.Join(other, "run", "inside")); err != nil {
				t.Error("a link that stays inside the folder was not copied")
			}
		})
	}
}

func TestAFolderWithOnlyInsideLinksIsStillVerified(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	run := filepath.Join(e.remote, "run")
	e.file(run, "a.fits", 3000)
	os.Symlink("a.fits", filepath.Join(run, "inside"))
	tr := e.download("run", nil)
	e.run()
	if g := e.get(tr.ID); g.State != transfers.StateDone || g.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", g)
	}
}

// ---- a name belongs to one live transfer per side and folder ----

func TestTwoTransfersFromDifferentHostsCannotBothWriteTheSameFinalName(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	h2, remote2 := e.secondHost()
	e.file(e.remote, "big.fits", 6<<20)
	other := e.file(remote2, "big.fits", 3000)
	a := e.download("big.fits", map[string]any{"bwlimitKBps": 3000})
	e.flag("DROP", true, e.local+" 1000000")
	e.pass(time.Now())
	ga := e.get(a.ID)
	if ga.State != transfers.StateWaiting || ga.FinalName != "big.fits" {
		t.Fatalf("A must hold its name while it waits: %+v", ga)
	}
	if _, err := os.Stat(filepath.Join(e.local, "big.fits")); err == nil {
		t.Fatal("the name must still be absent from the folder (the file is in the partial folder)")
	}
	// B, from another host, wants the same name: the folder looks empty, but the name is taken
	b := e.download("big.fits", map[string]any{"hostId": h2.ID, "sourcePath": filepath.Join(remote2, "big.fits")})
	e.pass(time.Now())
	gb := e.get(b.ID)
	if gb.State != transfers.StateConflict || !strings.Contains(gb.Detail, "another transfer") {
		t.Fatalf("B must wait for a decision: %+v", gb)
	}
	if _, err := os.Stat(filepath.Join(e.local, "big.fits")); err == nil {
		t.Fatal("B wrote the name that A holds")
	}
	// Replace cannot take it either; Keep both picks the next free name
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(b.ID))+"/resolve"), map[string]any{"policy": "replace"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.pass(time.Now())
	if g := e.get(b.ID); g.State != transfers.StateConflict {
		t.Fatalf("replace over a held name: %+v", g)
	}
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(b.ID))+"/resolve"), map[string]any{"policy": "keep_both"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.pass(time.Now())
	gb = e.get(b.ID)
	if gb.State != transfers.StateDone || gb.FinalName != "big (2).fits" || gb.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", gb)
	}
	if got, _ := os.ReadFile(filepath.Join(e.local, "big (2).fits")); string(got) != string(other) {
		t.Fatal("the kept copy differs")
	}
	// A resumes and still owns big.fits
	e.pass(time.Now().Add(time.Hour))
	if ga = e.get(a.ID); ga.State != transfers.StateDone || ga.Verif.Status != transfers.Verified || ga.FinalName != "big.fits" {
		t.Fatalf("%+v", ga)
	}
}

// ---- a replace that was interrupted resumes ----

func TestAnInterruptedReplaceResumesFromThePartialFile(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	want := e.file(e.remote, "big.fits", 6<<20)
	old := e.file(e.local, "big.fits", 100)
	tr := e.download("big.fits", map[string]any{"policy": "replace", "bwlimitKBps": 3000})
	e.flag("DROP", true, e.local+" 1000000")
	e.pass(time.Now())
	g := e.get(tr.ID)
	if g.State != transfers.StateWaiting || g.FinalName != "big.fits" {
		t.Fatalf("%+v", g)
	}
	if st, err := os.Stat(filepath.Join(e.local, hosts_PartialDir, "big.fits")); err != nil || st.Size() == 0 {
		t.Fatalf("no partial file to resume from: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "big.fits")); string(b) != string(old) {
		t.Fatal("the old file changed before the replace finished")
	}
	e.pass(time.Now().Add(time.Hour))
	g = e.get(tr.ID)
	got, _ := os.ReadFile(filepath.Join(e.local, "big.fits"))
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(got) != string(want) {
		t.Fatalf("an interrupted replace must resume, not ask again: %+v", g)
	}
}

func TestAReplaceWhoseTargetChangedMeanwhileAsksAgainAndTouchesNothing(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(e.remote, "big.fits", 6<<20)
	e.file(e.local, "big.fits", 100)
	tr := e.download("big.fits", map[string]any{"policy": "replace", "bwlimitKBps": 3000})
	e.flag("DROP", true, e.local+" 1000000")
	e.pass(time.Now())
	if g := e.get(tr.ID); g.State != transfers.StateWaiting {
		t.Fatalf("%+v", g)
	}
	mine := e.file(e.local, "big.fits", 250) // the owner saved something else under that name meanwhile
	e.pass(time.Now().Add(time.Hour))
	g := e.get(tr.ID)
	if g.State != transfers.StateConflict || !strings.Contains(g.Detail, "left alone") {
		t.Fatalf("%+v", g)
	}
	if b, _ := os.ReadFile(filepath.Join(e.local, "big.fits")); string(b) != string(mine) {
		t.Fatal("a file that changed meanwhile was overwritten")
	}
}

// ---- replacing a folder never deletes the owner's links ----

func TestReplacingAFolderStopsWhereItWouldDeleteALink(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(filepath.Join(e.remote, "run", "sub"), "a.fits", 3000)
	e.file(filepath.Join(e.remote, "run"), "top.fits", 2000)
	elsewhere := filepath.Join(e.local, "elsewhere")
	keep := e.file(elsewhere, "mine.txt", 10)
	run := filepath.Join(e.local, "run")
	os.MkdirAll(run, 0o755)
	os.Symlink(elsewhere, filepath.Join(run, "sub")) // the owner's link; the source has a folder sub
	tr := e.download("run", map[string]any{"policy": "replace"})
	e.run()
	g := e.get(tr.ID)
	if g.State != transfers.StateConflict || !strings.Contains(g.Detail, "sub") || !strings.Contains(g.Detail, "Nothing was changed") || g.FinalName != "" {
		t.Fatalf("%+v", g)
	}
	if st, err := os.Lstat(filepath.Join(run, "sub")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was deleted or replaced: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(elsewhere, "mine.txt")); string(b) != string(keep) {
		t.Fatal("the folder behind the link changed")
	}
	if _, err := os.Stat(filepath.Join(run, "top.fits")); err == nil {
		t.Fatal("part of the folder was copied before the owner decided")
	}
	if ns := e.notifications(); len(ns) != 1 {
		t.Fatalf("the owner must be told once: %+v", ns)
	}
	// the owner removes the link and chooses Replace again: it goes through
	os.Remove(filepath.Join(run, "sub"))
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/resolve"), map[string]any{"policy": "replace"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.run()
	if g := e.get(tr.ID); g.State != transfers.StateDone || g.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(run, "sub", "a.fits")); err != nil {
		t.Fatal("the folder did not arrive")
	}
}

func TestReplacingAFolderOnAHostStopsWhereAFileStandsInTheWayOfAFolder(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(filepath.Join(e.local, "run", "sub"), "a.fits", 3000)
	hostRun := filepath.Join(e.remote, "run")
	mine := e.file(hostRun, "sub", 77) // a file called sub where the upload has a folder sub
	tr := e.upload("run", map[string]any{"policy": "replace"})
	e.run()
	g := e.get(tr.ID)
	if g.State != transfers.StateConflict || !strings.Contains(g.Detail, "sub") || !strings.Contains(g.Detail, "Cluster") {
		t.Fatalf("%+v", g)
	}
	if b, _ := os.ReadFile(filepath.Join(hostRun, "sub")); string(b) != string(mine) {
		t.Fatal("the file on the host was deleted or replaced")
	}
}

// ---- an upload checks the host's folders for links first ----

func TestAnUploadIsRefusedWhenTheHostFolderHoldsALinkToAFolder(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(filepath.Join(e.local, "run"), "a.fits", 3000)
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(e.remote, "run", "d"), 0o755)
	os.Symlink(outside, filepath.Join(e.remote, "run", "d", "planted"))
	tr := e.upload("run", map[string]any{"policy": "replace"})
	e.run()
	g := e.get(tr.ID)
	if g.State != transfers.StateFailed || !strings.Contains(g.Detail, "symbolic link") || !strings.Contains(g.Detail, "d/planted") {
		t.Fatalf("%+v", g)
	}
	if e.sshCalls("rsync --server") != 0 {
		t.Fatal("rsync ran although the host folder held a link it could follow")
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatal("something was written through the link")
	}
	// a link where the partial folder goes is refused too, for a plain file
	os.Remove(filepath.Join(e.remote, "run", "d", "planted"))
	os.Symlink(outside, filepath.Join(e.remote, hosts_PartialDir))
	e.file(e.local, "cube.fits", 3000)
	f := e.upload("cube.fits", nil)
	e.run()
	if g := e.get(f.ID); g.State != transfers.StateFailed || !strings.Contains(g.Detail, hosts_PartialDir) {
		t.Fatalf("%+v", g)
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatal("something was written through the partial-folder link")
	}
}
