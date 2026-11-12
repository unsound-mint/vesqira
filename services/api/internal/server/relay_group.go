package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Relayed run requests (ADR 0066 section 3), the group server's side. A member files a request addressed to a key holder at a
// browser (authz.RunRequestFile); the key holder's computer, through a workspace-bound token with the relay scope only
// (authz.RelayServe, never a session), publishes its inbox, pulls what is addressed to its account, marks a request waiting for
// review, claims it bound to a script hash and reports its status; the addressee declines from either (authz.RunRequestDecline).
// The server holds no script, path, host or Slurm id: a request is typed values validated against the inbox, and a status is an
// enumerated word. Every route acts in the token's or the session's workspace, and a relay route only on requests addressed to
// the token's own account (anything else answers 404, as a missing id). Every transition is written to the request's record
// (actor, via, token, instance) and to the workspace's audit log.

func (s *Server) relayRoutes(mux *http.ServeMux) {
	s.route(mux, "PUT /api/relay/inbox", authz.RelayServe, s.relayPutInbox)
	s.route(mux, "GET /api/relay/requests", authz.RelayServe, s.relayPage)
	s.route(mux, "POST /api/relay/requests/{id}/review", authz.RelayServe, s.relayReview)
	s.route(mux, "POST /api/relay/requests/{id}/approve", authz.RelayServe, s.relayApprove)
	s.route(mux, "POST /api/relay/requests/{id}/status", authz.RelayServe, s.relayStatus)
	s.route(mux, "POST /api/run-requests/{id}/decline", authz.RunRequestDecline, s.declineRunRequest)
	s.route(mux, "GET /api/run-inboxes", authz.RunRequestRead, s.listRunInboxes)
	s.route(mux, "GET /api/run-requests", authz.RunRequestRead, s.listRunRequests)
	s.route(mux, "GET /api/run-requests/{id}", authz.RunRequestRead, s.getRunRequest)
	s.route(mux, "POST /api/run-requests", authz.RunRequestFile, s.fileRunRequest)
	s.route(mux, "POST /api/run-requests/{id}/withdraw", authz.RunRequestFile, s.withdrawRunRequest)
	s.route(mux, "POST /api/run-requests/{id}/note", authz.RunRequestFile, s.noteRunRequest)
}

// relayActor is the store's view of who acts: the account, how it came in, the token and the instance the body named.
func relayActor(r *http.Request, instance string) relay.Actor {
	a := actorOf(r)
	return relay.Actor{ID: a.ID, Via: string(a.Via), TokenID: a.TokenID, Instance: instance}
}

// relayFail answers a store refusal: 422 for a value outside the inbox or the whitelist, 404 for a request that is not there
// (or not addressed to this account), 409 for a transition the state forbids, 429 for a rate limit, 403 for another's request.
func (s *Server) relayFail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var limit relay.LimitError
	var conflict relay.ConflictError
	switch {
	case errors.Is(err, relay.ErrInvalid):
		s.audit(r.Context(), "run_request.refused", "run_request", 0, what+": "+relay.Sentence(err)+viaToken(r))
		fail(w, 422, relay.Sentence(err))
	case errors.Is(err, relay.ErrNotFound):
		fail(w, 404, "not found")
	case errors.Is(err, relay.ErrNoInbox):
		fail(w, 422, err.Error())
	case errors.Is(err, relay.ErrNotYours):
		fail(w, 403, "only the person who filed this request, a Maintainer or an Owner can change it")
	case errors.As(err, &limit):
		w.Header().Set("Retry-After", "3600")
		fail(w, 429, limit.Msg)
	case errors.As(err, &conflict):
		failCode(w, 409, "state", conflict.Msg, map[string]any{"state": conflict.State})
	default:
		log.Printf("%s: %v", what, err)
		fail(w, 500, "internal error")
	}
}

func viaToken(r *http.Request) string {
	if a := actorOf(r); a.TokenID != "" {
		return " (via token " + a.TokenID + ")"
	}
	return ""
}

