package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
)

// These tests run on the stand-in host of jobwatch_test.go (ssh, sbatch, squeue, sacct, scancel run locally; the ssh stand-in
// logs every command). They need python3 with astropy, like the other watcher tests, and skip without it.

func (e *watchEnv) sweepRequest(name string, params ...map[string]any) map[string]any {
	base := e.draft("vgt-base", nil)
	return map[string]any{"name": name, "baseJobId": base.ID, "params": params}
}

func p(key string, values ...string) map[string]any {
	return map[string]any{"key": key, "values": values}
}

func (e *watchEnv) createSweep(in map[string]any) sweepOut {
	e.t.Helper()
	r, b := postJSON(e.t, e.api("/api/sweeps"), in)
	if r.StatusCode != 201 {
		e.t.Fatalf("create sweep %d %s", r.StatusCode, b)
	}
	var out sweepOut
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *watchEnv) sweepReview(id int64) (hash string, raw map[string]any) {
	e.t.Helper()
	code, b := getBody(e.t, e.api("/api/sweeps/"+itoa(int(id))+"/review"))
	if code != 200 {
		e.t.Fatalf("review %d %s", code, b)
	}
	json.Unmarshal(b, &raw)
	return raw["sweepHash"].(string), raw
}

func (e *watchEnv) approve(id int64, concurrency int, stopOnFailure bool) sweepOut {
	e.t.Helper()
	hash, _ := e.sweepReview(id)
	r, b := postJSON(e.t, e.api("/api/sweeps/"+itoa(int(id))+"/submit"), map[string]any{"sweepHash": hash, "concurrency": concurrency, "stopOnFailure": stopOnFailure})
	if r.StatusCode != 200 {
		e.t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	e.srv.waitSweeps()
	e.srv.waitRecords()
	return e.sweep(id)
}

func (e *watchEnv) sweep(id int64) sweepOut {
	e.t.Helper()
	code, b := getBody(e.t, e.api("/api/sweeps/"+itoa(int(id))))
	if code != 200 {
		e.t.Fatalf("get sweep %d %s", code, b)
	}
	var out sweepOut
	json.Unmarshal(b, &out)
	return out
}

// end moves a submitted job to a Slurm end state and lets the shared refresh path see it, as the watcher or the Refresh button
// would; the scheduler then runs from the winning transition.
func (e *watchEnv) end(jobID int64, state string) {
	e.t.Helper()
	j := e.job(jobID)
	if j.SlurmID == "" {
		e.t.Fatalf("job %d was never submitted", jobID)
	}
	e.setSlurm(j.SlurmID, state)
	r, b := postJSON(e.t, e.api("/api/jobs/"+itoa(int(jobID))+"/refresh"), map[string]any{})
	if r.StatusCode != 200 {
		e.t.Fatalf("refresh %d %s", r.StatusCode, b)
	}
	e.srv.waitSweeps()
	e.srv.waitRecords()
	e.srv.waitImports()
}

func (e *watchEnv) states(sw sweepOut) []string {
	out := []string{}
	for _, m := range e.sweep(sw.ID).Jobs {
		out = append(out, m.State)
	}
	return out
}

func (e *watchEnv) submissions() int { return e.sshLines("sbatch --parsable") }

func TestSweepExpandsToLinkedDraftsAndSubmitsNothing(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("dnker", p("dn", "8", "16", "24"), p("ker", "2", "4")))
	if len(sw.Jobs) != 6 || sw.State != "draft" || sw.Concurrency != 1 || sw.Counts["draft"] != 6 {
		t.Fatalf("%+v", sw)
	}
	// parameter order: the first parameter changes slowest
	want := []string{"dn = 8, ker = 2", "dn = 8, ker = 4", "dn = 16, ker = 2", "dn = 16, ker = 4", "dn = 24, ker = 2", "dn = 24, ker = 4"}
	for i, m := range sw.Jobs {
		if m.Label != want[i] || m.State != "draft" {
			t.Errorf("job %d: %q %s", i, m.Label, m.State)
		}
		j := e.job(m.JobID)
		if j.State != "draft" || j.SlurmID != "" || !j.AutoImport || j.ParentRunID != nil && false {
			t.Errorf("draft %d: %+v", i, j)
		}
		if !strings.Contains(j.Script, "dn = "+m.Values["dn"]) || !strings.Contains(j.Script, "ker = "+m.Values["ker"]) {
			t.Errorf("job %d's script does not carry its values:\n%s", i, j.Script)
		}
	}
	if e.submissions() != 0 {
		t.Errorf("creating a sweep reached sbatch %d times", e.submissions())
	}
	// 6 drafts and the base: nothing else was made
	if js, _ := e.jobs.List(); len(js) != 7 {
		t.Errorf("%d jobs", len(js))
	}
	// the scripts differ from the base by the swept settings, the output folder and the job name, and nothing else
	_, rev := e.sweepReview(sw.ID)
	for _, jv := range rev["jobs"].([]any) {
		if n := jv.(map[string]any)["unexpected"].(float64); n != 0 {
			t.Errorf("a draft differs from the base in %v unexpected lines: %v", n, jv.(map[string]any)["diff"])
		}
	}
}

