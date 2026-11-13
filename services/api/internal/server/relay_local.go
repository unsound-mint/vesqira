package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Relayed run requests (ADR 0066 section 3), the key holder's side. This computer publishes the inbox the key holder typed (labels,
// templates, bounds; the host each label stands for stays here), pulls the requests addressed to its account through the relay
// token, validates each one again against its own inbox and whitelist (the server may be compromised), and renders an accepted one
// into an ordinary draft with the existing templates: inputs, repository and config from a run the key holder already ran, the
// output folder always theirs. Nothing is ever approved by itself. The key holder reads the exact script and approves; approving
// claims the request on the server first, bound to that script's hash, and only a successful claim lets submitDraft send it under
// the key holder's own account. Status flows back as enumerated words with a sequence number. All of it is local-only
// (authz.LocalOnly): the group server never mounts these routes.

func (s *Server) relayedRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/relay/local", authz.LocalOnly, s.relayLocalState)
	s.route(mux, "PUT /api/relay/local/inbox", authz.LocalOnly, s.relayLocalInbox)
	s.route(mux, "POST /api/relay/local/poll", authz.LocalOnly, s.relayLocalPoll)
	s.route(mux, "GET /api/relayed/{id}", authz.LocalOnly, s.getRelayed)
	s.route(mux, "POST /api/relayed/{id}/draft", authz.LocalOnly, s.draftRelayed)
	s.route(mux, "POST /api/relayed/{id}/approve", authz.LocalOnly, s.approveRelayed)
	s.route(mux, "POST /api/relayed/{id}/decline", authz.LocalOnly, s.declineRelayed)
	s.route(mux, "POST /api/relayed/{id}/detach", authz.LocalOnly, s.detachRelayed)
	s.relayGrantLocalRoutes(mux)
}

// relayState is the pull loop's state.
type relayState struct {
	relayOnce sync.Once
	relayKick chan struct{}
	relayMu   sync.Mutex // one pass, approval or decline at a time
	// RelayNow is the loop's wall clock (tests move it); nil is time.Now.
	RelayNow func() time.Time
	// relayAudits caps refusal audit rows: refused filings on the group server, refused pulled requests here
	relayAudits auditBudget
}

func (s *Server) relayKicker() chan struct{} {
	s.relayOnce.Do(func() { s.relayKick = make(chan struct{}, 1) })
	return s.relayKick
}

// kickRelay asks the pull loop to run now; it never blocks.
func (s *Server) kickRelay() {
	select {
	case s.relayKicker() <- struct{}{}:
	default:
	}
}

// relayWait is the wait before the next pull: a minute while the server answers, doubling with each pull it did not answer up to
// 15 minutes (ADR 0043's backoff); any answer resets it.
func relayWait(failures int) time.Duration {
	if failures <= 0 {
		return time.Minute
	}
	return backoff(failures)
}

// woke reports a wake from sleep: the wall clock moved further than the monotonic clock (which stops while the machine sleeps)
// since the last check, by more than the slack.
func woke(wallBefore, monoBefore, now time.Time, slack time.Duration) bool {
	wall := now.Round(0).Sub(wallBefore.Round(0))
	mono := now.Sub(monoBefore)
	return wall-mono > slack
}

// RunRelay pulls until ctx ends: at start, every minute while the server answers (backing off to 15 minutes while it does not),
// at once after a wake from sleep, and when kicked (a link, an inbox change, the Check now button). A missed pull changes nothing.
func (s *Server) RunRelay(ctx context.Context) {
	const tick = 10 * time.Second
	last := time.Now()
	s.RelayPass(ctx)
	for {
		before := time.Now()
		select {
		case <-ctx.Done():
			return
		case <-s.relayKicker():
		case <-time.After(tick):
			now := time.Now()
			failures := 0
			if l, err := s.Outbox.Link(); err == nil {
				if st, err := s.Relayed.State(l.Origin); err == nil {
					failures = st.Failures
				}
			}
			if !woke(before, before, now, 2*tick) && now.Sub(last) < relayWait(failures) {
				continue
			}
		}
		last = time.Now()
		s.RelayPass(ctx)
	}
}

