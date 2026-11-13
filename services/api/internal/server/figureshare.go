package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
)

// A figure shared on its own (ADR 0071) is a review snapshot with no board. It is frozen, gated by access labels, tokened,
// expired, revoked and commented exactly like a board's, by the same store code and the same publishGate; only the source of the
// one frozen item differs. The reviewer listener cannot tell the two apart.

// shareFigure freezes one figure and returns the review token once. Only its hash is kept.
func (s *Server) shareFigure(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Label    string  `json:"label"`
		Question string  `json:"question"`
		Hours    float64 `json:"hours"`
		// ConfirmAccess is the author's explicit yes to sharing a figure that uses labelled data (ADR 0069); it never overrides a
		// label the workspace has made block publishing.
		ConfirmAccess bool `json:"confirmAccess"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if in.Hours == 0 {
		in.Hours = 24 * 14
	}
	sn, tok, err := s.Boards.PublishFigure(wsOf(r), actorID(r.Context()), id, boards.PublishOptions{
		Label: in.Label, Question: in.Question, TTL: time.Duration(in.Hours * float64(time.Hour)),
		Access: s.publishGateFor(r.Context(), in.ConfirmAccess, "figure"),
	})
	if err != nil {
		if answerAccess(w, err) {
			return
		}
		s.boardErr(w, err)
		return
	}
	s.Audit.RecordCtx(r.Context(), "review published", "snapshot", sn.ID, "figure "+strconv.FormatInt(id, 10)+", label: "+in.Label+accessNote(sn.Access))
	writeJSON(w, 201, publishResponse{Snapshot: sn, Token: tok, ReviewPath: "/#/review/" + tok,
		Note: "Shown once. Anyone with this link can read this frozen figure and comment; revoke it to end access."})
}

// figureShares lists the links made from one figure, newest first. The figure's revision is the key, so links stay attached to the
// figure when its title or caption is edited.
func (s *Server) figureShares(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	f, err := s.Boards.Figure(wsOf(r), id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	sn, err := s.Boards.FigureShares(wsOf(r), f.Revision)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Snapshots []boards.Snapshot `json:"snapshots"`
	}{sn})
}

// allFigureShares lists every figure link of the workspace, so the author can find and revoke one without opening the figure.
func (s *Server) allFigureShares(w http.ResponseWriter, r *http.Request) {
	sn, err := s.Boards.FigureShares(wsOf(r), "")
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Snapshots []boards.Snapshot `json:"snapshots"`
	}{sn})
}
