package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Reviewing a ROHSA decomposition as a whole (ADR 0046, "Reviewing the decomposition"): a sigma-v diagram, the pixels behind one of
// its cells, and one map per width class. Each reads every row of the run's .dat once, so each runs as an analysis program (heavy
// workload class, its own memory cap and time budget), on this computer only: a fit on a host is refused, because the analysis
// program is local and class maps would be files written on the host.

const (
	maxReviewBins    = 128 // per axis; the worker checks again
	maxReviewClasses = 8
)

type binSpec struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	N   int     `json:"n"`
}

func (b *binSpec) check(name string) string {
	if b == nil {
		return ""
	}
	if b.N < 1 || b.N > maxReviewBins {
		return fmt.Sprintf("the %s bins: n must be from 1 to %d", name, maxReviewBins)
	}
	if math.IsNaN(b.Min) || math.IsInf(b.Min, 0) || math.IsNaN(b.Max) || math.IsInf(b.Max, 0) || !(b.Max > b.Min) {
		return "the " + name + " bins need finite limits with max above min"
	}
	return ""
}

type rohsaReviewReq struct {
	RunID        int64    `json:"runId"`
	Weight       string   `json:"weight"` // integrated (default), column_thin or count
	VelocityBins *binSpec `json:"velocityBins"`
	SigmaBins    *binSpec `json:"sigmaBins"`
	Cell         *struct {
		S *int `json:"s"`
		V *int `json:"v"`
	} `json:"cell"`
	Edges []float64 `json:"edges"` // class maps: the width edges in km/s, ascending
}

var rohsaReviewWeights = map[string]bool{"integrated": true, "column_thin": true, "count": true}

// params turns the request into the analysis program's parameters; what is out of range is refused here with a sentence.
func (in rohsaReviewReq) params(op string, t rohsaTask) (map[string]any, string) {
	w := in.Weight
	if w == "" {
		w = "integrated"
	}
	if !rohsaReviewWeights[w] {
		return nil, "weight must be integrated, column_thin or count"
	}
	p := map[string]any{"dat_path": t.datReal, "n_gauss": t.run.Info.NGauss, "weight": w}
	if op == "rohsa_classmaps" {
		if len(in.Edges) < 1 || len(in.Edges) > maxReviewClasses-1 {
			return nil, fmt.Sprintf("give from 1 to %d width edges", maxReviewClasses-1)
		}
		for i, e := range in.Edges {
			if math.IsNaN(e) || math.IsInf(e, 0) || e <= 0 || (i > 0 && e <= in.Edges[i-1]) {
				return nil, "the width edges must be positive and strictly increasing"
			}
		}
		p["edges"] = in.Edges
		return p, ""
	}
	if msg := in.VelocityBins.check("centre"); msg != "" {
		return nil, msg
	}
	if msg := in.SigmaBins.check("width"); msg != "" {
		return nil, msg
	}
	if in.VelocityBins != nil {
		p["velocity_bins"] = in.VelocityBins
	}
	if in.SigmaBins != nil {
		p["sigma_bins"] = in.SigmaBins
	}
	if op == "rohsa_cellmask" {
		if in.Cell == nil || in.Cell.S == nil || in.Cell.V == nil || *in.Cell.S < 0 || *in.Cell.V < 0 || *in.Cell.S >= maxReviewBins || *in.Cell.V >= maxReviewBins {
			return nil, "name the cell as bin numbers s and v"
		}
		p["cell"] = map[string]int{"s": *in.Cell.S, "v": *in.Cell.V}
	}
	return p, ""
}

// rohsaReview validates the request, resolves the run (the dataset must be its crop or exactly on its grid) and runs one analysis op
// over the crop and the .dat. It answers the errors itself and returns ok=false.
func (s *Server) rohsaReview(w http.ResponseWriter, r *http.Request, op string, extraArgs func(rohsaTask) []string) (rohsaReviewReq, rohsaTask, analysisOutcome, bool) {
	var in rohsaReviewReq
	if !body(w, r, 16<<10, &in, true) {
		return in, rohsaTask{}, analysisOutcome{}, false
	}
	// refuse a fit on a host before anything is asked of the host: the review reads the .dat with a local analysis program
	rr, err := s.Runs.ROHSA(wsOf(r), in.RunID)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "ROHSA run not found")
		return in, rohsaTask{}, analysisOutcome{}, false
	}
	if err != nil {
		log.Printf("rohsa: %v", err)
		fail(w, 500, "internal error")
		return in, rohsaTask{}, analysisOutcome{}, false
	}
	if rr.Info.HostID != 0 {
		fail(w, 409, "this fit lives on a host, and the decomposition review runs on this computer only: import a copy of the fit here to review it")
		return in, rohsaTask{}, analysisOutcome{}, false
	}
	t, ok := s.rohsaResolve(w, r, in.RunID)
	if !ok {
		return in, t, analysisOutcome{}, false
	}
	params, bad := in.params(op, t)
	if bad != "" {
		fail(w, 400, bad)
		return in, t, analysisOutcome{}, false
	}
	pj, _ := json.Marshal(params)
	args := []string{"-P", s.Analysis.Script, op, "--input", t.cropReal, "--params", string(pj)}
	if u := declaredUnitOf(t.crop); u != "" { // a unit the person declared for a crop with no BUNIT; the worker uses it only to fill that gap
		args = append(args, "--declared-unit="+u)
	}
	if extraArgs != nil {
		args = append(args, extraArgs(t)...)
	}
	res, code, msg := s.runAnalysisProgram(r, args)
	if code != 0 {
		fail(w, code, msg)
		return in, t, res, false
	}
	return in, t, res, true
}