// RelayPass publishes the inbox when it changed, pulls every page of requests that changed, tells the server which drafts are
// waiting for review and which requests this computer refused, and reports the status of approved ones. Tests call it directly.
func (s *Server) RelayPass(ctx context.Context) {
	if s.Relayed == nil || s.Outbox == nil {
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	c, l, err := s.groupClient()
	if err != nil {
		return
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil || !st.Configured {
		return
	}
	failed := func(err error) {
		msg := err.Error()
		var ge *groupError
		if errors.As(err, &ge) && ge.Status == 403 {
			msg = "the group server does not accept this computer's token for run requests: link this computer again with \"take run requests\" chosen"
		}
		_ = s.Relayed.Failed(l.Origin, msg)
	}
	// a failed publish or pull still lets this computer tell the server about drafts and report on approved jobs below: a page
	// refused whole must not also stop the status of work already running
	pull := true
	if !st.Published {
		if err := c.do("PUT", "/api/relay/inbox", "application/json", mustJSONBytes(map[string]any{"instance": l.Instance, "inbox": s.publishedInbox(l.Origin, st)}), nil); err != nil {
			failed(err)
			pull = false
		} else {
			_ = s.Relayed.MarkPublished(l.Origin)
		}
	}
	cursor := st.Cursor
	for pages := 0; pull && pages < 20 && ctx.Err() == nil; pages++ {
		var raw json.RawMessage
		if err := c.do("GET", "/api/relay/requests?after="+strconv.FormatInt(cursor, 10), "", nil, &raw); err != nil {
			failed(err)
			break
		}
		page, err := relay.DecodePage(raw, cursor)
		if err != nil {
			failed(fmt.Errorf("the server's answer was refused whole: %s", relay.Sentence(err)))
			break
		}
		full := false
		for _, req := range page.Requests {
			if err := s.receive(ctx, l, st, page.Workspace, req); errors.Is(err, relay.ErrFull) {
				failed(errors.New(relay.Sentence(err)))
				full = true
				break
			}
		}
		if full {
			break
		}
		cursor = page.Cursor
		if err := s.Relayed.Answered(l.Origin, cursor); err != nil {
			log.Printf("relay: %v", err)
			break
		}
		if len(page.Requests) < relay.MaxPage {
			break
		}
	}
	s.applyGrants(ctx, c, l)
	s.tellServer(c, l)
	s.reportAll(c, l)
}

func mustJSONBytes(v any) []byte { b, _ := json.Marshal(v); return b }

// receive records one pulled request after this computer's own validation, and renders the draft at once when the base resolves
// through this computer's own push records; otherwise the key holder chooses the base.
func (s *Server) receive(ctx context.Context, l outbox.Link, st relay.LocalState, wsName string, req relay.Request) error {
	refusal := ""
	if err := relay.CheckRequest(st.Inbox, st.Hosts, req); err != nil {
		refusal = relay.Sentence(err)
	}
	m, created, err := s.Relayed.Upsert(l.Origin, req, refusal)
	if err != nil {
		log.Printf("relay: %v", err)
		return err
	}
	if created && refusal != "" && s.relayAudits.take("refused "+l.Origin, time.Now()) {
		s.audit(localCtx(ctx), "run request refused here", "relayed", m.ID, fmt.Sprintf("request %d from %s: %s", req.ID, req.Requester, refusal))
	}
	if created && m.LocalState == relay.LocalReceived {
		s.audit(localCtx(ctx), "run request received", "relayed", m.ID, fmt.Sprintf("request %d from %s in %s", req.ID, req.Requester, wsName))
		if runID, ok := s.ownBase(l, req); ok {
			if aerr := s.renderRelayed(ctx, m, runID, false, nil, relay.Resources{}); aerr != nil {
				log.Printf("relay: request %d not drafted: %s", req.ID, aerr.Msg)
			}
		}
	}
	return nil
}

// ownBase resolves a request's base through this computer's push records: the base must have been pushed by this instance, its
// lineage must be a run here, and that run must have been made by a job that kept its request.
func (s *Server) ownBase(l outbox.Link, req relay.Request) (int64, bool) {
	if req.Base == nil || req.Base.Instance != l.Instance {
		return 0, false
	}
	kind, runID, err := s.Outbox.Origin(req.Base.Origin)
	if err != nil || kind != "run" {
		return 0, false
	}
	if _, err := s.Jobs.ForRun(runID); err != nil {
		return 0, false
	}
	return runID, true
}

// renderRelayed renders a request into a draft (or an ADR 0048 sweep for a grid) on the host its label stands for, from the job
// that made the base run: the repository, config and input are the key holder's, the output folder is a fresh one of theirs, and
// only the request's whitelisted values (with the key holder's changes) and resources differ. Every value is validated again here.
func (s *Server) renderRelayed(ctx context.Context, m relay.Mirrored, baseRunID int64, chosen bool, changes map[string]string, lower relay.Resources) *apiErr {
	if s.Jobs == nil || s.HostDB == nil {
		return &apiErr{404, "jobs are not available on this computer"}
	}
	if m.LocalState != relay.LocalReceived && m.LocalState != relay.LocalDraft {
		return &apiErr{409, "this request can no longer be drafted"}
	}
	if !relay.IsOpen(m.ServerState) {
		return &apiErr{409, "this request is " + m.ServerState + " on the group server"}
	}
	l, err := s.Outbox.Link()
	if err != nil || l.Origin != m.Origin() {
		return &apiErr{409, errNotLinked.Error()}
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil {
		return &apiErr{500, "internal error"}
	}
	ask := m.Request.Ask
	if aerr := stillAcceptable(st, m); aerr != nil {
		return aerr
	}
	hostID := st.Hosts[strings.TrimSpace(ask.Label)]
	h, err := s.HostDB.Get(hostID)
	if err != nil {
		return &apiErr{409, "the host the label " + ask.Label + " stands for no longer exists; set the label's host again"}
	}
	base, err := s.Jobs.ForRun(baseRunID)
	if err != nil {
		return &apiErr{422, "this run was not made by a job here, so how it was set up is not known; choose a run your jobs made"}
	}
	if base.Template != ask.Template {
		return &apiErr{422, "the request asks for " + ask.Template + "; choose a " + ask.Template + " run as its base"}
	}
	if base.HostID != h.ID {
		return &apiErr{422, "the base ran on another host than the one the label " + ask.Label + " stands for"}
	}
	var req jobs.VGTRequest
	var ok bool
	if base.Template == "rohsa" {
		req, ok = base.ROHSARequest()
	} else {
		req, ok = base.VGTRequest()
	}
	if !ok || req.BaseConfigText == "" {
		return &apiErr{422, "the base job kept no settings to start from; choose another run"}
	}
	params := map[string]string{}
	for k, v := range ask.Params {
		params[k] = v
	}
	for k, v := range changes {
		if !relay.Settable(ask.Template, k) {
			return &apiErr{422, k + " cannot be set for a request"}
		}
		if err := jobs.CheckTemplateSetting(ask.Template, k, v); err != nil {
			return &apiErr{422, err.Error()}
		}
		for _, g := range ask.Grid {
			if g.Key == k {
				return &apiErr{422, k + " is swept by the request; drop combinations instead"}
			}
		}
		params[k] = v
	}
	res := ask.Resources
	for _, p := range []struct{ low, ask *int }{{&lower.CPUs, &res.CPUs}, {&lower.MemGiB, &res.MemGiB}, {&lower.TimeMinutes, &res.TimeMinutes}} {
		if *p.low < 0 || (*p.ask > 0 && *p.low > *p.ask) {
			return &apiErr{422, "resources can only be lowered from what the request asks for"}
		}
		if *p.low > 0 {
			*p.ask = *p.low
		}
	}
	mem, tm := relay.ResourceText(res)
	name := fmt.Sprintf("request-%d", m.ServerID)
	note := fmt.Sprintf("Run request %d from %s, relayed through the group server.", m.ServerID, m.Request.Requester)
	if ask.Note != "" {
		note += " Their note: " + ask.Note
	}
	if utf8.RuneCountInString(note) > jobs.MaxNote {
		note = string([]rune(note)[:jobs.MaxNote])
	}
	ref := fmt.Sprintf("request %d from %s", m.ServerID, m.Request.Requester)
	rev, mode := s.relayPin(baseRunID)
	pinProfile := req.PinProfile
	if rev == "" {
		rev, pinProfile = s.grantPin(m, rev, pinProfile)
	}
	draft := relay.DraftValues{Params: params, Resources: res, Label: strings.TrimSpace(ask.Label)}
	ctx = localCtx(ctx)
	if len(ask.Grid) == 0 {
		set := map[string]string{}
		for k, v := range jobs.CarriedSet(base.Template, req.Set) {
			set[k] = v
		}
		for k, v := range params {
			set[k] = v
		}
		cpus := req.CPUs
		if res.CPUs > 0 {
			cpus = res.CPUs
		}
		if mem == "" {
			mem = req.Mem
		}
		if tm == "" {
			tm = req.Time
		}
		b, aerr := s.buildJob(ctx, h, base.Template, name, draftInput{RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, ConfigText: req.BaseConfigText, Set: set,
			CPUs: cpus, Mem: mem, Time: tm, Partition: req.Partition, Account: req.Account, HashInput: req.HashInput, RerunMode: mode, ExpectRev: rev, PinProfile: pinProfile})
		if aerr != nil {
			return aerr
		}
		parent := baseRunID
		created, err := s.Jobs.CreateDraft(jobs.Job{HostID: h.ID, Name: name, Template: base.Template, Script: b.Script, OutputDir: b.OutputDir, Request: b.Request,
			ParentRunID: &parent, Note: note, AutoImport: true})
		if err != nil {
			return jobAPIErr(err)
		}
		if err := s.Jobs.SetRequestRef(created.ID, ref); err != nil {
			s.Jobs.DeleteDrafts([]int64{created.ID})
			return jobAPIErr(err)
		}
		if err := s.Relayed.SetDraft(m.ID, &created.ID, nil, baseRunID, chosen, draft); err != nil {
			s.Jobs.DeleteDrafts([]int64{created.ID})
			return &apiErr{409, relay.Sentence(err)}
		}
		s.dropOldDraft(m)
		s.audit(ctx, "run request drafted", "relayed", m.ID, fmt.Sprintf("job %d from run %d on %s", created.ID, baseRunID, h.Name))
		return nil
	}
	in := sweepIn{Name: name, BaseRunID: &baseRunID, Note: note, fixed: params, cpus: res.CPUs, mem: mem, time: tm, expectRev: rev, rerunMode: mode}
	for _, g := range ask.Grid {
		in.Params = append(in.Params, sweepParamIn{Key: g.Key, Values: g.Values})
	}
	p, aerr := s.plan(ctx, in)
	if aerr != nil {
		return aerr
	}
	if p.host.ID != h.ID {
		return &apiErr{422, "the base ran on another host than the one the label " + ask.Label + " stands for"}
	}
	created, err := s.Jobs.CreateDrafts(p.drafts)
	if err != nil {
		return jobAPIErr(err)
	}
	ids := make([]int64, len(created))
	for i, j := range created {
		ids[i] = j.ID
	}
	sw, err := s.Sweeps.Create(sweeps.Sweep{Name: name, HostID: p.host.ID, BaseJobID: p.base.ID, BaseRunID: p.baseRun, Params: p.params, Note: note}, ids, p.combos)
	if err != nil {
		s.Jobs.DeleteDrafts(ids)
		log.Printf("relay sweep: %v", err)
		return &apiErr{500, "internal error"}
	}
	for _, id := range ids {
		_ = s.Jobs.SetRequestRef(id, ref)
	}
	if err := s.Relayed.SetDraft(m.ID, nil, &sw.ID, baseRunID, chosen, draft); err != nil {
		s.Jobs.DeleteDrafts(ids)
		return &apiErr{409, relay.Sentence(err)}
	}
	s.dropOldDraft(m)
	s.audit(ctx, "run request drafted", "relayed", m.ID, fmt.Sprintf("sweep %d of %d drafts from run %d on %s", sw.ID, len(ids), baseRunID, h.Name))
	return nil
}

// relayPin is the code a relayed draft runs (ADR 0054 applied to ADR 0066 section 3): the requester asked about the base run, so
// the draft insists on the base run's commit as a strict rerun does, and the job stops before the program starts unless the code
// folder is at it with no changed tracked file. The settings differ from the base by design, so the mode is adapted, which marks
// them, rather than strict, which refuses them. No commit when the base's code is not described by one (strictRev says why;
// relayCode shows it to the key holder).
func (s *Server) relayPin(baseRunID int64) (rev, mode string) {
	if s.Runs == nil {
		return "", rerunAdapted
	}
	b, err := s.parentBaseline(workspace.Local, baseRunID)
	if err != nil {
		return "", rerunAdapted
	}
	rev, _, _ = b.strictRev()
	return rev, rerunAdapted
}

// relayCode says, for the approval view, which code the draft runs: the commit its script insists on, or why it insists on none.
func (s *Server) relayCode(m relay.Mirrored, j jobs.Job) string {
	req, haveReq := j.ConfigRequest()
	if why := s.grantPinWhy(m, req); haveReq && why != "" {
		return why
	}
	if haveReq && req.ExpectCommit != "" {
		return "The script insists on commit " + req.ExpectCommit + ", the base run's: the job stops before the program starts unless the code folder on the host is at that commit with no changed tracked file."
	}
	if haveReq && req.PinProfile {
		// the base's own pin is carried into the draft: the base run's code is not described by one commit, but the job that made it
		// insisted on the profile's
		return s.codePinOf(j.Template, req).Why + " The base run's own code is not described by one commit here (" + s.relayWhyUnpinned(m) + ")."
	}
	return "Not pinned to a commit: " + s.relayWhyUnpinned(m) + ". The job runs the code as it is on the host when it starts."
}

// relayWhyUnpinned is why the base run's code gave no commit to insist on.
func (s *Server) relayWhyUnpinned(m relay.Mirrored) string {
	why := "the base run is not known here"
	if m.BaseRunID != nil && s.Runs != nil {
		if b, err := s.parentBaseline(workspace.Local, *m.BaseRunID); err == nil {
			if _, _, w := b.strictRev(); w != "" {
				why = w
			}
		}
	}
	return why
}

// stillAcceptable is what this computer requires of a request it accepted, at drafting and approval: its values pass the whitelist
// and the template validators, and its label still stands for a host here. A bound the key holder lowered since is left to their
// judgement (ADR 0066 section 3): outsideBounds tells them, it does not refuse.
func stillAcceptable(st relay.LocalState, m relay.Mirrored) *apiErr {
	if err := relay.CheckAskWhitelist(m.Request.Ask); err != nil {
		return &apiErr{422, "this computer refuses the request: " + relay.Sentence(err)}
	}
	if _, ok := st.Hosts[strings.TrimSpace(m.Request.Ask.Label)]; !ok {
		return &apiErr{422, "the label " + m.Request.Ask.Label + " no longer stands for a host on this computer; set it again under Run requests"}
	}
	return nil
}

// outsideBounds says when a request is outside the inbox as it is now (a bound lowered after it arrived), "" when it is inside.
func outsideBounds(st relay.LocalState, m relay.Mirrored) string {
	if err := relay.CheckAsk(st.Inbox, m.Request.Ask); err != nil {
		return "Outside your current inbox: " + relay.Sentence(err) + " It arrived within the bounds of the time; approving it is your judgement."
	}
	return ""
}

// dropOldDraft deletes the drafts of an earlier rendering of the same request (the key holder chose another base or changed a value).
func (s *Server) dropOldDraft(m relay.Mirrored) {
	if m.JobID != nil {
		s.Jobs.DeleteDrafts([]int64{*m.JobID})
	}
	if m.SweepID != nil {
		if ms, err := s.Sweeps.Members(*m.SweepID); err == nil {
			ids := []int64{}
			for _, x := range ms {
				ids = append(ids, x.JobID)
			}
			s.Jobs.DeleteDrafts(ids)
		}
	}
}

// tellServer says which drafts are waiting for review and declines what this computer refused, with the reason as the note.
func (s *Server) tellServer(c *groupClient, l outbox.Link) {
	ms, err := s.Relayed.Pending(l.Origin)
	if err != nil {
		return
	}
	for i, m := range ms {
		if i == maxTellsPerPass {
			break // the rest go in the next pass; a server sending many refusable requests gets as many calls back, slowly
		}
		if m.GrantState == relay.GrantStateWaiting || m.GrantState == relay.GrantStatePending {
			continue // still to be decided under its standing approval; the review then carries the reason it fell back
		}
		path := fmt.Sprintf("/api/relay/requests/%d/review", m.ServerID)
		var body any = relay.Review{Instance: l.Instance, Grant: grantCodeOf(m)}
		if m.LocalState == relay.LocalRefused {
			path = fmt.Sprintf("/api/run-requests/%d/decline", m.ServerID)
			body = relay.Decline{Instance: l.Instance, Note: "Refused by the key holder's computer: " + m.Refusal}
		}
		var out struct {
			State string `json:"state"`
		}
		err := c.postJSON(path, body, &out)
		var ge *groupError
		switch {
		case err == nil:
			_ = s.Relayed.SetServerState(m.ID, out.State)
		case errors.As(err, &ge) && ge.Status == 409:
			_ = s.Relayed.SetServerState(m.ID, conflictState(ge))
		case errors.As(err, &ge) && ge.Status == 404:
			_ = s.Relayed.SetLocalState(m.ID, relay.LocalClosed)
		}
	}
}

const maxTellsPerPass = 50

// conflictState is the state a 409 named; a request the server will not move is treated as closed here.
func conflictState(ge *groupError) string {
	if ge.State != "" {
		return ge.State
	}
	return relay.StateExpired
}

// ---- status flowing back ----

// relayReport is the status this computer reports for an approved request now: enumerated words only (ADR 0066 section 3).
func (s *Server) relayReport(m relay.Mirrored) relay.Report {
	rep := relay.Report{State: relay.StateApproved, RunHash: m.RunHash}
	if m.JobID != nil {
		j, err := s.Jobs.Get(*m.JobID)
		if err != nil {
			rep.State, rep.Code = relay.StateUnknown, relay.CodeGone
			return rep
		}
		rep.State, rep.Code = jobRelayState(j)
		if j.Usage != nil && relayExitRe(j.Usage.ExitCode) && (rep.State == relay.StateFinished || rep.State == relay.StateFailed) {
			rep.Exit = j.Usage.ExitCode
		}
		return rep
	}
	if m.SweepID == nil {
		rep.State, rep.Code = relay.StateUnknown, relay.CodeGone
		return rep
	}
	ms, err := s.Sweeps.Members(*m.SweepID)
	if err != nil {
		rep.State, rep.Code = relay.StateUnknown, relay.CodeGone
		return rep
	}
	rep.Counts = map[string]int{}
	ended, failed, kept := 0, 0, 0
	for _, x := range ms {
		if x.ApprovedHash == "" {
			continue // a combination the key holder dropped: held, never submitted
		}
		kept++
		j, err := s.Jobs.Get(x.JobID)
		st := relay.StateUnknown
		if err == nil {
			st, _ = jobRelayState(j)
			if j.State == jobs.StateDraft {
				st = "draft"
			}
		}
		rep.Counts[st]++
		switch st {
		case relay.StateFinished:
			ended++
		case relay.StateFailed, relay.StateUnknown:
			ended++
			failed++
		}
	}
	switch {
	case kept > 0 && ended == kept && failed > 0:
		rep.State = relay.StateFailed
	case kept > 0 && ended == kept:
		rep.State = relay.StateFinished
	case rep.Counts[relay.StateRunning] > 0:
		rep.State = relay.StateRunning
	case rep.Counts[relay.StateSubmitted] > 0 || ended > 0:
		rep.State = relay.StateSubmitted
	}
	return rep
}

func relayExitRe(s string) bool {
	a, b, ok := strings.Cut(s, ":")
	return ok && len(a) >= 1 && len(a) <= 3 && len(b) >= 1 && len(b) <= 3 && strings.Trim(a, "0123456789") == "" && strings.Trim(b, "0123456789") == ""
}

// jobRelayState maps a local job state to the words the server shows (ADR 0043: finished is never "success").
func jobRelayState(j jobs.Job) (string, string) {
	switch j.State {
	case jobs.StateDraft:
		return relay.StateApproved, relay.CodeSubmissionFailed // approved, and the submission was refused before Slurm queued it
	case jobs.StateSubmitting:
		return relay.StateApproved, relay.CodeNotConfirmed
	case jobs.StateSubmitted:
		return relay.StateSubmitted, ""
	case jobs.StateRunning:
		return relay.StateRunning, ""
	case jobs.StateCompleted:
		return relay.StateFinished, ""
	case jobs.StateCancelled:
		return relay.StateFailed, relay.CodeCancelled
	case jobs.StateFailed:
		if j.Usage != nil && j.Usage.State == "TIMEOUT" {
			return relay.StateFailed, relay.CodeTimeout
		}
		return relay.StateFailed, ""
	}
	return relay.StateUnknown, ""
}

// reportAll reports every approved request whose status changed since it was last accepted, and queues the run line of a finished
// single request for push when the key holder agreed to it at approval.
func (s *Server) reportAll(c *groupClient, l outbox.Link) {
	ms, err := s.Relayed.Reportable(l.Origin)
	if err != nil {
		return
	}
	for _, m := range ms {
		s.autoPushRun(l, &m)
		s.reportOne(c, l, m)
	}
}

func (s *Server) autoPushRun(l outbox.Link, m *relay.Mirrored) {
	if !m.AutoPush || m.RunHash != "" || m.JobID == nil {
		return
	}
	j, err := s.Jobs.Get(*m.JobID)
	if err != nil || j.RunID == nil {
		return
	}
	f, err := s.freezePush(workspace.Local, l, "run", *j.RunID)
	if err != nil {
		log.Printf("relay: run %d not pushed: %v", *j.RunID, err)
		return
	}
	b, err := json.Marshal(f.Push)
	if err != nil {
		return
	}
	if _, err := s.Outbox.Enqueue(workspace.Local, "run", *j.RunID, f.Push.Item.Origin, f.Push.Item.Title, f.Push.Item.Hash, b, f.Assets); err != nil {
		log.Printf("relay: %v", err)
		return
	}
	_ = s.Relayed.SetRunHash(m.ID, f.Push.Item.Hash)
	m.RunHash = f.Push.Item.Hash
	s.kickOutbox()
}

func reportKey(r relay.Report) string {
	keys := make([]string, 0, len(r.Counts))
	for k := range r.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s", r.State, r.Code, r.Exit, r.RunHash)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%d", k, r.Counts[k])
	}
	return b.String()
}

func (s *Server) reportOne(c *groupClient, l outbox.Link, m relay.Mirrored) {
	rep := s.relayReport(m)
	key := reportKey(rep)
	if key == m.Reported || m.Seq >= relay.MaxReports {
		return // nothing new, or the server's cap on reports for one request is reached
	}
	seq, err := s.Relayed.NextSeq(m.ID)
	if err != nil {
		return
	}
	rep.Instance, rep.Seq = l.Instance, seq
	var out struct {
		State string `json:"state"`
	}
	err = c.postJSON(fmt.Sprintf("/api/relay/requests/%d/status", m.ServerID), rep, &out)
	var ge *groupError
	switch {
	case err == nil:
		_ = s.Relayed.SetReported(m.ID, key)
		_ = s.Relayed.SetServerState(m.ID, out.State)
	case errors.As(err, &ge) && ge.Status == 404:
		_ = s.Relayed.SetLocalState(m.ID, relay.LocalClosed) // gone from the server (the account left the workspace): no more reports
	case errors.As(err, &ge) && !ge.transient():
		_ = s.Relayed.SetReported(m.ID, key) // refused for good; sending it again would not change the answer
	}
}

// ---- routes ----

type relayedView struct {
	relay.Mirrored
	Origin   string `json:"origin"`
	JobName  string `json:"jobName,omitempty"`
	JobState string `json:"jobState,omitempty"`
}

func (s *Server) relayedView(m relay.Mirrored) relayedView {
	v := relayedView{Mirrored: m, Origin: m.Origin()}
	if m.JobID != nil {
		if j, err := s.Jobs.Get(*m.JobID); err == nil {
			v.JobName, v.JobState = j.Name, j.State
		}
	}
	return v
}

type relayHost struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Server) relayLocalState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"linked": false, "requests": []relayedView{}, "hosts": []relayHost{}}
	if s.HostDB != nil {
		if hs, err := s.HostDB.List(); err == nil {
			list := []relayHost{}
			for _, h := range hs {
				list = append(list, relayHost{h.ID, h.Name})
			}
			out["hosts"] = list
		}
	}
	l, err := s.Outbox.Link()
	if err != nil {
		writeJSON(w, 200, out)
		return
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	ms, err := s.Relayed.List(l.Origin)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	views := make([]relayedView, 0, len(ms))
	for _, m := range ms {
		views = append(views, s.relayedView(m))
	}
	out["linked"], out["workspace"], out["state"], out["requests"] = true, l.Workspace, st, views
	writeJSON(w, 200, out)
}

