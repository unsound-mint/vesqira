package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// The profile pin (pinProfileCommit, ADR 0068) carried through sweeps, reruns and relayed run requests, and the output folder a
// request may not choose (VGT drafts used to overwrite it without saying so).

func vgtPinBody(e *watchEnv, repoDir string, pin bool) map[string]any {
	return map[string]any{"vgt": map[string]any{"repoDir": repoDir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00",
		"set": map[string]string{"ker": "8"}, "pinProfileCommit": pin}}
}

func profileCommit(t *testing.T) string {
	t.Helper()
	tm := jobs.ShippedTemplate("vgt")
	if tm == nil || len(tm.Repo.Commit) != 40 {
		t.Fatalf("the VGT profile has no pinned commit: %+v", tm)
	}
	return tm.Repo.Commit
}

func TestSweepCarriesTheBasesProfilePinAndTheFormCanChangeIt(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	commit := profileCommit(t)
	pinned := e.draft("pinned-base", vgtPinBody(e, "/shared/home/u/vgt", true))
	plain := e.draft("plain-base", nil)

	scripts := func(sw sweepOut) (out []string) {
		for _, m := range sw.Jobs {
			out = append(out, e.job(m.JobID).Script)
		}
		return
	}
	insists := func(script string) bool {
		return strings.Contains(script, "Pinned commit: stop here") && strings.Contains(script, `"`+commit+`"`)
	}

	// no choice in the request: the drafts do what the base did
	sw := e.createSweep(map[string]any{"name": "carry", "baseJobId": pinned.ID, "params": []map[string]any{p("dn", "8", "16")}})
	for _, s := range scripts(sw) {
		if !insists(s) {
			t.Errorf("a sweep of a pinned base lost the pin:\n%s", s)
		}
	}
	for _, m := range sw.Jobs {
		if req, _ := e.job(m.JobID).ConfigRequest(); !req.PinProfile || req.ExpectCommit != "" {
			t.Errorf("request: %+v", req)
		}
	}
	// the approval view says which commit and why, and the pin lines are the base's own, so no draft differs unexpectedly
	_, rev := e.sweepReview(sw.ID)
	cp, _ := rev["codePin"].(map[string]any)
	if cp["commit"] != commit || cp["basis"] != "profile" || !strings.Contains(cp["why"].(string), "was written against") {
		t.Errorf("codePin: %v", cp)
	}
	for _, jv := range rev["jobs"].([]any) {
		if n := jv.(map[string]any)["unexpected"].(float64); n != 0 {
			t.Errorf("unexpected lines in a draft of a pinned base: %v", jv.(map[string]any)["diff"])
		}
	}

	// the sweep can drop the pin, or add it to a base that had none
	off := e.createSweep(map[string]any{"name": "off", "baseJobId": pinned.ID, "pinProfileCommit": false, "params": []map[string]any{p("dn", "8")}})
	for _, s := range scripts(off) {
		if strings.Contains(s, "Pinned commit") {
			t.Errorf("the pin was not dropped:\n%s", s)
		}
	}
	on := e.createSweep(map[string]any{"name": "on", "baseJobId": plain.ID, "pinProfileCommit": true, "params": []map[string]any{p("dn", "8")}})
	for _, s := range scripts(on) {
		if !insists(s) {
			t.Errorf("the pin was not added:\n%s", s)
		}
	}
	// a pin the sweep chose is reported once, under "code pin"; it is not an unexpected difference in every draft
	_, rev = e.sweepReview(on.ID)
	for _, jv := range rev["jobs"].([]any) {
		if n := jv.(map[string]any)["unexpected"].(float64); n != 0 {
			t.Errorf("a chosen pin shows as unexpected: %v", jv.(map[string]any)["diff"])
		}
	}
	if cp, _ := rev["codePin"].(map[string]any); cp["basis"] != "profile" {
		t.Errorf("codePin of a sweep that chose the pin: %v", rev["codePin"])
	}
	// a plain base stays plain
	none := e.createSweep(map[string]any{"name": "none", "baseJobId": plain.ID, "params": []map[string]any{p("dn", "8")}})
	for _, s := range scripts(none) {
		if strings.Contains(s, "Pinned commit") {
			t.Errorf("a pin appeared from nowhere:\n%s", s)
		}
	}
	if _, rev = e.sweepReview(none.ID); rev["codePin"].(map[string]any)["commit"] != "" {
		t.Errorf("codePin of a plain sweep: %v", rev["codePin"])
	}
}

