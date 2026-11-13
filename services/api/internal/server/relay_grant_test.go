package server

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ADR 0074: standing approvals. These run on the relay environment of relay_e2e_test.go (a real group server on TLS, the key
// holder's computer on the stand-in Slurm host, sbatch lines counted) with the base run's input inside the host's source root.

type grantEnv struct {
	*relayEnv
	cubes string // the grant's input folder on the host
	base  *relay.Base
}

func newGrantEnv(t *testing.T) *grantEnv {
	t.Helper()
	var cubes string
	r := newRelayEnvWith(t, func(e *watchEnv) {
		cubes = e.remoteRoot + "/cubes"
		e.baseConfig = strings.Replace(e.baseConfig, `input_file = "x.fits"`, `input_file = "`+cubes+`/hnv1.fits"`, 1)
	})
	// the key holder's computer keeps the group server's (test) clock, so expiries agree on both sides
	r.srv.RelayNow, r.srv.Relayed.Now = r.g.clock.now, r.g.clock.now
	// the input really is on the host: the key holder's computer resolves it through the host's worker before it counts
	for _, d := range []string{cubes, r.remoteRoot + "/other"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, cubes+"/hnv1.fits")
	return &grantEnv{relayEnv: r, cubes: cubes}
}

type grantOut struct {
	relay.Grant
	Live     bool   `json:"live"`
	HostName string `json:"hostName"`
	OpenJobs []struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	} `json:"openJobs"`
}