// relayLocalInbox stores the inbox the key holder typed and the host each label stands for, after the same checks the server
// runs and the local ones (a label naming a host is refused); the next pull publishes it.
func (s *Server) relayLocalInbox(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Inbox relay.Inbox      `json:"inbox"`
		Hosts map[string]int64 `json:"hosts"`
	}
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	l, err := s.Outbox.Link()
	if err != nil {
		fail(w, 409, errNotLinked.Error())
		return
	}
	if in.Inbox.Labels == nil {
		in.Inbox.Labels = []relay.Label{}
	}
	if err := relay.CheckInbox(in.Inbox); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	var dests []string
	if s.HostDB != nil {
		if hs, err := s.HostDB.List(); err == nil {
			for _, h := range hs {
				dests = append(dests, h.Dest, h.Name)
			}
		}
	}
	hosts := map[string]int64{}
	for _, lb := range in.Inbox.Labels {
		if err := relay.LabelSafe(lb.Label, dests); err != nil {
			fail(w, 422, relay.Sentence(err))
			return
		}
		id, ok := in.Hosts[lb.Label]
		if !ok || s.HostDB == nil {
			fail(w, 422, "choose the host the label "+lb.Label+" stands for")
			return
		}
		if _, err := s.HostDB.Get(id); err != nil {
			fail(w, 422, "the host chosen for "+lb.Label+" does not exist")
			return
		}
		hosts[strings.TrimSpace(lb.Label)] = id
	}
	if err := s.Relayed.SetInbox(l.Origin, in.Inbox, hosts); err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "run request inbox set", "relayed", 0, fmt.Sprintf("%d labels, paused %v", len(in.Inbox.Labels), in.Inbox.Paused))
	s.kickRelay()
	s.relayLocalState(w, r)
}

