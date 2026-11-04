package server

import (
	"errors"
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
	d, code, msg := s.dataset(id)
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
	out, _ := s.Store.Get(d.ID)
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
	if err := s.Runs.SetReview(id, in); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			fail(w, 404, "run not found")
		} else {
			fail(w, 422, err.Error())
		}
		return
	}
	run, _ := s.Runs.Get(id)
	writeJSON(w, 200, s.runView(run))
}
