package server

import "net/http"

// Sky panel (ADR 0072). Where a dataset lies on the sky and where a pixel is, in the panel's frame. Both answers come from the
// worker's astropy WCS with the researcher's corrections in force (the same ones every other read carries); the browser
// never computes a position from a header. A dataset with no celestial WCS is answered 200 with footprint null and the
// reason, so the panel can list it as having none; no position is ever guessed.

// skyFrame is the panel frame the person asked for: only these two are drawn.
func skyFrame(w http.ResponseWriter, r *http.Request) (string, bool) {
	f := r.URL.Query().Get("frame")
	if f != "icrs" && f != "galactic" {
		fail(w, 400, "frame must be icrs or galactic")
		return "", false
	}
	return f, true
}

func (s *Server) skyFootprint(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	frame, ok := skyFrame(w, r)
	if !ok {
		return
	}
	k.op, k.channel, k.q = "sky_footprint", -1, frame
	rep, ok := s.ask(w, r, k, map[string]any{"op": "sky_footprint", "path": real, "frame": frame})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

// skyPoint is the position of one pixel (the viewer's cursor) in the panel's frame.
func (s *Server) skyPoint(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	frame, ok := skyFrame(w, r)
	if !ok {
		return
	}
	var v [2]int
	for i, n := range []string{"x", "y"} {
		if v[i], ok = index(w, r, n); !ok {
			return
		}
	}
	k.op, k.channel, k.x, k.y, k.q = "sky_point", -1, v[0], v[1], frame
	rep, ok := s.ask(w, r, k, map[string]any{"op": "sky_point", "path": real, "frame": frame, "x": v[0], "y": v[1]})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}