func TestSweepIsRefusedWhenItsGridOrBaseIsWrong(t *testing.T) {
	e := newWatchEnv(t)
	req := e.sweepRequest("bad", p("dn", "8"))
	before, _ := e.jobs.List()
	vals := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = itoa(i + 1)
		}
		return out
	}
	rohsa := e.draft("rohsa-base", map[string]any{"template": "rohsa", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": "/shared/home/u/vgt/c.toml", "cpus": 4, "mem": "8G", "time": "01:00:00"}})
	for name, in := range map[string]map[string]any{
		"65 jobs":          {"params": []map[string]any{p("dn", vals(13)...), p("ker", vals(5)...)}},
		"four parameters":  {"params": []map[string]any{p("dn", "1"), p("ker", "1"), p("min_valid", "1"), p("noise", "1")}},
		"no parameters":    {"params": []map[string]any{}},
		"not a setting":    {"params": []map[string]any{p("rm_rf", "1")}},
		"output_dir":       {"params": []map[string]any{p("output_dir", "x")}},
		"bad value type":   {"params": []map[string]any{p("dn", "abc")}},
		"shell in a value": {"params": []map[string]any{p("noise", "1;rm")}},
		"enum":             {"params": []map[string]any{p("noise_mode", "bogus")}},
		"name":             {"name": "bad name!", "params": []map[string]any{p("dn", "1")}},
		"rohsa base":       {"baseJobId": rohsa.ID, "params": []map[string]any{p("dn", "1")}},
		"no base":          {"baseJobId": nil, "params": []map[string]any{p("dn", "1")}},
		"both bases":       {"baseRunId": 1, "params": []map[string]any{p("dn", "1")}},
	} {
		body := map[string]any{}
		for k, v := range req {
			body[k] = v
		}
		for k, v := range in {
			body[k] = v
		}
		if r, b := postJSON(t, e.api("/api/sweeps"), body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s, want 422", name, r.StatusCode, b)
		}
	}
	after, _ := e.jobs.List()
	if len(after) != len(before)+1 { // only the rohsa base
		t.Errorf("a refused sweep left jobs behind: %d -> %d", len(before), len(after))
	}
	if list, _ := e.srv.Sweeps.List(); len(list) != 0 {
		t.Error("a refused sweep was stored")
	}
	// exactly 64 is allowed
	ok := map[string]any{"name": "big", "baseJobId": req["baseJobId"], "params": []map[string]any{p("dn", vals(8)...), p("ker", vals(8)...)}}
	if r, b := postJSON(t, e.api("/api/sweeps"), ok); r.StatusCode != 201 {
		t.Errorf("64 jobs: %d %s", r.StatusCode, b)
	}
}

func TestSweepPreviewCountsAndEstimatesFromTheBase(t *testing.T) {
	e := newWatchEnv(t)
	req := e.sweepRequest("pv", p("dn", "8", "16"), p("ker", "2", "4"))
	base, _ := e.jobs.Get(int64(req["baseJobId"].(int64)))
	e.jobs.SetState(base.ID, "completed", "")
	e.jobs.SetUsage(base.ID, jobs.Usage{ElapsedSeconds: 600, State: "COMPLETED", Unsupported: []string{}})
	r, b := postJSON(t, e.api("/api/sweeps/preview"), req)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var out struct {
		Count    int
		Max      int
		Totals   totals
		Estimate *estimate
	}
	json.Unmarshal(b, &out)
	if out.Count != 4 || out.Max != 64 || out.Totals.Jobs != 4 || out.Totals.CPUsEach != 8 || out.Totals.CPUsTotal != 32 || out.Totals.MemBytesEach != 16<<30 || out.Totals.TimeLimitEach != 7200 {
		t.Errorf("%+v", out)
	}
	if out.Estimate == nil || out.Estimate.ElapsedSeconds != 600 {
		t.Errorf("estimate %+v", out.Estimate)
	}
	if list, _ := e.srv.Sweeps.List(); len(list) != 0 || e.submissions() != 0 {
		t.Error("a preview stored or submitted something")
	}
}

func TestSweepSubmitsOneAtATimeWithLimitOne(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("seq", p("dn", "8", "16", "24")))
	sw = e.approve(sw.ID, 1, false)
	if sw.State != "running" || e.submissions() != 1 {
		t.Fatalf("state %s, %d submissions", sw.State, e.submissions())
	}
	if got := e.states(sw); got[0] != "submitted" || got[1] != "draft" || got[2] != "draft" {
		t.Fatalf("%v", got)
	}
	// more triggers while the first is still running submit nothing more
	e.srv.kickSweeps()
	e.srv.kickSweeps()
	e.srv.advanceSweeps(t.Context())
	e.srv.waitSweeps()
	if e.submissions() != 1 {
		t.Fatalf("%d submissions with a limit of 1 and a job in flight", e.submissions())
	}
	e.end(sw.Jobs[0].JobID, "COMPLETED")
	if got := e.states(sw); got[0] != "completed" || got[1] != "submitted" || got[2] != "draft" {
		t.Fatalf("after the first ended: %v", got)
	}
	e.end(sw.Jobs[1].JobID, "COMPLETED")
	e.end(sw.Jobs[2].JobID, "COMPLETED")
	done := e.sweep(sw.ID)
	if done.State != "finished" || e.submissions() != 3 {
		t.Errorf("state %s, %d submissions", done.State, e.submissions())
	}
	// every job went to Slurm once, with its own Slurm id
	seen := map[string]bool{}
	for _, m := range done.Jobs {
		id := e.job(m.JobID).SlurmID
		if id == "" || seen[id] {
			t.Errorf("job %d: slurm id %q", m.JobID, id)
		}
		seen[id] = true
	}
}

