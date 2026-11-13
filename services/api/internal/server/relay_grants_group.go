package server

import (
	"net/http"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
)

// Standing approvals on the group server (ADR 0074). The server holds no grant: it keeps what the key holder's computer published
// with its inbox (an offer per grant, without a path, host or commit), shows each member only the offers made to them, refuses a
// filing that names a grant not offered to the filer, and previews whether a request fits an offer. The key holder's computer
// checks every term again; the preview is advisory.

// relayMembers lists the workspace's members by account id and name for the key holder's computer, which makes a grant for one of
// them (a relay token only).
func (s *Server) relayMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.MultiUser.Accounts.Members(wsOf(r))
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	type member struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	self := actorOf(r).ID
	out := []member{}
	for _, m := range ms {
		if m.AccountID != self {
			out = append(out, member{m.AccountID, m.Name})
		}
	}
	writeJSON(w, 200, map[string]any{"members": out})
}

// offersFor keeps only the offers made to account.
func offersFor(in relay.Inbox, account string) relay.Inbox {
	var keep []relay.GrantOffer
	for _, o := range in.Grants {
		if o.Requester == account {
			keep = append(keep, o)
		}
	}
	in.Grants = keep
	return in
}

type previewAnswer struct {
	Route  string            `json:"route"` // grant | review
	Grant  *relay.GrantOffer `json:"grant,omitempty"`
	Reason string            `json:"reason"`
}

// previewRunRequest says, before sending, whether a request would go through a standing approval or wait for review, and why. It
// is advisory: the server sees neither the base run's input nor the live budget, and the key holder's computer decides.
func (s *Server) previewRunRequest(w http.ResponseWriter, r *http.Request) {
	var in fileRequest
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	ws, a := wsOf(r), actorOf(r)
	ib, err := s.Relay.InboxOf(ws, in.Addressee)
	if err != nil || ib.Inbox.Paused {
		fail(w, 422, relay.ErrNoInbox.Error())
		return
	}
	ask := in.Ask
	ask.Grant = ""
	if err := relay.CheckAsk(ib.Inbox, ask); err != nil {
		fail(w, 422, relay.Sentence(err))
		return
	}
	review := func(why string) { writeJSON(w, 200, previewAnswer{Route: "review", Reason: why}) }
	if in.Ask.Grant == "" {
		review("No standing approval was chosen, so the key holder reads the script and approves it.")
		return
	}
	o, ok := relay.OfferFor(ib.Inbox, in.Ask.Grant, a.ID, time.Now())
	if !ok {
		review("This standing approval is not offered to you now (revoked, expired or another person's), so the request would wait for review.")
		return
	}
	if _, err := relay.CheckGrantAsk(o, ask); err != nil {
		review("It would wait for review: " + relay.Sentence(err) + ".")
		return
	}
	if o.JobsUsed >= o.MaxJobs || o.CoreHoursUsed >= float64(o.MaxCoreHours) {
		review("It would wait for review: the standing approval's budget was used up when the key holder's computer last published it.")
		return
	}
	if in.BaseItemID == nil {
		review("It would wait for review: a standing approval needs a base run the key holder pushed.")
		return
	}
	if _, err := s.Relay.BaseOf(ws, *in.BaseItemID); err != nil {
		fail(w, 422, "the base run is not one pushed to this workspace")
		return
	}
	writeJSON(w, 200, previewAnswer{Route: "grant", Grant: &o,
		Reason: "It fits the standing approval as last published, so the key holder's computer should submit it without a click. That computer still checks the base run's input, the code commit and the budget itself, and anything outside the approval waits for review."})
}

func (s *Server) relayGrantRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/relay/members", authz.RelayServe, s.relayMembers)
	s.route(mux, "POST /api/run-requests/preview", authz.RunRequestFile, s.previewRunRequest)
}
