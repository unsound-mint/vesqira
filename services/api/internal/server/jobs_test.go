package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// fakeSlurm writes stand-ins for sbatch, squeue, sacct and scancel. They keep one state file per job so a test can
// move a job through PENDING, RUNNING and COMPLETED the way the scheduler would.
func fakeSlurm(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(dir, "state")
	os.MkdirAll(state, 0o755)
	write("sbatch", fmt.Sprintf(`[ "$1" = "--parsable" ] || exit 2
for f; do :; done
[ -f "$f" ] || exit 3
n=$(( $(cat %[1]s/counter 2>/dev/null || echo 4241) + 1 )); echo $n > %[1]s/counter
echo PENDING > %[1]s/$n
echo "fake log line for job $n" > "$PWD/$(sed -n 's/^#SBATCH --job-name=//p' "$f")-$n.out"
echo $n
`, state))
	// squeue -h -j LIST -o '%i|%T': one "id|STATE" line per id still queued or running
	write("squeue", fmt.Sprintf(`for id in $(echo "$3" | tr , ' '); do s=$(cat %[1]s/$id 2>/dev/null); case "$s" in PENDING|RUNNING) echo "$id|$s";; esac; done
`, state))
	// sacct [-n] [-P] [-X] -j LIST -o FIELDS: for an id that has ended, the job's row and (without -X) its .batch step, with
	// the columns asked for. A test may put its own rows (complete lines) in <state>/<id>.sacct to stand in for a particular
	// accounting record, list the columns this stand-in "site" rejects in <state>/sacct.reject (one per line; the error goes
	// to stderr and nothing is printed, as a real sacct does), and give the answer to --helpformat in <state>/sacct.helpformat
	// (without that file --helpformat prints nothing and fails). Every call is appended to <state>/sacct.calls.
	write("sacct", fmt.Sprintf(`echo "$*" >> %[1]s/sacct.calls
if [ "$1" = --helpformat ]; then cat %[1]s/sacct.helpformat 2>/dev/null; exit $?; fi
x=; list=; fields=
while [ $# -gt 0 ]; do case "$1" in -X) x=1; shift;; -j) list=$2; shift 2;; -o) fields=$2; shift 2;; *) shift;; esac; done
for f in $(echo "$fields" | tr , ' '); do
if grep -qix "$f" %[1]s/sacct.reject 2>/dev/null; then echo "sacct: error: Invalid field requested: \"$f\"" >&2; exit 1; fi
done
row() { out=; for f in $(echo "$fields" | tr , ' '); do v=
case "$f" in
JobID) v=$1;; State) v=$s;; ExitCode) v=$ec;; Elapsed) v=00:00:09;; Start) v=2026-11-04T10:00:00;; End) v=2026-11-04T10:00:09;;
Timelimit) [ "$2" = job ] && v=00:30:00;; ReqMem) [ "$2" = job ] && v=4G;; ReqTRES) [ "$2" = job ] && v=billing=2,cpu=2,mem=4G,node=1;;
AllocCPUS) v=2;; TotalCPU) v=00:00:12;; MaxRSS) [ "$2" = step ] && v=1048576K;; NNodes) v=1;;
esac; out="$out|$v"; done; echo "${out#|}"; }
for id in $(echo "$list" | tr , ' '); do
if [ -f %[1]s/$id.sacct ]; then cat %[1]s/$id.sacct; continue; fi
s=$(cat %[1]s/$id 2>/dev/null); ec=0:0; [ "$s" = FAILED ] && ec=1:0
case "$s" in COMPLETED|FAILED|CANCELLED|TIMEOUT) row $id job; if [ -z "$x" ]; then row $id.batch step; fi;; esac; done; exit 0
`, state))
	write("scancel", fmt.Sprintf(`echo CANCELLED > %[1]s/$1
`, state))
}

