package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
)

// ADR 0074 amendment of 2026-11-14: findings of the security review of standing approvals. Same environment as relay_grant_test.go.

// D1: the hash that was checked, reserved and claimed is the only one submitted. A draft edited after the claim is not sent.
func TestDraftEditedAfterTheClaimIsNotSubmitted(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxAtOnce": 1})
	before := r.submissions()
	var edited int64
	r.srv.grantHook = func(m relay.Mirrored) {
		j, _ := r.srv.Jobs.Get(*m.JobID)
		if err := r.srv.Jobs.SetScript(j.ID, j.Script+"\n# another line, added after the check\n"); err != nil {
			t.Error(err)
		}
		edited = j.ID
	}
	id := r.file(grantAsk(g.ID, "6"))
	r.pass()
	r.srv.waitRecords()
	m := r.mirrorOf(id)
	if edited == 0 || r.submissions() != before {
		t.Fatalf("an edited draft was submitted (edited %d, sbatch calls %d)", edited, r.submissions()-before)
	}
	if j := r.job(edited); j.State != jobs.StateDraft {
		t.Fatalf("job state %s", j.State)
	}
	if !strings.Contains(m.GrantNote, "changed after it was checked") || m.GrantState != relay.GrantStateStopped {
		t.Fatalf("grant state %q note %q", m.GrantState, m.GrantNote)
	}
	// D2 with it: nothing reached Slurm, so the jobs-at-once slot is free again (the claimed job stays counted)
	if u := r.grantNow(g.ID); u.AtOnce != 0 || u.JobsUsed != 1 {
		t.Fatalf("usage: %+v", u)
	}
}

// D2: a submission refused because too many jobs are active is tried again at the next pass, a bounded number of times, and the
// slot is held meanwhile.
func TestGrantSubmissionRefusedForTooManyActiveJobsIsRetried(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxAtOnce": 1})
	var fill []jobs.Job
	for i := 0; i < jobs.MaxActive; i++ {
		fill = append(fill, r.submit(r.draft("fill", nil), nil))
	}
	before := r.submissions()
	id := r.file(grantAsk(g.ID, "6"))
	r.pass()
	m := r.mirrorOf(id)
	if m.GrantState != relay.GrantStatePending || !strings.Contains(m.GrantNote, "attempt 1 of") || r.submissions() != before {
		t.Fatalf("first refusal: %+v", m)
	}
	if u := r.grantNow(g.ID); u.AtOnce != 1 {
		t.Fatalf("the slot is held while it is retried: %+v", u)
	}
	r.pass()
	if m := r.mirrorOf(id); !strings.Contains(m.GrantNote, "attempt 2 of") {
		t.Fatalf("second try: %q", m.GrantNote)
	}
	// a place frees up: the next pass submits it, once
	r.end(fill[0].ID, "COMPLETED")
	r.pass()
	r.srv.waitRecords()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantStateUsed || r.submissions() != before+1 {
		t.Fatalf("after a place freed: %+v (sbatch %d)", m, r.submissions()-before)
	}
	r.pass()
	if r.submissions() != before+1 {
		t.Fatal("submitted twice")
	}
}

func TestGrantSubmissionRetriesStopAndFreeTheSlot(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxAtOnce": 1})
	for i := 0; i < jobs.MaxActive; i++ {
		r.submit(r.draft("fill", nil), nil)
	}
	before := r.submissions()
	id := r.file(grantAsk(g.ID, "6"))
	for i := 0; i < 5; i++ {
		r.pass()
	}
	m := r.mirrorOf(id)
	if m.GrantState != relay.GrantStateStopped || !strings.Contains(m.GrantNote, "refused before it reached Slurm") || r.submissions() != before {
		t.Fatalf("after the last try: %+v", m)
	}
	if u := r.grantNow(g.ID); u.AtOnce != 0 {
		t.Fatalf("the slot stays held after the retries ended: %+v", u)
	}
}

// D2: any other refusal before sbatch (here a custom script, which cannot be imported automatically) frees the slot at once.
func TestGrantSubmissionRefusedBeforeSbatchFreesTheSlot(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxAtOnce": 1})
	custom := r.draft("custom", map[string]any{"template": "custom", "vgt": nil, "script": "#!/bin/bash\n#SBATCH --job-name=c\n#SBATCH -n 1\n#SBATCH -t 00:10:00\nhostname\n"})
	sid := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(sid)
	if ok, err := r.srv.Relayed.Reserve(g.ID, m.ID, custom.ID, 1, 10, custom.ScriptHash); err != nil || !ok {
		t.Fatal(ok, err)
	}
	m.JobID = &custom.ID
	ended := ""
	r.srv.submitGrantedJob(context.Background(), m, g.Grant, custom.ScriptHash, 0, func(note string) { ended = note })
	if !strings.Contains(ended, "refused before it reached Slurm") || r.job(custom.ID).State != jobs.StateDraft {
		t.Fatalf("a refusal before sbatch: %q", ended)
	}
}

