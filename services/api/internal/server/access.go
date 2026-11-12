package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Access labels (ADR 0069). A label is a researcher's declaration on a dataset; a figure or a derived result carries the most
// restrictive label among its sources, computed from the recorded edges whenever it is shown or published. Publishing a review
// asks for confirmation when anything on the board is labelled, and on the group server a workspace's admin can make a label
// block publishing outright. Neither check is in the browser: the handler decides, and a reviewer only ever receives frozen
// pictures and the label's name.
func (s *Server) accessRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/access", authz.DatasetRead, s.accessOverview)
	s.route(mux, "GET /api/datasets/{id}/access", authz.DatasetRead, s.getAccess)
	s.route(mux, "POST /api/datasets/{id}/access", authz.DatasetWrite, s.setAccess)
}

// accessView reads the workspace's labels. A failed read is an error, never "released": publishing must not proceed on a guess.
func (s *Server) accessView(ws workspace.ID) (*access.View, error) {
	if s.Access == nil {
		return nil, nil
	}
	return s.Access.View(ws)
}

// blockedLabels are the labels the workspace's admin has made block publishing; always empty in local mode, which has no admin.
func (s *Server) blockedLabels(ws workspace.ID) []access.Label {
	if !s.multiUser() || s.MultiUser.Accounts == nil {
		return nil
	}
	w, err := s.MultiUser.Accounts.Workspace(ws)
	if err != nil {
		log.Printf("blocked labels of workspace %d: %v", ws, err)
		return access.ParseSet("collaboration,unreleased") // cannot read the policy: refuse rather than publish
	}
	return access.ParseSet(strings.Join(w.BlockedLabels, ","))
}

func labelStrings(ls []access.Label) []string {
	out := []string{}
	for _, l := range ls {
		out = append(out, string(l))
	}
	return out
}

// ---- reading and setting a dataset's label ----

type datasetAccess struct {
	DatasetID int64            `json:"datasetId"`
	Label     access.Label     `json:"label"`     // the label set on this dataset
	Effective access.Effective `json:"effective"` // with what it inherits from the datasets it was made from
	History   []access.Change  `json:"history"`
	Blocked   []string         `json:"blockedLabels"` // labels that block publishing in this workspace
	CanLower  bool             `json:"canLower"`      // whether the actor may set a less restrictive label than the one in force
}

// mayLower: lowering a label (releasing data) needs the Maintainer role or higher, so that a Researcher cannot undo an admin's block
// by relabelling a dataset. Raising a label needs only the right to write datasets.
func mayLower(a authz.Actor) bool { return a.Role.AtLeast(authz.RoleMaintainer) }

func (s *Server) getAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if _, code, msg := s.dataset(wsOf(r), id); code != 0 {
		fail(w, code, msg)
		return
	}
	v, err := s.accessView(wsOf(r))
	if err != nil {
		log.Printf("access view: %v", err)
		fail(w, 500, "internal error")
		return
	}
	h, err := s.Access.History(wsOf(r), id)
	if err != nil {
		log.Printf("access history: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, datasetAccess{DatasetID: id, Label: v.Own(id), Effective: v.Dataset(id), History: h,
		Blocked: labelStrings(s.blockedLabels(wsOf(r))), CanLower: mayLower(actorOf(r))})
}

// accessOverview is what the lists need for their badges: every dataset that carries a label, with its own and its effective one.
func (s *Server) accessOverview(w http.ResponseWriter, r *http.Request) {
	v, err := s.accessView(wsOf(r))
	if err != nil {
		log.Printf("access view: %v", err)
		fail(w, 500, "internal error")
		return
	}
	type row struct {
		Own       access.Label     `json:"own"`
		Effective access.Effective `json:"effective"`
	}
	out := map[int64]row{}
	for id, e := range v.Labelled() {
		out[id] = row{Own: v.Own(id), Effective: e}
	}
	writeJSON(w, 200, map[string]any{"datasets": out, "blockedLabels": labelStrings(s.blockedLabels(wsOf(r))), "canLower": mayLower(actorOf(r))})
}

