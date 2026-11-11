package server

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// geometryKinds are the overlay kinds the worker places on a base map (ADR 0042, section 3). Regions are drawn from the
// scene alone, and same-dataset orientation lines from the plane on screen; the route still serves those for one call path.
var geometryKinds = map[string]bool{"contours": true, "orientation": true, "beam": true, "grid": true}

// overlayGeometry answers with the geometry of one overlay on this dataset as the base, in base pixels, as a binary frame.
// Overlays from another dataset are placed by WCS, never resampled. Every file the overlay reads is authorized as a plane
// read is (inside the roots, unchanged since it was registered), and must be on the base's machine. What the catalog
// declares about the overlay dataset (its unit for a file without BUNIT, the angle convention, the reliability map) is
// taken from the catalog, never from the request. A refusal from the worker (frames that cannot be related, footprints that
// do not overlap, a missing declaration, no beam) is a 422 with its reason.
func (s *Server) overlayGeometry(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	var in struct {
		Overlay json.RawMessage `json:"overlay"`
		Spacing *float64        `json:"spacing"`
	}
	if !body(w, r, scene.MaxBytes+1024, &in, true) {
		return
	}
	o, err := scene.ParseOverlay(in.Overlay)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !geometryKinds[o.Kind] {
		fail(w, 400, o.Kind+" overlays have no geometry to compute")
		return
	}
	if o.Kind == "grid" && o.DatasetID != d.ID {
		fail(w, 400, "a coordinate grid is drawn from the base map's own WCS: its dataset is the base")
		return
	}
	ov, ovReal, ovKey := d, real, k
	if o.DatasetID != d.ID {
		var code int
		var msg string
		if ov, code, msg = s.dataset(o.DatasetID); code != 0 {
			fail(w, code, "overlay dataset: "+msg)
			return
		}
		if ov.HostID != d.HostID {
			fail(w, 409, "the overlay dataset is on another machine: both maps must be on the same machine to be drawn together")
			return
		}
		var status int
		if ovReal, ovKey, status, msg = s.authorize(r.Context(), ov); status != 0 {
			fail(w, status, "overlay dataset: "+msg)
			return
		}
	}
	// The cache key is everything that can change the answer: the base and overlay file identities (and declared units, in
	// the keys), the overlay's geometry settings (not its colour or visibility), the declarations read from the catalog,
	// the reliability map's identity and the spacing.
	spec := o
	spec.ID, spec.Visible, spec.Style = "geometry", true, scene.Style{}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	var specMap map[string]any
	if err := json.Unmarshal(specJSON, &specMap); err != nil {
		fail(w, 500, "internal error")
		return
	}
	req := map[string]any{"op": "overlay_geometry", "path": real, "overlay_path": ovReal, "overlay": specMap}
	k.op = "overlay"
	k.q = string(specJSON) + "|o" + identity(ovKey) + "|u" + ovKey.unit
	axial := ov.Interpretation != nil && ov.Interpretation.Kind == "axial_orientation"
	switch o.Kind {
	case "contours":
		if ovKey.unit != "" {
			req["overlay_declared_bunit"] = ovKey.unit
		}
		req["overlay_axial"] = axial
		k.q += "|a" + strconv.FormatBool(axial)
	case "orientation":
		if !axial {
			fail(w, 409, "the overlay dataset is not declared as an axial orientation map")
			return
		}
		in := ov.Interpretation
		if in.Stale {
			fail(w, 409, "the orientation map changed since it was declared; declare it again to confirm its unit and convention still apply")
			return
		}
		req["overlay_interp"] = map[string]any{"unit": in.Unit, "frame": in.Frame, "quantity": in.Quantity}
		k.q += "|i" + in.Unit + "," + deref(in.Frame) + "," + deref(in.Quantity)
		needsRel := o.Opacity == "reliability" || o.Length == "reliability" || o.Threshold != nil
		if needsRel && in.ReliabilityID != nil {
			relReal, relKey, code, msg := s.reliabilityFor(r, ov, *in.ReliabilityID)
			if code != 0 {
				fail(w, code, msg)
				return
			}
			req["reliability_path"] = relReal
			k.q += "|r" + identity(relKey)
		}
	}
	if o.Kind == "orientation" && o.Sampling != nil && o.Sampling.Mode == "auto" {
		if in.Spacing == nil || math.IsNaN(*in.Spacing) || *in.Spacing < 1 || *in.Spacing > 4096 {
			fail(w, 400, "automatic sampling needs spacing, the wanted distance between lines in base pixels (1 to 4096)")
			return
		}
		sp := math.Round(*in.Spacing*100) / 100
		req["spacing"] = sp
		k.q += "|s" + strconv.FormatFloat(sp, 'g', -1, 64)
	}
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	writeFrameAs(w, rep, "application/vnd.vesqira.geometry")
}

// reliabilityFor authorizes the reliability map declared with an orientation map, held to the same rules as the
// orientation statistics: same machine, not itself declared as angles, on the map's grid, unchanged since registered.
func (s *Server) reliabilityFor(r *http.Request, d catalog.Dataset, relID int64) (string, cacheKey, int, string) {
	rel, code, msg := s.dataset(relID)
	if code != 0 {
		return "", cacheKey{}, 409, "the declared reliability dataset: " + msg
	}
	if rel.HostID != d.HostID {
		return "", cacheKey{}, 409, "the reliability map is on another host: both maps must be on the same machine to be used together"
	}
	if rel.Interpretation != nil && rel.Interpretation.Kind == "axial_orientation" {
		return "", cacheKey{}, 409, "the declared reliability dataset is itself declared as angles"
	}
	why, err := compat.Grids(d.Header, rel.Header)
	if err != nil {
		log.Printf("reliability grid: %v", err)
		return "", cacheKey{}, 500, "internal error"
	}
	if len(why) > 0 {
		return "", cacheKey{}, 409, "the reliability map no longer shares the orientation map's grid"
	}
	real, k, status, msg := s.authorize(r.Context(), rel)
	if status != 0 {
		return "", cacheKey{}, status, "reliability map: " + msg
	}
	return real, k, 0, ""
}

func identity(k cacheKey) string {
	return strconv.FormatInt(k.id, 10) + "," + strconv.FormatInt(k.size, 10) + "," + strconv.FormatInt(k.mtime, 10)
}

func deref(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}
