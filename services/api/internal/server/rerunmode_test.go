package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// A code folder that is a real git checkout, so the strict check and the probe read real state.
type codeRepo struct {
	t        *testing.T
	dir      string
	manifest string
}

func newCodeRepo(t *testing.T, base string) *codeRepo {
	t.Helper()
	c := &codeRepo{t: t, dir: filepath.Join(base, "vgt"), manifest: "julia_version = \"1.10\"\n"}
	os.MkdirAll(c.dir, 0o755)
	os.WriteFile(filepath.Join(c.dir, "main.jl"), []byte("println(1)\n"), 0o644)
	os.WriteFile(filepath.Join(c.dir, "Manifest.toml"), []byte(c.manifest), 0o644)
	c.git("init", "-q")
	c.git("add", ".")
	c.git("commit", "-q", "-m", "one")
	return c
}

func (c *codeRepo) git(args ...string) string {
	c.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", c.dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
	if err != nil {
		c.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (c *codeRepo) head() string { return c.git("rev-parse", "HEAD") }

func (c *codeRepo) manifestHash() string {
	h := sha256.Sum256([]byte(c.manifest))
	return hex.EncodeToString(h[:])
}

func (c *codeRepo) edit() {
	c.t.Helper()
	os.WriteFile(filepath.Join(c.dir, "main.jl"), []byte("println(2)\n"), 0o644)
}

func (c *codeRepo) commitAgain() {
	c.t.Helper()
	c.edit()
	c.git("commit", "-q", "-am", "two")
}

// envFile writes the vesqira-env.json a job would have left next to its outputs, with the facts given (all verified) and
// everything else absent.
func writeEnvFile(t *testing.T, outDir, slurmID string, facts map[string]string) {
	t.Helper()
	m := map[string]map[string]string{}
	for k, v := range facts {
		m[k] = map[string]string{"status": "verified", "value": v}
	}
	m["python.version"] = map[string]string{"status": "unresolved", "reason": "the job template does not run Python"}
	b, _ := json.Marshal(map[string]any{"record": "vesqira-env", "version": 1, "slurm_job_id": slurmID, "facts": m})
	if err := os.WriteFile(filepath.Join(outDir, runs.EnvFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *watchEnv) draftIn(name, repoDir string, over map[string]any) (int, jobs.Job, string) {
	e.t.Helper()
	in := map[string]any{"hostId": e.host.ID, "template": "vgt", "name": name, "vgt": map[string]any{
		"repoDir": repoDir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "8"}}}
	for k, v := range over {
		in[k] = v
	}
	r, b := postJSON(e.t, e.api("/api/jobs"), in)
	var j jobs.Job
	json.Unmarshal(b, &j)
	return r.StatusCode, j, string(b)
}

func (e *watchEnv) tryJobSubmit(j jobs.Job) (int, string) {
	e.t.Helper()
	r, b := postJSON(e.t, e.api("/api/jobs/"+itoa(int(j.ID))+"/submit"), map[string]any{"scriptHash": j.ScriptHash})
	e.srv.waitRecords()
	return r.StatusCode, string(b)
}

// finishedRun runs a job through to an imported run: its folder, the environment file the job would have written, then the
// scheduler's COMPLETED.
func (e *watchEnv) finishedRun(j jobs.Job, facts map[string]string) runs.Run {
	e.t.Helper()
	j = e.submit(j, nil)
	e.writeRun(j.OutputDir)
	if facts != nil {
		writeEnvFile(e.t, j.OutputDir, j.SlurmID, facts)
	}
	e.end(j.ID, "COMPLETED")
	j = e.job(j.ID)
	if j.RunID == nil {
		e.t.Fatalf("the job was not imported: %s", j.Detail)
	}
	r, err := e.runs.Get(*j.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *watchEnv) runView(id int64) map[string]any {
	e.t.Helper()
	_, b := get(e.t, e.api("/api/runs/"+itoa(int(id))))
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func factsOf(r runs.Run) map[string]runs.EnvFact {
	m := map[string]runs.EnvFact{}
	for _, f := range r.Environment {
		m[f.Key] = f
	}
	return m
}

func codeFacts(c *codeRepo) map[string]string {
	return map[string]string{"code.commit": c.head(), "code.dirty": "no", "code.diff_sha256": "none", "code.untracked_files": "0",
		"julia.manifest_sha256": c.manifestHash(), "julia.version": "julia version 1.10.0", "node.name": "node01", "input.size_bytes": "2880"}
}

func TestRunKeepsWhatItsJobObservedAndTheRecordCarriesIt(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, j, _ := e.draftIn("parent", code.dir, nil)
	run := e.finishedRun(j, codeFacts(code))
	f := factsOf(run)
	if f["code.commit"].Status != runs.EnvVerified || f["code.commit"].Value != code.head() || f["julia.manifest_sha256"].Value != code.manifestHash() {
		t.Fatalf("environment not kept: %+v", run.Environment)
	}
	if x := f["python.version"]; x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "does not run Python") {
		t.Errorf("a fact the job could not read must stay unresolved with its reason: %+v", x)
	}
	if x := f["modules.sha256"]; x.Status != runs.EnvUnresolved || x.Reason != "the job did not report it" {
		t.Errorf("a missing fact is unresolved, never defaulted: %+v", x)
	}
	if len(run.Environment) != len(runs.EnvKeys) {
		t.Errorf("%d facts, want every known key (%d)", len(run.Environment), len(runs.EnvKeys))
	}
	// the run page's reproducibility facts say verified for what the job observed, unresolved for what it did not
	status := map[string]string{}
	for _, x := range e.runView(run.ID)["reproducibility"].([]any) {
		m := x.(map[string]any)
		status[m["key"].(string)] = m["status"].(string)
	}
	if status["code"] != "verified" || status["environment"] != "verified" || status["machine"] != "verified" {
		t.Errorf("reproducibility facts: %v", status)
	}
	// the end-of-job rewrite of run.json carries the same facts
	var doc struct {
		Vesqira struct {
			Environment []runs.EnvFact    `json:"environment"`
			Provenance  map[string]string `json:"provenance"`
		}
	}
	b, err := os.ReadFile(filepath.Join(j.OutputDir, jobs.RunRecordName))
	if err != nil || json.Unmarshal(b, &doc) != nil {
		t.Fatalf("run.json: %v", err)
	}
	if len(doc.Vesqira.Environment) != len(runs.EnvKeys) || !strings.Contains(doc.Vesqira.Provenance["environment"], "not signed") {
		t.Errorf("run.json environment: %d facts, provenance %q", len(doc.Vesqira.Environment), doc.Vesqira.Provenance["environment"])
	}
}

func TestEnvironmentFileOfAnotherJobOrNoFileIsNeverTrusted(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, j, _ := e.draftIn("copied", code.dir, nil)
	j = e.submit(j, nil)
	e.writeRun(j.OutputDir)
	writeEnvFile(t, j.OutputDir, "99999", codeFacts(code)) // written by some other Slurm job
	e.end(j.ID, "COMPLETED")
	run, _ := e.runs.Get(*e.job(j.ID).RunID)
	for _, x := range run.Environment {
		if x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "another job") {
			t.Fatalf("a file from another job was used: %+v", x)
		}
	}
	// no file at all (a job from before environments were recorded)
	_, j2, _ := e.draftIn("nofile", code.dir, nil)
	run2 := e.finishedRun(j2, nil)
	for _, x := range run2.Environment {
		if x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "no vesqira-env.json") {
			t.Fatalf("absent file: %+v", x)
		}
	}
	if v := e.runView(run2.ID)["reproducibility"].([]any); len(v) == 0 {
		t.Error("no reproducibility facts")
	}
}

func TestStrictRerunIsRefusedWhenTheCodeIsNotTheParents(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, pj, _ := e.draftIn("parent", code.dir, nil)
	parent := e.finishedRun(pj, codeFacts(code))
	strict := map[string]any{"parentRunId": parent.ID, "rerunMode": "strict"}

	// the code is as the parent saw it: the draft is made, the script insists on the revision, and it submits
	st, j, raw := e.draftIn("same", code.dir, strict)
	if st != 201 {
		t.Fatalf("strict draft: %d %s", st, raw)
	}
	if !strings.Contains(j.Script, "strict rerun stopped") || !strings.Contains(j.Script, code.head()) || e.job(j.ID).RerunMode() != "strict" {
		t.Errorf("a strict script must stop the job itself when the code moved; mode %q", e.job(j.ID).RerunMode())
	}
	_, cb := get(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/rerun-check"))
	var chk struct {
		Mode        string
		Blocking    bool
		CodeChecked bool
		Differences []runs.EnvDifference
		Messages    []string
	}
	json.Unmarshal(cb, &chk)
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/jobs/{id}/rerun-check", "GET", 200, "application/json"), "rerun check", cb)
	if chk.Mode != "strict" || chk.Blocking || !chk.CodeChecked || len(chk.Messages) != 0 {
		t.Fatalf("check on an unchanged folder: %s", cb)
	}
	matched := map[string]string{}
	for _, d := range chk.Differences {
		matched[d.Key] = d.Result
	}
	if matched["code.commit"] != "matched" || matched["code.dirty"] != "matched" || matched["julia.manifest_sha256"] != "matched" {
		t.Errorf("differences: %v", matched)
	}
	if code, body := e.tryJobSubmit(j); code != 200 {
		t.Fatalf("an unchanged folder must submit: %d %s", code, body)
	}

	// an uncommitted change: refused before anything is sent, with what differs
	st, j2, _ := e.draftIn("dirty", code.dir, strict)
	if st != 201 {
		t.Fatal(st)
	}
	before := e.submissions()
	code.edit()
	status, body := e.tryJobSubmit(j2)
	if status != 409 || !strings.Contains(body, "uncommitted changes") || !strings.Contains(body, "nothing was sent") {
		t.Fatalf("dirty: %d %s", status, body)
	}
	if e.submissions() != before || e.job(j2.ID).State != jobs.StateDraft {
		t.Error("a refused strict submit must send nothing and leave the draft a draft")
	}

	// another commit: refused, naming both revisions
	code.git("commit", "-q", "-am", "two")
	status, body = e.tryJobSubmit(j2)
	if status != 409 || !strings.Contains(body, "the code folder is at "+code.head()[:12]) || !strings.Contains(body, "the parent run used "+parent.Environment[0].Value[:12]) {
		t.Fatalf("moved: %d %s", status, body)
	}
	// ... but a best effort rerun of the same folder is submitted, and the audit keeps what differed
	st, j3, _ := e.draftIn("best", code.dir, map[string]any{"parentRunId": parent.ID, "rerunMode": "best_effort"})
	if st != 201 {
		t.Fatal(st)
	}
	if status, body := e.tryJobSubmit(j3); status != 200 {
		t.Fatalf("best effort: %d %s", status, body)
	}
	found := false
	ents, _ := e.srv.Audit.List("job", j3.ID, 50)
	for _, ent := range ents {
		if ent.Action == "job submitted" && strings.Contains(ent.Detail, "best_effort rerun, differences:") && strings.Contains(ent.Detail, "the parent run used") {
			found = true
		}
	}
	if !found {
		t.Error("the audit line of a best effort submit must list the differences")
	}
}

func TestStrictDraftNeedsAParentWhoseCodeIsKnownAndSettingsUnchanged(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	strict := func(parent int64) map[string]any { return map[string]any{"parentRunId": parent, "rerunMode": "strict"} }

	// an imported run that records no revision
	none, _ := e.runs.Save("/data/none", runs.Parsed{Run: runs.Run{Name: "none", Kind: "vgt"}}, nil)
	if st, _, raw := e.draftIn("a", code.dir, strict(none.ID)); st != 422 || !strings.Contains(raw, "recorded no code revision") {
		t.Errorf("no revision: %d %s", st, raw)
	}
	// a declared short revision cannot be compared exactly; a declared dirty one does not describe its code
	short, _ := e.runs.Save("/data/short", runs.Parsed{Run: runs.Run{Name: "short", Kind: "vgt", GitCommit: "abc1234"}}, nil)
	if st, _, raw := e.draftIn("b", code.dir, strict(short.ID)); st != 422 || !strings.Contains(raw, "short revision") {
		t.Errorf("short: %d %s", st, raw)
	}
	dirty, _ := e.runs.Save("/data/dirty", runs.Parsed{Run: runs.Run{Name: "dirty", Kind: "vgt", GitCommit: strings.Repeat("a", 40) + "-dirty"}}, nil)
	if st, _, raw := e.draftIn("c", code.dir, strict(dirty.ID)); st != 422 || !strings.Contains(raw, "uncommitted changes") {
		t.Errorf("declared dirty: %d %s", st, raw)
	}
	// a declared full revision is accepted and the check says it is declared, not observed
	full, _ := e.runs.Save("/data/full", runs.Parsed{Run: runs.Run{Name: "full", Kind: "vgt", GitCommit: code.head()}}, nil)
	st, j, raw := e.draftIn("d", code.dir, strict(full.ID))
	if st != 201 {
		t.Fatalf("declared revision: %d %s", st, raw)
	}
	_, cb := get(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/rerun-check"))
	if !strings.Contains(string(cb), "declared by its own metadata, not observed") || strings.Contains(string(cb), `"blocking":true`) {
		t.Errorf("check against a declared revision: %s", cb)
	}
	// an observed dirty parent, or one whose cleanliness was not observed
	_, pj, _ := e.draftIn("p1", code.dir, nil)
	dirtyParent := e.finishedRun(pj, map[string]string{"code.commit": code.head(), "code.dirty": "yes", "code.diff_sha256": strings.Repeat("c", 64)})
	if st, _, raw := e.draftIn("e", code.dir, strict(dirtyParent.ID)); st != 422 || !strings.Contains(raw, "uncommitted changes") {
		t.Errorf("observed dirty: %d %s", st, raw)
	}
	_, pj2, _ := e.draftIn("p2", code.dir, nil)
	unknown := e.finishedRun(pj2, map[string]string{"code.commit": code.head()})
	if st, _, raw := e.draftIn("f", code.dir, strict(unknown.ID)); st != 422 || !strings.Contains(raw, "was not observed") {
		t.Errorf("cleanliness not observed: %d %s", st, raw)
	}
	// a strict rerun repeats the run with its own settings: a changed one is refused and named
	_, pj3, _ := e.draftIn("p3", code.dir, nil)
	good := e.finishedRun(pj3, codeFacts(code))
	changed := strict(good.ID)
	changed["vgt"] = map[string]any{"repoDir": code.dir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "16"}}
	if st, _, raw := e.draftIn("g", code.dir, changed); st != 422 || !strings.Contains(raw, "vgt.ker") || !strings.Contains(raw, "adapted") {
		t.Errorf("strict with a changed setting: %d %s", st, raw)
	}
	// a mode needs a parent, and must be one of the three
	if st, _, raw := e.draftIn("h", code.dir, map[string]any{"rerunMode": "strict"}); st != 422 || !strings.Contains(raw, "needs the run it repeats") {
		t.Errorf("mode without a parent: %d %s", st, raw)
	}
	if st, _, raw := e.draftIn("i", code.dir, map[string]any{"parentRunId": good.ID, "rerunMode": "careful"}); st != 422 {
		t.Errorf("unknown mode: %d %s", st, raw)
	}
}

func TestAdaptedRerunMarksTheSettingsThatChangedAndAsksNothingOfTheHost(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, pj, _ := e.draftIn("parent", code.dir, nil)
	parent := e.finishedRun(pj, codeFacts(code))
	over := map[string]any{"parentRunId": parent.ID}
	over["vgt"] = map[string]any{"repoDir": code.dir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "16", "dn": "20"}}
	st, j, _ := e.draftIn("child", code.dir, over)
	if st != 201 || e.job(j.ID).RerunMode() != "adapted" {
		t.Fatalf("default mode: %d %q", st, e.job(j.ID).RerunMode())
	}
	calls := e.sshLines("bash -s --")
	_, cb := get(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/rerun-check"))
	var chk struct {
		Mode          string
		SettingsKnown bool
		Settings      []settingChange
		CodeChecked   bool
		Blocking      bool
	}
	json.Unmarshal(cb, &chk)
	if chk.Mode != "adapted" || !chk.SettingsKnown || chk.CodeChecked || chk.Blocking {
		t.Fatalf("check: %s", cb)
	}
	var keys []string
	for _, c := range chk.Settings {
		keys = append(keys, c.Key)
		if c.Key == "vgt.ker" && (c.Parent == nil || *c.Parent != "8" || c.Now == nil || *c.Now != "16") {
			t.Errorf("ker: %+v", c)
		}
	}
	if fmt.Sprint(keys) != "[vgt.ker]" { // dn = 20 is the base config's own value: unchanged
		t.Errorf("changed settings: %v", keys)
	}
	if e.sshLines("bash -s --") != calls {
		t.Error("an adapted draft must not read the code folder")
	}
}

func TestRunPageComparesWhatABestEffortRerunObservedWithItsParent(t *testing.T) {
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, pj, _ := e.draftIn("parent", code.dir, nil)
	parentFacts := codeFacts(code)
	parent := e.finishedRun(pj, parentFacts)
	code.commitAgain()
	st, cj, raw := e.draftIn("child", code.dir, map[string]any{"parentRunId": parent.ID, "rerunMode": "best_effort"})
	if st != 201 {
		t.Fatal(st, raw)
	}
	childFacts := codeFacts(code)
	childFacts["node.name"] = "node07"
	delete(childFacts, "julia.version") // this job could not read it
	child := e.finishedRun(cj, childFacts)

	v := e.runView(child.ID)
	if v["rerunMode"] != "best_effort" {
		t.Errorf("mode: %v", v["rerunMode"])
	}
	cmp, _ := v["parentComparison"].(map[string]any)
	if cmp == nil || cmp["parentObserved"] != true || cmp["thisObserved"] != true {
		t.Fatalf("comparison: %v", v["parentComparison"])
	}
	res := map[string]map[string]any{}
	for _, x := range cmp["facts"].([]any) {
		m := x.(map[string]any)
		res[m["key"].(string)] = m
	}
	if res["code.commit"]["result"] != "differed" || res["code.commit"]["blocking"] != true || res["code.commit"]["parent"] != parentFacts["code.commit"] {
		t.Errorf("commit: %v", res["code.commit"])
	}
	if res["julia.manifest_sha256"]["result"] != "matched" || res["code.dirty"]["result"] != "matched" {
		t.Errorf("matched facts: %v %v", res["julia.manifest_sha256"], res["code.dirty"])
	}
	if res["node.name"]["result"] != "differed" || res["node.name"]["blocking"] != false {
		t.Errorf("node differs but never blocks: %v", res["node.name"])
	}
	if res["julia.version"]["result"] != "unknown" || !strings.Contains(res["julia.version"]["why"].(string), "not observed") {
		t.Errorf("a fact one side did not observe is unknown, not matched: %v", res["julia.version"])
	}
	// the list leaves the comparison out; a run that repeats nothing has none
	if pv := e.runView(parent.ID); pv["parentComparison"] != nil || pv["rerunMode"] != nil {
		t.Errorf("parent run: %v %v", pv["parentComparison"], pv["rerunMode"])
	}
}

func TestPostCompareFactsAreNeverMatchedWhenNeitherSideObservedThem(t *testing.T) {
	for _, d := range runs.CompareEnv(nil, nil) {
		if d.Result != "unknown" || d.Blocking {
			t.Errorf("%s: %+v", d.Key, d)
		}
	}
}