// A: a request counts as filed after the grant by when this computer stored it, not only by the createdAt the server claims.
func TestRequestStoredBeforeTheGrantIsStaleWhateverTheServerSays(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(nil)
	sid := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(sid)
	m, _ = r.srv.Relayed.Get(m.ID) // the JSON view of a mirror has lost its origin
	m.Request.Ask.Grant = g.ID
	m.Request.RequesterID = r.beaID
	m.Request.CreatedAt = g.CreatedAt.Add(time.Hour) // what a forging server would claim
	m.Request.ExpiresAt = m.Request.CreatedAt.Add(72 * time.Hour)
	st, err := r.srv.Relayed.State(m.Origin())
	if err != nil {
		t.Fatal(err)
	}
	m.ReceivedAt = g.CreatedAt.Add(-time.Hour)
	if _, code, _ := r.srv.grantVerdict(context.Background(), st, m); code != relay.GrantStale {
		t.Fatalf("stored before the grant: code %q", code)
	}
	m.ReceivedAt = g.CreatedAt
	if _, code, _ := r.srv.grantVerdict(context.Background(), st, m); code == relay.GrantStale {
		t.Fatalf("stored when the grant was made counts as stale")
	}
}

// H1: revoking does not wait for a pass, and a request whose slot was taken before the revocation is not submitted.
func TestRevokeDuringAPassIsPromptAndStopsTheSubmission(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(nil)
	before := r.submissions()
	entered, release := make(chan struct{}), make(chan struct{})
	r.srv.grantHook = func(relay.Mirrored) { close(entered); <-release }
	id := r.file(grantAsk(g.ID, "6"))
	done := make(chan struct{})
	go func() { r.pass(); close(done) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the pass never reached the submission")
	}
	revoked := make(chan int, 1)
	go func() {
		st, _ := callJSON(t, "POST", r.api("/api/relay/local/grants/"+g.ID+"/revoke"), map[string]any{})
		revoked <- st
	}()
	select {
	case st := <-revoked:
		if st != 200 {
			t.Fatalf("revoke: %d", st)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("revoking waited for the pass")
	}
	close(release)
	<-done
	r.srv.waitRecords()
	if r.submissions() != before {
		t.Fatal("a request was submitted after its grant was revoked")
	}
	if m := r.mirrorOf(id); !strings.Contains(m.GrantNote, "revoked") {
		t.Fatalf("note %q", m.GrantNote)
	}
	if u := r.grantNow(g.ID); u.AtOnce != 0 {
		t.Fatalf("usage %+v", u)
	}
}

// H1: a host that did not answer is not asked again for the rest of the pass.
func TestHostThatDoesNotAnswerIsNotAskedAgainInThePass(t *testing.T) {
	r := newGrantEnv(t)
	h, err := r.srv.HostDB.Get(r.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.srv.grantDown = map[int64]bool{}
	defer func() { r.srv.grantDown = nil }()
	r.srv.Remote.Close()
	writeFile(t, r.home+"/DOWN")
	defer os.Remove(r.home + "/DOWN")
	ask := map[string]any{"op": "resolve", "path": r.cubes + "/hnv1.fits"}
	if _, err := r.srv.grantHostDo(context.Background(), h, ask, 30*time.Second); err == nil || errors.Is(err, errHostDown) {
		t.Fatalf("the first call to a host that is down: %v", err)
	}
	calls := r.sshLines("")
	if _, err := r.srv.grantHostDo(context.Background(), h, ask, 30*time.Second); !errors.Is(err, errHostDown) {
		t.Fatalf("the second call: %v", err)
	}
	if r.sshLines("") != calls {
		t.Fatal("the host was asked again in the same pass")
	}
}

// C: a code folder or Julia depot that group or others can write is refused, with the path and mode.
func TestCodeFolderWritableByOthersRefusesTheGrant(t *testing.T) {
	r := newGrantEnv(t)
	code := t.TempDir() + "/vgt"
	if err := os.MkdirAll(code+"/.julia-vesqira", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(code, 0o755); err != nil {
		t.Fatal(err)
	}
	r.draft("ownrun", map[string]any{"vgt": map[string]any{"repoDir": code, "configText": r.baseConfig, "cpus": 4, "mem": "8G", "time": "01:00:00"}})
	post := func() (int, string) {
		st, b := callJSON(t, "POST", r.api("/api/relay/local/grants"), r.grantBody(nil))
		return st, string(b)
	}
	if st, b := post(); st != 201 {
		t.Fatalf("a code folder only its owner can write: %d %s", st, b)
	}
	for _, c := range []struct {
		path string
		mode os.FileMode
		want string
	}{{code, 0o775, "writable by its group (mode 0775)"}, {code + "/.julia-vesqira", 0o757, "writable by others (mode 0757)"}} {
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
		st, b := post()
		if st != 422 || !strings.Contains(b, c.path) || !strings.Contains(b, c.want) {
			t.Fatalf("%s at %v: %d %s", c.path, c.mode, st, b)
		}
		if err := os.Chmod(c.path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