func TestSweepWithLimitTwoKeepsTwoInFlight(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("par", p("dn", "8", "16", "24", "32", "40")))
	sw = e.approve(sw.ID, 2, false)
	if got := e.states(sw); strings.Join(got, ",") != "submitted,submitted,draft,draft,draft" {
		t.Fatalf("%v", got)
	}
	e.end(sw.Jobs[0].JobID, "COMPLETED")
	if got := e.states(sw); strings.Join(got, ",") != "completed,submitted,submitted,draft,draft" {
		t.Fatalf("%v", got)
	}
	e.end(sw.Jobs[2].JobID, "COMPLETED")
	if got := e.states(sw); strings.Join(got, ",") != "completed,submitted,completed,submitted,draft" {
		t.Fatalf("%v", got)
	}
	if e.submissions() != 4 {
		t.Errorf("%d submissions", e.submissions())
	}
}

func TestCancellingASweepStopsTheRestAndLeavesThemDrafts(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("cx", p("dn", "8", "16", "24")))
	sw = e.approve(sw.ID, 1, false)
	r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/cancel"), map[string]any{})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if got := e.sweep(sw.ID); got.State != "stopped" || !strings.Contains(got.Detail, "Cancelled") {
		t.Fatalf("%+v", got)
	}
	// the job already in Slurm keeps going; when it ends nothing else is submitted
	if got := e.states(sw); strings.Join(got, ",") != "submitted,draft,draft" {
		t.Fatalf("%v", got)
	}
	e.end(sw.Jobs[0].JobID, "COMPLETED")
	e.srv.advanceSweeps(t.Context())
	if got := e.states(sw); strings.Join(got, ",") != "completed,draft,draft" || e.submissions() != 1 {
		t.Fatalf("%v, %d submissions", got, e.submissions())
	}
	if r, _ := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/cancel"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("a stopped sweep was cancelled again: %d", r.StatusCode)
	}
	// the owner may approve what is left again; it then continues from the drafts
	sw2 := e.approve(sw.ID, 1, false)
	if got := e.states(sw2); strings.Join(got, ",") != "completed,submitted,draft" {
		t.Errorf("after approving again: %v", got)
	}
}

