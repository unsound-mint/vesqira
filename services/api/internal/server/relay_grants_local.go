package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Standing approvals, the key holder's side (ADR 0074). A grant lets one collaborator's requests be submitted without a click, inside
// narrow terms and a budget. Every term is checked here, for every request, against the grant this computer stored, the inbox it
// stored and the draft its own template rendered; the group server's word that a request fits counts for nothing. A request that
// fits takes a slot of the budget in one conditional statement, is claimed on the server bound to the rendered script's hash (the
// same claim as a manual approval), and only then submitted. Anything else waits for the key holder, with the reason.

func (s *Server) relayGrantLocalRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/relay/local/grants", authz.LocalOnly, s.listGrants)
	s.route(mux, "POST /api/relay/local/grants", authz.LocalOnly, s.createGrant)
	s.route(mux, "POST /api/relay/local/grants/{id}/revoke", authz.LocalOnly, s.revokeGrant)
	s.route(mux, "GET /api/relay/local/members", authz.LocalOnly, s.relayLocalMembers)
}

func (s *Server) relayClock() time.Time {
	if s.RelayNow != nil {
		return s.RelayNow()
	}
	return time.Now()
}

// publishedInbox is the inbox the key holder typed with the offers of the live grants that still fit it (a grant the key holder's
// later inbox no longer admits is not offered, and is refused at use with the reason).
func (s *Server) publishedInbox(origin string, st relay.LocalState) relay.Inbox {
	in := st.Inbox
	in.Grants = nil
	gs, err := s.Relayed.Grants(origin)
	if err != nil {
		return in
	}
	now := s.relayClock()
	for _, g := range gs {
		if g.Live(now) && relay.CheckGrantTerms(st.Inbox, g, now) == nil && len(in.Grants) < relay.MaxGrants {
			in.Grants = append(in.Grants, g.Offer())
		}
	}
	return in
}

// ---- the pass ----

// settleGrantUses frees the jobs-at-once slot of every use whose job ended or is gone; the job and its core-hours stay counted.
func (s *Server) settleGrantUses() {
	us, err := s.Relayed.OpenUses()
	if err != nil {
		return
	}
	for _, u := range us {
		j, err := s.Jobs.Get(u.JobID)
		if err != nil || j.State == jobs.StateCompleted || j.State == jobs.StateFailed || j.State == jobs.StateCancelled {
			_ = s.Relayed.EndUse(u.ID)
		}
	}
}