// reviewReply is the worker's summary with the run it was made from, as one JSON object.
func (s *Server) reviewReply(w http.ResponseWriter, status int, res analysisOutcome, t rohsaTask, extra map[string]any) {
	var sum map[string]json.RawMessage
	if json.Unmarshal(res.Summary, &sum) != nil {
		fail(w, 502, "the analysis answered unexpectedly")
		return
	}
	put := func(k string, v any) {
		b, _ := json.Marshal(v)
		sum[k] = b
	}
	put("runId", t.run.RunID)
	put("cropName", t.crop.Name)
	put("software", derived.CleanSoftware(res.Software))
	for k, v := range extra {
		put(k, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, sum)
}

// rohsaSigmav answers the diagram: the weighted 2D histogram of width against centre, with each cell's contributor count.
func (s *Server) rohsaSigmav(w http.ResponseWriter, r *http.Request) {
	_, t, res, ok := s.rohsaReview(w, r, "rohsa_sigmav", nil)
	if !ok {
		return
	}
	s.reviewReply(w, 200, res, t, nil)
}

// rohsaCellMask answers the pixels that have a component in one cell of the diagram, as a compact packed mask.
func (s *Server) rohsaCellMask(w http.ResponseWriter, r *http.Request) {
	_, t, res, ok := s.rohsaReview(w, r, "rohsa_cellmask", nil)
	if !ok {
		return
	}
	s.reviewReply(w, 200, res, t, nil)
}

type classSummary struct {
	Index  int      `json:"index"`
	Label  string   `json:"label"`
	Lo     float64  `json:"lo"`
	Hi     *float64 `json:"hi"`
	Comps  int      `json:"components"`
	Weight float64  `json:"weight_total"`
}

// rohsaClassMaps sums the decomposition per width class and registers one map per class as a derived dataset (on the crop's sky
// grid), each with a record of the run, the cut, the weighting and the fit it was made from.
func (s *Server) rohsaClassMaps(w http.ResponseWriter, r *http.Request) {
	var b [4]byte
	_, _ = rand.Read(b[:])
	var stem string
	in, t, res, ok := s.rohsaReview(w, r, "rohsa_classmaps", func(t rohsaTask) []string {
		stem = filepath.Join(s.derivedDirOf(wsOf(r)), fmt.Sprintf("rohsa-classes-%d-%s", t.run.RunID, hex.EncodeToString(b[:])))
		return []string{"--out", stem + ".fits"}
	})
	if !ok {
		if stem != "" { // a failed run may have written some classes before it stopped
			removeClassFiles(stem)
		}
		return
	}
	var sum struct {
		Classes []classSummary `json:"classes"`
		Weight  string         `json:"weight"`
	}
	if json.Unmarshal(res.Summary, &sum) != nil || len(sum.Classes) != len(in.Edges)+1 {
		removeClassFiles(stem)
		fail(w, 502, "the analysis answered unexpectedly")
		return
	}
	var full map[string]json.RawMessage
	_ = json.Unmarshal(res.Summary, &full)
	type registered struct {
		Dataset catalog.Dataset `json:"dataset"`
		Class   classSummary    `json:"class"`
	}
	var out []registered
	var ids []int64
	for _, c := range sum.Classes {
		path := fmt.Sprintf("%s-class%d.fits", stem, c.Index)
		nd, code, msg := s.registerPath(waiting(r.Context()), path)
		if code != 0 {
			removeClassFiles(stem)
			fail(w, code, msg)
			return
		}
		out = append(out, registered{nd, c})
		ids = append(ids, nd.ID)
	}
	// What each map's record keeps: the run (by id and name), the fit by file name, size and time (the run record holds no location, and
	// neither does this), the cut and the weighting. The crop is the source: the maps are on its sky grid.
	var dat json.RawMessage = full["dat"]
	for _, o := range out {
		params, _ := json.Marshal(map[string]any{
			"runId": t.run.RunID, "runName": t.run.RunName, "weight": sum.Weight, "edges": in.Edges, "classIndex": o.Class.Index,
			"classMaps": ids, "dat": dat,
		})
		summ, _ := json.Marshal(map[string]any{
			"policy": rawString(full["policy"]) + " This map: width class " + o.Class.Label + " (" + strconv.Itoa(o.Class.Comps) + " components over the crop; weight: " +
				rawString(full["weight_how"]) + ").",
			"class": o.Class, "weight": sum.Weight, "weightUnit": rawString(full["weight_unit"]), "unitSource": rawString(full["unit_source"]),
			"invalidSigma": full["invalid_sigma"], "pixelsWithoutValidComponent": full["pixels_without_valid_component"],
			"convention": rawString(full["convention"]),
		})
		if err := s.Derived.Save(derived.Record{DatasetID: o.Dataset.ID, Op: "rohsa_class_map", Params: params, Sources: []int64{t.crop.ID}, Summary: summ, Software: res.Software}); err != nil {
			log.Printf("derived: %v", err)
		}
	}
	s.Audit.RecordCtx(r.Context(), "ROHSA width class maps", "run", t.run.RunID, fmt.Sprintf("%d classes, weight %s", len(out), sum.Weight))
	s.reviewReply(w, 201, res, t, map[string]any{"maps": out})
}

func rawString(r json.RawMessage) string {
	var s string
	_ = json.Unmarshal(r, &s)
	return s
}

func removeClassFiles(stem string) {
	for i := 0; i < maxReviewClasses; i++ {
		_ = os.Remove(fmt.Sprintf("%s-class%d.fits", stem, i))
	}
}
