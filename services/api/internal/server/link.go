package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// linkRegionStep is the grid the visible region is widened to before it is asked of the worker and keyed in the cache, in
// pixels of the first map. A small pan then asks for the same region again and the cache answers; widening only ever covers
// more, so the approximation's measured error still holds over everything the pane shows.
const linkRegionStep = 0.5

const linkMaxPoints = 64

type linkRegion struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// link answers what linking this map to another needs (ADR 0042, section 4), computed by the worker with astropy: for the
// cursor and the view an affine approximation of this map's pixels to the other's over the visible region with its measured
// error, plus exact positions of a few points; for the channel the nearest channel of the other map for every channel of this
// one, by spectral world value, or the reason the spectral axes cannot be related. The parts are independent: each is
// absent when not asked for and carries its own refusal. Both files are authorized as a plane read is and must be on one
// machine; a point catalog has no grid to link. A pair that cannot be related is an answer (the reason), not an error.
func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	var in struct {
		OtherID  int64       `json:"otherId"`
		Region   *linkRegion `json:"region"`
		Points   [][]float64 `json:"points"`
		Channels bool        `json:"channels"`
	}
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	if in.OtherID == d.ID {
		fail(w, 400, "a map is not linked to itself")
		return
	}
	if in.Region == nil && len(in.Points) == 0 && !in.Channels {
		fail(w, 400, "ask for a region (sky), points or channels")
		return
	}
	if len(in.Points) > linkMaxPoints {
		fail(w, 400, "at most "+strconv.Itoa(linkMaxPoints)+" points")
		return
	}
	if len(in.Points) > 0 && in.Region == nil {
		fail(w, 400, "points are mapped together with the region they lie in")
		return
	}
	other, code, msg := s.dataset(wsOf(r), in.OtherID)
	if code != 0 {
		fail(w, code, "other map: "+msg)
		return
	}
	if other.Kind == catalog.KindCatalog {
		fail(w, 409, "the other dataset is a point catalog: it has no pixel grid to link")
		return
	}
	if other.HostID != d.HostID {
		fail(w, 409, "the other map is on another machine: both maps must be on the same machine to be linked")
		return
	}
	otherReal, otherKey, status, msg := s.authorize(r.Context(), other)
	if status != 0 {
		fail(w, status, "other map: "+msg)
		return
	}
	req := map[string]any{"op": "link", "path": real, "other_path": otherReal}
	q := new(strings.Builder)
	q.WriteString("o" + identity(otherKey) + "|u" + otherKey.unit)
	num := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 1e7 }
	if in.Region != nil {
		g := *in.Region
		if !finite(g.X0) || !finite(g.Y0) || !finite(g.X1) || !finite(g.Y1) {
			fail(w, 400, "region must be finite numbers")
			return
		}
		if !(g.X1 > g.X0 && g.Y1 > g.Y0) {
			fail(w, 400, "region must have x1 > x0 and y1 > y0")
			return
		}
		g = linkRegion{math.Floor(g.X0/linkRegionStep) * linkRegionStep, math.Floor(g.Y0/linkRegionStep) * linkRegionStep,
			math.Ceil(g.X1/linkRegionStep) * linkRegionStep, math.Ceil(g.Y1/linkRegionStep) * linkRegionStep}
		req["region"] = map[string]any{"x0": g.X0, "y0": g.Y0, "x1": g.X1, "y1": g.Y1}
		q.WriteString("|r" + num(g.X0) + "," + num(g.Y0) + "," + num(g.X1) + "," + num(g.Y1))
	}
	if len(in.Points) > 0 {
		q.WriteString("|p")
		for _, p := range in.Points {
			if len(p) != 2 || !finite(p[0]) || !finite(p[1]) {
				fail(w, 400, "points must be pairs of finite numbers")
				return
			}
			q.WriteString(num(p[0]) + "," + num(p[1]) + ";")
		}
		req["points"] = in.Points
	}
	if in.Channels {
		req["channels"] = true
		q.WriteString("|c")
	}
	k.op, k.channel, k.q = "link", -1, q.String()
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(rep.Head)
}
