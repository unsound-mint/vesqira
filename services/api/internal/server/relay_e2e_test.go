package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ADR 0066 section 3, slice 5: a member asks a key holder's computer to run a job; the key holder reads the script and approves.
// These tests run a real group server on TLS, a local instance on the stand-in Slurm host of jobwatch_test.go (every ssh command
// is logged, so "nothing was submitted" is a count of sbatch lines), and the local instance's background pass called by hand.

// forger lets a test answer the pull itself, as a compromised group server would.
type forger struct {
	mu   sync.Mutex
	next http.RoundTripper
	page func(after string) []byte // non-nil: answers GET /api/relay/requests
	// forged ids at or above forgedFrom: the answers to review, decline and claim come from the forger too, as they would from a
	// compromised server, which accepts whatever it is sent
	forgedFrom int64
	// gone: the server answers 404 to every POST about this request, as when it no longer has it
	gone int64
}

func (f *forger) RoundTrip(q *http.Request) (*http.Response, error) {
	f.mu.Lock()
	page := f.page
	f.mu.Unlock()
	if page != nil && q.Method == "GET" && q.URL.Path == "/api/relay/requests" && q.URL.Query().Get("after") != fmt.Sprint(int64(1<<63-1)) {
		if b := page(q.URL.Query().Get("after")); b != nil {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b)), Request: q}, nil
		}
	}
	if f.gone > 0 && q.Method == "POST" {
		for _, pat := range []string{"/api/relay/requests/%d/", "/api/run-requests/%d/"} {
			var id int64
			if n, _ := fmt.Sscanf(q.URL.Path, pat, &id); n == 1 && id == f.gone {
				return &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`)), Request: q}, nil
			}
		}
	}
	if f.forgedFrom > 0 && q.Method == "POST" {
		for _, pat := range []string{"/api/relay/requests/%d/", "/api/run-requests/%d/"} {
			var id int64
			if n, _ := fmt.Sscanf(q.URL.Path, pat, &id); n == 1 && id >= f.forgedFrom {
				return f.answer(q, id)
			}
		}
	}
	return f.next.RoundTrip(q)
}

func (f *forger) answer(q *http.Request, id int64) (*http.Response, error) {
	state := relay.StateReview
	switch {
	case strings.HasSuffix(q.URL.Path, "/decline"):
		state = relay.StateDeclined
	case strings.HasSuffix(q.URL.Path, "/approve"):
		state = relay.StateApproved
	case strings.HasSuffix(q.URL.Path, "/status"):
		state = relay.StateSubmitted
	}
	b, _ := json.Marshal(map[string]any{"id": id, "state": state})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b)), Request: q}, nil
}

func (f *forger) set(p func(string) []byte) { f.mu.Lock(); f.page = p; f.mu.Unlock() }

type relayEnv struct {
	*watchEnv
	g             *group
	wire          *wire
	forge         *forger
	ada, bea      string // sessions on the group server: the key holder and a researcher in the same workspace
	adaID, beaID  string
	token         string // ada's push+relay token, held by her computer
	baseJob       jobs.Job
	baseRun       int64
	baseItem      int64 // the base run as pushed to the group server
	label         string
	relayExchange func() []exchange
}

const relayLabel = "Lab A"

func newRelayEnv(t *testing.T) *relayEnv {
	t.Helper()
	g, gts, _ := pushGroup(t)
	rs, err := relay.Open(g.cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	rs.Now = g.clock.now
	g.s.Relay = rs
	g.h = g.s.Handler()
	ada, _ := g.setupAdmin("ada")
	bea := g.invite(workspace.Local, "researcher", "bea")
	w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"laptop","scopes":["push","relay"],"days":30}`, cookie: ada})
	if w.Code != 201 {
		t.Fatalf("mint: %d %s", w.Code, w.Body)
	}
	token := decode[struct {
		Token string `json:"token"`
	}](t, w).Token

	e := newWatchEnv(t)
	base := t.TempDir()
	ob, err := outbox.Open(e.srv.Store.DB(), filepath.Join(base, "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	rl, err := relay.OpenLocal(e.srv.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	wr := &wire{base: gts.Client().Transport}
	fg := &forger{next: wr}
	e.srv.Outbox, e.srv.Relayed, e.srv.GroupKeyFile = ob, rl, filepath.Join(base, "group.key")
	e.srv.GroupHTTP = &http.Client{Transport: fg, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ts := httptest.NewServer(e.srv.Handler()) // mounted again now that the relay stores are set
	t.Cleanup(ts.Close)
	e.api = func(p string) string { return ts.URL + p }
	if st, b := callJSON(t, "POST", e.api("/api/group-link"), map[string]string{"origin": g.origin, "token": token}); st != 200 {
		t.Fatalf("link: %d %s", st, b)
	}

	// the base: a job of ada's that finished and became a run, pushed to the group as in slice 4
	j := e.submit(e.draft("vgt-base", nil), nil)
	e.writeRun(j.OutputDir)
	e.end(j.ID, "COMPLETED")
	j = e.job(j.ID)
	if j.RunID == nil {
		t.Fatalf("the base job was not imported: %s", j.Detail)
	}
	st, b := callJSON(t, "POST", e.api("/api/outbox/preflight"), map[string]any{"kind": "run", "id": *j.RunID})
	if st != 200 {
		t.Fatalf("preflight: %d %s", st, b)
	}
	var pf struct {
		Hash string `json:"hash"`
	}
	json.Unmarshal(b, &pf)
	if st, b := callJSON(t, "POST", e.api("/api/outbox"), map[string]any{"kind": "run", "id": *j.RunID, "hash": pf.Hash}); st != 202 {
		t.Fatalf("enqueue: %d %s", st, b)
	}
	e.srv.OutboxPass(context.Background())
	list := decode[struct {
		Items []pushedSummary `json:"items"`
	}](t, g.do(req{path: "/api/pushed", cookie: bea}))
	if len(list.Items) != 1 {
		t.Fatalf("pushed: %+v", list.Items)
	}
	r := &relayEnv{watchEnv: e, g: g, wire: wr, forge: fg, ada: ada, bea: bea, adaID: g.mustAccount("ada").ID, beaID: g.mustAccount("bea").ID, token: token,
		baseJob: j, baseRun: *j.RunID, baseItem: list.Items[0].ID, label: relayLabel}
	r.setInbox(`[{"key":"ker","min":2,"max":16},{"key":"dn","choices":["8","16","24"]}]`, `{"maxCpus":16,"maxMemGiB":32,"maxTimeMinutes":240}`)
	r.pass()
	return r
}

func (r *relayEnv) setInbox(keys, limits string) {
	r.t.Helper()
	body := `{"inbox":{"paused":false,"labels":[{"label":"` + r.label + `","templates":[{"template":"vgt","keys":` + keys + `,"limits":` + limits + `}]}]},"hosts":{"` + r.label + `":` + itoa(int(r.host.ID)) + `}}`
	q, _ := http.NewRequest("PUT", r.api("/api/relay/local/inbox"), strings.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(q)
	if err != nil {
		r.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		r.t.Fatalf("inbox: %d %s", resp.StatusCode, b)
	}
}

func (r *relayEnv) pass() { r.srv.RelayPass(context.Background()) }

// file has bea file a request to ada; it returns the server's id.
func (r *relayEnv) file(ask string) int64 {
	r.t.Helper()
	w := r.g.do(req{method: "POST", path: "/api/run-requests", cookie: r.bea,
		body: `{"addressee":"` + r.adaID + `","ask":` + ask + `,"baseItemId":` + itoa(int(r.baseItem)) + `,"expiresInDays":7}`})
	if w.Code != 201 {
		r.t.Fatalf("file: %d %s", w.Code, w.Body)
	}
	return decode[struct {
		ID int64 `json:"id"`
	}](r.t, w).ID
}

type serverRequest struct {
	Request struct {
		relay.Row
		RunItemID *int64 `json:"runItemId"`
	} `json:"request"`
	Events []struct {
		Actor  string `json:"actor"`
		Via    string `json:"via"`
		Event  string `json:"event"`
		Detail string `json:"detail"`
	} `json:"events"`
}

func (r *relayEnv) onServer(id int64) serverRequest {
	r.t.Helper()
	w := r.g.do(req{path: "/api/run-requests/" + itoa(int(id)), cookie: r.bea})
	if w.Code != 200 {
		r.t.Fatalf("server request: %d %s", w.Code, w.Body)
	}
	return decode[serverRequest](r.t, w)
}

func (r *relayEnv) mirrors() []relay.Mirrored {
	r.t.Helper()
	_, b := callJSON(r.t, "GET", r.api("/api/relay/local"), nil)
	var out struct {
		Requests []relay.Mirrored `json:"requests"`
	}
	json.Unmarshal(b, &out)
	return out.Requests
}

func (r *relayEnv) mirrorOf(serverID int64) relay.Mirrored {
	r.t.Helper()
	for _, m := range r.mirrors() {
		if m.ServerID == serverID {
			return m
		}
	}
	r.t.Fatalf("request %d is not on the key holder's computer", serverID)
	return relay.Mirrored{}
}

type relayedDetail struct {
	Request       relay.Mirrored `json:"request"`
	OutsideBounds string         `json:"outsideBounds"`
	Job           *struct {
		ID         int64  `json:"id"`
		State      string `json:"state"`
		Script     string `json:"script"`
		ScriptHash string `json:"scriptHash"`
	} `json:"job"`
	Sweep *struct {
		ID        int64  `json:"id"`
		SweepHash string `json:"sweepHash"`
		Drafts    []struct {
			JobID int64 `json:"jobId"`
		} `json:"drafts"`
	} `json:"sweep"`
}

func (r *relayEnv) detail(localID int64) relayedDetail {
	r.t.Helper()
	st, b := callJSON(r.t, "GET", r.api("/api/relayed/"+itoa(int(localID))), nil)
	if st != 200 {
		r.t.Fatalf("detail: %d %s", st, b)
	}
	var d relayedDetail
	json.Unmarshal(b, &d)
	return d
}

func (r *relayEnv) approve(localID int64, body map[string]any) (int, string) {
	st, b := callJSON(r.t, "POST", r.api("/api/relayed/"+itoa(int(localID))+"/approve"), body)
	return st, string(b)
}

const singleAsk = `{"label":"Lab A","template":"vgt","params":{"ker":"8"},"resources":{"cpus":4},"grid":[],"note":"ker 8 on the LMC block, please"}`

// The whole path: filed on the server, pulled and drafted on the key holder's computer, read and approved there (the claim on the
// server comes first, bound to the hash), submitted, reported back, and the run pushed back through the slice-4 queue.
func TestRelayedRequestEndToEndNeedsTheKeyHoldersApprovalOfTheScript(t *testing.T) {
	r := newRelayEnv(t)
	before := r.submissions()
	id := r.file(singleAsk)
	if s := r.onServer(id); s.Request.State != relay.StateRequested {
		t.Fatalf("filed as %q", s.Request.State)
	}
	// pulled and drafted from ada's own base run; several passes later nothing has been submitted: there is no auto-approval
	for i := 0; i < 4; i++ {
		r.pass()
	}
	m := r.mirrorOf(id)
	if m.LocalState != relay.LocalDraft || m.JobID == nil || m.BaseRunID == nil || *m.BaseRunID != r.baseRun || m.BaseChosen {
		t.Fatalf("mirror: %+v", m)
	}
	if n := r.submissions(); n != before {
		t.Fatalf("a request was submitted without approval: %d sbatch", n-before)
	}
	if s := r.onServer(id); s.Request.State != relay.StateReview {
		t.Fatalf("server state after drafting: %q", s.Request.State)
	}
	d := r.detail(m.ID)
	draft := r.job(d.Job.ID)
	if d.Job == nil || d.Job.State != "draft" || draft.OutputDir == r.baseJob.OutputDir || !strings.Contains(d.Job.Script, "ker = 8") {
		t.Fatalf("draft: %+v (output %s, base %s)", d.Job, draft.OutputDir, r.baseJob.OutputDir)
	}
	if !strings.Contains(draft.Script, "/shared/home/u/vgt") {
		t.Fatalf("the draft does not use the key holder's repository: %s", draft.Script)
	}

	// a direct submission of the relayed draft is refused: only the approval path submits it, after the claim
	if st, b := callJSON(t, "POST", r.api("/api/jobs/"+itoa(int(d.Job.ID))+"/submit"), map[string]any{"scriptHash": d.Job.ScriptHash}); st != 409 {
		t.Fatalf("direct submit of a relayed draft: %d %s", st, b)
	}
	// a hash other than the script read is refused here, and nothing reaches the server or Slurm
	if st, b := r.approve(m.ID, map[string]any{"scriptHash": strings.Repeat("a", 64)}); st != 409 {
		t.Fatalf("approval with another hash: %d %s", st, b)
	}
	// the server unreachable: no claim, so no submission
	r.wire.setDown(true)
	if st, b := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash}); st != 502 || !strings.Contains(b, "server_unreachable") {
		t.Fatalf("approval with the server down: %d %s", st, b)
	}
	r.wire.setDown(false)
	if n := r.submissions(); n != before {
		t.Fatalf("submitted without a claim: %d", n-before)
	}
	if s := r.onServer(id); s.Request.State != relay.StateReview || s.Request.Approval != nil {
		t.Fatalf("server after the refused approvals: %+v", s.Request)
	}

	if st, b := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash}); st != 200 {
		t.Fatalf("approve: %d %s", st, b)
	}
	r.srv.waitRecords()
	if n := r.submissions(); n != before+1 {
		t.Fatalf("approval submitted %d jobs", n-before)
	}
	s := r.onServer(id)
	if s.Request.Approval == nil || s.Request.Approval.ScriptHash != d.Job.ScriptHash {
		t.Fatalf("the claim is not bound to the hash read: %+v", s.Request.Approval)
	}
	if s.Request.State != relay.StateSubmitted {
		t.Fatalf("after approval the server says %q", s.Request.State)
	}
	// approving twice submits nothing more
	if st, _ := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash}); st == 200 && r.submissions() != before+1 {
		t.Fatal("a second approval submitted again")
	}

	// finished: the status flows back, and the run comes back through the push queue
	e := r.job(d.Job.ID)
	r.writeRun(e.OutputDir)
	r.end(e.ID, "COMPLETED")
	r.pass()
	r.srv.OutboxPass(context.Background())
	r.pass()
	s = r.onServer(id)
	if s.Request.State != relay.StateFinished {
		t.Fatalf("after the job finished the server says %q", s.Request.State)
	}
	if s.Request.RunItemID == nil {
		t.Fatalf("the run did not come back through the push queue: %+v", s.Request)
	}
	// every transition is recorded with who and through what
	seen := map[string]string{}
	for _, ev := range s.Events {
		if ev.Actor == "" || ev.Via == "" {
			t.Errorf("an event without actor or via: %+v", ev)
		}
		seen[ev.Event] = ev.Via
	}
	statuses := ""
	for _, ev := range s.Events {
		if ev.Event == "status" {
			statuses += ev.Detail + ";"
		}
	}
	if !strings.Contains(statuses, "submitted") || !strings.Contains(statuses, "finished") {
		t.Errorf("status events: %q", statuses)
	}
	for _, want := range []string{"filed", "review", "approved"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("no %q event: %+v", want, s.Events)
		}
	}
	if seen["filed"] != "session" || !strings.HasPrefix(seen["approved"], "token") {
		t.Errorf("via: %+v", seen)
	}

	// what crossed for the relay names no path, script, host or Slurm id of the key holder's
	for _, x := range r.wire.exchanges() {
		if !strings.Contains(x.url, "/api/relay/") {
			continue
		}
		for _, mark := range []string{e.OutputDir, "/shared/home/u/vgt", "login1", e.SlurmID, "#SBATCH", r.remoteRoot} {
			if mark != "" && bytes.Contains(x.body, []byte(mark)) {
				t.Errorf("%s %s carried %q: %s", x.method, x.url, mark, x.body)
			}
		}
	}
}

