package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// Channel maps and position-velocity diagrams (ADR 0049). Three routes; every number is computed by the worker.
//
//	GET  /datasets/{id}/channel-grid  the plan of a grid of panels: channels, spectral labels (ADR 0040), the shared scale
//	GET  /datasets/{id}/slab          one panel that averages several channels (a single channel is the plane route)
//	POST /datasets/{id}/pv            a position-velocity slice along a drawn path

const (
	axialNoAverage = "this dataset is declared as an axial orientation map; averaging its angles arithmetically across channels or along a path is not valid"
	maxPVBody      = 16 << 10
)

// spectralAsk reads the optional spectral unit and velocity convention shared by the grid and the diagram.
func spectralAsk(w http.ResponseWriter, unit, convention string, req map[string]any) (key string, ok bool) {
	if unit != "" {
		if !catalog.ValidUnit(unit) {
			fail(w, 400, "unit is not a valid unit")
			return "", false
		}
		req["unit"], key = unit, key+"|u"+unit
	}
	if convention != "" {
		if !convConventions[convention] {
			fail(w, 400, "convention must be radio, optical or relativistic")
			return "", false
		}
		req["convention"], key = convention, key+"|c"+convention
	}
	return key, true
}

func (s *Server) notAxial(w http.ResponseWriter, d catalog.Dataset) bool {
	if d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation" {
		fail(w, 409, axialNoAverage)
		return false
	}
	return true
}

// channelGrid answers with the plan of a channel-map grid as JSON (no payload): which channels each panel shows or
// averages, its spectral label in the file's unit or a converted one with the convention that was used, how the panels are
// reduced to fit the pixel caps, and the one colour scale they share.
func (s *Server) channelGrid(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok || !s.notAxial(w, d) {
		return
	}
	q := r.URL.Query()
	req := map[string]any{"op": "channel_grid", "path": real}
	key := ""
	for _, n := range []struct {
		name string
		lo   int
		def  string
	}{{"first", 0, ""}, {"last", 0, ""}, {"stride", 1, "1"}, {"average", 1, "1"}} {
		t := q.Get(n.name)
		if t == "" {
			t = n.def
		}
		v, err := strconv.Atoi(t)
		if err != nil || v < n.lo || v > scene.MaxGridChannels {
			fail(w, 400, n.name+" must be a whole number of "+strconv.Itoa(n.lo)+" or more")
			return
		}
		req[n.name], key = v, key+n.name+"="+t+";"
	}
	if t := q.Get("limits"); t != "" {
		parts := strings.Split(t, ",")
		lo, e1 := strconv.ParseFloat(parts[0], 64)
		var hi float64
		var e2 error
		if len(parts) == 2 {
			hi, e2 = strconv.ParseFloat(parts[1], 64)
		}
		if len(parts) != 2 || e1 != nil || e2 != nil || math.IsNaN(lo) || math.IsInf(lo, 0) || math.IsNaN(hi) || math.IsInf(hi, 0) || !(lo < hi) {
			fail(w, 400, "limits must be two finite numbers, the lower below the upper")
			return
		}
		req["limits"], key = []float64{lo, hi}, key+"limits="+t+";"
	}
	sk, ok := spectralAsk(w, q.Get("unit"), q.Get("convention"), req)
	if !ok {
		return
	}
	k.op, k.channel, k.q = "channel_grid", -1, key+sk
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

// slab answers with the per-pixel mean of channels first..last as a plane frame, with the same optional block-mean preview
// as a plane. The head says how many samples were excluded as not finite.
func (s *Server) slab(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok || !s.notAxial(w, d) {
		return
	}
	first, ok := index(w, r, "first")
	if !ok {
		return
	}
	last, ok := index(w, r, "last")
	if !ok {
		return
	}
	req := map[string]any{"op": "slab", "path": real, "first": first, "last": last}
	k.op, k.channel, k.q = "slab", first, "last="+strconv.Itoa(last)
	if t := r.URL.Query().Get("step"); t != "" {
		step, err := strconv.Atoi(t)
		if err != nil || step < 1 || step > 64 {
			fail(w, 400, "step must be an integer from 1 to 64")
			return
		}
		if step > 1 {
			k.step, req["step"] = step, step
		}
	}
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	writeFrame(w, rep)
}

// pv answers with a position-velocity diagram: a frame whose payload is the offsets (arcsec along the path, or pixels when
// the file has no celestial WCS), the spectral axis, the values and the number of finite samples averaged into each.
func (s *Server) pv(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok || !s.notAxial(w, d) {
		return
	}
	var in struct {
		Points        [][]int  `json:"points"`
		Width         *float64 `json:"width"`
		WidthUnit     string   `json:"widthUnit"`
		Interpolation string   `json:"interpolation"`
		First         *int     `json:"first"`
		Last          *int     `json:"last"`
		Unit          string   `json:"unit"`
		Convention    string   `json:"convention"`
	}
	if !body(w, r, maxPVBody, &in, true) {
		return
	}
	if len(in.Points) < 2 || len(in.Points) > regions.MaxLinePoints {
		fail(w, 400, "the path needs 2 to "+strconv.Itoa(regions.MaxLinePoints)+" vertices")
		return
	}
	for _, p := range in.Points {
		if len(p) != 2 || p[0] < -(1<<20) || p[0] > 1<<20 || p[1] < -(1<<20) || p[1] > 1<<20 {
			fail(w, 400, "each vertex is a pair of whole pixel numbers")
			return
		}
	}
	if in.Width == nil || math.IsNaN(*in.Width) || math.IsInf(*in.Width, 0) || *in.Width <= 0 || *in.Width > regions.MaxLineWidth {
		fail(w, 400, "width must be a positive number")
		return
	}
	if in.WidthUnit != "pixel" && in.WidthUnit != "arcsec" {
		fail(w, 400, "widthUnit must be pixel or arcsec")
		return
	}
	if in.Interpolation != "linear" && in.Interpolation != "nearest" {
		fail(w, 400, "interpolation must be linear (bilinear) or nearest (no interpolation)")
		return
	}
	req := map[string]any{"op": "pv", "path": real, "points": in.Points, "width": *in.Width, "width_unit": in.WidthUnit, "interpolation": in.Interpolation}
	key := ""
	for _, p := range in.Points {
		key += strconv.Itoa(p[0]) + "," + strconv.Itoa(p[1]) + ";"
	}
	key += "w" + strconv.FormatFloat(*in.Width, 'g', -1, 64) + in.WidthUnit + "|" + in.Interpolation
	for name, v := range map[string]*int{"first": in.First, "last": in.Last} {
		if v == nil {
			continue
		}
		if *v < 0 || *v > scene.MaxGridChannels {
			fail(w, 400, name+" must be a channel number")
			return
		}
		req[name], key = *v, key+"|"+name+strconv.Itoa(*v)
	}
	sk, ok := spectralAsk(w, in.Unit, in.Convention, req)
	if !ok {
		return
	}
	k.op, k.channel, k.q = "pv", -1, key+sk
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	writeFrameAs(w, rep, "application/vnd.vesqira.pv")
}