func TestCancellingWithRunningJobsAsksSlurmToCancelThem(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("cr", p("dn", "8", "16")))
	sw = e.approve(sw.ID, 2, false)
	r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/cancel"), map[string]any{"cancelRunning": true})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if e.sshLines("scancel") != 2 {
		t.Errorf("%d scancel calls, want 2", e.sshLines("scancel"))
	}
}

func TestAFailedJobDoesNotBlockTheRestUnlessTheOwnerChoseToStop(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("fail", p("dn", "8", "16", "24")))
	sw = e.approve(sw.ID, 1, false)
	e.end(sw.Jobs[0].JobID, "FAILED")
	if got := e.states(sw); strings.Join(got, ",") != "failed,submitted,draft" {
		t.Fatalf("a failure blocked the sweep: %v", got)
	}
	// nothing is retried
	e.end(sw.Jobs[1].JobID, "COMPLETED")
	e.end(sw.Jobs[2].JobID, "COMPLETED")
	if e.submissions() != 3 || e.job(sw.Jobs[0].JobID).State != "failed" {
		t.Errorf("%d submissions; the failed job is %s", e.submissions(), e.job(sw.Jobs[0].JobID).State)
	}

	// stop on failure: the choice made at approval
	e2 := newWatchEnv(t)
	sw2 := e2.createSweep(e2.sweepRequest("stop", p("dn", "8", "16", "24")))
	sw2 = e2.approve(sw2.ID, 1, true)
	e2.end(sw2.Jobs[0].JobID, "FAILED")
	got := e2.sweep(sw2.ID)
	if got.State != "stopped" || !strings.Contains(got.Detail, "failed") || e2.submissions() != 1 {
		t.Errorf("%s %q, %d submissions", got.State, got.Detail, e2.submissions())
	}
	if s := e2.states(got); strings.Join(s, ",") != "failed,draft,draft" {
		t.Errorf("%v", s)
	}
}

func TestAnUnconfirmedSubmissionHoldsItsSlotAndIsNeverResubmitted(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("unc", p("dn", "8", "16")))
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644) // the host does not answer
	e.approve(sw.ID, 1, false)
	got := e.sweep(sw.ID)
	if s := e.states(got); strings.Join(s, ",") != "submitting,draft" {
		t.Fatalf("%v", s)
	}
	if len(got.WaitingOn) != 1 || !strings.Contains(got.WaitingOn[0], "Slurm's answer was not seen") {
		t.Errorf("the stall is not visible: %v", got.WaitingOn)
	}
	os.Remove(filepath.Join(e.home, "DOWN"))
	n := e.submissions()
	e.srv.kickSweeps()
	e.srv.advanceSweeps(t.Context())
	e.srv.waitSweeps()
	if e.submissions() != n || strings.Join(e.states(got), ",") != "submitting,draft" {
		t.Errorf("an unconfirmed job was skipped or resubmitted: %d -> %d submissions", n, e.submissions())
	}
}