// applyGrants decides the requests that named a grant and are not decided yet (called by RelayPass, which holds relayMu).
func (s *Server) applyGrants(ctx context.Context, c *groupClient, l outbox.Link) {
	if s.Jobs == nil || s.HostDB == nil {
		return
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil {
		return
	}
	s.settleGrantUses()
	ms, err := s.Relayed.Undecided(l.Origin)
	if err != nil {
		return
	}
	for i, m := range ms {
		if i == maxTellsPerPass || ctx.Err() != nil {
			break
		}
		s.applyGrant(ctx, c, l, st, m)
	}
}

// grantVerdict checks every term of the grant a request named against this computer's own records. It returns the grant, the draft
// and its CPUs and minutes when the request is inside it, else a code and the sentence shown to the key holder. The code is
// relay.GrantStatePending when the host did not answer, which is not a verdict: the request is tried again at the next pass.
func (s *Server) grantVerdict(ctx context.Context, st relay.LocalState, m relay.Mirrored) (relay.Grant, jobs.Job, int, int, string, string) {
	var none jobs.Job
	now := s.relayClock()
	ask := m.Request.Ask
	g, err := s.Relayed.GrantByID(ask.Grant)
	if err != nil || g.Origin() != m.Origin() {
		return g, none, 0, 0, relay.GrantUnknown, "It names a standing approval this computer does not have (a forged or deleted id)."
	}
	if g.RequesterID != m.Request.RequesterID {
		return g, none, 0, 0, relay.GrantUnknown, "It names the standing approval made for " + g.RequesterName + ", but someone else filed it."
	}
	switch {
	case g.RevokedAt != nil:
		return g, none, 0, 0, relay.GrantRevoked, "The standing approval it names was revoked on " + g.RevokedAt.Format("2006-01-02") + "."
	case !g.ExpiresAt.After(now):
		return g, none, 0, 0, relay.GrantExpired, "The standing approval it names expired on " + g.ExpiresAt.Format("2006-01-02") + "."
	case m.Request.CreatedAt.Before(g.CreatedAt):
		return g, none, 0, 0, relay.GrantStale, "It was filed before the standing approval was made, so it is not taken as one made under it."
	case !m.Request.ExpiresAt.After(now) || !relay.IsOpen(m.ServerState):
		return g, none, 0, 0, relay.GrantStale, "It is no longer open on the group server (" + m.ServerState + ")."
	case st.Hosts[strings.TrimSpace(ask.Label)] != g.HostID:
		return g, none, 0, 0, relay.GrantUnknown, "The label " + ask.Label + " now stands for another host than when the standing approval was made."
	}
	if err := relay.CheckGrantTerms(st.Inbox, g, now); err != nil {
		return g, none, 0, 0, relay.GrantOutside, "The standing approval no longer fits your inbox or the template: " + relay.Sentence(err) + "."
	}
	h, err := s.HostDB.Get(g.HostID)
	if err != nil {
		return g, none, 0, 0, relay.GrantUnknown, "The host of the standing approval no longer exists."
	}
	if err := relay.CheckGrantInputs(g.Roots, g.Files, h.InRoots); err != nil {
		return g, none, 0, 0, relay.GrantInput, "The standing approval's inputs no longer lie inside the host's source roots: " + relay.Sentence(err) + "."
	}
	if _, label := s.profiles().TemplateProfile(g.Template); label != g.Profile {
		return g, none, 0, 0, relay.GrantCommit, "The tool's profile is now " + label + "; the standing approval was made under " + g.Profile + "."
	}
	if code, err := relay.CheckGrantAsk(g.Offer(), ask); err != nil {
		return g, none, 0, 0, code, relay.Sentence(err) + "."
	}
	if m.LocalState != relay.LocalDraft || m.JobID == nil || m.BaseChosen {
		return g, none, 0, 0, relay.GrantBase, "Its base run did not resolve through your own pushed runs, so you choose it."
	}
	j, err := s.Jobs.Get(*m.JobID)
	if err != nil || j.State != jobs.StateDraft || editedLines(j) != 0 {
		return g, none, 0, 0, relay.GrantEdited, "The draft is gone, no longer a draft, or was edited."
	}
	req, ok := j.ConfigRequest()
	t := jobs.ShippedTemplate(j.Template)
	if !ok || t == nil {
		return g, none, 0, 0, relay.GrantBase, "The draft kept no settings to check."
	}
	input := jobs.InputPathFor(t, req.BaseConfigText)
	if v, set := req.Set[jobs.InputKey(j.Template)]; set {
		input = v
	}
	// The input is checked where it really is, not by its name: a collaborator who can write in a granted folder could put a link
	// there to other data this account reads. The host's worker resolves it (and refuses anything outside the host's source roots);
	// the grant's folders and files were stored resolved when it was made, so a path through an alias of a granted folder still fits.
	real, code, why := s.resolveGrantInput(ctx, h, input)
	if code != "" {
		return g, none, 0, 0, code, why
	}
	if !g.InputAllowed(real) {
		if real != input {
			return g, none, 0, 0, relay.GrantInput, fmt.Sprintf("The base run's input %q resolves on the host to %q, which is not inside the standing approval's input folders or files.", input, real)
		}
		return g, none, 0, 0, relay.GrantInput, fmt.Sprintf("The base run's input %q is not one of the standing approval's input folders or files.", input)
	}
	if pin := s.codePinOf(j.Template, req); pin.Commit != g.Commit {
		got := "no commit"
		if pin.Commit != "" {
			got = "commit " + pin.Commit
		}
		return g, none, 0, 0, relay.GrantCommit, "The draft insists on " + got + "; the standing approval allows commit " + g.Commit + "."
	}
	rq := jobs.RequestedOf(j.Script)
	minutes := int((rq.TimeSeconds + 59) / 60)
	if rq.CPUs < 1 || rq.MemBytes <= 0 || minutes < 1 {
		return g, none, 0, 0, relay.GrantOutside, "The draft's script does not state its CPUs, memory and time limit in a form this computer reads."
	}
	if rq.CPUs > g.PerJob.MaxCPUs || rq.MemBytes > int64(g.PerJob.MaxMemGiB)<<30 || minutes > g.PerJob.MaxTimeMinutes {
		return g, none, 0, 0, relay.GrantOutside, fmt.Sprintf("The draft asks for %d CPUs, %.1f GiB and %d minutes; the standing approval allows at most %d CPUs, %d GiB and %d minutes per job.",
			rq.CPUs, float64(rq.MemBytes)/(1<<30), minutes, g.PerJob.MaxCPUs, g.PerJob.MaxMemGiB, g.PerJob.MaxTimeMinutes)
	}
	return g, j, rq.CPUs, minutes, "", ""
}

func (s *Server) applyGrant(ctx context.Context, c *groupClient, l outbox.Link, st relay.LocalState, m relay.Mirrored) {
	fallback := func(code, why string) {
		_ = s.Relayed.SetGrantState(m.ID, "", code, "Waiting for you: outside the standing approval. "+why)
	}
	g, j, cpus, minutes, code, why := s.grantVerdict(ctx, st, m)
	if code == relay.GrantStatePending {
		_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantStatePending, why)
		return
	}
	if code != "" {
		fallback(code, why)
		return
	}
	took, err := s.Relayed.Reserve(g.ID, m.ID, j.ID, cpus, minutes, j.ScriptHash)
	if err != nil {
		log.Printf("relay grant: %v", err)
		return
	}
	if !took {
		g, _ = s.Relayed.GrantByID(g.ID)
		now := s.relayClock()
		switch {
		case g.RevokedAt != nil:
			fallback(relay.GrantRevoked, "The standing approval was revoked.")
		case !g.ExpiresAt.After(now):
			fallback(relay.GrantExpired, "The standing approval has expired.")
		case g.JobsUsed >= g.MaxJobs:
			fallback(relay.GrantBudget, fmt.Sprintf("All %d jobs of the standing approval are used.", g.MaxJobs))
		case g.CoreMinutesUsed+cpus*minutes > g.MaxCoreHours*60:
			fallback(relay.GrantBudget, fmt.Sprintf("This job asks for %.1f core-hours and %.1f of %d are left.",
				float64(cpus*minutes)/60, float64(g.MaxCoreHours*60-g.CoreMinutesUsed)/60, g.MaxCoreHours))
		case g.AtOnce >= g.MaxAtOnce:
			_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantStateWaiting, fmt.Sprintf("Waiting for a free slot: %d jobs of the standing approval are queued or running, its limit. You can approve it yourself.", g.AtOnce))
		default:
			_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantStatePending, "The budget could not be read; tried again at the next check.")
		}
		return
	}
	claim := relay.Claim{Instance: l.Instance, ScriptHash: j.ScriptHash, Params: m.Draft.Params, Resources: m.Draft.Resources, Label: m.Draft.Label, AutoPush: true, Grant: g.ID}
	if err := relay.CheckClaim(m.Request.Ask, claim); err != nil {
		_ = s.Relayed.Release(m.ID)
		fallback(relay.GrantOutside, relay.Sentence(err)+".")
		return
	}
	if err := s.claimOnServer(c, l, m, claim); err != nil {
		_ = s.Relayed.Release(m.ID) // nothing was submitted, so the slot goes back
		var ge *groupError
		if !errors.As(err, &ge) || ge.transient() {
			_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantStatePending, "The group server did not answer the claim; tried again at the next check.")
			return
		}
		s.noteClaimRefused(m, ge)
		_ = s.Relayed.SetGrantState(m.ID, "", relay.GrantStale, "The group server did not grant the claim: "+ge.Msg)
		return
	}
	if err := s.Relayed.SetApproved(m.ID, j.ScriptHash, true); err != nil {
		log.Printf("relay grant: %v", err)
	}
	_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "")
	_ = s.Jobs.AddDraftNote(j.ID, fmt.Sprintf("Submitted without a click under standing approval %s, made for %s.", g.ID[:8], g.RequesterName))
	_ = s.Relayed.Unpublish(l.Origin) // the offer's usage changed
	ctx = localCtx(ctx)
	s.audit(ctx, "run request submitted under a standing approval", "relayed", m.ID,
		fmt.Sprintf("request %d from %s, standing approval %s, job %d, script sha256 %s", m.ServerID, m.Request.Requester, g.ID, j.ID, j.ScriptHash))
	h, err := s.HostDB.Get(j.HostID)
	if err != nil {
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "Claimed, but the host no longer exists; nothing was submitted.")
		return
	}
	j, _ = s.Jobs.Get(j.ID)
	t := true
	if _, aerr := s.submitDraft(ctx, j, h, j.ScriptHash, &t); aerr != nil {
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "Claimed under the standing approval, but the submission failed: "+aerr.Msg+" Approve it again yourself.")
	}
	m, _ = s.Relayed.Get(m.ID)
	s.reportOne(c, l, m)
}