func TestStrictRerunsCommitBeatsTheProfilePin(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	code := newCodeRepo(t, filepath.Dir(e.remoteRoot))
	_, pj, _ := e.draftIn("parent", code.dir, nil)
	parent := e.finishedRun(pj, codeFacts(code))
	profile := profileCommit(t)
	if profile == code.head() {
		t.Fatal("the test repository cannot share the profile's commit")
	}

	// strict and pinned: the run's commit is the one the script insists on, worded as a strict rerun
	st, j, raw := e.draftIn("both", code.dir, map[string]any{"parentRunId": parent.ID, "rerunMode": "strict", "vgt": map[string]any{
		"repoDir": code.dir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "8"}, "pinProfileCommit": true}})
	if st != 201 {
		t.Fatalf("%d %s", st, raw)
	}
	if !strings.Contains(j.Script, "strict rerun stopped") || !strings.Contains(j.Script, code.head()) || strings.Contains(j.Script, profile) || strings.Contains(j.Script, "Pinned commit") {
		t.Errorf("a strict rerun's commit must win over the profile's:\n%s", j.Script)
	}
	_, cb := get(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/rerun-check"))
	var chk struct {
		CodePin codePin `json:"codePin"`
	}
	json.Unmarshal(cb, &chk)
	if chk.CodePin.Basis != "run" || chk.CodePin.Commit != code.head() || !strings.Contains(chk.CodePin.Why, "also asked for") || !strings.Contains(chk.CodePin.Why, profile) {
		t.Errorf("the approval view does not say both: %+v", chk.CodePin)
	}
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/jobs/{id}/rerun-check", "GET", 200, "application/json"), "rerun check", cb)

	// adapted and pinned: the profile's commit, said to be the profile's
	st, j2, raw := e.draftIn("adapted", code.dir, map[string]any{"parentRunId": parent.ID, "rerunMode": "adapted", "vgt": map[string]any{
		"repoDir": code.dir, "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "8"}, "pinProfileCommit": true}})
	if st != 201 {
		t.Fatalf("%d %s", st, raw)
	}
	if !strings.Contains(j2.Script, "Pinned commit: stop here") || !strings.Contains(j2.Script, profile) || strings.Contains(j2.Script, "strict rerun") {
		t.Errorf("an adapted rerun with the pin:\n%s", j2.Script)
	}
	_, cb = get(t, e.api("/api/jobs/"+itoa(int(j2.ID))+"/rerun-check"))
	json.Unmarshal(cb, &chk)
	if chk.CodePin.Basis != "profile" || chk.CodePin.Commit != profile {
		t.Errorf("codePin of an adapted pinned rerun: %+v", chk.CodePin)
	}
}

func TestRelayedDraftCarriesTheBasesProfilePin(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(id)
	profile := profileCommit(t)
	// a pinned base whose own code no commit describes (the run recorded no environment)
	run := r.finishedRun(r.draft("pinned-base", vgtPinBody(r.watchEnv, "/shared/home/u/vgt", true)), nil)
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/draft"), map[string]any{"baseRunId": run.ID}); st != 200 {
		t.Fatalf("draft: %d %s", st, b)
	}
	j := r.job(r.detail(m.ID).Job.ID)
	if !strings.Contains(j.Script, "Pinned commit: stop here") || !strings.Contains(j.Script, profile) {
		t.Errorf("the relayed draft lost the base's pin:\n%s", j.Script)
	}
	code := r.detailCode(m.ID)
	if !strings.Contains(code, profile) || !strings.Contains(code, "was written against") {
		t.Errorf("approval view: %q", code)
	}
}

func TestOutputFolderIsRefusedWithASentenceAndOldStoredSettingsStillCarry(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	for name, body := range map[string]map[string]any{
		"vgt body":  {"vgt": map[string]any{"repoDir": "/shared/home/u/vgt", "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"output_dir": "/elsewhere"}}},
		"tool body": {"tool": map[string]any{"repoDir": "/shared/home/u/vgt", "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"output_dir": "/elsewhere"}}},
	} {
		in := map[string]any{"hostId": e.host.ID, "template": "vgt", "name": "x"}
		for k, v := range body {
			in[k] = v
		}
		r, b := postJSON(t, e.api("/api/jobs"), in)
		if r.StatusCode != 422 || !strings.Contains(string(b), "output_dir is set by Vesqira") {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}
	// a request stored by an earlier version may hold an output_dir its job never used; a sweep from it drops the key
	base := e.draft("old-base", nil)
	var req jobs.VGTRequest
	json.Unmarshal([]byte(e.job(base.ID).Request), &req)
	req.Set["output_dir"] = "/elsewhere"
	stored, _ := json.Marshal(req)
	if _, err := e.srv.Store.DB().Exec(`UPDATE jobs SET request=? WHERE id=?`, string(stored), base.ID); err != nil {
		t.Fatal(err)
	}
	sw := e.createSweep(map[string]any{"name": "old", "baseJobId": base.ID, "params": []map[string]any{p("dn", "8")}})
	s := e.job(sw.Jobs[0].JobID).Script
	if strings.Contains(s, "/elsewhere") || !strings.Contains(s, e.remoteRoot) {
		t.Errorf("a stored output_dir reached a new draft:\n%s", s)
	}
	if got, _ := e.job(sw.Jobs[0].JobID).ConfigRequest(); got.Set["output_dir"] != "" {
		t.Errorf("a stored output_dir was recorded again: %v", got.Set)
	}
}
