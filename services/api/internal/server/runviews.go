package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Saved views of the runs table (ADR 0062): owner routes only. A view is words and keys, never run data.

func (s *Server) runViewsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runs.ErrViewNotFound):
		fail(w, 404, "view not found")
	case strings.HasPrefix(err.Error(), "invalid: "):
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		log.Printf("run views: %v", err)
		fail(w, 500, "internal error")
	}
}

func (s *Server) listRunViews(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Runs.Views(wsOf(r))
	if err != nil {
		s.runViewsErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"views": vs, "max": runs.MaxViews})
}

func (s *Server) saveRunView(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string        `json:"name"`
		Spec runs.ViewSpec `json:"spec"`
	}
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	v, err := s.Runs.SaveView(wsOf(r), actorID(r.Context()), in.Name, in.Spec)
	if err != nil {
		s.runViewsErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) deleteRunView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := s.Runs.DeleteView(wsOf(r), id); err != nil {
		s.runViewsErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
