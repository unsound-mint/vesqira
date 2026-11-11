package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// watchEnv is a host whose ssh, sbatch, squeue and sacct are stand-ins that run locally. The ssh stand-in logs every command
// it is given, so a test can count the status calls the watcher makes. squeue and sacct understand one id or a list.
type watchEnv struct {
	t          *testing.T
	srv        *Server
	api        func(string) string
	jobs       *jobs.Store
	notes      *notify.Store
	runs       *runs.Store
	host       hosts.Host
	bin        string
	home       string
	remoteRoot string
	repo       string
	baseConfig string
}

func newWatchEnv(t *testing.T) *watchEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, "remote-home")
	remoteRoot := filepath.Join(base, "remote_data")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(remoteRoot, 0o755)
	os.MkdirAll(filepath.Join(base, "localroot"), 0o755)
	bin := filepath.Join(base, "slurm-bin")
	fakeSlurm(t, bin)
	sshScript := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#\$HOME#%[1]s#g' -e 's#bash -lc#bash -c#')
printf '%%s\n' "$cmd" >> %[3]s
[ -e %[2]s ] && exit 255
PATH=%[4]s:$PATH exec sh -c "$cmd"
`, home, filepath.Join(home, "DOWN"), filepath.Join(base, "ssh.log"), bin)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)

	files := workerFiles(t, repo)
	mgr := hosts.NewManager(sshBin, files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	ns, err := notify.Open(cat.DB())
	ss, _ := sweeps.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := sourceroot.New([]string{filepath.Join(base, "localroot")})
	local := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), Planes: local, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js, Audit: au, Notes: ns, Sweeps: ss, Boards: bs}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	py, _ := exec.LookPath("python3")
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Python: py, Roots: []string{remoteRoot}})
	if err != nil {
		t.Fatal(err)
	}
	return &watchEnv{t: t, srv: srv, api: func(p string) string { return ts.URL + p }, jobs: js, notes: ns, runs: rn, host: h, bin: bin, home: home, remoteRoot: remoteRoot, repo: repo,
		baseConfig: "[input]\ninput_file = \"x.fits\"\noutput_dir = \"\"\n\n[noise]\nnoise = 0.17\nnoise_mode = \"scalar\"\n\n[vgt]\ndn = 20\nker = 4\n"}
}

func (e *watchEnv) draft(name string, over map[string]any) jobs.Job {
	e.t.Helper()
	in := map[string]any{"hostId": e.host.ID, "template": "vgt", "name": name, "vgt": map[string]any{
		"repoDir": "/shared/home/u/vgt", "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "8"}}}
	for k, v := range over {
		in[k] = v
	}
	r, b := postJSON(e.t, e.api("/api/jobs"), in)
	if r.StatusCode != 201 {
		e.t.Fatalf("create %d %s", r.StatusCode, b)
	}
	var j jobs.Job
	json.Unmarshal(b, &j)
	return j
}

func (e *watchEnv) submit(j jobs.Job, extra map[string]any) jobs.Job {
	e.t.Helper()
	in := map[string]any{"scriptHash": j.ScriptHash}
	for k, v := range extra {
		in[k] = v
	}
	r, b := postJSON(e.t, e.api("/api/jobs/"+itoa(int(j.ID))+"/submit"), in)
	if r.StatusCode != 200 {
		e.t.Fatalf("submit %d %s", r.StatusCode, b)
	}
	e.srv.waitRecords()
	var out jobs.Job
	json.Unmarshal(b, &out)
	return out
}

func (e *watchEnv) setSlurm(slurmID, state string) {
	os.WriteFile(filepath.Join(e.bin, "state", slurmID), []byte(state+"\n"), 0o644)
}

// statusCalls counts the squeue/sacct commands the host has been given.
func (e *watchEnv) statusCalls() int { return e.sshLines("squeue -h -j") }

// attempts counts the times the watcher has reached for the host: a status call, or the probe of the host's sacct columns
// that comes first (once per 6 hours) and is the only thing asked of a host that is down.
func (e *watchEnv) attempts() int {
	return e.sshLines("squeue -h -j") + e.sshLines("sacct --helpformat")
}

func (e *watchEnv) job(id int64) jobs.Job {
	j, err := e.jobs.Get(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *watchEnv) notifications() []notify.Notification {
	ns, _, err := e.notes.List(0)
	if err != nil {
		e.t.Fatal(err)
	}
	return ns
}

func (e *watchEnv) endsOf(id int64) []notify.Notification {
	var out []notify.Notification
	for _, n := range e.notifications() {
		if n.JobID != nil && *n.JobID == id && n.Kind != notify.KindImported && n.Kind != notify.KindImportFailed {
			out = append(out, n)
		}
	}
	return out
}

func (e *watchEnv) writeRun(outDir string) {
	e.t.Helper()
	gen := exec.Command("python3", "-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]))", outDir)
	gen.Dir = filepath.Join(e.repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
}

func TestWatcherPollsOnlyWithActiveJobsOnceAPerHostPerInterval(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	w := newJobWatcher(5 * time.Second)
	if w.interval != MinWatchInterval {
		t.Fatalf("the interval must be clamped to %s, got %s", MinWatchInterval, w.interval)
	}
	t0 := time.Now()
	// nothing queued or running: no call to any host, however often it looks
	e.srv.watchOnce(ctx, w, t0)
	e.srv.watchOnce(ctx, w, t0.Add(10*time.Minute))
	if n := e.statusCalls(); n != 0 {
		t.Fatalf("%d status calls with no active job", n)
	}
	// a draft is not active either
	d := e.draft("vgt-a", nil)
	e.srv.watchOnce(ctx, w, t0.Add(20*time.Minute))
	if n := e.statusCalls(); n != 0 {
		t.Fatalf("%d status calls with only a draft", n)
	}
	a := e.submit(d, nil)
	b := e.submit(e.draft("vgt-b", nil), nil)
	e.setSlurm(a.SlurmID, "RUNNING")
	e.setSlurm(b.SlurmID, "PENDING")

	// two active jobs on one host: one call for both, then none until the interval has passed
	t1 := t0.Add(time.Hour)
	e.srv.watchOnce(ctx, w, t1)
	if n := e.statusCalls(); n != 1 {
		t.Fatalf("%d status calls for two jobs on one host, want 1", n)
	}
	log, _ := os.ReadFile(filepath.Join(filepath.Dir(e.bin), "ssh.log"))
	if !strings.Contains(string(log), a.SlurmID+","+b.SlurmID) {
		t.Errorf("the one call must name both jobs:\n%s", log)
	}
	if e.job(a.ID).State != "running" || e.job(b.ID).State != "submitted" {
		t.Errorf("states after the call: %s, %s", e.job(a.ID).State, e.job(b.ID).State)
	}
	e.srv.watchOnce(ctx, w, t1.Add(59*time.Second))
	e.srv.watchOnce(ctx, w, t1.Add(30*time.Second))
	if n := e.statusCalls(); n != 1 {
		t.Fatalf("%d status calls inside one interval, want 1", n)
	}
	e.srv.watchOnce(ctx, w, t1.Add(61*time.Second))
	if n := e.statusCalls(); n != 2 {
		t.Fatalf("%d status calls after the interval, want 2", n)
	}
	// both end: they are noticed, and with nothing active the watcher goes quiet again
	e.setSlurm(a.SlurmID, "COMPLETED")
	e.setSlurm(b.SlurmID, "FAILED")
	e.srv.watchOnce(ctx, w, t1.Add(130*time.Second))
	e.srv.waitRecords()
	e.srv.waitImports()
	if e.job(a.ID).State != "completed" || e.job(b.ID).State != "failed" {
		t.Fatalf("end states: %s, %s", e.job(a.ID).State, e.job(b.ID).State)
	}
	calls := e.statusCalls()
	e.srv.watchOnce(ctx, w, t1.Add(10*time.Minute))
	e.srv.watchOnce(ctx, w, t1.Add(20*time.Minute))
	if e.statusCalls() != calls {
		t.Errorf("the watcher kept asking after nothing was active")
	}
}

func TestWatcherBacksOffWhenTheHostIsUnreachableAndSaysNothingAboutTheJob(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	j := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(j.SlurmID, "RUNNING")
	w := newJobWatcher(MinWatchInterval)
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	base := e.attempts()
	t0 := time.Now()
	e.srv.watchOnce(ctx, w, t0)
	if e.attempts()-base != 1 {
		t.Fatalf("the first attempt must be made")
	}
	// the wait grows with each failure: 2, then 4, then 8 intervals
	e.srv.watchOnce(ctx, w, t0.Add(MinWatchInterval+time.Second)) // inside the first backoff
	if e.attempts()-base != 1 {
		t.Fatalf("asked again inside the backoff")
	}
	t2 := t0.Add(2*MinWatchInterval + time.Second)
	e.srv.watchOnce(ctx, w, t2)
	if e.attempts()-base != 2 {
		t.Fatalf("no second attempt after the first backoff")
	}
	e.srv.watchOnce(ctx, w, t2.Add(3*MinWatchInterval))
	if e.attempts()-base != 2 {
		t.Fatalf("the second wait must be longer than three intervals")
	}
	t3 := t2.Add(4*MinWatchInterval + time.Second)
	e.srv.watchOnce(ctx, w, t3)
	if e.attempts()-base != 3 {
		t.Fatalf("no third attempt after the second backoff")
	}
	if got := w.backoff(30); got != maxWatchBackoff {
		t.Errorf("the backoff must stop at %s, got %s", maxWatchBackoff, got)
	}
	// unreachable is not a verdict: the job is as it was, and nobody is told it ended
	if got := e.job(j.ID); got.State != "running" && got.State != "submitted" {
		t.Errorf("an unreachable host changed the job: %s", got.State)
	}
	if n := len(e.notifications()); n != 0 {
		t.Errorf("%d notifications while the host was unreachable", n)
	}
	// the host returns: the job's end is read at the next due time and the schedule starts over
	os.Remove(filepath.Join(e.home, "DOWN"))
	e.setSlurm(j.SlurmID, "COMPLETED")
	e.srv.watchOnce(ctx, w, t3.Add(8*MinWatchInterval+time.Second))
	e.srv.waitRecords()
	e.srv.waitImports()
	if e.job(j.ID).State != "completed" || len(e.endsOf(j.ID)) != 1 {
		t.Fatalf("after the host returned: %s, %d notifications", e.job(j.ID).State, len(e.endsOf(j.ID)))
	}
	if hw := w.host(e.host.ID); hw.fails != 0 {
		t.Errorf("a success must reset the failures, got %d", hw.fails)
	}
}

func TestAnEndStateIsAnnouncedExactlyOnceWhoeverSeesItAndNeverAsSuccess(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	cases := []struct {
		slurm, kind, title string
	}{
		{"COMPLETED", notify.KindCompleted, "finished (exit 0:0) — results not yet checked"},
		{"FAILED", notify.KindFailed, "failed (exit 1:0)"},
		{"TIMEOUT", notify.KindTimeout, "time limit"},
		{"CANCELLED", notify.KindCancelled, "was cancelled"},
	}
	var ids []int64
	for i, c := range cases {
		j := e.submit(e.draft(fmt.Sprintf("vgt-%d", i), nil), map[string]any{"autoImport": false})
		e.setSlurm(j.SlurmID, c.slurm)
		ids = append(ids, j.ID)
	}
	// the watcher, the Refresh button and the page's timer all look at the same moment
	w := newJobWatcher(MinWatchInterval)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, id := range ids {
				postJSON(t, e.api("/api/jobs/"+itoa(int(id))+"/refresh"), map[string]any{})
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); e.srv.watchOnce(ctx, w, time.Now()) }()
	wg.Wait()
	e.srv.watchOnce(ctx, newJobWatcher(MinWatchInterval), time.Now().Add(time.Hour))
	e.srv.waitRecords()
	for i, c := range cases {
		ns := e.endsOf(ids[i])
		if len(ns) != 1 {
			t.Fatalf("%s: %d notifications, want exactly 1", c.slurm, len(ns))
		}
		n := ns[0]
		if n.Kind != c.kind || !strings.Contains(n.Title, c.title) {
			t.Errorf("%s: kind %q title %q", c.slurm, n.Kind, n.Title)
		}
		if strings.Contains(strings.ToLower(n.Title+n.Body), "success") {
			t.Errorf("%s: a finished job must not be called a success: %q %q", c.slurm, n.Title, n.Body)
		}
		if c.kind == notify.KindCompleted && !strings.Contains(n.Body, "not that its result is valid") {
			t.Errorf("completed: the text must separate completion from validity: %q", n.Body)
		}
	}
	// a stale "running" answer cannot take a finished job back
	j := e.job(ids[0])
	e.srv.applyJobStatus(jobs.Job{ID: j.ID, State: "running", SlurmID: j.SlurmID, HostID: j.HostID}, e.host, jobs.Status{State: "running", Detail: "running", Slurm: "RUNNING"}, time.Now())
	if e.job(ids[0]).State != "completed" {
		t.Errorf("a late running answer overwrote the end state: %s", e.job(ids[0]).State)
	}
	if n := len(e.endsOf(ids[0])); n != 1 {
		t.Errorf("%d notifications after a stale answer", n)
	}
	// the list reaches the owner through the API with the unread count, and reading clears it
	_, body := get(t, e.api("/api/notifications"))
	var list struct {
		Notifications []notify.Notification
		Unread        int
	}
	json.Unmarshal(body, &list)
	if len(list.Notifications) != 4 || list.Unread != 4 {
		t.Fatalf("list: %s", body)
	}
	_, body = postJSON(t, e.api("/api/notifications/read"), map[string]any{"ids": []int64{list.Notifications[0].ID}})
	json.Unmarshal(body, &list)
	if list.Unread != 3 {
		t.Errorf("unread after reading one: %d", list.Unread)
	}
	_, body = postJSON(t, e.api("/api/notifications/read"), map[string]any{"all": true})
	json.Unmarshal(body, &list)
	if list.Unread != 0 {
		t.Errorf("unread after reading all: %d", list.Unread)
	}
}

func TestAJobSlurmNoLongerListsIsAnnouncedAsUnknownOnceAndLeftAsItWas(t *testing.T) {
	e := newWatchEnv(t)
	j := e.submit(e.draft("vgt-a", nil), nil)
	os.Remove(filepath.Join(e.bin, "state", j.SlurmID)) // gone from squeue and sacct
	w := newJobWatcher(MinWatchInterval)
	ctx := context.Background()
	e.srv.watchOnce(ctx, w, time.Now().Add(time.Minute)) // too early to say anything
	if n := len(e.notifications()); n != 0 {
		t.Fatalf("%d notifications a minute after submission", n)
	}
	for i := 0; i < 3; i++ {
		e.srv.watchOnce(ctx, w, time.Now().Add(time.Hour+time.Duration(i)*2*MinWatchInterval))
	}
	e.srv.waitRecords()
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindUnknown || !strings.Contains(ns[0].Title, "not known") {
		t.Fatalf("notifications: %+v", ns)
	}
	if got := e.job(j.ID).State; got != "submitted" {
		t.Errorf("an unlisted job must keep its state, got %s", got)
	}
}

func TestAutoImportOptionDefaultsByTemplateAndIsStoredWithTheJob(t *testing.T) {
	e := newWatchEnv(t)
	if j := e.draft("vgt-a", nil); !j.AutoImport {
		t.Errorf("a VGT job should import by default")
	}
	if j := e.draft("vgt-b", map[string]any{"autoImport": false}); j.AutoImport {
		t.Errorf("the option was turned off at creation")
	}
	rohsa := map[string]any{"template": "rohsa", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": "/shared/home/u/vgt/configs/rohsa/a.toml", "configText": "[input]\ninput = \"a.fits\"\noutput_dir = \"x\"\n[rohsa]\nn_gauss = [4]\n", "cpus": 4, "mem": "8G", "time": "01:00:00"}}
	if j := e.draft("rohsa-a", rohsa); !j.AutoImport {
		t.Errorf("a ROHSA job's fit is imported from its host by default (ADR 0046)")
	}
	if r, _ := postJSON(t, e.api("/api/jobs"), map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": "rohsa-b", "rohsa": rohsa["rohsa"], "autoImport": false}); r.StatusCode != 201 {
		t.Errorf("a ROHSA job with the option off: %d", r.StatusCode)
	}
	// a custom script names no output folder, so there is nothing to import from it
	if r, _ := postJSON(t, e.api("/api/jobs"), map[string]any{"hostId": e.host.ID, "template": "custom", "name": "c", "script": "#!/bin/bash\necho hi\n", "autoImport": true}); r.StatusCode != 422 {
		t.Errorf("automatic import for a custom job: %d, want 422", r.StatusCode)
	}
	// the draft's option can be changed when submitting, and then it stands
	j := e.draft("vgt-c", nil)
	s := e.submit(j, map[string]any{"autoImport": false})
	if s.AutoImport || e.job(j.ID).AutoImport {
		t.Errorf("the submit body's choice was not kept")
	}
	if err := e.jobs.SetAutoImport(j.ID, true); err == nil {
		t.Errorf("the option of a submitted job must not change")
	}
}

func TestACompletedJobIsImportedOnceLinkedAndAnnounced(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	j := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(j.SlurmID, "RUNNING")
	w := newJobWatcher(MinWatchInterval)
	t0 := time.Now()
	e.srv.watchOnce(ctx, w, t0)
	e.writeRun(j.OutputDir)
	e.setSlurm(j.SlurmID, "COMPLETED")
	e.srv.watchOnce(ctx, w, t0.Add(2*MinWatchInterval))
	e.srv.waitRecords()
	e.srv.waitImports()
	got := e.job(j.ID)
	if got.State != "completed" || got.RunID == nil {
		t.Fatalf("job after completion: state %s run %v detail %q", got.State, got.RunID, got.Detail)
	}
	ns := e.notifications()
	var imported []notify.Notification
	for _, n := range ns {
		if n.Kind == notify.KindImported {
			imported = append(imported, n)
		}
	}
	if len(ns) != 2 || len(imported) != 1 || imported[0].RunID == nil || *imported[0].RunID != *got.RunID {
		t.Fatalf("notifications: %+v", ns)
	}
	if rs, _ := e.runs.List(); len(rs) != 1 {
		t.Fatalf("%d runs, want 1", len(rs))
	}
	// the board panel finds the default output the way it does after a manual import: the run is linked to its job
	if l, _ := e.jobs.Lineages(); l[*got.RunID].JobID != j.ID {
		t.Errorf("the run is not linked to the job: %+v", l)
	}
	// looking again, from every direction, imports nothing more and says nothing more
	postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/refresh"), map[string]any{})
	e.srv.autoImport(ctx, j.ID, e.host)
	e.srv.watchOnce(ctx, w, t0.Add(time.Hour))
	e.srv.waitImports()
	if rs, _ := e.runs.List(); len(rs) != 1 || len(e.notifications()) != 2 {
		t.Errorf("a second look changed things: %d runs, %d notifications", len(rs), len(e.notifications()))
	}
}

func TestAutoImportIsOffWhenTheOwnerTurnedItOff(t *testing.T) {
	e := newWatchEnv(t)
	j := e.submit(e.draft("vgt-a", nil), map[string]any{"autoImport": false})
	e.setSlurm(j.SlurmID, "COMPLETED")
	e.writeRun(j.OutputDir)
	e.srv.watchOnce(context.Background(), newJobWatcher(MinWatchInterval), time.Now())
	e.srv.waitRecords()
	e.srv.waitImports()
	if got := e.job(j.ID); got.State != "completed" || got.RunID != nil {
		t.Fatalf("job: state %s run %v", got.State, got.RunID)
	}
	if rs, _ := e.runs.List(); len(rs) != 0 {
		t.Errorf("%d runs imported against the option", len(rs))
	}
	ns := e.endsOf(j.ID)
	if len(ns) != 1 || !strings.Contains(ns[0].Body, "Open the job to import") {
		t.Errorf("notifications: %+v", ns)
	}
}

func TestAFailedAutoImportIsOneNotificationWithTheReasonAndIsNotRetried(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	j := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(j.SlurmID, "COMPLETED") // the job wrote no run_metadata.toml
	w := newJobWatcher(MinWatchInterval)
	e.srv.watchOnce(ctx, w, time.Now())
	e.srv.waitRecords()
	e.srv.waitImports()
	var failed []notify.Notification
	for _, n := range e.notifications() {
		if n.Kind == notify.KindImportFailed {
			failed = append(failed, n)
		}
	}
	if len(failed) != 1 || failed[0].JobID == nil || *failed[0].JobID != j.ID || !strings.Contains(failed[0].Body, "not retried") || len(strings.TrimSpace(failed[0].Body)) < 40 {
		t.Fatalf("notifications: %+v", e.notifications())
	}
	if e.job(j.ID).RunID != nil {
		t.Errorf("a failed import linked a run")
	}
	// the output appears later: it is still not imported by itself, and the manual button works
	e.writeRun(j.OutputDir)
	calls := func() int {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(e.bin), "ssh.log"))
		return strings.Count(string(b), "run_metadata")
	}
	before := calls()
	for i := 0; i < 3; i++ {
		postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/refresh"), map[string]any{})
		e.srv.watchOnce(ctx, w, time.Now().Add(time.Duration(i+1)*time.Hour))
		e.srv.autoImport(ctx, j.ID, e.host)
	}
	e.srv.waitRecords()
	e.srv.waitImports()
	if e.job(j.ID).RunID != nil || calls() != before {
		t.Errorf("the failed import was retried")
	}
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/register"), map[string]any{}); r.StatusCode != 201 {
		t.Errorf("manual import after a failed automatic one: %d %s", r.StatusCode, b)
	}
}

// The notifications belong to the owner's loopback listener. The reviewer's handler has no such route, and a request to the
// owner's with a Host that is not loopback is refused before any handler runs.
func TestNotificationsAreOwnerOnly(t *testing.T) {
	e := newWatchEnv(t)
	req, _ := http.NewRequest("GET", e.api("/api/notifications"), nil)
	req.Host = "attacker.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("a non-loopback Host: %d, want 403", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", e.api("/api/notifications"), nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("a cross-site request: %d, want 403", resp.StatusCode)
	}
}