func (s *Server) setAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Label  string `json:"label"`
		Reason string `json:"reason"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	label, err := access.Parse(in.Label)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	v, err := s.accessView(wsOf(r))
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	if label.Rank() < v.Own(id).Rank() && !mayLower(actorOf(r)) {
		fail(w, 403, "setting a less restrictive label releases data: it needs the Maintainer role or higher in this workspace")
		return
	}
	prev, changed, err := s.Access.Set(wsOf(r), id, d.Name, label, in.Reason, actorID(r.Context()))
	if errors.Is(err, access.ErrInvalid) {
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
		return
	}
	if err != nil {
		log.Printf("set access label: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if changed {
		s.audit(r.Context(), "access label set", "dataset", id, fmt.Sprintf("%s -> %s: %s", prev, label, strings.TrimSpace(in.Reason)))
	}
	s.getAccess(w, r)
}

// ---- policy: which labels block publishing on the group server ----

func (s *Server) setBlockedLabels(w http.ResponseWriter, r *http.Request) {
	ws, ok := adminWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Labels []string `json:"labels"`
	}
	if !decodeSmall(w, r, &in) {
		return
	}
	var set []access.Label
	for _, name := range in.Labels {
		l, err := access.Parse(name)
		if err != nil || !l.Restricted() {
			fail(w, 422, "a label that blocks publishing is collaboration or unreleased")
			return
		}
		set = append(set, l)
	}
	names := labelStrings(access.ParseSet(strings.Join(labelStrings(set), ",")))
	if err := s.MultiUser.Accounts.SetBlockedLabels(ws, names); errors.Is(err, accounts.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		log.Printf("blocked labels: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.accountAudit(sessionActor(actorOf(r).ID), "publishing policy set", fmt.Sprintf("workspace %d blocks: %s", ws, strings.Join(names, ", ")))
	writeJSON(w, 200, map[string]any{"blockedLabels": names})
}

// ---- boards ----

// restrictedPanel names a figure on a board that carries a label, for the confirmation the author sees.
type restrictedPanel struct {
	Position int             `json:"position"`
	Title    string          `json:"title"`
	Label    access.Label    `json:"label"`
	Via      []access.Source `json:"via"`
}

// boardAccess is the labels of a board's figures and the board's own (the most restrictive among them).
type boardAccess struct {
	Label      access.Label
	Figures    map[int64]access.Label
	Restricted []restrictedPanel
}

func (s *Server) labelsOfBoard(v *access.View, items []boards.Item) boardAccess {
	ba := boardAccess{Label: access.None, Figures: map[int64]access.Label{}}
	for _, it := range items {
		if it.Kind == boards.KindJob || it.Figure == nil {
			continue
		}
		e := v.Figure(it.Figure.DatasetID, it.Figure.Scene, it.Figure.Identity)
		if !e.Label.Restricted() {
			continue
		}
		ba.Figures[it.Figure.ID] = e.Label
		ba.Label = access.Max(ba.Label, e.Label)
		ba.Restricted = append(ba.Restricted, restrictedPanel{Position: it.Position + 1, Title: it.Figure.Title, Label: e.Label, Via: e.Via})
	}
	return ba
}

func (b boardAccess) labels() boards.AccessLabels {
	out := boards.AccessLabels{Figures: map[int64]string{}}
	if b.Label.Restricted() {
		out.Board = string(b.Label)
	}
	for id, l := range b.Figures {
		out.Figures[id] = string(l)
	}
	return out
}

// sentence names the restricted panels and the datasets behind them, in the author's words.
func (b boardAccess) sentence() string {
	var parts []string
	for _, p := range b.Restricted {
		names := []string{}
		for _, v := range p.Via {
			names = append(names, fmt.Sprintf("%s (%s)", v.Name, v.Label.Name()))
		}
		parts = append(parts, fmt.Sprintf("%q from %s", p.Title, strings.Join(names, ", ")))
	}
	return strings.Join(parts, "; ")
}

// accessRefusal is a publication refused for its labels. It carries the response so the handler answers exactly what the check decided.
type accessRefusal struct {
	status int
	code   string
	msg    string
	extra  map[string]any
}

func (e *accessRefusal) Error() string { return e.msg }

// answerAccess writes a refusal; it reports whether err was one.
func answerAccess(w http.ResponseWriter, err error) bool {
	var ar *accessRefusal
	if !errors.As(err, &ar) {
		return false
	}
	failCode(w, ar.status, ar.code, ar.msg, ar.extra)
	return true
}

// publishGate is the check publishing a review runs on the board as it is about to be frozen (ADR 0069). A blocked label refuses
// whatever the request says; any other label needs the author's explicit confirmation, which names the datasets. confirm never
// overrides a block.
func (s *Server) publishGate(ctx context.Context, confirm bool) func([]boards.Item) (boards.AccessLabels, error) {
	return func(items []boards.Item) (boards.AccessLabels, error) {
		ws := wsCtx(ctx)
		v, err := s.accessView(ws)
		if err != nil {
			return boards.AccessLabels{}, fmt.Errorf("the access labels could not be read: %w", err)
		}
		ba := s.labelsOfBoard(v, items)
		if !ba.Label.Restricted() {
			return boards.AccessLabels{}, nil
		}
		blocked := s.blockedLabels(ws)
		for _, p := range ba.Restricted {
			if access.Blocks(blocked, p.Label) {
				s.audit(ctx, "review publish refused", "board", 0, "blocked by the workspace policy: "+ba.sentence())
				return boards.AccessLabels{}, &accessRefusal{status: 403, code: "publishing_blocked",
					msg:   fmt.Sprintf("Publishing is blocked in this workspace for data labelled %s: %s. A workspace admin can change this policy; confirming does not override it.", p.Label.Name(), ba.sentence()),
					extra: map[string]any{"restricted": ba.Restricted, "label": ba.Label}}
			}
		}
		if !confirm {
			return boards.AccessLabels{}, &accessRefusal{status: 409, code: "access_confirmation_required",
				msg: fmt.Sprintf("This board uses data labelled %s: %s. A reviewer receives the pictures only, never the data, but the pictures show it. Confirm to publish.",
					ba.Label.Name(), ba.sentence()),
				extra: map[string]any{"restricted": ba.Restricted, "label": ba.Label}}
		}
		s.audit(ctx, "review publish confirmed with labelled data", "board", 0, string(ba.Label)+": "+ba.sentence())
		return ba.labels(), nil
	}
}

// attachAccess sets the Access field of every figure on the board for the owner's view.
func (s *Server) attachAccess(ws workspace.ID, b *boards.Board) {
	v, err := s.accessView(ws)
	if err != nil {
		log.Printf("access view: %v", err)
		return
	}
	for i := range b.Items {
		if f := b.Items[i].Figure; f != nil {
			f.Access = figureAccess(v, f)
		}
	}
}

// figureAccess is the effective label of a figure for the owner's view, or nil when released.
func figureAccess(v *access.View, f *boards.Figure) any {
	e := v.Figure(f.DatasetID, f.Scene, f.Identity)
	if !e.Label.Restricted() {
		return nil
	}
	return e
}

// anyBlocked reports whether the workspace policy blocks publishing this board: some figure carries a blocked label.
func anyBlocked(blocked []access.Label, ba boardAccess) bool {
	for _, p := range ba.Restricted {
		if access.Blocks(blocked, p.Label) {
			return true
		}
	}
	return false
}

func nonNilPanels(p []restrictedPanel) []restrictedPanel {
	if p == nil {
		return []restrictedPanel{}
	}
	return p
}

// accessNote is the label of a published review as the audit log says it.
func accessNote(label string) string {
	if label == "" {
		return ""
	}
	return ", access: " + label
}