func TestApprovalNeedsTheHashOfTheScriptsShownAndAnEditedDraftIsHeld(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("hash", p("dn", "8", "16", "24")))
	hash, _ := e.sweepReview(sw.ID)
	// a script edited after it was reviewed: the old hash is refused
	j := e.job(sw.Jobs[1].JobID)
	postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/script"), map[string]any{"script": j.Script + "\n# edited\n"})
	r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/submit"), map[string]any{"sweepHash": hash, "concurrency": 1})
	if r.StatusCode != 409 || e.submissions() != 0 {
		t.Fatalf("%d %s; %d submissions", r.StatusCode, b, e.submissions())
	}
	if r, _ := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/submit"), map[string]any{"sweepHash": "x", "concurrency": 0}); r.StatusCode != 422 {
		t.Errorf("concurrency 0: %d", r.StatusCode)
	}
	// reviewed again, approved, then edited while waiting: that draft is held, never submitted, and the sweep says so
	sw = e.approve(sw.ID, 1, false)
	third := e.job(sw.Jobs[2].JobID)
	postJSON(t, e.api("/api/jobs/"+itoa(int(third.ID))+"/script"), map[string]any{"script": third.Script + "\n# edited later\n"})
	e.end(sw.Jobs[0].JobID, "COMPLETED")
	e.end(sw.Jobs[1].JobID, "COMPLETED")
	got := e.sweep(sw.ID)
	if s := e.states(got); strings.Join(s, ",") != "completed,completed,draft" || e.submissions() != 2 {
		t.Fatalf("%v, %d submissions", s, e.submissions())
	}
	if !got.Jobs[2].Held || len(got.WaitingOn) != 1 || got.State != "finished" {
		t.Errorf("held %v, waiting %v, state %s", got.Jobs[2].Held, got.WaitingOn, got.State)
	}
}