// grantBody is a grant for bea on "Lab A": ker narrowed to 4..8, inputs under cubes, 8 CPUs, 16 GiB and two hours a job.
func (r *grantEnv) grantBody(over map[string]any) map[string]any {
	b := map[string]any{"requesterId": r.beaID, "requesterName": "bea", "label": relayLabel, "template": "vgt", "commitBasis": "profile",
		"keys": []map[string]any{{"key": "ker", "min": 4, "max": 8}}, "roots": []string{r.cubes}, "files": []string{},
		"maxJobs": 3, "maxCoreHours": 100, "perJob": map[string]int{"maxCpus": 8, "maxMemGiB": 16, "maxTimeMinutes": 120},
		"maxAtOnce": 2, "expiresInDays": 30, "note": "LMC block sweep"}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func (r *grantEnv) grant(over map[string]any) grantOut {
	r.t.Helper()
	st, b := callJSON(r.t, "POST", r.api("/api/relay/local/grants"), r.grantBody(over))
	if st != 201 {
		r.t.Fatalf("grant: %d %s", st, b)
	}
	var g grantOut
	json.Unmarshal(b, &g)
	r.pass() // published with the inbox
	if _, b := callJSON(r.t, "GET", r.api("/api/relay/local"), nil); bytes.Contains(b, []byte(`"error":"`)) {
		r.t.Logf("relay state: %s", b)
	}
	return g
}

func (r *grantEnv) grantNow(id string) grantOut {
	r.t.Helper()
	_, b := callJSON(r.t, "GET", r.api("/api/relay/local/grants"), nil)
	var out struct {
		Grants []grantOut `json:"grants"`
	}
	json.Unmarshal(b, &out)
	for _, g := range out.Grants {
		if g.ID == id {
			return g
		}
	}
	r.t.Fatalf("grant %s not listed: %s", id, b)
	return grantOut{}
}

func grantAsk(grant, ker string) string {
	return `{"label":"Lab A","template":"vgt","params":{"ker":"` + ker + `"},"resources":{"cpus":4},"grid":[],"note":"","grant":"` + grant + `"}`
}

// forgeGrantPage has the (compromised) group server hand the key holder's computer these requests, as filed by bea under grant.
func (r *grantEnv) forgeGrantPage(reqs []relay.Request) {
	last := reqs[len(reqs)-1].Change
	b, _ := json.Marshal(relay.Page{Workspace: "Main workspace", Cursor: last, Requests: reqs})
	empty, _ := json.Marshal(relay.Page{Workspace: "Main workspace", Cursor: last, Requests: []relay.Request{}})
	r.forge.set(func(after string) []byte {
		if after == itoa(int(last)) {
			return empty
		}
		return b
	})
}

func (r *grantEnv) forged(id int64, grant, requesterID string, params map[string]string, created time.Time, mut func(*relay.Ask)) relay.Request {
	if r.base == nil { // the base as the server names it, from a real request outside every grant
		sid := r.file(singleAsk)
		r.pass()
		r.base = r.mirrorOf(sid).Request.Base
	}
	a := relay.Ask{Label: relayLabel, Template: "vgt", Params: params, Grid: []relay.GridKey{}, Grant: grant}
	if mut != nil {
		mut(&a)
	}
	return relay.Request{ID: id, Change: id, State: relay.StateRequested, Requester: "bea", RequesterID: requesterID, Ask: a,
		Base: r.base, CreatedAt: created, ExpiresAt: created.Add(72 * time.Hour), Notes: []relay.FollowUp{}}
}

// The whole path: ada grants bea a standing approval; bea's request inside it is submitted by ada's computer with no click, through
// the same template, claimed on the server bound to the script's hash with the grant's id, audited, and shown to ada afterwards
// with the exact script. Nothing about the host crosses to the server: the offer names no path, host or commit.
func TestStandingApprovalSubmitsWithoutAClickAndRecordsIt(t *testing.T) {
	r := newGrantEnv(t)
	// the collaborator is chosen from the workspace's members, asked of the server
	st, b := callJSON(t, "GET", r.api("/api/relay/local/members"), nil)
	if st != 200 || !strings.Contains(string(b), r.beaID) || strings.Contains(string(b), r.adaID) {
		t.Fatalf("members: %d %s", st, b)
	}
	g := r.grant(nil)
	if !g.Live || g.CommitBasis != "profile" || len(g.Commit) != 40 || g.HostName != "Cluster" {
		t.Fatalf("grant: %+v", g)
	}
	// bea sees the offer, without a path, host or commit; another member would not
	w := r.g.do(req{path: "/api/run-inboxes", cookie: r.bea})
	if !strings.Contains(w.Body.String(), g.ID) || strings.Contains(w.Body.String(), r.cubes) || strings.Contains(w.Body.String(), g.Commit) {
		t.Fatalf("bea's inboxes: %s", w.Body)
	}
	// the preview says it goes through the grant
	w = r.g.do(req{method: "POST", path: "/api/run-requests/preview", cookie: r.bea,
		body: `{"addressee":"` + r.adaID + `","ask":` + grantAsk(g.ID, "6") + `,"baseItemId":` + itoa(int(r.baseItem)) + `,"expiresInDays":7}`})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"route":"grant"`) {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	w = r.g.do(req{method: "POST", path: "/api/run-requests/preview", cookie: r.bea,
		body: `{"addressee":"` + r.adaID + `","ask":` + grantAsk(g.ID, "12") + `,"baseItemId":` + itoa(int(r.baseItem)) + `,"expiresInDays":7}`})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"route":"review"`) || !strings.Contains(w.Body.String(), "ker") {
		t.Fatalf("preview outside: %d %s", w.Code, w.Body)
	}

	before := r.submissions()
	id := r.file(grantAsk(g.ID, "6"))
	r.pass()
	r.srv.waitRecords()
	if n := r.submissions(); n != before+1 {
		t.Fatalf("a request inside the grant submitted %d jobs", n-before)
	}
	m := r.mirrorOf(id)
	if m.LocalState != relay.LocalApproved || m.GrantID != g.ID || m.GrantState != relay.GrantStateUsed {
		t.Fatalf("mirror: %+v", m)
	}
	d := r.detail(m.ID)
	j := r.job(d.Job.ID)
	if j.State == "draft" || !strings.Contains(j.Script, "ker = 6") || !strings.Contains(j.Note, "standing approval "+g.ID[:8]) {
		t.Fatalf("job: state %s note %q", j.State, j.Note)
	}
	// the script insists on the grant's commit (the profile's): the base run gave none
	if !strings.Contains(j.Script, g.Commit) {
		t.Fatalf("the script does not insist on the grant's commit %s", g.Commit)
	}
	s := r.onServer(id)
	if s.Request.Approval == nil || s.Request.Approval.ScriptHash != j.ScriptHash || s.Request.Approval.Grant != g.ID {
		t.Fatalf("server claim: %+v", s.Request.Approval)
	}
	var detail string
	if err := r.srv.Store.DB().QueryRow(`SELECT detail FROM audit WHERE action='run request submitted under a standing approval'`).Scan(&detail); err != nil || !strings.Contains(detail, g.ID) || !strings.Contains(detail, j.ScriptHash) {
		t.Fatalf("audit: %q %v", detail, err)
	}
	if u := r.grantNow(g.ID); u.JobsUsed != 1 || u.CoreMinutesUsed != 4*120 || u.AtOnce != 1 || len(u.OpenJobs) != 1 {
		t.Fatalf("usage: %+v", u)
	}
	// nothing of the host crossed for the relay
	for _, x := range r.wire.exchanges() {
		if strings.Contains(x.url, "/api/relay/") && (bytes.Contains(x.body, []byte(r.cubes)) || bytes.Contains(x.body, []byte(r.remoteRoot))) {
			t.Errorf("%s %s carried a host path: %s", x.method, x.url, x.body)
		}
	}
}