func (s *Server) relayLocalPoll(w http.ResponseWriter, r *http.Request) {
	s.RelayPass(r.Context())
	s.relayLocalState(w, r)
}

func (s *Server) relayedFrom(w http.ResponseWriter, r *http.Request) (relay.Mirrored, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return relay.Mirrored{}, false
	}
	m, err := s.Relayed.Get(id)
	if err != nil {
		fail(w, 404, "not found")
		return m, false
	}
	return m, true
}

// getRelayed is one request with what the key holder reviews: the exact script and its hash for a single request (and how many
// lines were edited by hand), or every pending draft of the sweep with the sweep hash.
func (s *Server) getRelayed(w http.ResponseWriter, r *http.Request) {
	m, ok := s.relayedFrom(w, r)
	if !ok {
		return
	}
	out := map[string]any{"request": s.relayedView(m), "outsideBounds": ""}
	if l, err := s.Outbox.Link(); err == nil {
		if st, err := s.Relayed.State(l.Origin); err == nil {
			out["outsideBounds"] = outsideBounds(st, m)
		}
	}
	if m.JobID != nil {
		if j, err := s.Jobs.Get(*m.JobID); err == nil {
			out["job"] = map[string]any{"id": j.ID, "name": j.Name, "state": j.State, "script": j.Script, "scriptHash": j.ScriptHash, "editedLines": editedLines(j)}
			out["code"] = s.relayCode(m, j)
		}
	}
	if m.SweepID != nil {
		if sw, err := s.Sweeps.Get(*m.SweepID); err == nil {
			_, pj, _ := s.pendingDrafts(sw)
			type draft struct {
				JobID      int64             `json:"jobId"`
				Name       string            `json:"name"`
				Values     map[string]string `json:"values"`
				ScriptHash string            `json:"scriptHash"`
				Script     string            `json:"script"`
			}
			ms, _ := s.Sweeps.Members(sw.ID)
			vals := map[int64]map[string]string{}
			for _, x := range ms {
				vals[x.JobID] = x.Values
			}
			list := []draft{}
			for _, j := range pj {
				list = append(list, draft{j.ID, j.Name, vals[j.ID], j.ScriptHash, j.Script})
			}
			if len(pj) > 0 {
				out["code"] = s.relayCode(m, pj[0])
			}
			out["sweep"] = map[string]any{"id": sw.ID, "drafts": list, "sweepHash": sweepHash(pj), "view": s.sweepView(sw), "maxConcurrency": relayMaxConcurrency}
		}
	}
	writeJSON(w, 200, out)
}