type publishInbox struct {
	Instance string      `json:"instance"`
	Inbox    relay.Inbox `json:"inbox"`
}

func (s *Server) relayPutInbox(w http.ResponseWriter, r *http.Request) {
	var in publishInbox
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	if !relay.ValidInstance(in.Instance) {
		fail(w, 422, "send this computer's instance id")
		return
	}
	a := actorOf(r)
	if err := s.Relay.PutInbox(wsOf(r), a.ID, relayActor(r, in.Instance), in.Inbox); err != nil {
		s.relayFail(w, r, "inbox", err)
		return
	}
	state := "open"
	if in.Inbox.Paused {
		state = "paused"
	}
	s.audit(r.Context(), "run_request.inbox", "relay_inbox", 0, fmt.Sprintf("%d labels, %s%s", len(in.Inbox.Labels), state, viaToken(r)))
	w.WriteHeader(http.StatusNoContent)
}

// wireRequest is a request as the key holder's computer pulls it: the requester's name, the ask, the base as its pushed lineage
// (the instance only when the addressee pushed it, so another computer's id never reaches them), and the follow-up notes.
func (s *Server) wireRequest(ws workspace.ID, row relay.Row, names map[string]string) (relay.Request, error) {
	out := relay.Request{ID: row.ID, Change: row.Change, State: row.State, Requester: s.accountName(names, row.Requester), Ask: row.Ask,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, Notes: []relay.FollowUp{}}
	if row.BaseItemID != nil {
		b, err := s.Relay.BaseOf(ws, *row.BaseItemID)
		if err == nil {
			out.Base = &relay.Base{Origin: b.Origin, Hash: b.Hash, Title: b.Title}
			if b.PushedBy == row.Addressee {
				out.Base.Instance = b.Instance
			}
		}
	}
	evs, err := s.Relay.Events(ws, row.ID)
	if err != nil {
		return out, err
	}
	for _, e := range evs {
		if e.Event == "note" {
			out.Notes = append(out.Notes, relay.FollowUp{At: e.At, By: s.accountName(names, e.Actor), Text: e.Detail})
		}
	}
	return out, nil
}

func (s *Server) relayPage(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			fail(w, 400, "after is the cursor of the last page, a whole number")
			return
		}
		after = n
	}
	ws, a := wsOf(r), actorOf(r)
	rows, err := s.Relay.Page(ws, a.ID, after)
	if err != nil {
		s.relayFail(w, r, "relay page", err)
		return
	}
	page := relay.Page{Cursor: after, Requests: make([]relay.Request, 0, len(rows))}
	if wk, err := s.MultiUser.Accounts.Workspace(ws); err == nil {
		page.Workspace = wk.Name
	}
	names := map[string]string{}
	for _, row := range rows {
		req, err := s.wireRequest(ws, row, names)
		if err != nil {
			s.relayFail(w, r, "relay page", err)
			return
		}
		page.Requests = append(page.Requests, req)
		page.Cursor = row.Change
	}
	writeJSON(w, 200, page)
}

func (s *Server) relayReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in relay.Review
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if !relay.ValidInstance(in.Instance) {
		fail(w, 422, "send this computer's instance id")
		return
	}
	row, err := s.Relay.Review(wsOf(r), id, actorOf(r).ID, relayActor(r, in.Instance))
	if err != nil {
		s.relayFail(w, r, "relay review", err)
		return
	}
	s.audit(r.Context(), "run_request.review", "run_request", id, "draft rendered, waiting for review"+viaToken(r))
	writeJSON(w, 200, map[string]any{"state": row.State})
}