// Every value a forged page can carry is checked again on the key holder's computer: values outside the whitelist or the inbox,
// paths and shell, labels that stand for no host and malformed text are refused, nothing is drafted for them and Slurm is never
// asked. A forged request that passes every check is drafted and still waits for the key holder: there is never auto-approval.
func TestForgedRequestsFromACompromisedServerNeverRunAnything(t *testing.T) {
	r := newRelayEnv(t)
	sshBefore := r.sshLines("")
	jobsBefore, _ := r.jobs.List()
	now := time.Unix(1_800_000_000, 0).UTC()
	req := func(id int64, ask relay.Ask, requester string) relay.Request {
		return relay.Request{ID: id, Change: id, State: relay.StateRequested, Requester: requester, Ask: ask, CreatedAt: now, ExpiresAt: now.Add(72 * time.Hour), Notes: []relay.FollowUp{}}
	}
	ask := func(params map[string]string, mut func(*relay.Ask)) relay.Ask {
		a := relay.Ask{Label: relayLabel, Template: "vgt", Params: params, Grid: []relay.GridKey{}}
		if mut != nil {
			mut(&a)
		}
		return a
	}
	forged := []relay.Request{
		req(9001, ask(map[string]string{"ker": "99"}, nil), "Eve"),                                         // outside the bound
		req(9002, ask(map[string]string{"input_file": "/etc/passwd"}, nil), "Eve"),                         // a path
		req(9003, ask(map[string]string{"dn": "$(id)"}, nil), "Eve"),                                       // shell
		req(9004, ask(map[string]string{"ker": "8;rm -rf ~"}, nil), "Eve"),                                 // shell
		req(9005, ask(map[string]string{"ker": "8"}, func(a *relay.Ask) { a.Label = "Other" }), "Eve"),     // a label with no host
		req(9006, ask(map[string]string{"ker": "8"}, func(a *relay.Ask) { a.Template = "custom" }), "Eve"), // no custom script
		req(9007, ask(map[string]string{"ker": "8"}, nil), "Eve\x1b[2J"),                                   // control characters
		req(9008, ask(map[string]string{"ker": "8"}, func(a *relay.Ask) { a.Resources.CPUs = 512 }), "Eve"),
		req(9009, ask(map[string]string{"ker": "8"}, nil), "Eve"), // passes every check
	}
	r.forge.forgedFrom = 9000
	page := relay.Page{Workspace: "Main workspace", Cursor: 9009, Requests: forged}
	b, _ := json.Marshal(page)
	r.forge.set(func(after string) []byte {
		if after == "9009" {
			p, _ := json.Marshal(relay.Page{Workspace: "Main workspace", Cursor: 9009, Requests: []relay.Request{}})
			return p
		}
		return b
	})
	for i := 0; i < 4; i++ {
		r.pass()
	}
	got := map[int64]relay.Mirrored{}
	for _, m := range r.mirrors() {
		got[m.ServerID] = m
	}
	for _, f := range forged[:8] {
		m, ok := got[f.ID]
		// refused here, then declined on the (forged) server with the refusal as the note, so closed
		if ok && (m.Refusal == "" || m.JobID != nil || m.SweepID != nil || (m.LocalState != relay.LocalRefused && m.LocalState != relay.LocalClosed)) {
			t.Errorf("forged request %d was accepted: %+v", f.ID, m)
		}
	}
	last, ok := got[9009]
	if !ok || last.LocalState == relay.LocalApproved {
		t.Fatalf("the valid forged request: %+v", last)
	}
	jobsAfter, _ := r.jobs.List()
	// the base belongs to no request, so the valid forged request (whose base is unknown here) is received and drafts nothing
	// until the key holder chooses a base; nothing else is created and the host is never asked
	if len(jobsAfter) != len(jobsBefore) {
		t.Fatalf("forged requests created %d jobs", len(jobsAfter)-len(jobsBefore))
	}
	if n := r.sshLines(""); n != sshBefore {
		t.Fatalf("forged requests reached the host: %d commands", n-sshBefore)
	}
	// the key holder chooses a base for it: a draft, still waiting, still nothing submitted
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(last.ID))+"/draft"), map[string]any{"baseRunId": r.baseRun}); st != 200 {
		t.Fatalf("draft: %d %s", st, b)
	}
	before := r.submissions()
	for i := 0; i < 3; i++ {
		r.pass()
	}
	if m := r.mirrorOf(9009); m.LocalState != relay.LocalDraft || r.submissions() != before {
		t.Fatalf("a forged request moved past the draft by itself: %+v", m)
	}
	// a forged page that says it was approved closes the mirror; it does not submit
	approved := forged[8]
	approved.State, approved.Change = relay.StateApproved, 9010
	b2, _ := json.Marshal(relay.Page{Workspace: "Main workspace", Cursor: 9010, Requests: []relay.Request{approved}})
	r.forge.set(func(after string) []byte {
		if after == "9010" {
			p, _ := json.Marshal(relay.Page{Workspace: "Main workspace", Cursor: 9010, Requests: []relay.Request{}})
			return p
		}
		return b2
	})
	r.pass()
	if m := r.mirrorOf(9009); m.LocalState == relay.LocalApproved || r.submissions() != before {
		t.Fatalf("a page saying approved approved it here: %+v", m)
	}
}

