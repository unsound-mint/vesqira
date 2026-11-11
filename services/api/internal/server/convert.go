package server

import (
	"math"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// conversions lists which display conversions the file allows (spectral units, sky frames, brightness units) and why the
// others are not available. Facts about the file and its (declared) unit; the browser only offers what is enabled.
func (s *Server) conversions(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	k.op, k.channel = "conversions", -1
	rep, ok := s.ask(w, r, k, map[string]any{"op": "conversions", "path": real})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

var (
	convFrames      = map[string]bool{"icrs": true, "fk5": true, "galactic": true}
	convConventions = map[string]bool{"radio": true, "optical": true, "relativistic": true}
)

// convert answers, for one pixel and channel, the requested unit conversions and frame transforms with the native values
// beside them. Every conversion is done by the worker (astropy); this handler only validates the request and keeps an
// orientation map (angles, not a brightness) out of brightness conversion.
func (s *Server) convert(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	var v [3]int
	for i, n := range []string{"x", "y", "channel"} {
		if v[i], ok = index(w, r, n); !ok {
			return
		}
	}
	q := r.URL.Query()
	req := map[string]any{"op": "convert", "path": real, "x": v[0], "y": v[1], "channel": v[2]}
	key := ""
	for _, n := range []string{"spectral", "to"} {
		if t := q.Get(n); t != "" {
			if !catalog.ValidUnit(t) {
				fail(w, 400, n+" is not a valid unit")
				return
			}
			req[n], key = t, key+n+"="+t+";"
		}
	}
	if t := q.Get("convention"); t != "" {
		if !convConventions[t] {
			fail(w, 400, "convention must be radio, optical or relativistic")
			return
		}
		req["convention"], key = t, key+"convention="+t+";"
	}
	if t := q.Get("frame"); t != "" {
		if !convFrames[t] {
			fail(w, 400, "frame must be icrs, fk5 or galactic")
			return
		}
		req["frame"], key = t, key+"frame="+t+";"
	}
	if t := q.Get("value"); t != "" {
		f, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			fail(w, 400, "value must be a finite number")
			return
		}
		req["value"], key = f, key+"value="+strconv.FormatFloat(f, 'g', -1, 64)+";"
	}
	if req["to"] != nil && d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation" {
		fail(w, 409, "this dataset is declared as an axial orientation map: its values are angles, not a brightness, so no brightness unit conversion applies")
		return
	}
	k.op, k.x, k.y, k.channel, k.q = "convert", v[0], v[1], v[2], key
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}
