package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Regression tests for the security review of relayed run requests (ADR 0066 section 3).

// A page refused whole stops the pull, not the status of work already running: the report of an approved job still reaches the
// server in the same pass.
func TestReportsStillGoOutWhenThePullFails(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(id)
	d := r.detail(m.ID)
	if st, b := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash}); st != 200 {
		t.Fatalf("approve: %d %s", st, b)
	}
	r.srv.waitRecords()
	r.forge.set(func(string) []byte { return []byte(`{"workspace":"x","requests":[`) })
	j := r.job(d.Job.ID)
	r.writeRun(j.OutputDir)
	r.end(j.ID, "COMPLETED")
	r.pass()
	if s := r.onServer(id); s.Request.State != relay.StateFinished {
		t.Fatalf("with the pull refused the server says %q", s.Request.State)
	}
}

// The group server runs CheckInbox too: a relay token cannot publish what the key holder's computer would refuse.
func TestServerRefusesAnInboxTheComputerWouldNotPublish(t *testing.T) {
	r := newRelayEnv(t)
	l, err := r.srv.Outbox.Link()
	if err != nil {
		t.Fatal(err)
	}
	for name, inbox := range map[string]string{
		"custom template": `{"labels":[{"label":"Lab A","templates":[{"template":"custom","keys":[],"limits":{"maxCpus":4,"maxMemGiB":4,"maxTimeMinutes":60}}]}]}`,
		"path key":        `{"labels":[{"label":"Lab A","templates":[{"template":"vgt","keys":[{"key":"input_file"}],"limits":{"maxCpus":4,"maxMemGiB":4,"maxTimeMinutes":60}}]}]}`,
		"huge caps":       `{"labels":[{"label":"Lab A","templates":[{"template":"vgt","keys":[],"limits":{"maxCpus":1000000,"maxMemGiB":4,"maxTimeMinutes":60}}]}]}`,
	} {
		w := r.g.bearer(r.token, req{method: "PUT", path: "/api/relay/inbox", body: `{"instance":"` + l.Instance + `","inbox":` + inbox + `}`})
		if w.Code != 422 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// A requested sweep is approved once over drafts rendered from the base: none of them is edited by hand.
func TestRelayedSweepDraftsCannotBeEdited(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(`{"label":"Lab A","template":"vgt","params":{},"resources":{},"grid":[{"key":"ker","values":["2","4"]}],"note":""}`)
	r.pass()
	d := r.detail(r.mirrorOf(id).ID)
	if d.Sweep == nil || len(d.Sweep.Drafts) != 2 {
		t.Fatalf("sweep: %+v", d.Sweep)
	}
	j := r.job(d.Sweep.Drafts[0].JobID)
	if st, b := callJSON(t, "POST", r.api("/api/jobs/"+itoa(int(j.ID))+"/script"), map[string]string{"script": j.Script + "\necho edited\n"}); st != 409 {
		t.Fatalf("edit of a relayed sweep draft: %d %s", st, b)
	}
	if r.job(j.ID).ScriptHash != j.ScriptHash {
		t.Fatal("the script changed")
	}
}

// BeginSubmit is bound to the hash the caller read: an edit between the read and the submit loses.
func TestBeginSubmitRefusesAStaleScriptHash(t *testing.T) {
	e := newWatchEnv(t)
	j := e.draft("stale", nil)
	if won, err := e.srv.Jobs.BeginSubmit(j.ID, strings.Repeat("0", 64)); err != nil || won {
		t.Fatalf("a stale hash began the submit: %v %v", won, err)
	}
	if won, err := e.srv.Jobs.BeginSubmit(j.ID, j.ScriptHash); err != nil || !won {
		t.Fatalf("the current hash: %v %v", won, err)
	}
}

// A request the server no longer has is closed here when declined, like a decline (its draft is dropped). A request closed on the
// server while drafted leaves its draft tied to it, so the key holder can keep it as their own instead of it staying unsubmittable.
func TestGoneAndClosedRequestsLeaveNoStuckDraft(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(id)
	if m.JobID == nil {
		t.Fatalf("not drafted: %+v", m)
	}
	r.forge.gone = id
	st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/decline"), map[string]string{"note": "no"})
	if st != 200 {
		t.Fatalf("decline of a gone request: %d %s", st, b)
	}
	if m = r.mirrorOf(id); m.LocalState != relay.LocalClosed {
		t.Fatalf("local state %s", m.LocalState)
	}
	if _, err := r.srv.Jobs.Get(*m.JobID); err == nil {
		t.Fatal("the declined draft was kept")
	}
	// the mirror still names the deleted draft, so detaching must not claim to keep it
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/detach"), map[string]any{}); st != 409 {
		t.Fatalf("detach with no drafts left: %d %s", st, b)
	}

	id = r.file(singleAsk)
	r.pass()
	m = r.mirrorOf(id)
	if w := r.g.do(req{method: "POST", path: "/api/run-requests/" + itoa(int(id)) + "/withdraw", cookie: r.bea, body: `{}`}); w.Code != 200 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body)
	}
	r.pass()
	if m = r.mirrorOf(id); m.LocalState != relay.LocalClosed || m.JobID == nil || !r.srv.relayedDraft(*m.JobID) {
		t.Fatalf("after the withdrawal: %+v", m)
	}
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/detach"), map[string]any{}); st != 200 {
		t.Fatalf("detach of a closed request: %d %s", st, b)
	}
	if r.srv.relayedDraft(*m.JobID) {
		t.Fatal("the kept draft is still tied to the request, so it can never be submitted")
	}
}

