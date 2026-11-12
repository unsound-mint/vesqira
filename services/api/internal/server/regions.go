package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
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
	d, err := s.Store.Get(wsOf(r), id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return d, false
	}
	if err != nil {
		log.Printf("regions: %v", err)
		fail(w, 500, "internal error")
		return d, false
	}
	if d.Kind == catalog.KindCatalog {
		fail(w, 409, "regions are drawn on images; a point catalog has no pixel grid")
		return d, false
	}
	return d, true
}

func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	rs, err := s.Regions.List(wsOf(r), d.ID)
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
	reg, err := s.Regions.Create(wsOf(r), actorID(r.Context()), d.ID, in.Name, in.Shape)
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
	reg, err := s.Regions.Update(wsOf(r), id, in.Name, in.Shape)
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
	if err := s.Regions.Delete(wsOf(r), id); err != nil {
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
	rs, err := s.Regions.List(wsOf(r), d.ID)
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

// importRegions reads a DS9 region file. Without apply it only reports what each line would become; with apply it
// saves the exact ones, and the approximated ones only when asked.
func (s *Server) importRegions(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Text                string `json:"text"`
		Apply               bool   `json:"apply"`
		IncludeApproximated bool   `json:"includeApproximated"`
	}
	if !body(w, r, 300<<10, &in, true) {
		return
	}
	items := regions.ParseDS9(in.Text)
	for _, it := range items {
		if it.Status == regions.Pending { // a region on the sky needs the file and its WCS
			_, real, k, ok := s.source(w, r)
			if !ok {
				return
			}
			items = s.placeSkyRegions(r.Context(), k.host, real, items)
			break
		}
	}
	if !in.Apply {
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	created := []regions.Region{}
	failed := []string{}
	for _, it := range items {
		if it.Shape == nil || (it.Status == regions.Approximate && !in.IncludeApproximated) {
			continue
		}
		reg, err := s.Regions.Create(wsOf(r), actorID(r.Context()), d.ID, it.Name, *it.Shape)
		if err != nil {
			failed = append(failed, it.Name+": "+strings.TrimPrefix(err.Error(), "invalid: "))
			continue
		}
		created = append(created, reg)
	}
	s.Audit.RecordCtx(r.Context(), "regions imported", "dataset", d.ID, strconv.Itoa(len(created))+" from a DS9 file")
	writeJSON(w, 201, map[string]any{"items": items, "created": created, "failed": failed})
}

// placeSkyRegions settles the DS9 regions written on the sky using the dataset's own WCS, evaluated by the worker. A region
// is placed only if its frame fits the dataset's declared frame; nothing is transformed between frames.
func (s *Server) placeSkyRegions(ctx context.Context, host int64, real string, items []regions.ImportItem) []regions.ImportItem {
	svc := s.planesOn(host)
	if svc == nil {
		return regions.ResolveSky(items, "", func([][2]float64) (regions.Placement, error) {
			return regions.Placement{}, errors.New("the scientific worker is not available")
		})
	}
	frame := ""
	if rep, err := svc.Do(ctx, map[string]any{"op": "view", "path": real}); err == nil {
		var v struct {
			Sky struct {
				Kind  string  `json:"kind"`
				Frame *string `json:"frame"`
			} `json:"sky"`
		}
		if json.Unmarshal(rep.Head, &v) == nil && v.Sky.Kind == "celestial" && v.Sky.Frame != nil {
			frame = strings.ToLower(*v.Sky.Frame)
		}
	}
	return regions.ResolveSky(items, frame, func(pts [][2]float64) (regions.Placement, error) {
		list := make([][]float64, len(pts))
		for i, p := range pts {
			list[i] = []float64{p[0], p[1]}
		}
		rep, err := svc.Do(ctx, map[string]any{"op": "sky2pix", "path": real, "points": list})
		var re *worker.RequestError
		if errors.As(err, &re) {
			return regions.Placement{}, errors.New(re.Message)
		}
		if err != nil {
			return regions.Placement{}, errors.New("the scientific worker did not answer")
		}
		var out struct {
			Pixels [][]float64 `json:"pixels"`
			Scale  []float64   `json:"pixel_scale_arcsec"`
			North  *float64    `json:"north_angle_deg"`
		}
		if json.Unmarshal(rep.Head, &out) != nil || len(out.Pixels) != len(pts) || len(out.Scale) != 2 {
			return regions.Placement{}, errors.New("unreadable answer from the worker")
		}
		pl := regions.Placement{ScaleX: out.Scale[0], ScaleY: out.Scale[1], NorthAngle: out.North, Pixels: make([][2]float64, len(pts))}
		for i, p := range out.Pixels {
			if len(p) == 2 {
				pl.Pixels[i] = [2]float64{p[0], p[1]}
			} else {
				pl.Pixels[i] = [2]float64{math.NaN(), math.NaN()}
			}
		}
		return pl, nil
	})
}