// What the grant does not cover waits for the key holder, with the reason here and only a fixed code on the server.
func TestOutsideTheGrantWaitsForTheKeyHolderAndSaysWhy(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(nil)
	before := r.submissions()
	cases := []struct {
		ask, code string
	}{
		{grantAsk(g.ID, "12"), relay.GrantOutside}, // ker inside the inbox, outside the grant
		{`{"label":"Lab A","template":"vgt","params":{"dn":"16"},"resources":{"cpus":4},"grid":[],"note":"","grant":"` + g.ID + `"}`, relay.GrantOutside},  // a key the grant does not cover
		{`{"label":"Lab A","template":"vgt","params":{"ker":"6"},"resources":{"cpus":12},"grid":[],"note":"","grant":"` + g.ID + `"}`, relay.GrantOutside}, // over the per-job CPUs
		{`{"label":"Lab A","template":"vgt","params":{},"resources":{},"grid":[{"key":"ker","values":["4","6"]}],"note":"","grant":"` + g.ID + `"}`, relay.GrantGrid},
	}
	for _, c := range cases {
		id := r.file(c.ask)
		r.pass()
		m := r.mirrorOf(id)
		if m.LocalState == relay.LocalApproved || m.GrantState != c.code || !strings.HasPrefix(m.GrantNote, "Waiting for you") {
			t.Errorf("%s: %+v", c.ask, m)
		}
		r.pass()
		s := r.onServer(id)
		if s.Request.State != relay.StateReview {
			t.Errorf("%s: server state %s", c.ask, s.Request.State)
		}
		found := false
		for _, ev := range s.Events {
			found = found || (ev.Event == "review" && strings.Contains(ev.Detail, relay.GrantCodeText[c.code]))
		}
		if !found {
			t.Errorf("%s: the server was not told why: %+v", c.ask, s.Events)
		}
	}
	if n := r.submissions(); n != before {
		t.Fatalf("requests outside the grant submitted %d jobs", n-before)
	}
	// the key holder can still approve one by hand
	id := r.file(grantAsk(g.ID, "12"))
	r.pass()
	m := r.mirrorOf(id)
	d := r.detail(m.ID)
	if st, b := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash}); st != 200 {
		t.Fatalf("manual approval of a request outside the grant: %d %s", st, b)
	}
	// a filing that names a grant offered to someone else is refused on the server
	cid := r.g.invite(workspace.Local, "researcher", "cid")
	w := r.g.do(req{method: "POST", path: "/api/run-requests", cookie: cid,
		body: `{"addressee":"` + r.adaID + `","ask":` + grantAsk(g.ID, "6") + `,"baseItemId":` + itoa(int(r.baseItem)) + `,"expiresInDays":7}`})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "not offered to you") {
		t.Fatalf("filing under another person's grant: %d %s", w.Code, w.Body)
	}
}