// A member demoted below Researcher takes no requests: the role change closes their inbox, and filing checks the role itself.
func TestDemotionClosesTheInboxAndFilingNeedsAResearcher(t *testing.T) {
	r := newRelayEnv(t)
	body := `{"addressee":"` + r.adaID + `","ask":` + singleAsk + `,"baseItemId":` + itoa(int(r.baseItem)) + `,"expiresInDays":7}`
	if w := r.g.do(req{method: "POST", path: "/api/members/" + r.beaID + "/role", cookie: r.ada, body: `{"role":"owner"}`}); w.Code != 204 {
		t.Fatalf("bea to owner: %d %s", w.Code, w.Body)
	}
	// the role alone, set behind the member route so the inbox is still published: refused at filing
	if _, err := r.g.acc.SetRole(workspace.Local, r.adaID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if w := r.g.do(req{method: "POST", path: "/api/run-requests", cookie: r.bea, body: body}); w.Code != 422 {
		t.Fatalf("filed to a viewer: %d %s", w.Code, w.Body)
	}
	if _, err := r.g.acc.SetRole(workspace.Local, r.adaID, "owner"); err != nil {
		t.Fatal(err)
	}
	// through the member route: ada demoted to researcher keeps her inbox; to viewer she loses it and her open requests close
	open := r.file(singleAsk)
	if w := r.g.do(req{method: "POST", path: "/api/members/" + r.adaID + "/role", cookie: r.bea, body: `{"role":"researcher"}`}); w.Code != 204 {
		t.Fatalf("role to researcher: %d %s", w.Code, w.Body)
	}
	if s := r.onServer(open); s.Request.State != relay.StateRequested {
		t.Fatalf("a researcher's request was closed: %q", s.Request.State)
	}
	if w := r.g.do(req{method: "POST", path: "/api/members/" + r.adaID + "/role", cookie: r.bea, body: `{"role":"viewer"}`}); w.Code != 204 {
		t.Fatalf("role to viewer: %d %s", w.Code, w.Body)
	}
	if s := r.onServer(open); s.Request.State != relay.StateDeclined {
		t.Fatalf("after demotion the open request is %q", s.Request.State)
	}
	if _, err := r.g.s.Relay.InboxOf(workspace.Local, r.adaID); err == nil {
		t.Fatal("the demoted member's inbox is still published")
	}
}

// A relayed draft insists on its base run's commit, as a strict rerun does, and the approval view says which code runs; with no
// commit that describes the base's code, it says why nothing is pinned.
func TestRelayedDraftCarriesTheBaseRunsCommit(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(id)
	code := r.detailCode(m.ID)
	if !strings.HasPrefix(code, "Not pinned to a commit: ") || !strings.Contains(code, "uncommitted") {
		t.Fatalf("a dirty base: %q", code)
	}
	rev := strings.Repeat("ab", 20)
	run := r.finishedRun(r.draft("pinned-base", nil), map[string]string{"code.commit": rev, "code.dirty": "no"})
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/draft"), map[string]any{"baseRunId": run.ID}); st != 200 {
		t.Fatalf("draft from the pinned base: %d %s", st, b)
	}
	d := r.detail(m.ID)
	j := r.job(d.Job.ID)
	if !strings.Contains(j.Script, rev) || j.RerunMode() != rerunAdapted {
		t.Fatalf("the draft does not insist on the base's commit (mode %q)", j.RerunMode())
	}
	if code := r.detailCode(m.ID); !strings.Contains(code, "insists on commit "+rev) {
		t.Fatalf("approval view: %q", code)
	}
}

// A host with no source root has nowhere to write results: refused with a sentence, not a panic.
func TestBuildVGTRefusesAHostWithoutRoots(t *testing.T) {
	e := newWatchEnv(t)
	h := e.host
	h.Roots = nil
	_, aerr := e.srv.buildVGT(context.Background(), h, "x", vgtInput{RepoDir: "/shared/home/u/vgt", ConfigText: e.baseConfig})
	if aerr == nil || aerr.Code != 422 || !strings.Contains(aerr.Msg, "source folder") {
		t.Fatalf("%+v", aerr)
	}
}

func TestAuditBudgetCapsEachSourcePerHour(t *testing.T) {
	var b auditBudget
	now := time.Date(2026, 11, 13, 10, 5, 0, 0, time.UTC)
	for i := 0; i < auditsPerHour; i++ {
		if !b.take("bea", now) {
			t.Fatalf("row %d refused", i)
		}
	}
	if b.take("bea", now.Add(time.Minute)) {
		t.Fatal("a row past the hourly cap was taken")
	}
	if !b.take("cy", now) {
		t.Fatal("another source shares the cap")
	}
	if !b.take("bea", now.Add(time.Hour)) {
		t.Fatal("the next hour starts again")
	}
}

func (r *relayEnv) detailCode(localID int64) string {
	r.t.Helper()
	st, b := callJSON(r.t, "GET", r.api("/api/relayed/"+itoa(int(localID))), nil)
	if st != 200 {
		r.t.Fatalf("detail: %d %s", st, b)
	}
	var d struct {
		Code string `json:"code"`
	}
	json.Unmarshal(b, &d)
	return d.Code
}