// runWithRMS writes a finished VGT run folder whose declared metric has the given value.
func (e *watchEnv) runWithRMS(outDir, value string) {
	e.t.Helper()
	gen := exec.Command("python3", "-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]), metrics=True)", outDir)
	gen.Dir = filepath.Join(e.repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
	f := filepath.Join(outDir, "run_metadata.toml")
	b, _ := os.ReadFile(f)
	os.WriteFile(f, []byte(strings.Replace(string(b), "value = 0.344", "value = "+value, 1)), 0o644)
}

func TestFinishedJobsBecomeRunsLinkedToTheSweepAndFeedTheGrid(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("grid", p("dn", "8", "16"), map[string]any{"key": "ker", "label": "κ", "values": []string{"2", "4"}}))
	sw = e.approve(sw.ID, 4, false)
	// three of the four finish with a run folder; the first has a different value of the declared metric
	vals := []string{"0.30", "0.35", "0.40"}
	for i := 0; i < 3; i++ {
		j := e.job(sw.Jobs[i].JobID)
		e.runWithRMS(j.OutputDir, vals[i])
		e.end(j.ID, "COMPLETED")
	}
	e.end(sw.Jobs[3].JobID, "FAILED")
	for i := 0; i < 3; i++ {
		if j := e.job(sw.Jobs[i].JobID); j.RunID == nil {
			t.Fatalf("job %d was not imported (detail %q)", i, j.Detail)
		}
	}
	code, b := getBody(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/grid?metric=alignment_measure&x=dn"))
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var g struct {
		Rows    []sweeps.Row
		Metrics []sweeps.MetricInfo
		Plot    *sweeps.Plot
	}
	json.Unmarshal(b, &g)
	if len(g.Rows) != 4 || g.Rows[0].Label != "dn = 8, κ = 2" || g.Rows[2].Label != "dn = 16, κ = 2" {
		t.Fatalf("rows %+v", g.Rows)
	}
	for i := 0; i < 3; i++ {
		if g.Rows[i].RunID == nil || len(g.Rows[i].Metrics) != 2 {
			t.Errorf("row %d: %+v", i, g.Rows[i])
		}
	}
	if g.Rows[3].RunID != nil || len(g.Rows[3].Metrics) != 0 || g.Rows[3].State != "failed" {
		t.Errorf("the failed job's row: %+v", g.Rows[3])
	}
	if len(g.Metrics) != 2 || g.Metrics[0].Key != "alignment_measure" || g.Metrics[0].Values != 3 || g.Metrics[0].UnitsDiffer {
		t.Errorf("metrics %+v", g.Metrics)
	}
	if g.Plot == nil || !g.Plot.Numeric || len(g.Plot.Lines) != 2 || g.Plot.Lines[0].Label != "κ = 2" {
		t.Fatalf("plot %+v", g.Plot)
	}
	// dn = 8 and 16 at κ = 2 (jobs 0 and 2), dn = 8 at κ = 4 (job 1); the failed dn = 16, κ = 4 point is a gap, not zero
	l0, l1 := g.Plot.Lines[0].Points, g.Plot.Lines[1].Points
	if *l0[0].Value != 0.30 || *l0[1].Value != 0.40 || *l1[0].Value != 0.35 || l1[1].Value != nil {
		t.Errorf("series %+v %+v", l0, l1)
	}
	// nothing in the grid names a best run
	if strings.Contains(strings.ToLower(string(b)), "best") {
		t.Error("the grid labels something best")
	}
	// a metric nobody declared gives a reason, not a plot
	_, b = getBody(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/grid?metric=nope"))
	var none struct {
		Plot     *sweeps.Plot
		PlotNote string
	}
	json.Unmarshal(b, &none)
	if none.Plot != nil || none.PlotNote == "" {
		t.Errorf("%+v", none)
	}
}

func TestBoardFromASweepFollowsEachJobInParameterOrderWithOwnerOnlyLabels(t *testing.T) {
	e := newWatchEnv(t)
	sw := e.createSweep(e.sweepRequest("brd", p("dn", "8", "16"), map[string]any{"key": "ker", "label": "κ", "values": []string{"2", "4"}}))
	r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/board"), map[string]any{})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var board boards.Board
	json.Unmarshal(b, &board)
	if board.Title != "Sweep: brd" || len(board.Items) != 4 {
		t.Fatalf("%+v", board)
	}
	want := []string{"dn = 8, κ = 2", "dn = 8, κ = 4", "dn = 16, κ = 2", "dn = 16, κ = 4"}
	for i, it := range board.Items {
		if it.Kind != boards.KindJob || it.Job.ID != sw.Jobs[i].JobID || it.Job.Label != want[i] || it.Job.SweepID == nil || *it.Job.SweepID != sw.ID || it.Note != "" {
			t.Errorf("item %d: %+v job %+v note %q", i, it, it.Job, it.Note)
		}
	}
	// one board per sweep
	if r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/board"), map[string]any{}); r.StatusCode != 409 || !strings.Contains(string(b), "boardId") {
		t.Errorf("a second board: %d %s", r.StatusCode, b)
	}
	if got := e.sweep(sw.ID); got.BoardID == nil || *got.BoardID != board.ID {
		t.Errorf("the sweep does not know its board: %+v", got.BoardID)
	}
	// a reviewer's snapshot carries the job's name and state and no parameter, label or sweep
	_, token, err := e.srv.Boards.PublishWith(board.ID, boards.PublishOptions{Label: "x", TTL: time.Hour, Jobs: e.srv.jobSnapshots(board)})
	if err != nil {
		t.Fatal(err)
	}
	rv, err := e.srv.Boards.Review(token)
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := json.Marshal(rv)
	if !strings.Contains(string(frozen), "-dn8-ker2") { // the job's name and state are there
		t.Fatalf("the snapshot does not show the jobs: %s", frozen)
	}
	for _, leak := range []string{"dn = ", "ker = ", "κ", "sweepId", `"values"`} {
		if strings.Contains(string(frozen), leak) {
			t.Errorf("a snapshot carries %q: %s", leak, frozen)
		}
	}
}