// The relay token is narrow: it pulls and answers the requests addressed to its account in its workspace, and nothing else.
func TestRelayTokenCannotReadContentFileRequestsOrActElsewhere(t *testing.T) {
	r := newRelayEnv(t)
	g := r.g
	id := r.file(singleAsk)
	// content and the member routes are not for a token
	for _, q := range []req{
		{path: "/api/pushed"},
		{path: "/api/pushed/" + itoa(int(r.baseItem))},
		{path: "/api/run-requests"},
		{path: "/api/run-requests/" + itoa(int(id))},
		{path: "/api/run-inboxes"},
		{method: "POST", path: "/api/run-requests", body: `{"addressee":"` + r.beaID + `","ask":` + singleAsk + `}`},
		{method: "POST", path: "/api/run-requests/" + itoa(int(id)) + "/withdraw", body: `{}`},
		{method: "POST", path: "/api/run-requests/" + itoa(int(id)) + "/note", body: `{"text":"x"}`},
	} {
		if w := g.bearer(r.token, q); w.Code != 403 && w.Code != 401 {
			t.Errorf("relay token on %s %s: %d %s", q.method, q.path, w.Code, w.Body)
		}
	}
	// a session cannot use the token routes
	if w := g.do(req{path: "/api/relay/requests?after=0", cookie: r.ada}); w.Code == 200 {
		t.Errorf("a session pulled requests: %s", w.Body)
	}
	// a push-only token cannot pull
	w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"push only","scopes":["push"],"days":30}`, cookie: r.ada})
	pushOnly := decode[struct {
		Token string `json:"token"`
	}](t, w).Token
	if w := g.bearer(pushOnly, req{path: "/api/relay/requests?after=0"}); w.Code != 403 {
		t.Errorf("a push token pulled: %d", w.Code)
	}
	// another workspace: the token is bound to the one it was minted in, whatever the header says
	ada := g.mustAccount("ada")
	bws, err := g.acc.CreateWorkspace("B", ada.ID, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := g.bearer(r.token, req{path: "/api/relay/requests?after=0", hdr: map[string]string{WorkspaceHeader: itoa(int(bws.ID))}}); w.Code == 200 {
		var p relay.Page
		json.Unmarshal(w.Body.Bytes(), &p)
		if p.Workspace == "B" {
			t.Errorf("the token acted in another workspace: %s", w.Body)
		}
	}
	// a request in B addressed to ada is not in the token's pull, and its id cannot be claimed or declined through the token
	if err := r.g.s.Relay.PutInbox(bws.ID, ada.ID, relay.Actor{ID: ada.ID, Via: "token", Instance: "elsewhere"}, relay.Inbox{Labels: []relay.Label{{Label: relayLabel,
		Templates: []relay.TemplateBounds{{Template: "vgt", Keys: []relay.Bound{{Key: "ker"}}}}}}}); err != nil {
		t.Fatal(err)
	}
	gid, err := r.g.s.Relay.File(bws.ID, ada.ID, ada.ID, relay.Ask{Label: relayLabel, Template: "vgt", Params: map[string]string{"ker": "8"}, Grid: []relay.GridKey{}}, nil, 3, relay.Actor{ID: ada.ID, Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	w = g.bearer(r.token, req{path: "/api/relay/requests?after=0"})
	if bytes.Contains(w.Body.Bytes(), []byte(fmt.Sprintf(`"id":%d,`, gid.ID))) && gid.ID != id {
		t.Errorf("a request of another workspace was pulled: %s", w.Body)
	}
	claim := `{"instance":"x","scriptHash":"` + strings.Repeat("b", 64) + `","params":{"ker":"8"},"resources":{},"label":"Lab A","baseChosen":true,"editedLines":0,"autoPush":false}`
	for _, p := range []string{"/api/relay/requests/" + itoa(int(gid.ID)) + "/approve", "/api/relay/requests/" + itoa(int(gid.ID)) + "/review", "/api/run-requests/" + itoa(int(gid.ID)) + "/decline"} {
		if w := g.bearer(r.token, req{method: "POST", path: p, body: claim}); w.Code != 404 && w.Code != 403 {
			t.Errorf("token acted on another workspace's request through %s: %d %s", p, w.Code, w.Body)
		}
	}
	// a token claim for a different instance than the one that published the inbox is refused
	if w := g.bearer(r.token, req{method: "POST", path: "/api/relay/requests/" + itoa(int(id)) + "/approve", body: claim}); w.Code == 200 {
		t.Errorf("a claim from an unknown instance was accepted: %s", w.Body)
	}
	// revoked: refused at once
	toks := decode[struct {
		Tokens []accounts.Token `json:"tokens"`
	}](t, g.do(req{path: "/api/account/tokens", cookie: r.ada}))
	for _, tk := range toks.Tokens {
		g.do(req{method: "POST", path: "/api/account/tokens/" + tk.ID + "/revoke", body: `{}`, cookie: r.ada})
	}
	if w := g.bearer(r.token, req{path: "/api/relay/requests?after=0"}); w.Code != 401 {
		t.Errorf("a revoked token pulled: %d", w.Code)
	}
}

// A withdrawal that lands between the key holder's reading and the approval wins: the claim is refused and nothing is submitted.
// An expiry does the same.
func TestWithdrawalAndExpiryWinOverALateApproval(t *testing.T) {
	r := newRelayEnv(t)
	for _, end := range []string{"withdraw", "expire"} {
		id := r.file(singleAsk)
		r.pass()
		m := r.mirrorOf(id)
		d := r.detail(m.ID)
		if d.Job == nil {
			t.Fatalf("%s: no draft: %+v", end, m)
		}
		before := r.submissions()
		if end == "withdraw" {
			if w := r.g.do(req{method: "POST", path: "/api/run-requests/" + itoa(int(id)) + "/withdraw", body: `{}`, cookie: r.bea}); w.Code != 200 {
				t.Fatalf("withdraw: %d %s", w.Code, w.Body)
			}
		} else {
			r.g.clock.add(8 * 24 * time.Hour)
		}
		st, b := r.approve(m.ID, map[string]any{"scriptHash": d.Job.ScriptHash})
		if st == 200 || r.submissions() != before {
			t.Fatalf("%s: the late approval went through: %d %s", end, st, b)
		}
		if !strings.Contains(b, "not_claimed") && !strings.Contains(b, "state") {
			t.Errorf("%s: the refusal does not say the claim failed: %s", end, b)
		}
		r.pass()
		if m := r.mirrorOf(id); m.LocalState == relay.LocalApproved || relay.IsOpen(m.ServerState) {
			t.Errorf("%s: mirror after the race: %+v", end, m)
		}
	}
}

// Declining sends the note; the requester sees it and nothing is submitted.
func TestDeclineSendsTheNoteAndSubmitsNothing(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(singleAsk)
	r.pass()
	m := r.mirrorOf(id)
	before := r.submissions()
	if st, b := callJSON(t, "POST", r.api("/api/relayed/"+itoa(int(m.ID))+"/decline"), map[string]string{"note": "the queue is full this week"}); st != 200 {
		t.Fatalf("decline: %d %s", st, b)
	}
	s := r.onServer(id)
	if s.Request.State != relay.StateDeclined || s.Request.DeclineNote != "the queue is full this week" || r.submissions() != before {
		t.Fatalf("after declining: %+v", s.Request)
	}
	if d := r.detail(m.ID); d.Job != nil && d.Job.State != "draft" {
		t.Fatalf("the declined draft moved: %+v", d.Job)
	}
}

// A grid is an ADR 0048 sweep: one approval over the sweep hash of the kept drafts, the concurrency the key holder sets, at most
// 64 combinations, and nothing submitted before the claim.
func TestGridRequestIsOneSweepApprovedOnceOverItsHash(t *testing.T) {
	r := newRelayEnv(t)
	id := r.file(`{"label":"Lab A","template":"vgt","params":{},"resources":{},"grid":[{"key":"ker","values":["2","4","8"]},{"key":"dn","values":["8","16"]}],"note":""}`)
	r.pass()
	m := r.mirrorOf(id)
	if m.SweepID == nil || m.LocalState != relay.LocalDraft {
		t.Fatalf("mirror: %+v", m)
	}
	d := r.detail(m.ID)
	if d.Sweep == nil || len(d.Sweep.Drafts) != 6 {
		t.Fatalf("sweep: %+v", d.Sweep)
	}
	before := r.submissions()
	if st, b := r.approve(m.ID, map[string]any{"sweepHash": strings.Repeat("c", 64), "concurrency": 2}); st != 409 || r.submissions() != before {
		t.Fatalf("a sweep approved over another hash: %d %s", st, b)
	}
	if st, b := r.approve(m.ID, map[string]any{"sweepHash": d.Sweep.SweepHash, "concurrency": 17}); st != 422 || r.submissions() != before {
		t.Fatalf("concurrency 17: %d %s", st, b)
	}
	if st, b := r.approve(m.ID, map[string]any{"sweepHash": d.Sweep.SweepHash, "concurrency": 2}); st != 200 {
		t.Fatalf("approve: %d %s", st, b)
	}
	r.srv.waitSweeps()
	r.srv.waitRecords()
	if n := r.submissions() - before; n != 2 {
		t.Fatalf("concurrency 2 submitted %d", n)
	}
	s := r.onServer(id)
	if s.Request.Approval == nil || s.Request.Approval.SweepHash != d.Sweep.SweepHash {
		t.Fatalf("approval: %+v", s.Request.Approval)
	}
}

// Values outside the whitelist or the inbox are refused when filed, before anything reaches the key holder.
func TestServerRefusesOutOfBoundsAndNonWhitelistedValues(t *testing.T) {
	r := newRelayEnv(t)
	for _, ask := range []string{
		`{"label":"Lab A","template":"vgt","params":{"ker":"99"},"resources":{},"grid":[],"note":""}`,
		`{"label":"Lab A","template":"vgt","params":{"input_file":"/etc/passwd"},"resources":{},"grid":[],"note":""}`,
		`{"label":"Lab A","template":"vgt","params":{"dn":"$(id)"},"resources":{},"grid":[],"note":""}`,
		`{"label":"Lab A","template":"vgt","params":{"ker":"8"},"resources":{"cpus":64},"grid":[],"note":""}`,
		`{"label":"Lab A","template":"custom","params":{},"resources":{},"grid":[],"note":""}`,
		`{"label":"Lab A","template":"vgt","params":{},"resources":{},"grid":[],"note":"","script":"#!/bin/sh"}`,
		`{"label":"Lab A","template":"vgt","params":{},"resources":{},"grid":[],"note":"","outputDir":"/tmp"}`,
	} {
		w := r.g.do(req{method: "POST", path: "/api/run-requests", cookie: r.bea, body: `{"addressee":"` + r.adaID + `","ask":` + ask + `}`})
		if w.Code == 201 {
			t.Errorf("filed: %s", ask)
		}
	}
	list := decode[struct {
		Requests []relay.Row `json:"requests"`
	}](t, r.g.do(req{path: "/api/run-requests", cookie: r.bea}))
	if len(list.Requests) != 0 {
		t.Fatalf("refused requests were stored: %+v", list.Requests)
	}
}

// Local mode is unchanged: the group server's relay routes do not exist on a local instance and the local routes do not exist on
// the group server.
func TestRelayRoutesBelongToTheirSide(t *testing.T) {
	r := newRelayEnv(t)
	for _, p := range []string{"/api/relay/requests?after=0", "/api/run-requests", "/api/run-inboxes"} {
		if st, _ := callJSON(t, "GET", r.api(p), nil); st != 404 {
			t.Errorf("local %s: %d", p, st)
		}
	}
	for _, p := range []string{"/api/relay/local", "/api/relayed/1"} {
		if w := r.g.do(req{path: p, cookie: r.ada}); w.Code != 404 {
			t.Errorf("group %s: %d", p, w.Code)
		}
	}
}