// resolveGrantInput asks the host where a granted request's input really is. A refusal (outside the host's source roots, not a
// regular file, or missing) is a verdict; a host that does not answer is not, and the request waits for the next pass.
func (s *Server) resolveGrantInput(ctx context.Context, h hosts.Host, input string) (string, string, string) {
	if s.Remote == nil {
		return "", relay.GrantStatePending, "The host's input could not be checked on this computer; tried again at the next check."
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "resolve", "path": input})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return "", relay.GrantInput, fmt.Sprintf("The base run's input %q is not a file inside %s's source roots once its links are followed (the host said: %s).", input, h.Name, re.Message)
	case err != nil:
		return "", relay.GrantStatePending, "The host " + h.Name + " did not answer, so the base run's input could not be checked; tried again at the next check."
	}
	var out struct {
		Real string `json:"real"`
	}
	if json.Unmarshal(rep.Head, &out) != nil || !path.IsAbs(out.Real) || path.Clean(out.Real) != out.Real {
		return "", relay.GrantInput, "The host answered unexpectedly when asked where the base run's input is."
	}
	return out.Real, "", ""
}

// resolveGrantPaths replaces a new grant's folders and files with their real paths on the host: each folder through the worker's
// canon op (it must be a folder), each file through resolve, which the worker refuses outside the host's source roots.
func (s *Server) resolveGrantPaths(ctx context.Context, h hosts.Host, g *relay.Grant) (int, string) {
	if s.Remote == nil {
		return 502, "the host cannot be asked where the input folders are"
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for i, p := range g.Roots {
		real, isDir, _, err := s.remoteCanon(ctx, h, p)
		if err != nil {
			return hostErrStatus(err)
		}
		if !isDir {
			return 422, fmt.Sprintf("%s is not a folder on %s", p, h.Name)
		}
		g.Roots[i] = real
	}
	for i, p := range g.Files {
		real, code, _ := s.resolveGrantInput(ctx, h, p)
		switch code {
		case "":
			g.Files[i] = real
		case relay.GrantStatePending:
			return 502, "the host is not reachable (is the VPN up?)"
		default:
			return 422, fmt.Sprintf("%s is not a file inside %s's source roots once its links are followed", p, h.Name)
		}
	}
	return 0, ""
}

// claimOnServer claims a drafted request on the group server: it first marks the request waiting for review when the server still
// has it as requested, then sends the one-winner claim. Nothing is submitted here; the caller submits only after it returns nil.
// The key holder's approval and a standing approval both claim through it.
func (s *Server) claimOnServer(c *groupClient, l outbox.Link, m relay.Mirrored, claim relay.Claim) error {
	if m.ServerState == relay.StateRequested {
		var out struct {
			State string `json:"state"`
		}
		if err := c.postJSON(fmt.Sprintf("/api/relay/requests/%d/review", m.ServerID), relay.Review{Instance: l.Instance}, &out); err != nil {
			return err
		}
		_ = s.Relayed.SetServerState(m.ID, out.State)
	}
	return c.postJSON(fmt.Sprintf("/api/relay/requests/%d/approve", m.ServerID), claim, nil)
}

// noteClaimRefused records the state a refused claim named; a request the server will no longer move closes here.
func (s *Server) noteClaimRefused(m relay.Mirrored, ge *groupError) {
	if ge.Status != 409 && ge.Status != 404 {
		return
	}
	st := conflictState(ge)
	if ge.Status == 404 {
		st = relay.StateWithdrawn
	}
	_ = s.Relayed.SetServerState(m.ID, st)
	if !relay.IsOpen(st) {
		_ = s.Relayed.SetLocalState(m.ID, relay.LocalClosed)
	}
}

// grantCodeOf is the code a review carries for a request that named a grant and waits for the key holder ("" otherwise).
func grantCodeOf(m relay.Mirrored) string {
	if m.Request.Ask.Grant != "" && relay.GrantFallback(m.GrantState) {
		return m.GrantState
	}
	return ""
}

// ---- routes ----

type grantJob struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type grantView struct {
	relay.Grant
	Live     bool       `json:"live"`
	HostName string     `json:"hostName"`
	OpenJobs []grantJob `json:"openJobs"`
}

func (s *Server) grantViewOf(g relay.Grant) grantView {
	v := grantView{Grant: g, Live: g.Live(s.relayClock()), OpenJobs: []grantJob{}}
	if h, err := s.HostDB.Get(g.HostID); err == nil {
		v.HostName = h.Name
	}
	if us, err := s.Relayed.Uses(g.ID, true); err == nil {
		for _, u := range us {
			gj := grantJob{ID: u.JobID, State: "gone"}
			if j, err := s.Jobs.Get(u.JobID); err == nil {
				gj.Name, gj.State = j.Name, j.State
			}
			v.OpenJobs = append(v.OpenJobs, gj)
		}
	}
	return v
}

type templateCommit struct {
	Profile string `json:"profile"`
	Commit  string `json:"commit"`
}

func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"linked": false, "grants": []grantView{}, "templates": map[string]templateCommit{}}
	tpl := map[string]templateCommit{}
	for _, id := range []string{relay.TemplateVGT, relay.TemplateROHSA} {
		if t := jobs.ShippedTemplate(id); t != nil {
			_, label := s.profiles().TemplateProfile(id)
			tpl[id] = templateCommit{Profile: label, Commit: t.Repo.Commit}
		}
	}
	out["templates"] = tpl
	l, err := s.Outbox.Link()
	if err != nil || s.HostDB == nil || s.Jobs == nil {
		writeJSON(w, 200, out)
		return
	}
	gs, err := s.Relayed.Grants(l.Origin)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	views := make([]grantView, 0, len(gs))
	for _, g := range gs {
		views = append(views, s.grantViewOf(g))
	}
	out["linked"], out["grants"] = true, views
	writeJSON(w, 200, out)
}

