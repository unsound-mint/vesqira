package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The viewer's exploration history (ADR 0057): owner routes only. The reviewer listener (internal/review) has no history
// route and cannot import this store, so a published review never exposes the owner's dataset ids or what was looked at.

type stepBody struct {
	history.Step
	Scene json.RawMessage `json:"scene,omitempty"`
}

func (s *Server) historyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, history.ErrNotFound):
		fail(w, 404, "step not found")
	case strings.HasPrefix(err.Error(), "invalid: "):
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		log.Printf("history: %v", err)
		fail(w, 500, "internal error")
	}
}

// projectOK accepts 0 (no project open) or an existing project, and answers the request itself when it is neither.
func (s *Server) projectOK(ws workspace.ID, w http.ResponseWriter, id int64) bool {
	if id < 0 {
		fail(w, 400, "bad project")
		return false
	}
	if id == 0 {
		return true
	}
	if s.Projects == nil {
		fail(w, 404, "project not found")
		return false
	}
	if _, err := s.Projects.Get(ws, id); errors.Is(err, projects.ErrNotFound) {
		fail(w, 404, "project not found")
		return false
	} else if err != nil {
		log.Printf("history: %v", err)
		fail(w, 500, "internal error")
		return false
	}
	return true
}

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) {
	pid, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	if !s.projectOK(wsOf(r), w, pid) {
		return
	}
	steps, err := s.History.List(wsOf(r), pid)
	if err != nil {
		s.historyErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"steps": steps, "keep": history.Keep})
}

func (s *Server) getHistoryStep(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	st, err := s.History.Get(wsOf(r), id)
	if err != nil {
		s.historyErr(w, err)
		return
	}
	writeJSON(w, 200, stepBody{Step: st, Scene: st.Scene})
}

func (s *Server) addHistoryStep(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID int64           `json:"projectId"`
		ParentID  *int64          `json:"parentId"`
		Kind      string          `json:"kind"`
		Label     string          `json:"label"`
		Scene     json.RawMessage `json:"scene"`
	}
	if !body(w, r, history.MaxScene+4<<10, &in, true) {
		return
	}
	if !s.projectOK(wsOf(r), w, in.ProjectID) {
		return
	}
	sc, err := scene.Parse(in.Scene) // strict: the same reader a figure's scene goes through
	if err != nil {
		fail(w, 422, "the scene is not valid: "+err.Error())
		return
	}
	if _, err := s.Store.Get(wsOf(r), sc.Base.DatasetID); errors.Is(err, catalog.ErrNotFound) {
		fail(w, 422, "the scene opens a dataset that is not in the catalog")
		return
	} else if err != nil {
		log.Printf("history: %v", err)
		fail(w, 500, "internal error")
		return
	}
	st, err := s.History.Add(wsOf(r), actorID(r.Context()), in.ProjectID, in.ParentID, sc.Base.DatasetID, in.Kind, in.Label, in.Scene)
	if err != nil {
		s.historyErr(w, err)
		return
	}
	writeJSON(w, 201, stepBody{Step: st})
}
