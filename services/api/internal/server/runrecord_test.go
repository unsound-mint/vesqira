package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type recEnv struct {
	t          *testing.T
	srv        *Server
	api        func(string) string
	au         *audit.Store
	jobs       *jobs.Store
	bin        string
	base       string
	remoteRoot string
	baseConfig string
	host       hosts.Host
}

func newRecEnv(t *testing.T) *recEnv {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	remoteRoot := filepath.Join(base, "remote_data")
	os.MkdirAll(remoteHome, 0o755)
	os.MkdirAll(filepath.Join(remoteRoot, "configs"), 0o755)
	baseConfig := "[input]\ninput_file = \"x.fits\"\noutput_dir = \"\"\n\n[noise]\nnoise = 0.17\nnoise_mode = \"scalar\"\n\n[vgt]\ndn = 20\nker = 4\n"
	os.WriteFile(filepath.Join(remoteRoot, "configs", "vgt.toml"), []byte(baseConfig), 0o644)
	bin := filepath.Join(base, "slurm-bin")
	fakeSlurm(t, bin)
	sshScript := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#\$HOME#%s#g' -e 's#bash -lc#bash -c#')
PATH=%s:$PATH exec sh -c "$cmd"
`, remoteHome, bin)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)

	mgr := hosts.NewManager(sshBin, map[string][]byte{})
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
	rs, _ := sourceroot.New([]string{filepath.Join(base, "localroot")})
	os.MkdirAll(filepath.Join(base, "localroot"), 0o755)
	srv := &Server{Roots: rs, Store: cat, Worker: &fakeWorker{}, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js, Audit: au}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Python: "python3", Roots: []string{remoteRoot}})
	if err != nil {
		t.Fatal(err)
	}
	return &recEnv{t: t, srv: srv, api: func(p string) string { return ts.URL + p }, au: au, jobs: js, bin: bin, base: base, remoteRoot: remoteRoot, baseConfig: baseConfig, host: h}
}

func (e *recEnv) draft() jobs.Job {
	r, b := postJSON(e.t, e.api("/api/jobs"), map[string]any{"hostId": e.host.ID, "template": "vgt", "name": "vgt-ker8", "vgt": map[string]any{
		"repoDir": "/shared/home/u/vgt", "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00",
		"set": map[string]string{"ker": "8", "noise_mode": "channel"}}})
	if r.StatusCode != 201 {
		e.t.Fatalf("%d %s", r.StatusCode, b)
	}
	var j jobs.Job
	json.Unmarshal(b, &j)
	return j
}

// submit answers, then waits for the background record write the submit started.
func (e *recEnv) submit(j jobs.Job) jobs.Job {
	r, b := postJSON(e.t, e.api("/api/jobs/"+itoa(int(j.ID))+"/submit"), map[string]any{"scriptHash": j.ScriptHash})
	if r.StatusCode != 200 {
		e.t.Fatalf("submit %d %s", r.StatusCode, b)
	}
	var out jobs.Job
	json.Unmarshal(b, &out)
	e.srv.waitRecords()
	return out
}

func (e *recEnv) refresh(j jobs.Job) {
	postJSON(e.t, e.api("/api/jobs/"+itoa(int(j.ID))+"/refresh"), map[string]any{})
	e.srv.waitRecords()
}

func (e *recEnv) setSlurm(slurmID, state string) {
	os.WriteFile(filepath.Join(e.bin, "state", slurmID), []byte(state+"\n"), 0o644)
}

func (e *recEnv) scanRuns() []discover.Run {
	return discover.Scan(context.Background(), discover.LocalFS{Root: e.remoteRoot}, discover.Options{}).Runs
}

type recDoc struct {
	Vesqira struct {
		State struct {
			Value  string
			AsOf   string `json:"as_of"`
			Source string
		}
		WrittenAt string `json:"written_at"`
		Job       struct {
			SlurmID    string  `json:"slurm_id"`
			ScriptSHA  string  `json:"script_sha256"`
			FinishedAt *string `json:"finished_at"`
		}
		Code       struct{ Commit *string }
		Provenance map[string]string
	}
}

func readDoc(t *testing.T, p string) (recDoc, []byte) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("no run record: %v", err)
	}
	var d recDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d, b
}

func (e *recEnv) auditCount(action string) int {
	entries, _ := e.au.List(workspace.Local, "job", 0, 200)
	n := 0
	for _, en := range entries {
		if en.Action == action {
			n++
		}
	}
	return n
}

// A job submitted to a host leaves a run record next to its outputs, in a format discovery reads, once the job has created
// its output folder (Vesqira never makes it); the record is rewritten with the end state; and a run.json that Vesqira did
// not write is never replaced.
func TestSubmittedJobLeavesARunRecordDiscoveryReads(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	setting := func(r discover.Run, k string) string {
		for _, s := range r.Settings {
			if s.Key == k {
				return s.Value
			}
		}
		return "<absent>"
	}

	// 1. submit: the output folder does not exist yet, so nothing is written and nothing is created
	j := e.draft()
	sub := e.submit(j)
	rec := filepath.Join(j.OutputDir, "run.json")
	if _, err := os.Stat(j.OutputDir); err == nil {
		t.Fatalf("Vesqira created the job's output folder")
	}
	if got, _ := e.jobs.Get(j.ID); !got.RecordOwed || strings.Contains(got.Detail, "run.json not written") || e.auditCount("run record not written") != 0 {
		t.Fatalf("a missing folder is a debt, not a failure: %+v", got)
	}

	// the job starts and creates its folder; the next refresh writes the record, with the state as squeue reported it
	if err := os.MkdirAll(j.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	e.refresh(j)
	d, data := readDoc(t, rec)
	asOf, _ := time.Parse(time.RFC3339, d.Vesqira.State.AsOf)
	if d.Vesqira.State.Value != "submitted" || !strings.Contains(d.Vesqira.State.Source, "squeue/sacct") || asOf.Before(before) ||
		!strings.Contains(d.Vesqira.Provenance["state"], "reported by squeue/sacct") {
		t.Fatalf("%s", data)
	}
	if got, _ := e.jobs.Get(j.ID); got.RecordOwed {
		t.Fatal("the debt must be settled once the record is written")
	}
	found := e.scanRuns()
	if len(found) != 1 {
		t.Fatalf("a rescan must find the run: %+v", found)
	}
	r := found[0]
	if r.RecordKind != "run-json" || !strings.HasPrefix(r.Path, "vesqira_runs/vgt-ker8-") {
		t.Fatalf("%+v", r)
	}
	for k, want := range map[string]string{"vgt.ker": "8", "noise.noise_mode": "channel", "vgt.dn": "20", "slurm.cpus-per-task": "8", "slurm.mem": "16G"} {
		if got := setting(r, k); got != want {
			t.Errorf("setting %s = %q, want %q (%+v)", k, got, want, r.Settings)
		}
	}
	for _, f := range r.Fields { // nothing Vesqira cannot establish is declared: no commit, no start time
		if f.Key == "commit" || f.Key == "startedAt" {
			t.Errorf("the record claimed %+v", f)
		}
	}
	if d.Vesqira.Job.SlurmID != sub.SlurmID || d.Vesqira.Job.ScriptSHA != sub.ScriptHash || d.Vesqira.Job.FinishedAt != nil ||
		d.Vesqira.Code.Commit != nil || !strings.HasPrefix(d.Vesqira.Provenance["code"], "unresolved") || !strings.HasPrefix(d.Vesqira.Provenance["script_sha256"], "verified") ||
		!strings.HasPrefix(d.Vesqira.Provenance["settings"], "declared") {
		t.Fatalf("%s", data)
	}

	// 2. the end state is added when a refresh sees it; nothing else about the settings changes
	e.setSlurm(sub.SlurmID, "COMPLETED")
	e.refresh(j)
	d, data = readDoc(t, rec)
	if d.Vesqira.State.Value != "completed" || d.Vesqira.Job.FinishedAt == nil {
		t.Fatalf("the final state was not recorded: %s", data)
	}
	if got := setting(e.scanRuns()[0], "vgt.ker"); got != "8" {
		t.Errorf("after the rewrite: %s", got)
	}

	// 3. a run.json that is not ours is never replaced; the job is still submitted and says the record is missing
	j2 := e.draft()
	if err := os.MkdirAll(j2.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := `{"params": {"lr": 0.1}}`
	os.WriteFile(filepath.Join(j2.OutputDir, "run.json"), []byte(theirs), 0o644)
	sub2 := e.submit(j2)
	if b, _ := os.ReadFile(filepath.Join(j2.OutputDir, "run.json")); string(b) != theirs {
		t.Fatalf("a foreign run.json was replaced: %s", b)
	}
	got2, _ := e.jobs.Get(j2.ID)
	if sub2.State != "submitted" || got2.State != "submitted" || !strings.Contains(got2.Detail, "run.json not written") || got2.RecordOwed {
		t.Fatalf("%+v", got2)
	}

	// 4. a link where the output folder's parent should be is not written through
	j3 := e.draft()
	outside := filepath.Join(e.base, "outside")
	os.MkdirAll(outside, 0o755)
	os.RemoveAll(filepath.Join(e.remoteRoot, "vesqira_runs"))
	if err := os.Symlink(outside, filepath.Join(e.remoteRoot, "vesqira_runs")); err != nil {
		t.Fatal(err)
	}
	sub3 := e.submit(j3)
	e.refresh(j3) // the link is refused for good: a refresh does not try again
	if ents, _ := os.ReadDir(outside); len(ents) != 0 || sub3.State != "submitted" {
		t.Fatalf("a record was written through a link: %v %+v", ents, sub3)
	}
	if e.auditCount("run record written") != 2 || e.auditCount("run record not written") != 2 {
		entries, _ := e.au.List(workspace.Local, "job", 0, 100)
		t.Fatalf("the audit log must say what was written and what was not: %+v", entries)
	}
}

// At submit the record names what was actually known: sbatch returned an id. Nothing has read the queue yet.
func TestRunRecordAtSubmitSaysSbatchReturnedAnId(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	j := e.draft()
	if err := os.MkdirAll(j.OutputDir, 0o755); err != nil { // a folder that already exists is written into at once
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Second)
	sub := e.submit(j)
	d, data := readDoc(t, filepath.Join(j.OutputDir, "run.json"))
	asOf, err := time.Parse(time.RFC3339, d.Vesqira.State.AsOf)
	if err != nil || asOf.Before(t0) || d.Vesqira.State.Value != "submitted" || d.Vesqira.State.Source != "sbatch returned job id "+sub.SlurmID ||
		!strings.HasPrefix(d.Vesqira.Provenance["state"], "verified: sbatch returned this job id") || strings.Contains(d.Vesqira.Provenance["state"], "squeue") {
		t.Fatalf("%s", data)
	}
}

// as_of is when the state was obtained, not when the record was written.
func TestRunRecordAsOfIsWhenTheStateWasObtained(t *testing.T) {
	t.Parallel()
	obtained := time.Date(2026, 11, 4, 10, 0, 9, 0, time.UTC)
	written := obtained.Add(3 * time.Hour)
	j := jobs.Job{ID: 1, Name: "n", Template: "vgt", SlurmID: "9", ScriptHash: strings.Repeat("a", 64), Script: "#!/bin/bash\n"}
	for name, ev := range map[string]jobs.StateEvidence{
		"submit":  jobs.SubmittedEvidence("9", obtained),
		"refresh": jobs.ReportedEvidence("running", "running", obtained),
		"found":   jobs.FoundEvidence("9", obtained),
	} {
		b, err := jobs.RunRecord(j, "H", nil, ev, written)
		if err != nil {
			t.Fatal(err)
		}
		var d recDoc
		json.Unmarshal(b, &d)
		if d.Vesqira.State.AsOf != "2026-11-04T10:00:09Z" || d.Vesqira.WrittenAt != "2026-11-04T13:00:09Z" || d.Vesqira.State.Source == "" || !strings.HasPrefix(d.Vesqira.Provenance["state"], "verified:") {
			t.Errorf("%s: %s", name, b)
		}
	}
}

// A rewrite at the terminal state that fails for a reason that may pass is retried by the next refresh, from the stored end
// state, until it is written; a refusal that will not pass (a file that is not ours) is not retried.
func TestRunRecordRewriteIsRetriedOnTheNextRefresh(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
	e := newRecEnv(t)
	j := e.draft()
	os.MkdirAll(j.OutputDir, 0o755)
	sub := e.submit(j)
	rec := filepath.Join(j.OutputDir, "run.json")
	if d, _ := readDoc(t, rec); d.Vesqira.State.Value != "submitted" {
		t.Fatal("not written at submit")
	}
	e.setSlurm(sub.SlurmID, "COMPLETED")
	os.Chmod(j.OutputDir, 0o555) // the host cannot make the temporary folder right now
	t.Cleanup(func() { os.Chmod(j.OutputDir, 0o755) })
	e.refresh(j)
	got, _ := e.jobs.Get(j.ID)
	if got.State != "completed" || !got.RecordOwed || !strings.Contains(got.Detail, "run.json not written") {
		t.Fatalf("the failed rewrite must stay owed: %+v", got)
	}
	if d, _ := readDoc(t, rec); d.Vesqira.State.Value != "submitted" {
		t.Fatal("the record must be unchanged by the failed rewrite")
	}
	os.Chmod(j.OutputDir, 0o755)
	e.refresh(j) // the job is finished: no squeue call, the stored end state is written
	d, data := readDoc(t, rec)
	if d.Vesqira.State.Value != "completed" || d.Vesqira.Job.FinishedAt == nil || !strings.Contains(d.Vesqira.State.Source, "squeue/sacct") || strings.Contains(string(data), "run.json not written") {
		t.Fatalf("%s", data)
	}
	if got, _ = e.jobs.Get(j.ID); got.RecordOwed {
		t.Fatal("still owed after a successful write")
	}
	n := e.auditCount("run record written")
	e.refresh(j) // nothing is owed any more: no further write
	if e.auditCount("run record written") != n {
		t.Fatal("a settled record was written again")
	}
}

// The record is written after the answer: the submit response is the job as submitted, whatever the host does next.
func TestSubmitAnswersBeforeTheRecordIsWritten(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	j := e.draft()
	os.MkdirAll(j.OutputDir, 0o755)
	r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/submit"), map[string]any{"scriptHash": j.ScriptHash})
	var out jobs.Job
	json.Unmarshal(b, &out)
	if r.StatusCode != 200 || out.State != "submitted" || strings.Contains(out.Detail, "run.json") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	e.srv.waitRecords()
	if _, err := os.Stat(filepath.Join(j.OutputDir, "run.json")); err != nil {
		t.Fatalf("the background write did not happen: %v", err)
	}
}

// A run.json that only mentions the marker (or is not JSON) is not ours; the top-level "vesqira" block is what counts.
func TestIsRunRecord(t *testing.T) {
	t.Parallel()
	good, _ := jobs.RunRecord(jobs.Job{ID: 1, Script: "x"}, "H", nil, jobs.SubmittedEvidence("1", time.Now()), time.Now())
	if !isRunRecord(good) {
		t.Fatal("its own record must be recognised")
	}
	for name, b := range map[string]string{
		"mention":     `{"notes": "see vesqira-run-record"}`,
		"wrong place": `{"record": "vesqira-run-record"}`,
		"nested":      `{"x": {"vesqira": {"record": "vesqira-run-record"}}}`,
		"not json":    `# vesqira-run-record`,
		"truncated":   string(good[:len(good)/2]),
		"array":       `[{"vesqira": {"record": "vesqira-run-record"}}]`,
	} {
		if isRunRecord([]byte(b)) {
			t.Errorf("%s accepted", name)
		}
	}
}

