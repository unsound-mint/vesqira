package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

var natures = map[string]bool{"": true, "none": true, "observed": true, "simulated": true, "constructed_control": true}

// setNature records what the user says a dataset is. A constructed control must never look like an observation.
func (s *Server) setNature(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	var in struct {
		Nature string `json:"nature"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if !natures[in.Nature] {
		fail(w, 400, "nature must be observed, simulated, constructed_control or none")
		return
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	n := in.Nature
	if n == "none" {
		n = ""
	}
	if err := s.Store.SetNature(d.ID, n); err != nil {
		fail(w, 500, "internal error")
		return
	}
	out, _ := s.Store.Get(wsOf(r), d.ID)
	writeJSON(w, 200, out)
}

func (s *Server) setRunReview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	var in runs.Review
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if err := s.Runs.SetReview(wsOf(r), id, in); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			fail(w, 404, "run not found")
		} else {
			fail(w, 422, err.Error())
		}
		return
	}
	s.Audit.RecordCtx(r.Context(), "run assessment recorded", "run", id, "qc: "+in.QC+", interpretation: "+in.Interpretation)
	run, _ := s.Runs.Get(wsOf(r), id)
	writeJSON(w, 200, s.runView(wsOf(r), run))
}

func (s *Server) runHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	h, err := s.Runs.History(wsOf(r), id)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "run not found")
		return
	}
	if err != nil {
		log.Printf("run history: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"history": h})
}

// listAudit serves the owner's record of what was done. The reviewer listener never mounts it.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	target, _ := strconv.ParseInt(r.URL.Query().Get("target"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	es, err := s.Audit.List(wsOf(r), r.URL.Query().Get("kind"), target, limit)
	if err != nil {
		log.Printf("audit: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"entries": es})
}
