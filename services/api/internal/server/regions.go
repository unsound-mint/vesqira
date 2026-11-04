package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
)

func (s *Server) regionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, regions.ErrNotFound):
		fail(w, 404, "region not found")
	case errors.Is(err, regions.ErrInvalid):
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		log.Printf("regions: %v", err)
		fail(w, 500, "internal error")
	}
}

func (s *Server) datasetFor(w http.ResponseWriter, r *http.Request) (catalog.Dataset, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return catalog.Dataset{}, false
	}
	d, err := s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return d, false
	}
	if err != nil {
		log.Printf("regions: %v", err)
		fail(w, 500, "internal error")
		return d, false
	}
	return d, true
}

func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	rs, err := s.Regions.List(d.ID)
	if err != nil {
		s.regionErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"regions": rs})
}

func (s *Server) createRegion(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Name  string        `json:"name"`
		Shape regions.Shape `json:"shape"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	reg, err := s.Regions.Create(d.ID, in.Name, in.Shape)
	if err != nil {
		s.regionErr(w, err)
		return
	}
	writeJSON(w, 201, reg)
}

func (s *Server) updateRegion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Name  *string        `json:"name"`
		Shape *regions.Shape `json:"shape"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	reg, err := s.Regions.Update(id, in.Name, in.Shape)
	if err != nil {
		s.regionErr(w, err)
		return
	}
	writeJSON(w, 200, reg)
}

func (s *Server) deleteRegion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := s.Regions.Delete(id); err != nil {
		s.regionErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// regionsDS9 serves the dataset's saved regions as a DS9 region file, or one region with ?id=.
func (s *Server) regionsDS9(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	rs, err := s.Regions.List(d.ID)
	if err != nil {
		s.regionErr(w, err)
		return
	}
	if q := r.URL.Query().Get("id"); q != "" {
		want, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			fail(w, 400, "bad id")
			return
		}
		keep := rs[:0]
		for _, x := range rs {
			if x.ID == want {
				keep = append(keep, x)
			}
		}
		rs = keep
	}
	name := strings.Map(func(c rune) rune {
		if c < 128 && (c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return c
		}
		return '_'
	}, strings.TrimSuffix(d.Name, ".fits"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.reg"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(regions.DS9(rs)))
}