type grantIn struct {
	RequesterID   string        `json:"requesterId"`
	RequesterName string        `json:"requesterName"`
	Label         string        `json:"label"`
	Template      string        `json:"template"`
	CommitBasis   string        `json:"commitBasis"`
	Commit        string        `json:"commit"`
	Keys          []relay.Bound `json:"keys"`
	Roots         []string      `json:"roots"`
	Files         []string      `json:"files"`
	MaxJobs       int           `json:"maxJobs"`
	MaxCoreHours  int           `json:"maxCoreHours"`
	PerJob        relay.Limits  `json:"perJob"`
	MaxAtOnce     int           `json:"maxAtOnce"`
	ExpiresInDays int           `json:"expiresInDays"`
	Note          string        `json:"note"`
}

// createGrant makes a standing approval after every check its use repeats: the label and template are in the inbox, the keys are
// narrowed inside the template and the inbox, the inputs lie inside the host's source roots, and the budget and expiry are bounded.
func (s *Server) createGrant(w http.ResponseWriter, r *http.Request) {
	var in grantIn
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	l, err := s.Outbox.Link()
	if err != nil || s.HostDB == nil {
		fail(w, 409, errNotLinked.Error())
		return
	}
	st, err := s.Relayed.State(l.Origin)
	if err != nil || !st.Configured {
		fail(w, 409, "set up your inbox first: a standing approval is for one of its labels")
		return
	}
	if s.Relayed.LiveGrants(l.Origin) >= relay.MaxGrants {
		fail(w, 422, fmt.Sprintf("at most %d standing approvals at a time; revoke one first", relay.MaxGrants))
		return
	}
	now := s.relayClock()
	if in.ExpiresInDays < 1 || in.ExpiresInDays > relay.MaxGrantDays {
		fail(w, 422, fmt.Sprintf("a standing approval lasts 1 to %d days", relay.MaxGrantDays))
		return
	}
	label := strings.TrimSpace(in.Label)
	hostID, ok := st.Hosts[label]
	if !ok {
		fail(w, 422, "the label "+in.Label+" is not one of your inbox's labels")
		return
	}
	h, err := s.HostDB.Get(hostID)
	if err != nil {
		fail(w, 422, "the host the label stands for no longer exists")
		return
	}
	if len(h.Roots) == 0 {
		fail(w, 422, "the host "+h.Name+" has no source root; add one under Hosts first, so the inputs can be checked against it")
		return
	}
	commit := strings.TrimSpace(in.Commit)
	t := jobs.ShippedTemplate(in.Template)
	if in.CommitBasis == "profile" && t != nil {
		commit = t.Repo.Commit
	}
	_, profile := s.profiles().TemplateProfile(in.Template)
	g := relay.Grant{RequesterID: strings.TrimSpace(in.RequesterID), RequesterName: strings.TrimSpace(in.RequesterName), Label: label, HostID: hostID,
		Template: in.Template, Profile: profile, Commit: commit, CommitBasis: in.CommitBasis, Keys: in.Keys, Roots: in.Roots, Files: in.Files,
		MaxJobs: in.MaxJobs, MaxCoreHours: in.MaxCoreHours, PerJob: in.PerJob, MaxAtOnce: in.MaxAtOnce, Note: in.Note,
		ExpiresAt: now.Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour).Truncate(time.Second)}
	if g.Roots == nil {
		g.Roots = []string{}
	}
	if g.Files == nil {
		g.Files = []string{}
	}
	if err := relay.CheckGrantTerms(st.Inbox, g, now); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	if err := relay.CheckGrantInputs(g.Roots, g.Files, h.InRoots); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	// stored where they really are, so a request's input, resolved the same way, is compared with real paths on both sides
	if code, msg := s.resolveGrantPaths(r.Context(), h, &g); code != 0 {
		fail(w, code, msg)
		return
	}
	if err := relay.CheckGrantInputs(g.Roots, g.Files, h.InRoots); err != nil {
		fail(w, 422, "once their links are followed on the host: "+relay.Sentence(err))
		return
	}
	g, err = s.Relayed.CreateGrant(l.Origin, g)
	if err != nil {
		log.Printf("relay grant: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "standing approval made", "relay_grant", 0, fmt.Sprintf("%s for %s (%s): %s on %q, %d jobs, %d core-hours, at most %d at once, until %s, commit %s",
		g.ID, g.RequesterName, g.RequesterID, g.Template, g.Label, g.MaxJobs, g.MaxCoreHours, g.MaxAtOnce, g.ExpiresAt.Format("2006-01-02"), g.Commit))
	s.kickRelay()
	writeJSON(w, 201, s.grantViewOf(g))
}