// A compromised server can forge anything; the key holder's computer trusts none of it. Forged and other people's grant ids,
// expired and revoked grants, requests filed before the grant, replays and an input outside the grant's folders all wait, and a
// flood of forged requests inside the grant stops at its budget.
func TestForgedGrantRequestsStopAtTheGrantsOwnTerms(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxJobs": 2, "maxAtOnce": 2})
	other := r.grant(map[string]any{"requesterId": r.adaID, "requesterName": "ada"})
	revoked := r.grant(nil)
	if st, b := callJSON(t, "POST", r.api("/api/relay/local/grants/"+revoked.ID+"/revoke"), map[string]any{}); st != 200 {
		t.Fatalf("revoke: %d %s", st, b)
	}
	r.forge.forgedFrom = 9000
	now := r.g.clock.now().UTC()
	in := map[string]string{"ker": "6"}
	r.forged(0, "", "", nil, now, nil) // fetches the base
	before := r.submissions()
	reqs := []relay.Request{
		r.forged(9001, strings.Repeat("ab", 16), r.beaID, in, now, nil),                    // a grant this computer does not have
		r.forged(9002, other.ID, r.beaID, in, now, nil),                                    // another person's grant
		r.forged(9003, revoked.ID, r.beaID, in, now, nil),                                  // revoked
		r.forged(9004, g.ID, r.beaID, in, g.CreatedAt.Add(-time.Hour), nil),                // filed before the grant: an old request replayed under it
		r.forged(9005, g.ID, r.beaID, map[string]string{"ker": "16"}, now, nil),            // outside the narrowed range
		r.forged(9006, g.ID, r.beaID, map[string]string{"input_file": "/etc/x"}, now, nil), // a path: refused before any draft
		r.forged(9007, g.ID, r.beaID, in, now, nil),                                        // inside
		r.forged(9008, g.ID, r.beaID, in, now, nil),                                        // inside
		r.forged(9009, g.ID, r.beaID, in, now, nil),                                        // inside, but the budget has two jobs
	}
	r.forgeGrantPage(reqs)
	for i := 0; i < 3; i++ {
		r.pass()
	}
	r.srv.waitRecords()
	got := map[int64]relay.Mirrored{}
	for _, m := range r.mirrors() {
		got[m.ServerID] = m
	}
	want := map[int64]string{9001: relay.GrantUnknown, 9002: relay.GrantUnknown, 9003: relay.GrantRevoked, 9004: relay.GrantStale, 9005: relay.GrantOutside,
		9009: relay.GrantBudget}
	for id, code := range want {
		if m := got[id]; m.GrantState != code || m.LocalState == relay.LocalApproved {
			t.Errorf("request %d: want %s, got %+v", id, code, m)
		}
	}
	if m, ok := got[9006]; ok && m.LocalState != relay.LocalRefused && m.LocalState != relay.LocalClosed {
		t.Errorf("a path in a forged request: %+v", m)
	}
	for _, id := range []int64{9007, 9008} {
		if m := got[id]; m.LocalState != relay.LocalApproved || m.GrantState != relay.GrantStateUsed {
			t.Errorf("request %d inside the grant: %+v", id, m)
		}
	}
	if n := r.submissions(); n != before+2 {
		t.Fatalf("forged requests submitted %d jobs; the grant allows 2", n-before)
	}
	// a replay of the same requests submits nothing more
	r.forgeGrantPage(reqs)
	r.pass()
	if n := r.submissions(); n != before+2 {
		t.Fatalf("a replay submitted again: %d", n-before)
	}
	if u := r.grantNow(g.ID); u.JobsUsed != 2 {
		t.Fatalf("usage after the flood: %+v", u)
	}
	// expired: the clock moves past the grant's end
	g2 := r.grant(nil)
	later := r.g.clock.now().Add(31 * 24 * time.Hour)
	r.srv.RelayNow, r.srv.Relayed.Now = func() time.Time { return later }, func() time.Time { return later }
	r.forgeGrantPage([]relay.Request{r.forged(9010, g2.ID, r.beaID, in, later.Add(-time.Minute), nil)})
	r.pass()
	if m := r.mirrorOf(9010); m.GrantState != relay.GrantExpired || m.LocalState == relay.LocalApproved {
		t.Fatalf("a request under an expired grant: %+v", m)
	}
	if n := r.submissions(); n != before+2 {
		t.Fatalf("an expired grant submitted: %d", n-before)
	}
}