const relayMaxConcurrency = 16

// editedLines counts the lines of a draft that differ from what its settings render: the key holder's hand edits.
func editedLines(j jobs.Job) int {
	rendered, ok := renderedFromSettings(j)
	if !ok {
		return 0
	}
	return len(sweeps.Diff(rendered, j.Script, nil)) // lines, "-" and "+" together, as the sweep review counts them
}

// draftRelayed renders the draft with a base the key holder chooses (when it did not resolve by itself) and their changes:
// lowered resources or other whitelisted values. A new rendering replaces the earlier drafts.
func (s *Server) draftRelayed(w http.ResponseWriter, r *http.Request) {
	m, ok := s.relayedFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		BaseRunID *int64            `json:"baseRunId"`
		Changes   map[string]string `json:"changes"`
		Resources relay.Resources   `json:"resources"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if m, _ = s.Relayed.Get(m.ID); m.LocalState != relay.LocalReceived && m.LocalState != relay.LocalDraft {
		fail(w, 409, "this request can no longer be drafted")
		return
	}
	baseRunID, chosen := int64(0), true
	switch {
	case in.BaseRunID != nil:
		baseRunID = *in.BaseRunID
		if l, err := s.Outbox.Link(); err == nil {
			if own, ok := s.ownBase(l, m.Request); ok && own == baseRunID {
				chosen = false
			}
		}
	case m.BaseRunID != nil:
		baseRunID, chosen = *m.BaseRunID, m.BaseChosen
	default:
		fail(w, 422, "choose the run this request starts from")
		return
	}
	if aerr := s.renderRelayed(r.Context(), m, baseRunID, chosen, in.Changes, in.Resources); aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	if m.Request.Ask.Grant != "" {
		_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantEdited, "Waiting for you: you drafted it again yourself, so it is no longer submitted under the standing approval.")
	}
	s.kickRelay() // tell the server the draft is waiting for review
	m, _ = s.Relayed.Get(m.ID)
	writeJSON(w, 200, s.relayedView(m))
}

// approveRelayed is the key holder's approval of the script they read. The order is the ADR's: validate again here, check that the
// script (or the sweep's scripts) is still what was read, claim on the server bound to that hash, and only after the claim
// succeeded submit through submitDraft (or approve the sweep for its scheduler). An unreachable server means no claim and no
// submission; the answer says so and offers to keep the draft as the key holder's own job.
func (s *Server) approveRelayed(w http.ResponseWriter, r *http.Request) {
	m, ok := s.relayedFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		ScriptHash    string  `json:"scriptHash"`
		SweepHash     string  `json:"sweepHash"`
		Drop          []int64 `json:"drop"` // sweep drafts the key holder leaves out
		Concurrency   int     `json:"concurrency"`
		StopOnFailure bool    `json:"stopOnFailure"`
		AutoPush      *bool   `json:"autoPush"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	m, _ = s.Relayed.Get(m.ID)
	// an approved request whose draft was never handed on (the claim succeeded, then the submission was refused before Slurm, the
	// sweep's approval was not recorded, or this computer stopped in between) is approved again: the claim is idempotent for the
	// same hash and instance, and only the hash claimed can be submitted
	retry := m.LocalState == relay.LocalApproved
	if m.LocalState != relay.LocalDraft && !retry {
		fail(w, 409, "only a drafted request can be approved")
		return
	}
	c, l, err := s.groupClient()
	if err != nil || l.Origin != m.Origin() {
		fail(w, 409, errNotLinked.Error())
		return
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	if aerr := stillAcceptable(st, m); aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	autoPush := in.AutoPush == nil || *in.AutoPush
	if retry {
		autoPush = m.AutoPush
	}
	if m.SweepID != nil {
		autoPush = false // the runs of a sweep are pushed by hand (deferred: one run line per request crosses today)
	}
	claim := relay.Claim{Instance: l.Instance, Params: m.Draft.Params, Resources: m.Draft.Resources, Label: m.Draft.Label, BaseChosen: m.BaseChosen, AutoPush: autoPush}
	var job jobs.Job
	var kept []jobs.Job
	var sw sweeps.Sweep
	if m.JobID != nil {
		if job, err = s.Jobs.Get(*m.JobID); err != nil || job.State != jobs.StateDraft {
			fail(w, 409, "the draft is gone or no longer a draft; draft the request again")
			return
		}
		if in.ScriptHash != job.ScriptHash {
			fail(w, 409, "the script changed since you read it; read the current script and approve again")
			return
		}
		claim.ScriptHash, claim.EditedLines = job.ScriptHash, editedLines(job)
		if retry && job.ScriptHash != m.ScriptHash {
			fail(w, 409, "the script changed after the request was claimed with another hash; it cannot be submitted for the request")
			return
		}
	} else if m.SweepID != nil {
		if sw, err = s.Sweeps.Get(*m.SweepID); err != nil {
			fail(w, 409, "the sweep is gone; draft the request again")
			return
		}
		_, pj, err := s.pendingDrafts(sw)
		if err != nil || len(pj) == 0 || in.SweepHash != sweepHash(pj) {
			fail(w, 409, "the scripts changed since you read them; review the sweep again")
			return
		}
		drop := map[int64]bool{}
		for _, id := range in.Drop {
			drop[id] = true
		}
		ms, _ := s.Sweeps.Members(sw.ID)
		vals := map[int64]map[string]string{}
		for _, x := range ms {
			vals[x.JobID] = x.Values
		}
		for _, j := range pj {
			if !drop[j.ID] {
				kept = append(kept, j)
				claim.Kept = append(claim.Kept, relay.Combo(vals[j.ID]))
			}
		}
		if len(kept) == 0 {
			fail(w, 422, "keep at least one combination, or decline the request")
			return
		}
		claim.SweepHash = sweepHash(kept)
		if retry && (claim.SweepHash != m.ScriptHash || sw.State != sweeps.StateDraft) {
			fail(w, 409, "the sweep was already approved, or its scripts changed after the request was claimed")
			return
		}
		claim.Concurrency = in.Concurrency
		if claim.Concurrency == 0 {
			claim.Concurrency = sweeps.DefaultConcurrency
		}
	} else {
		fail(w, 409, "draft the request first")
		return
	}
	if err := relay.CheckClaim(m.Request.Ask, claim); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	if err := s.claimOnServer(c, l, m, claim); err != nil {
		s.claimFailed(w, m, err)
		return
	}
	hash := claim.ScriptHash
	if hash == "" {
		hash = claim.SweepHash
	}
	if err := s.Relayed.SetApproved(m.ID, hash, autoPush); err != nil {
		log.Printf("relay: %v", err)
	}
	ctx := localCtx(r.Context())
	s.audit(ctx, "run request approved", "relayed", m.ID, fmt.Sprintf("request %d claimed with %s", m.ServerID, relay.Short(hash)))
	if m.JobID != nil {
		h, err := s.HostDB.Get(job.HostID)
		if err != nil {
			fail(w, 409, "the host no longer exists; the request is approved and nothing was submitted")
			return
		}
		t := true
		_, aerr := s.submitDraft(ctx, job, h, claim.ScriptHash, &t)
		m, _ = s.Relayed.Get(m.ID)
		s.reportOne(c, l, m)
		m, _ = s.Relayed.Get(m.ID)
		if aerr != nil {
			writeJSON(w, 200, map[string]any{"request": s.relayedView(m), "submitted": false, "message": aerr.Msg})
			return
		}
		writeJSON(w, 200, map[string]any{"request": s.relayedView(m), "submitted": true})
		return
	}
	hashes := map[int64]string{}
	for _, j := range kept {
		hashes[j.ID] = j.ScriptHash
	}
	s.sweepMu.Lock()
	won, err := s.Sweeps.Approve(sw.ID, claim.Concurrency, in.StopOnFailure, hashes)
	s.sweepMu.Unlock()
	if err != nil || !won {
		log.Printf("relay sweep approve: %v %v", won, err)
	}
	s.kickSweeps()
	m, _ = s.Relayed.Get(m.ID)
	s.reportOne(c, l, m)
	m, _ = s.Relayed.Get(m.ID)
	writeJSON(w, 200, map[string]any{"request": s.relayedView(m), "submitted": won})
}

// claimFailed answers a claim the server did not grant; nothing was submitted.
func (s *Server) claimFailed(w http.ResponseWriter, m relay.Mirrored, err error) {
	var ge *groupError
	if !errors.As(err, &ge) || ge.Status == 0 || ge.Status >= 500 {
		failCode(w, 502, "server_unreachable", "The group server could not be reached, so the request was not claimed and nothing was submitted. Try again when it answers, or keep the draft as your own job.", nil)
		return
	}
	s.noteClaimRefused(m, ge)
	failCode(w, 409, "not_claimed", "The group server did not grant the claim, so nothing was submitted: "+ge.Msg, nil)
}

func (s *Server) declineRelayed(w http.ResponseWriter, r *http.Request) {
	m, ok := s.relayedFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	if err := relay.PlainText(in.Note, "the note"); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	m, _ = s.Relayed.Get(m.ID)
	if m.LocalState != relay.LocalReceived && m.LocalState != relay.LocalDraft && m.LocalState != relay.LocalRefused {
		fail(w, 409, "this request can no longer be declined")
		return
	}
	c, l, err := s.groupClient()
	if err != nil || l.Origin != m.Origin() {
		fail(w, 409, errNotLinked.Error())
		return
	}
	if err := c.postJSON(fmt.Sprintf("/api/run-requests/%d/decline", m.ServerID), relay.Decline{Instance: l.Instance, Note: in.Note}, nil); err != nil {
		var ge *groupError
		if !errors.As(err, &ge) || ge.transient() {
			fail(w, 502, "the group server could not be reached; the request is not declined yet")
			return
		}
		if ge.Status == 404 { // gone from the server (the account left the workspace): nothing to decline, so closed here
			_ = s.Relayed.SetLocalState(m.ID, relay.LocalClosed)
			s.dropOldDraft(m)
			s.audit(r.Context(), "run request closed", "relayed", m.ID, fmt.Sprintf("request %d is no longer on the group server", m.ServerID))
			m, _ = s.Relayed.Get(m.ID)
			writeJSON(w, 200, s.relayedView(m))
			return
		}
		if ge.Status == 409 {
			_ = s.Relayed.SetServerState(m.ID, conflictState(ge))
		}
		fail(w, ge.Status, ge.Msg)
		return
	}
	_ = s.Relayed.SetServerState(m.ID, relay.StateDeclined)
	_ = s.Relayed.SetLocalState(m.ID, relay.LocalDeclined)
	s.dropOldDraft(m)
	s.audit(r.Context(), "run request declined", "relayed", m.ID, fmt.Sprintf("request %d", m.ServerID))
	m, _ = s.Relayed.Get(m.ID)
	writeJSON(w, 200, s.relayedView(m))
}

// detachRelayed keeps a draft (or a sweep's drafts) as the key holder's own work, no longer tied to the request: it is not reported,
// and an open request stays open on the server until it is declined, withdrawn or expires. A request that closed (withdrawn,
// expired, declined elsewhere, gone from the server) can be detached too, so its drafts never stay stuck as unsubmittable.
func (s *Server) detachRelayed(w http.ResponseWriter, r *http.Request) {
	m, ok := s.relayedFrom(w, r)
	if !ok {
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	m, _ = s.Relayed.Get(m.ID)
	if (m.LocalState != relay.LocalDraft && m.LocalState != relay.LocalClosed) || (m.JobID == nil && m.SweepID == nil) {
		fail(w, 409, "only a drafted request, or a closed one that left drafts, can be kept as your own work")
		return
	}
	var ids []int64
	if m.JobID != nil {
		ids = append(ids, *m.JobID)
	}
	if m.SweepID != nil && s.Sweeps != nil {
		ms, _ := s.Sweeps.Members(*m.SweepID)
		for _, x := range ms {
			ids = append(ids, x.JobID)
		}
	}
	// only drafts that still exist: a decline (or a request gone from the server) deletes them but keeps the ids on the mirror
	kept := ids[:0]
	for _, id := range ids {
		if j, err := s.Jobs.Get(id); err == nil && j.State == jobs.StateDraft {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		fail(w, 409, "this request has no drafts left to keep")
		return
	}
	ids = kept
	for _, id := range ids {
		_ = s.Jobs.SetRequestRef(id, "")
	}
	_ = s.Relayed.SetLocalState(m.ID, relay.LocalDetached)
	s.audit(r.Context(), "run request detached", "relayed", m.ID, fmt.Sprintf("request %d: %d draft(s) kept as your own", m.ServerID, len(ids)))
	m, _ = s.Relayed.Get(m.ID)
	writeJSON(w, 200, s.relayedView(m))
}

// relayedDraft refuses the ordinary Submit (and a sweep's Approve) for drafts rendered for a request: those are submitted only
// through the request's approval, which claims it on the group server first.
func (s *Server) relayedDraft(jobID int64) bool {
	if s.Relayed == nil {
		return false
	}
	ref, err := s.Jobs.RequestRef(jobID)
	return err == nil && ref != ""
}