// revokeGrant revokes a grant at once. It takes the pass's lock, so a revocation never lands between a reservation and sbatch.
// Jobs already queued or running under it are listed, never cancelled.
func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !relay.ValidGrantID(id) {
		fail(w, 404, "not found")
		return
	}
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	g, err := s.Relayed.GrantByID(id)
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	done, err := s.Relayed.RevokeGrant(id)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	g, _ = s.Relayed.GrantByID(id)
	v := s.grantViewOf(g)
	if done {
		s.audit(r.Context(), "standing approval revoked", "relay_grant", 0, fmt.Sprintf("%s for %s; %d job(s) still queued or running under it, not cancelled", id, g.RequesterName, len(v.OpenJobs)))
		s.kickRelay()
	}
	writeJSON(w, 200, v)
}

// relayLocalMembers asks the group server who the workspace's members are, so the key holder can choose the collaborator.
func (s *Server) relayLocalMembers(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.groupClient()
	if err != nil {
		fail(w, 409, errNotLinked.Error())
		return
	}
	var out struct {
		Members []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"members"`
	}
	if err := c.do("GET", "/api/relay/members", "", nil, &out); err != nil {
		fail(w, 502, "the group server could not be asked who the members are: "+err.Error())
		return
	}
	keep := out.Members[:0]
	for _, m := range out.Members {
		if relay.ValidAccountID(m.ID) && relay.PlainText(m.Name, "a name") == nil && len([]rune(m.Name)) <= 100 && !strings.ContainsAny(m.Name, "\n\t") {
			keep = append(keep, m)
		}
	}
	writeJSON(w, 200, map[string]any{"members": keep})
}

// grantPin is the commit a draft insists on when its base run gives none and the request names a live grant of this computer made
// for its requester: the grant's commit (the profile's, through the profile pin, or the typed one). A base run's own commit is
// never replaced, so a strict-commit requirement stays; a grant whose commit differs then falls back with the code commit.
func (s *Server) grantPin(m relay.Mirrored, rev string, pinProfile bool) (string, bool) {
	g, ok := s.grantOf(m)
	if !ok {
		return rev, pinProfile
	}
	if g.CommitBasis == "profile" {
		return rev, true
	}
	return g.Commit, pinProfile
}

func (s *Server) grantOf(m relay.Mirrored) (relay.Grant, bool) {
	if m.Request.Ask.Grant == "" || len(m.Request.Ask.Grid) > 0 {
		return relay.Grant{}, false
	}
	g, err := s.Relayed.GrantByID(m.Request.Ask.Grant)
	if err != nil || g.Origin() != m.Origin() || g.RequesterID != m.Request.RequesterID || !g.Live(s.relayClock()) {
		return relay.Grant{}, false
	}
	return g, true
}

// grantPinWhy says, for the approval view, that the commit the script insists on came from the standing approval.
func (s *Server) grantPinWhy(m relay.Mirrored, req jobs.VGTRequest) string {
	g, ok := s.grantOf(m)
	if !ok || (req.ExpectCommit != g.Commit && !(req.ExpectCommit == "" && req.PinProfile && g.CommitBasis == "profile")) {
		return ""
	}
	if m.BaseRunID != nil {
		if rev, _ := s.relayPin(*m.BaseRunID); rev != "" {
			return "" // the base run's own commit
		}
	}
	return "The script insists on commit " + g.Commit + ", the one the standing approval allows (the base run's code is not described by one commit here: " +
		s.relayWhyUnpinned(m) + "). The job stops before the program starts unless the code folder on the host is at that commit with no changed tracked file."
}
