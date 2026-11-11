package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
)

// What an orientation angle is measured against, and what it describes (ADR 0042). Both are declared, never inferred.
var (
	axialFrames     = map[string]bool{"pixel": true, "sky": true}
	axialQuantities = map[string]bool{"b_field": true, "gradient": true, "dust_polarization": true, "starlight_polarization": true, "unspecified": true}
)

func (s *Server) dataset(id int64) (catalog.Dataset, int, string) {
	d, err := s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		return d, 404, "dataset not found"
	}
	if err != nil {
		log.Printf("dataset: %v", err)
		return d, 500, "internal error"
	}
	return d, 0, ""
}

// imageDataset is dataset for operations that need a pixel grid: a point catalog (ADR 0044) is refused with 409.
func (s *Server) imageDataset(id int64) (catalog.Dataset, int, string) {
	d, code, msg := s.dataset(id)
	if code == 0 && d.Kind == catalog.KindCatalog {
		return d, 409, "this is a point catalog: it has no pixel grid, so this operation does not apply to it"
	}
	return d, code, msg
}

// interpret records what the user declares about a dataset: today only "axial_orientation" with a required unit
// and an optional reliability companion on the same grid. Nothing is inferred and the file is never modified.
// POST, so the JSON content-type and cross-site guards apply.
func (s *Server) interpret(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Kind          string  `json:"kind"`
		Unit          string  `json:"unit"`
		ReliabilityID *int64  `json:"reliabilityId"`
		Frame         *string `json:"frame"`
		Quantity      *string `json:"quantity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	d, code, msg := s.dataset(id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if d.Kind == catalog.KindCatalog {
		fail(w, 409, "a point catalog has no pixel grid: declare its angle column in the catalog declaration instead")
		return
	}
	switch in.Kind {
	case "none":
		if err := s.Store.ClearInterpretation(id); err != nil {
			log.Printf("interpret: %v", err)
			fail(w, 500, "internal error")
			return
		}
	case "axial_orientation":
		if n := nonDegenerateAxes(d.Header); n != 2 {
			fail(w, 422, "an orientation map must be a single plane (two axes longer than 1)")
			return
		}
		if in.Unit != "deg" && in.Unit != "rad" {
			fail(w, 400, "unit is required and must be deg or rad; Vesqira does not guess it from the values")
			return
		}
		if in.Frame != nil && !axialFrames[*in.Frame] {
			fail(w, 400, "frame must be pixel, sky or null (not declared); Vesqira does not guess it from the values")
			return
		}
		if in.Quantity != nil && !axialQuantities[*in.Quantity] {
			fail(w, 400, "quantity must be b_field, gradient, dust_polarization, starlight_polarization, unspecified or null (not declared)")
			return
		}
		if in.ReliabilityID != nil {
			if *in.ReliabilityID == id {
				fail(w, 422, "a map cannot be its own reliability")
				return
			}
			rel, code, msg := s.imageDataset(*in.ReliabilityID)
			if code != 0 {
				fail(w, 422, "reliability dataset: "+msg)
				return
			}
			why, err := compat.Grids(d.Header, rel.Header)
			if err != nil {
				fail(w, 500, "internal error")
				return
			}
			if len(why) > 0 {
				writeJSON(w, 422, map[string]any{"error": "the reliability map is not known to share this map's grid", "reasons": why})
				return
			}
		}
		if err := s.Store.SetInterpretation(d, catalog.Interpretation{Kind: in.Kind, Unit: in.Unit, ReliabilityID: in.ReliabilityID, Frame: in.Frame, Quantity: in.Quantity}); err != nil {
			log.Printf("interpret: %v", err)
			fail(w, 500, "internal error")
			return
		}
	default:
		fail(w, 400, "kind must be axial_orientation or none")
		return
	}
	d, code, msg = s.dataset(id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 200, d)
}

// axial answers with doubled-angle orientation statistics for the whole map or a region, using the declared
// unit and, when declared, the reliability companion as weights.
func (s *Server) axial(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	in := d.Interpretation
	if in == nil || in.Kind != "axial_orientation" {
		fail(w, 409, "this dataset is not declared as an axial orientation map")
		return
	}
	if in.Stale {
		fail(w, 409, "the file changed since you declared it as an orientation map; declare it again to confirm the unit still applies")
		return
	}
	req := map[string]any{"op": "axial", "path": real, "unit": in.Unit}
	k.op, k.q = "axial", in.Unit
	if in.ReliabilityID != nil {
		rel, code, msg := s.imageDataset(*in.ReliabilityID)
		if code != 0 {
			fail(w, 409, "the declared reliability dataset: "+msg)
			return
		}
		if rel.HostID != d.HostID {
			fail(w, 409, "the reliability map is on another host: both maps must be on the same machine to be used together")
			return
		}
		if rel.Interpretation != nil && rel.Interpretation.Kind == "axial_orientation" {
			fail(w, 409, "the declared reliability dataset is itself declared as angles")
			return
		}
		why, gerr := compat.Grids(d.Header, rel.Header)
		if gerr != nil || len(why) > 0 {
			writeJSON(w, 409, map[string]any{"error": "the reliability map no longer shares this map's grid", "reasons": why})
			return
		}
		wreal, wk, status, msg := s.authorize(r.Context(), rel)
		if status != 0 {
			fail(w, status, "reliability map: "+msg)
			return
		}
		req["weights_path"] = wreal
		k.q += "|w" + strconv.FormatInt(wk.id, 10) + "," + strconv.FormatInt(wk.size, 10) + "," + strconv.FormatInt(wk.mtime, 10)
	}
	if shape := r.URL.Query().Get("shape"); shape != "" {
		names := map[string][]string{"rect": {"x0", "y0", "x1", "y1"}, "circle": {"cx", "cy", "r"}}[shape]
		if names == nil {
			fail(w, 400, "shape must be rect or circle")
			return
		}
		req["shape"] = shape
		k.q += "|" + shape
		for _, n := range names {
			v, ok := integer(w, r, n)
			if !ok {
				return
			}
			req[n] = v
			k.q += "," + strconv.Itoa(v)
		}
	}
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

// nonDegenerateAxes counts the axes longer than 1 in a catalogued header.
func nonDegenerateAxes(h json.RawMessage) int {
	var v struct {
		Axes []struct {
			Length int `json:"length"`
		} `json:"axes"`
	}
	if json.Unmarshal(h, &v) != nil {
		return 0
	}
	n := 0
	for _, a := range v.Axes {
		if a.Length > 1 {
			n++
		}
	}
	return n
}

// compatibility reports whether two registered datasets are known to share a grid. Comparing maps links their
// cursors and channels only when they do; otherwise the reasons are shown and nothing is resampled or merged.
func (s *Server) compatibility(w http.ResponseWriter, r *http.Request) {
	a, errA := strconv.ParseInt(r.URL.Query().Get("a"), 10, 64)
	b, errB := strconv.ParseInt(r.URL.Query().Get("b"), 10, 64)
	if errA != nil || errB != nil {
		fail(w, 400, "a and b must be dataset ids")
		return
	}
	da, code, msg := s.imageDataset(a)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	db, code, msg := s.imageDataset(b)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	why, err := compat.Grids(da.Header, db.Header)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	if why == nil {
		why = []string{}
	}
	writeJSON(w, 200, map[string]any{"compatible": len(why) == 0, "reasons": why})
}