// The base run's input must lie in the grant's folders; the grant's folders must lie in the host's source roots and be clean.
func TestGrantInputsAreCheckedAtCreationAndAtUse(t *testing.T) {
	r := newGrantEnv(t)
	for _, roots := range [][]string{{r.cubes + "/../secret"}, {"/etc"}, {r.cubes + "/"}, {"relative/cubes"}} {
		st, b := callJSON(t, "POST", r.api("/api/relay/local/grants"), r.grantBody(map[string]any{"roots": roots}))
		if st != 422 {
			t.Errorf("roots %v: %d %s", roots, st, b)
		}
	}
	// wider than the inbox (ker 2..16) or a key the inbox does not list
	for _, keys := range []any{
		[]map[string]any{{"key": "ker", "min": 1, "max": 8}},
		[]map[string]any{{"key": "ker", "min": 4, "max": 32}},
		[]map[string]any{{"key": "noise", "min": 0, "max": 1}},
		[]map[string]any{{"key": "dn", "choices": []string{"8", "12"}}},
	} {
		st, b := callJSON(t, "POST", r.api("/api/relay/local/grants"), r.grantBody(map[string]any{"keys": keys}))
		if st != 422 {
			t.Errorf("keys %v: %d %s", keys, st, b)
		}
	}
	// a grant on another folder of the root: the base run's input is outside it
	g := r.grant(map[string]any{"roots": []string{r.remoteRoot + "/other"}})
	before := r.submissions()
	id := r.file(grantAsk(g.ID, "6"))
	r.pass()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantInput || r.submissions() != before {
		t.Fatalf("input outside the grant: %+v", m)
	}
	// a grant on the exact file
	g = r.grant(map[string]any{"roots": []string{}, "files": []string{r.cubes + "/hnv1.fits"}})
	id = r.file(grantAsk(g.ID, "6"))
	r.pass()
	r.srv.waitRecords()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantStateUsed || r.submissions() != before+1 {
		t.Fatalf("input on the grant's file: %+v", m)
	}
}

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("SIMPLE  =                    T"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// The input is judged where it really is. A collaborator who can write in a granted folder puts a link there to other data the key
// holder's account reads inside the host's roots: the link's name is inside the grant, its target is not, so the request waits with
// the resolved path in the reason (and only the code on the server). A link to another place inside the grant is fine. A grant made
// through an alias of its folder is stored resolved, so the real input still fits it; a granted file that links out of the host's
// roots is refused when the grant is made.
func TestGrantInputIsCheckedThroughItsLinks(t *testing.T) {
	r := newGrantEnv(t)
	secret, deep := r.remoteRoot+"/secret", r.cubes+"/deep"
	for _, d := range []string{secret, deep} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, secret+"/other.fits")
	writeFile(t, deep+"/hnv1.fits")
	input := r.cubes + "/hnv1.fits"
	alias := r.remoteRoot + "/lab-cubes" // another name for the granted folder
	symlink(t, r.cubes, alias)
	g := r.grant(map[string]any{"roots": []string{alias}})
	if len(g.Roots) != 1 || g.Roots[0] != r.cubes {
		t.Fatalf("the grant's folder is not stored resolved: %v", g.Roots)
	}
	before := r.submissions()

	symlink(t, secret+"/other.fits", input)
	id := r.file(grantAsk(g.ID, "6"))
	r.pass()
	m := r.mirrorOf(id)
	if m.GrantState != relay.GrantInput || m.LocalState == relay.LocalApproved || r.submissions() != before {
		t.Fatalf("a link out of the grant went through: %+v", m)
	}
	if !strings.Contains(m.GrantNote, "resolves on the host to") || !strings.Contains(m.GrantNote, secret+"/other.fits") {
		t.Fatalf("the reason does not say where the input really is: %q", m.GrantNote)
	}
	for _, x := range r.wire.exchanges() {
		if strings.Contains(x.url, "/api/relay/") && bytes.Contains(x.body, []byte(secret)) {
			t.Errorf("%s %s carried the resolved path: %s", x.method, x.url, x.body)
		}
	}

	symlink(t, deep+"/hnv1.fits", input)
	id = r.file(grantAsk(g.ID, "6"))
	r.pass()
	r.srv.waitRecords()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantStateUsed || r.submissions() != before+1 {
		t.Fatalf("a link to another place inside the grant: %+v", m)
	}

	// a host that does not answer is not a verdict: the request waits as pending and goes once the host answers again
	r.srv.Remote.Close() // the next worker call starts a new ssh, which the stand-in refuses while DOWN exists
	writeFile(t, r.home+"/DOWN")
	id = r.file(grantAsk(g.ID, "6"))
	r.pass()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantStatePending || r.submissions() != before+1 {
		t.Fatalf("an unreachable host: %+v", m)
	}
	if err := os.Remove(r.home + "/DOWN"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.srv.waitRecords()
	if m := r.mirrorOf(id); m.GrantState != relay.GrantStateUsed || r.submissions() != before+2 {
		t.Fatalf("after the host answered again: %+v", m)
	}

	outside := t.TempDir() + "/elsewhere.fits"
	writeFile(t, outside)
	symlink(t, outside, r.cubes+"/away.fits")
	if st, b := callJSON(t, "POST", r.api("/api/relay/local/grants"), r.grantBody(map[string]any{"roots": []string{}, "files": []string{r.cubes + "/away.fits"}})); st != 422 {
		t.Fatalf("a granted file that links out of the host's roots: %d %s", st, b)
	}
}

// Revoking is immediate, lists the jobs still queued or running under the grant and cancels none; the offer leaves the server.
// The jobs-at-once limit holds a request until a slot frees.
func TestRevokeIsImmediateAndCancelsNothing(t *testing.T) {
	r := newGrantEnv(t)
	g := r.grant(map[string]any{"maxAtOnce": 1})
	before := r.submissions()
	first := r.file(grantAsk(g.ID, "6"))
	second := r.file(grantAsk(g.ID, "8"))
	r.pass()
	if m := r.mirrorOf(second); m.GrantState != relay.GrantStateWaiting || r.submissions() != before+1 {
		t.Fatalf("the second request with one slot: %+v", m)
	}
	// the first job ends: the slot frees and the second goes
	j := r.job(*r.mirrorOf(first).JobID)
	r.end(j.ID, "COMPLETED")
	r.pass()
	r.srv.waitRecords()
	if m := r.mirrorOf(second); m.GrantState != relay.GrantStateUsed || r.submissions() != before+2 {
		t.Fatalf("after the slot freed: %+v", m)
	}
	cancels := r.sshLines("scancel")
	st, b := callJSON(t, "POST", r.api("/api/relay/local/grants/"+g.ID+"/revoke"), map[string]any{})
	var out grantOut
	json.Unmarshal(b, &out)
	if st != 200 || out.Live || out.RevokedAt == nil || len(out.OpenJobs) != 1 {
		t.Fatalf("revoke: %d %s", st, b)
	}
	if r.sshLines("scancel") != cancels {
		t.Fatal("revoking cancelled a job")
	}
	third := r.file(grantAsk(g.ID, "6"))
	_ = third
	r.pass()
	r.pass()
	w := r.g.do(req{path: "/api/run-inboxes", cookie: r.bea})
	if strings.Contains(w.Body.String(), g.ID) {
		t.Fatalf("the revoked grant is still offered: %s", w.Body)
	}
	if r.submissions() != before+2 {
		t.Fatal("a request under a revoked grant was submitted")
	}
}