// relayApprove is the one-winner claim: it binds the request to the hash of the script the key holder read (or the sweep hash
// and the kept combinations) and the final values, which are validated again. Only after it answers 200 does the key holder's
// computer submit anything.
func (s *Server) relayApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in relay.Claim
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	if !relay.ValidInstance(in.Instance) {
		fail(w, 422, "send this computer's instance id")
		return
	}
	row, created, err := s.Relay.Claim(wsOf(r), id, actorOf(r).ID, relayActor(r, in.Instance), in)
	if err != nil {
		s.relayFail(w, r, "relay claim", err)
		return
	}
	if created {
		hash := in.ScriptHash
		if hash == "" {
			hash = "sweep " + in.SweepHash
		}
		s.audit(r.Context(), "run_request.approve", "run_request", id, "claimed with "+short(hash)+viaToken(r))
	}
	writeJSON(w, 200, map[string]any{"state": row.State, "approval": row.Approval})
}

func short(h string) string {
	if len(h) > 18 {
		return h[:18]
	}
	return h
}

func (s *Server) relayStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in relay.Report
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	row, applied, err := s.Relay.Report(wsOf(r), id, actorOf(r).ID, relayActor(r, in.Instance), in)
	if err != nil {
		s.relayFail(w, r, "relay status", err)
		return
	}
	if applied {
		s.audit(r.Context(), "run_request.status", "run_request", id, fmt.Sprintf("reported %s %s%s", in.State, in.Code, viaToken(r)))
	}
	writeJSON(w, 200, map[string]any{"state": row.State, "applied": applied})
}

func (s *Server) declineRunRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in relay.Decline
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	if in.Instance != "" && !relay.ValidInstance(in.Instance) {
		fail(w, 422, "send this computer's instance id")
		return
	}
	if err := relay.PlainText(in.Note, "the note"); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	if _, err := s.Relay.Decline(wsOf(r), id, actorOf(r).ID, relayActor(r, in.Instance), in.Note); err != nil {
		s.relayFail(w, r, "decline", err)
		return
	}
	s.audit(r.Context(), "run_request.decline", "run_request", id, "declined"+viaToken(r))
	writeJSON(w, 200, map[string]any{"state": relay.StateDeclined})
}

type runInbox struct {
	Account  string      `json:"account"`
	Name     string      `json:"name"`
	Inbox    relay.Inbox `json:"inbox"`
	Updated  time.Time   `json:"updatedAt"`
	PolledAt *time.Time  `json:"polledAt,omitempty"`
}

func (s *Server) listRunInboxes(w http.ResponseWriter, r *http.Request) {
	xs, err := s.Relay.Inboxes(wsOf(r))
	if err != nil {
		s.relayFail(w, r, "inboxes", err)
		return
	}
	names := map[string]string{}
	out := make([]runInbox, 0, len(xs))
	for _, x := range xs {
		out = append(out, runInbox{x.Account, s.accountName(names, x.Account), x.Inbox, x.UpdatedAt, x.PolledAt})
	}
	writeJSON(w, 200, map[string]any{"inboxes": out})
}

// runRequestView is a request as members read it: the record with names, when the addressee's computer last checked, the base's
// title and the pushed run line its results became. Never a script, folder or Slurm id: the server has none.
type runRequestView struct {
	relay.Row
	RequesterName string     `json:"requesterName"`
	AddresseeName string     `json:"addresseeName"`
	BaseTitle     string     `json:"baseTitle"`
	CheckedAt     *time.Time `json:"checkedAt,omitempty"`
	RunItemID     *int64     `json:"runItemId,omitempty"`
}

func (s *Server) runRequestView(ws workspace.ID, row relay.Row, names map[string]string) runRequestView {
	v := runRequestView{Row: row, RequesterName: s.accountName(names, row.Requester), AddresseeName: s.accountName(names, row.Addressee),
		CheckedAt: s.Relay.Polled(ws, row.Addressee)}
	if row.BaseItemID != nil {
		if b, err := s.Relay.BaseOf(ws, *row.BaseItemID); err == nil {
			v.BaseTitle = b.Title
		}
	}
	if row.RunHash != "" {
		if id, ok := s.Relay.RunItem(ws, row.Addressee, row.RunHash); ok {
			v.RunItemID = &id
		}
	}
	return v
}