func TestSlurmJobLifecycleOnAStandInHost(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base := t.TempDir()
	remoteHome := filepath.Join(base, "remote-home")
	os.MkdirAll(remoteHome, 0o755)
	remoteRoot := filepath.Join(base, "remote_data")
	os.MkdirAll(filepath.Join(remoteRoot, "configs"), 0o755)
	os.MkdirAll(filepath.Join(base, "localroot"), 0o755)
	baseConfig := "[input]\ninput_file = \"x.fits\"\noutput_dir = \"\"\n\n[noise]\nnoise = 0.17\nnoise_mode = \"scalar\"\n\n[vgt]\ndn = 20\nker = 4\n"
	os.WriteFile(filepath.Join(remoteRoot, "configs", "vgt.toml"), []byte(baseConfig), 0o644)
	bin := filepath.Join(base, "slurm-bin")
	fakeSlurm(t, bin)
	// the stand-in ssh: drop options and destination, use plain bash (no profile) with the Slurm stand-ins first on PATH
	sshScript := fmt.Sprintf(`#!/bin/sh
[ -e %q ] && exit 255
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#\$HOME#%s#g' -e 's#bash -lc#bash -c#')
[ -e %q ] && exit 255
if [ -e %q ]; then PATH=%s:$PATH sh -c "$cmd" >/dev/null 2>&1; exit 255; fi
PATH=%s:$PATH exec sh -c "$cmd"
`, filepath.Join(remoteHome, "DOWN"), remoteHome, filepath.Join(remoteHome, "DROP"), filepath.Join(remoteHome, "LOSE"), bin, bin)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)

	files := map[string][]byte{}
	for _, f := range []string{"plane_worker.py", "fits_header.py", "convert.py", "orientation.py", "overlay_geometry.py"} {
		b, _ := os.ReadFile(filepath.Join(repo, "services/scientific-worker", f))
		files[f] = b
	}
	mgr := hosts.NewManager(sshBin, files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), base)
	rs, _ := sourceroot.New([]string{filepath.Join(base, "localroot")})
	local := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo),
		Planes: local, Cache: NewPlaneCache(8 << 20), Boards: bs, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js}).Handler())
	defer ts.Close()
	api := func(p string) string { return ts.URL + p }

	py, _ := exec.LookPath("python3")
	_, hb := postJSON(t, api("/api/hosts"), map[string]any{"name": "Cluster", "dest": "login1", "python": py, "roots": []string{remoteRoot}})
	var h hosts.Host
	json.Unmarshal(hb, &h)
	vgt := func(over map[string]any) map[string]any {
		v := map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": filepath.Join(remoteRoot, "configs", "vgt.toml"), "cpus": 8, "mem": "16G", "time": "02:00:00",
			"set": map[string]string{"ker": "8", "noise_mode": "channel"}}
		for k, x := range over {
			v[k] = x
		}
		return map[string]any{"hostId": h.ID, "template": "vgt", "name": "vgt-ker8", "vgt": v}
	}

	// settings that could reach the shell, or are not settings at all, are refused before anything exists
	for name, in := range map[string]map[string]any{
		"repo injection":    vgt(map[string]any{"repoDir": "/x; rm -rf ~"}),
		"setting value":     vgt(map[string]any{"set": map[string]string{"ker": "8; id"}}),
		"unknown setting":   vgt(map[string]any{"set": map[string]string{"output_file": "/etc/passwd"}}),
		"config outside":    vgt(map[string]any{"configPath": filepath.Join(base, "somewhere", "x.toml")}),
		"bad template":      {"hostId": h.ID, "template": "evil", "name": "x"},
		"custom no shebang": {"hostId": h.ID, "template": "custom", "name": "x", "script": "echo hi"},
	} {
		if r, _ := postJSON(t, api("/api/jobs"), in); r.StatusCode < 400 {
			t.Errorf("%s: accepted (%d)", name, r.StatusCode)
		}
	}
	// the ROHSA template: a reviewed script, nothing from the request reaches a command
	rr, rohsaBody := postJSON(t, api("/api/jobs"), map[string]any{"hostId": h.ID, "template": "rohsa", "name": "rohsa-n8", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": "/shared/home/u/vgt/configs/rohsa/a.toml", "cpus": 16, "mem": "12G", "time": "03:00:00"}})
	if rr.StatusCode != 201 || !strings.Contains(string(rohsaBody), "rohsa/cli/preprocess.jl") {
		t.Errorf("rohsa draft: %d %s", rr.StatusCode, rohsaBody)
	}
	if r, _ := postJSON(t, api("/api/jobs"), map[string]any{"hostId": h.ID, "template": "rohsa", "name": "x", "rohsa": map[string]any{"repoDir": "/a", "configPath": "/b/$(id).toml", "cpus": 1, "mem": "1G", "time": "01:00:00"}}); r.StatusCode < 400 {
		t.Errorf("an injected config path was accepted: %d", r.StatusCode)
	}
	r, jb := postJSON(t, api("/api/jobs"), vgt(nil))
	if r.StatusCode != 201 {
		t.Fatalf("create job: %d %s", r.StatusCode, jb)
	}
	var j jobs.Job
	json.Unmarshal(jb, &j)
	if j.State != "draft" || !strings.Contains(j.Script, "ker = 8") || !strings.Contains(j.Script, `noise_mode = "channel"`) || !strings.Contains(j.Script, "#SBATCH --cpus-per-task=8") ||
		!strings.HasPrefix(j.OutputDir, filepath.Join(remoteRoot, "vesqira_runs")) || !strings.Contains(j.Script, `output_dir = "`+j.OutputDir+`"`) {
		t.Fatalf("draft: %s", jb)
	}
	jp := func(id int64, p string) string { return api("/api/jobs/" + itoa(int(id)) + p) }

	// what is submitted is exactly what was reviewed: the hash must match, and editing changes it
	if r, _ := postJSON(t, jp(j.ID, "/submit"), map[string]any{"scriptHash": strings.Repeat("0", 64)}); r.StatusCode != 409 {
		t.Errorf("wrong hash: %d, want 409", r.StatusCode)
	}
	edited := strings.Replace(j.Script, "echo \"job_id", "echo \"edited by the user; job_id", 1)
	_, eb := postJSON(t, jp(j.ID, "/script"), map[string]any{"script": edited})
	var j2 jobs.Job
	json.Unmarshal(eb, &j2)
	if j2.ScriptHash == j.ScriptHash || j2.ScriptHash != jobs.Hash(edited) {
		t.Fatalf("the hash must follow the edited script")
	}
	if r, _ := postJSON(t, jp(j.ID, "/submit"), map[string]any{"scriptHash": j.ScriptHash}); r.StatusCode != 409 {
		t.Errorf("stale hash after an edit: %d, want 409", r.StatusCode)
	}
	r, sb := postJSON(t, jp(j.ID, "/submit"), map[string]any{"scriptHash": j2.ScriptHash})
	if r.StatusCode != 200 {
		t.Fatalf("submit: %d %s", r.StatusCode, sb)
	}
	var sub jobs.Job
	json.Unmarshal(sb, &sub)
	if sub.State != "submitted" || sub.SlurmID != "4242" {
		t.Fatalf("submitted job: %+v", sub)
	}
	sent, err := os.ReadFile(filepath.Join(remoteHome, "vesqira-jobs", sub.WorkDir, "job.sh"))
	if err != nil || string(sent) != edited {
		t.Errorf("the host must have received exactly the reviewed script (err %v)", err)
	}
	if r, _ := postJSON(t, jp(j.ID, "/submit"), map[string]any{"scriptHash": j2.ScriptHash}); r.StatusCode != 409 {
		t.Errorf("a job can be submitted once: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, jp(j.ID, "/script"), map[string]any{"script": "#!/bin/sh\n"}); r.StatusCode != 422 {
		t.Errorf("a submitted job's script must not change: %d", r.StatusCode)
	}

	// an answer that never arrives must not lead to a second submission (the command ran, the connection dropped)
	r, lb2 := postJSON(t, api("/api/jobs"), vgt(map[string]any{}))
	var lost jobs.Job
	json.Unmarshal(lb2, &lost)
	os.WriteFile(filepath.Join(remoteHome, "LOSE"), nil, 0o644)
	if r, b := postJSON(t, jp(lost.ID, "/submit"), map[string]any{"scriptHash": lost.ScriptHash}); r.StatusCode != 502 {
		t.Fatalf("a lost answer: %d %s", r.StatusCode, b)
	}
	_, lostBody := get(t, jp(lost.ID, ""))
	var lj jobs.Job
	json.Unmarshal(lostBody, &lj)
	if lj.State != "submitting" {
		t.Fatalf("a lost answer must leave the record submitting, got %s", lj.State)
	}
	if r, _ := postJSON(t, jp(lost.ID, "/submit"), map[string]any{"scriptHash": lost.ScriptHash}); r.StatusCode != 409 {
		t.Errorf("a retry while unresolved: %d, want 409 (it would submit twice)", r.StatusCode)
	}
	if r, _ := postJSON(t, jp(lost.ID, "/reopen"), map[string]any{}); r.StatusCode != 502 {
		t.Errorf("reopen while the host cannot be checked: %d, want 502", r.StatusCode)
	}
	os.Remove(filepath.Join(remoteHome, "LOSE"))
	// the job is on the host (the command ran), and checking finds it by what it left, not by guessing
	_, checkBody := postJSON(t, jp(lost.ID, "/refresh"), map[string]any{})
	var cj jobs.Job
	json.Unmarshal(checkBody, &cj)
	if cj.State != "submitted" || cj.SlurmID == "" {
		t.Fatalf("after checking the host: %+v", cj)
	}
	// a submission that never reached the host can be reopened, and only after the host confirmed it holds nothing
	_, db := postJSON(t, api("/api/jobs"), vgt(map[string]any{}))
	var dropped jobs.Job
	json.Unmarshal(db, &dropped)
	os.WriteFile(filepath.Join(remoteHome, "DROP"), nil, 0o644)
	postJSON(t, jp(dropped.ID, "/submit"), map[string]any{"scriptHash": dropped.ScriptHash})
	os.Remove(filepath.Join(remoteHome, "DROP"))
	_, ob := postJSON(t, jp(dropped.ID, "/reopen"), map[string]any{})
	var oj jobs.Job
	json.Unmarshal(ob, &oj)
	if oj.State != "draft" {
		t.Errorf("a submission that never arrived must reopen as a draft: %+v", oj)
	}
	_ = r

	// follow it through the scheduler
	stateFile := filepath.Join(bin, "state", "4242")
	state := func() jobs.Job {
		_, b := postJSON(t, jp(j.ID, "/refresh"), map[string]any{})
		var x jobs.Job
		json.Unmarshal(b, &x)
		return x
	}
	if x := state(); x.State != "submitted" {
		t.Errorf("pending: %s", x.State)
	}
	os.WriteFile(stateFile, []byte("RUNNING\n"), 0o644)
	if x := state(); x.State != "running" {
		t.Errorf("running: %s", x.State)
	}
	_, lb := get(t, jp(j.ID, "/log"))
	if !strings.Contains(string(lb), "fake log line for job 4242") {
		t.Errorf("log: %s", lb)
	}
	// the run writes its outputs where the config said, then Slurm reports it done
	gen := exec.Command("python3", "-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]))", j.OutputDir)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	os.WriteFile(stateFile, []byte("COMPLETED\n"), 0o644)
	done := state()
	if done.State != "completed" || !strings.Contains(done.Detail, "exit 0:0") || done.FinishedAt == nil {
		t.Errorf("completed: %+v", done)
	}

	// the finished job's outputs become a Run on the host, linked both ways
	r, rb := postJSON(t, jp(j.ID, "/register"), map[string]any{})
	if r.StatusCode != 201 {
		t.Fatalf("register outputs: %d %s", r.StatusCode, rb)
	}
	var run struct {
		ID      int64
		Outputs []struct {
			Role      string
			DatasetID int64
		}
	}
	json.Unmarshal(rb, &run)
	if len(run.Outputs) != 4 {
		t.Fatalf("outputs: %s", rb)
	}
	_, gb := get(t, jp(j.ID, ""))
	var linked jobs.Job
	json.Unmarshal(gb, &linked)
	if linked.RunID == nil || *linked.RunID != run.ID {
		t.Errorf("the job should link to its run: %+v", linked)
	}
	for _, o := range run.Outputs {
		_, db := get(t, api("/api/datasets/"+itoa(int(o.DatasetID))))
		var d catalog.Dataset
		json.Unmarshal(db, &d)
		if d.HostID != h.ID {
			t.Errorf("output %s should live on the host", o.Role)
		}
		if o.Role == "psi" && (d.Interpretation == nil || d.Interpretation.DeclaredBy != "vgt-run-adapter") {
			t.Errorf("the adapter's proposal is missing: %+v", d.Interpretation)
		}
	}

	// rerun lineage: the run knows its job, the prefill is the stored request, and a changed config file is reported
	_, runBody := get(t, api("/api/runs/"+itoa(int(run.ID))))
	var runView struct {
		JobID       *int64 `json:"jobId"`
		ParentRunID *int64 `json:"parentRunId"`
	}
	json.Unmarshal(runBody, &runView)
	if runView.JobID == nil || *runView.JobID != j.ID || runView.ParentRunID != nil {
		t.Errorf("run lineage: %s", runBody)
	}
	_, rrb := get(t, api("/api/runs/"+itoa(int(run.ID))+"/rerun"))
	var rerun struct {
		ParentRunID int64  `json:"parentRunId"`
		Source      string `json:"source"`
		HostID      *int64 `json:"hostId"`
		Name        string `json:"name"`
		VGT         struct {
			RepoDir        *string           `json:"repoDir"`
			ConfigPath     *string           `json:"configPath"`
			BaseConfigText *string           `json:"baseConfigText"`
			Set            map[string]string `json:"set"`
			CPUs           *int              `json:"cpus"`
			Mem            *string           `json:"mem"`
		} `json:"vgt"`
		ConfigChanged *bool `json:"configChanged"`
	}
	json.Unmarshal(rrb, &rerun)
	if rerun.Source != "job" || rerun.ParentRunID != run.ID || rerun.HostID == nil || *rerun.HostID != h.ID || !strings.HasPrefix(rerun.Name, "vgt-ker8-") || !strings.HasSuffix(rerun.Name, "-again") ||
		rerun.VGT.RepoDir == nil || *rerun.VGT.RepoDir != "/shared/home/u/vgt" || rerun.VGT.BaseConfigText == nil || *rerun.VGT.BaseConfigText != baseConfig ||
		rerun.VGT.Set["ker"] != "8" || rerun.VGT.CPUs == nil || *rerun.VGT.CPUs != 8 || rerun.VGT.Mem == nil || *rerun.VGT.Mem != "16G" ||
		rerun.ConfigChanged == nil || *rerun.ConfigChanged {
		t.Errorf("rerun prefill: %s", rrb)
	}
	if strings.Contains(string(rrb), "output_dir = \""+j.OutputDir) {
		t.Errorf("the stored base config must be the text before output_dir was replaced: %s", rrb)
	}
	// the file on the host changes: it is reported, and a rerun built from the stored text does not read it
	changedConfig := strings.Replace(baseConfig, "dn = 20", "dn = 99", 1)
	os.WriteFile(filepath.Join(remoteRoot, "configs", "vgt.toml"), []byte(changedConfig), 0o644)
	_, rrb2 := get(t, api("/api/runs/"+itoa(int(run.ID))+"/rerun"))
	var rerun2 struct {
		ConfigChanged *bool    `json:"configChanged"`
		Notes         []string `json:"notes"`
	}
	json.Unmarshal(rrb2, &rerun2)
	if rerun2.ConfigChanged == nil || !*rerun2.ConfigChanged || len(rerun2.Notes) == 0 {
		t.Errorf("a changed config on the host must be reported: %s", rrb2)
	}
	again := vgt(map[string]any{"configText": *rerun.VGT.BaseConfigText, "set": map[string]string{"ker": "16"}})
	again["name"], again["parentRunId"] = "vgt-ker16", run.ID
	ra, ab := postJSON(t, api("/api/jobs"), again)
	var child jobs.Job
	json.Unmarshal(ab, &child)
	if ra.StatusCode != 201 || child.ParentRunID == nil || *child.ParentRunID != run.ID || !strings.Contains(child.Script, "ker = 16") || !strings.Contains(child.Script, "dn = 20") || strings.Contains(child.Script, "dn = 99") {
		t.Errorf("rerun draft: %d %s", ra.StatusCode, ab)
	}
	stored, _ := js.Get(child.ID)
	if sr, ok := stored.VGTRequest(); !ok || sr.BaseConfigText != baseConfig || sr.Set["ker"] != "16" || sr.ConfigPath != filepath.Join(remoteRoot, "configs", "vgt.toml") {
		t.Errorf("stored request: %+v", stored.Request)
	}
	if strings.Contains(string(ab), "baseConfigText") || strings.Contains(string(ab), "request") {
		t.Errorf("the job JSON must not carry the stored request: %s", ab)
	}
	again["parentRunId"] = run.ID + 1000
	if r, _ := postJSON(t, api("/api/jobs"), again); r.StatusCode != 422 {
		t.Errorf("a parent run that does not exist: %d, want 422", r.StatusCode)
	}
	os.WriteFile(filepath.Join(remoteRoot, "configs", "vgt.toml"), []byte(baseConfig), 0o644)
	// the runs list carries the same links
	_, lsb := get(t, api("/api/runs"))
	var ls struct {
		Runs []struct {
			ID    int64  `json:"id"`
			JobID *int64 `json:"jobId"`
		} `json:"runs"`
	}
	json.Unmarshal(lsb, &ls)
	if len(ls.Runs) != 1 || ls.Runs[0].JobID == nil || *ls.Runs[0].JobID != j.ID {
		t.Errorf("run list lineage: %s", lsb)
	}

	// cancelling: only queued or running jobs, and the state follows the scheduler
	_, jb2 := postJSON(t, api("/api/jobs"), vgt(map[string]any{}))
	var c jobs.Job
	json.Unmarshal(jb2, &c)
	if r, _ := postJSON(t, jp(c.ID, "/cancel"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("cancelling a draft: %d, want 409", r.StatusCode)
	}
	postJSON(t, jp(c.ID, "/submit"), map[string]any{"scriptHash": c.ScriptHash})
	if r, _ := postJSON(t, jp(c.ID, "/cancel"), map[string]any{}); r.StatusCode != 200 {
		t.Errorf("cancel: %d", r.StatusCode)
	}
	_, cb := postJSON(t, jp(c.ID, "/refresh"), map[string]any{})
	json.Unmarshal(cb, &c)
	if c.State != "cancelled" {
		t.Errorf("after cancel: %+v", c)
	}

	// an unreachable host is a plain error that does not name it
	_, jb3 := postJSON(t, api("/api/jobs"), vgt(map[string]any{}))
	var d3 jobs.Job
	json.Unmarshal(jb3, &d3)
	os.WriteFile(filepath.Join(remoteHome, "DOWN"), nil, 0o644)
	r, ub := postJSON(t, jp(d3.ID, "/submit"), map[string]any{"scriptHash": d3.ScriptHash})
	if r.StatusCode != 502 || strings.Contains(string(ub), "login1") {
		t.Errorf("host down: %d %s", r.StatusCode, ub)
	}
	// an unanswered submission is not assumed to have failed: it waits until the host can say so
	if x, _ := js.Get(d3.ID); x.State != "submitting" {
		t.Errorf("an unanswered submission must stay unresolved: %s", x.State)
	}
	os.Remove(filepath.Join(remoteHome, "DOWN"))
	_, rb = postJSON(t, jp(d3.ID, "/reopen"), map[string]any{})
	if x, _ := js.Get(d3.ID); x.State != "draft" {
		t.Errorf("after the host confirmed nothing was submitted: %s %s", x.State, rb)
	}
}