// project.toml: the marker must start the first line; a project file edited on the host between the read and the write is
// not overwritten.
func TestProjectMarkerIsTheFirstLine(t *testing.T) {
	t.Parallel()
	if !hasProjectMarker([]byte(projectMarker+" (explanation)\n[project]\n")) || !hasProjectMarker([]byte(projectMarker)) {
		t.Fatal("the first line is the marker")
	}
	for _, b := range []string{"[project]\n" + projectMarker + "\n", "\n" + projectMarker, " " + projectMarker, "#" + projectMarker, ""} {
		if hasProjectMarker([]byte(b)) {
			t.Errorf("%q accepted", b)
		}
	}
	if got := withProjectMarker("[project]\n# Written by Vesqira\n"); !hasProjectMarker([]byte(got)) || !strings.HasSuffix(got, "[project]\n# Written by Vesqira\n") {
		t.Fatalf("a marker further down must not stand in for the first line: %q", got)
	}
}

// An analysis job's record carries the operation, its options and the catalog files it reads, as references.
func TestRunRecordOfAnAnalysisJobRoundTripsThroughDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	j := jobs.Job{ID: 7, Name: "moment-3", Template: "analysis", State: "submitted", SlurmID: "4242", ScriptHash: strings.Repeat("a", 64),
		Script: "#!/bin/bash -l\n#SBATCH --job-name=moment-3\n#SBATCH --cpus-per-task=4\n#SBATCH --mem=16G\n#SBATCH --time=02:00:00\nset -e\n",
		Params: `{"op":"moment","options":{"order":"0","vmin":"-5"},"sources":[3]}`}
	data, err := jobs.RunRecord(j, "Cluster", []jobs.RunInput{{DatasetID: 3, Name: "cube.fits", Path: "/data/cube.fits", Source: "catalog"}}, jobs.SubmittedEvidence("4242", time.Now()), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "vesqira_derived", "moment-3-ab12"), 0o755)
	os.WriteFile(filepath.Join(dir, "vesqira_derived", "moment-3-ab12", "run.json"), data, 0o644)
	res := discover.Scan(context.Background(), discover.LocalFS{Root: dir}, discover.Options{})
	if len(res.Runs) != 1 {
		t.Fatalf("%+v", res.Runs)
	}
	got := map[string]string{}
	for _, s := range res.Runs[0].Settings {
		got[s.Key] = s.Value
	}
	if got["analysis"] != "moment" || got["option.order"] != "0" || got["option.vmin"] != "-5" || got["slurm.cpus-per-task"] != "4" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(string(data), `"/data/cube.fits"`) || !strings.Contains(string(data), "the files were not hashed") {
		t.Fatalf("%s", data)
	}
}

// A run.json that merely contains the marker text is another program's: it is not replaced.
func TestARunJsonThatOnlyMentionsTheMarkerIsNotReplaced(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	j := e.draft()
	os.MkdirAll(j.OutputDir, 0o755)
	theirs := `{"notes": "compare with vesqira-run-record files"}`
	rec := filepath.Join(j.OutputDir, "run.json")
	os.WriteFile(rec, []byte(theirs), 0o644)
	e.submit(j)
	if b, _ := os.ReadFile(rec); string(b) != theirs {
		t.Fatalf("replaced: %s", b)
	}
	if got, _ := e.jobs.Get(j.ID); !strings.Contains(got.Detail, "run.json not written") {
		t.Fatalf("%+v", got)
	}
}