func (s *Server) listRunRequests(w http.ResponseWriter, r *http.Request) {
	ws := wsOf(r)
	rows, err := s.Relay.List(ws)
	if err != nil {
		s.relayFail(w, r, "run requests", err)
		return
	}
	names := map[string]string{}
	out := make([]runRequestView, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.runRequestView(ws, row, names))
	}
	writeJSON(w, 200, map[string]any{"requests": out})
}

func (s *Server) getRunRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	ws := wsOf(r)
	_ = s.Relay.ExpireDue(ws)
	row, err := s.Relay.Get(ws, id)
	if err != nil {
		s.relayFail(w, r, "run request", err)
		return
	}
	evs, err := s.Relay.Events(ws, id)
	if err != nil {
		s.relayFail(w, r, "run request", err)
		return
	}
	names := map[string]string{}
	type event struct {
		relay.Event
		Name string `json:"name"`
	}
	out := make([]event, 0, len(evs))
	for _, e := range evs {
		n := "the server clock"
		if e.Via != "clock" {
			n = s.accountName(names, e.Actor)
		}
		out = append(out, event{e, n})
	}
	writeJSON(w, 200, map[string]any{"request": s.runRequestView(ws, row, names), "events": out})
}

type fileRequest struct {
	Addressee     string    `json:"addressee"`
	Ask           relay.Ask `json:"ask"`
	BaseItemID    *int64    `json:"baseItemId"`
	ExpiresInDays int       `json:"expiresInDays"`
}

func (s *Server) fileRunRequest(w http.ResponseWriter, r *http.Request) {
	var in fileRequest
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	ws, a := wsOf(r), actorOf(r)
	if in.BaseItemID != nil { // looked up in the actor's workspace only: another workspace's item is not found
		if _, err := s.Relay.BaseOf(ws, *in.BaseItemID); err != nil {
			fail(w, 422, "the base is a run pushed to this workspace")
			return
		}
	}
	if role, err := s.MultiUser.Accounts.Role(in.Addressee, ws); err != nil || role == "" {
		fail(w, 422, relay.ErrNoInbox.Error())
		return
	}
	row, err := s.Relay.File(ws, a.ID, in.Addressee, in.Ask, in.BaseItemID, in.ExpiresInDays, relayActor(r, ""))
	if err != nil {
		s.relayFail(w, r, "file run request", err)
		return
	}
	names := map[string]string{}
	s.audit(r.Context(), "run_request.file", "run_request", row.ID, fmt.Sprintf("%s on %q to %s", in.Ask.Template, in.Ask.Label, s.accountName(names, in.Addressee)))
	writeJSON(w, 201, s.runRequestView(ws, row, names))
}

func (s *Server) withdrawRunRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	a := actorOf(r)
	row, err := s.Relay.Withdraw(wsOf(r), id, relayActor(r, ""), a.Role.AtLeast(authz.RoleMaintainer))
	if err != nil {
		s.relayFail(w, r, "withdraw run request", err)
		return
	}
	s.audit(r.Context(), "run_request.withdraw", "run_request", id, "withdrawn")
	writeJSON(w, 200, map[string]any{"state": row.State})
}

func (s *Server) noteRunRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	a := actorOf(r)
	if _, err := s.Relay.AddNote(wsOf(r), id, relayActor(r, ""), a.Role.AtLeast(authz.RoleMaintainer), in.Text); err != nil {
		s.relayFail(w, r, "run request note", err)
		return
	}
	s.audit(r.Context(), "run_request.note", "run_request", id, "follow-up note")
	w.WriteHeader(http.StatusNoContent)
}

// closeRelayFor closes what a member who left had in the workspace's relay (ADR 0066 section 3, account changes).
func (s *Server) closeRelayFor(ws workspace.ID, account string) {
	if s.Relay == nil {
		return
	}
	names := map[string]string{}
	sentence := s.accountName(names, account) + " can no longer take requests in this workspace"
	if err := s.Relay.CloseAccount(ws, account, sentence); err != nil {
		log.Printf("relay close account: %v", err)
	}
}
