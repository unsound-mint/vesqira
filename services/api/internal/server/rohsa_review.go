package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

// Reviewing a ROHSA decomposition as a whole (ADR 0046, "Reviewing the decomposition"): a sigma-v diagram, the pixels behind one of
// its cells, and one map per width class. Each reads every row of the run's .dat once, so each runs as an analysis program (heavy
// workload class, its own memory cap and time budget). A fit kept on a host is reviewed there, by the host's own copy of the program
// (hosts.HostReview, hostrun.py): the .dat or the maps are read where they are and never copied here, the diagram's pixel index goes in
// the host's cache folder, and the class maps are written in the host's derived folder and registered as datasets of that host.

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

// reviewParams turns the request into the analysis program's parameters; what is out of range is refused here with a sentence.
func (s *Server) reviewParams(in rohsaReviewReq, op string, t rohsaTask) (map[string]any, string) {
	w := in.Weight
	if w == "" {
		w = "integrated"
	}
	if !rohsaReviewWeights[w] {
		return nil, "weight must be integrated, column_thin or count"
	}
	p := map[string]any{"dat_path": t.datReal, "n_gauss": t.run.Info.NGauss, "weight": w, "decomposition": t.decl}
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
	// the diagram writes a per-row cell index here and a cell click reads it, so a click never parses the .dat again; a host keeps it in
	// its own cache folder, which its program fixes (the request cannot name one)
	if t.host == nil {
		p["cache_dir"] = s.CacheDir
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
// over the crop and the .dat, here or, for a fit kept on a host, there. stem names the class maps' files (without ".fits"; the folder is
// this computer's derived folder, or the host's, where only its file name is used) and is nil for the diagram and the cell. It answers the
// errors itself and returns ok=false.
func (s *Server) rohsaReview(w http.ResponseWriter, r *http.Request, op string, stem func(rohsaTask) string) (rohsaReviewReq, rohsaTask, analysisOutcome, bool) {
	var in rohsaReviewReq
	if !body(w, r, 16<<10, &in, true) {
		return in, rohsaTask{}, analysisOutcome{}, false
	}
	t, ok := s.rohsaResolve(w, r, in.RunID)
	if !ok {
		return in, t, analysisOutcome{}, false
	}
	params, bad := s.reviewParams(in, op, t)
	if bad != "" {
		fail(w, 400, bad)
		return in, t, analysisOutcome{}, false
	}
	pj, _ := json.Marshal(params)
	opts := []string{"--input", t.cropReal, "--params", string(pj)}
	if u := declaredUnitOf(t.crop); u != "" { // a unit the person declared for a crop with no BUNIT; the worker uses it only to fill that gap
		opts = append(opts, "--declared-unit="+u)
	}
	var res analysisOutcome
	var code int
	var msg string
	if t.host != nil {
		res, code, msg = s.hostReview(r.Context(), *t.host, op, stem, t, opts)
	} else {
		args := append([]string{"-P", s.Analysis.Script, op}, opts...)
		if stem != nil {
			args = append(args, "--out", stem(t)+".fits")
		}
		res, code, msg = s.runAnalysisProgram(r, args)
	}
	if code != 0 {
		fail(w, code, msg)
		return in, t, res, false
	}
	return in, t, res, true
}

// hostReview runs a review on the host the fit is kept on (ADR 0068). The budget, the heavy slot, the corrections and the memory cap are a
// local run's; the host's program reads the fit and the crop where they are. Class maps come back as the resolved paths of the files it
// placed in the host's derived folder, one per class, under the name asked for.
func (s *Server) hostReview(ctx context.Context, h hosts.Host, op string, stem func(rohsaTask) string, t rohsaTask, opts []string) (analysisOutcome, int, string) {
	name := ""
	if stem != nil {
		name = filepath.Base(stem(t))
	}
	// the same lane as a local review: on a group server one running batch item per account, ten waiting (batchlane.go)
	leave, err := s.batchSlot(ctx)
	if err != nil {
		if full, ok := err.(errBatchFull); ok {
			return analysisOutcome{}, 429, full.Error()
		}
		return analysisOutcome{}, 499, "request cancelled"
	}
	defer leave()
	slow := "this took longer on the host than an analysis may (%s), so Vesqira stopped waiting and the program on the host stopped with it; a fit this large is better reviewed on the host as a job"
	res, out, code, msg := s.hostCall(ctx, h, op, opts, slow, func(argv []string, timeout time.Duration) ([]byte, error) {
		return s.Remote.HostReview(ctx, h, op, name, argv, timeout)
	})
	if code != 0 {
		return analysisOutcome{}, code, msg
	}
	if stem != nil {
		var sum struct {
			Classes []classSummary `json:"classes"`
		}
		if json.Unmarshal(res.Summary, &sum) != nil || len(res.Extra) != len(sum.Classes) || len(res.Extra) == 0 {
			return analysisOutcome{}, 502, "the host answered unexpectedly"
		}
		folder := path.Dir(res.Extra[0])
		for i, f := range res.Extra {
			// the host resolved its derived folder; each file must be the one asked for, in that one folder
			if !hostFileNamed(f, fmt.Sprintf("%s-class%d.fits", name, i)) || path.Dir(f) != folder {
				return analysisOutcome{}, 502, "the host answered unexpectedly"
			}
		}
		out.Files = res.Extra
	}
	return out, 0, ""
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
	in, t, res, ok := s.rohsaReview(w, r, "rohsa_classmaps", func(t rohsaTask) string {
		stem = fmt.Sprintf("rohsa-classes-%d-%s", t.run.RunID, hex.EncodeToString(b[:]))
		if t.host == nil { // on a host the folder is the host's derived folder, which its program chooses
			stem = filepath.Join(s.derivedDirOf(wsOf(r)), stem)
		}
		return stem
	})
	// a failed or refused run here may have written some classes before it stopped; on a host the program removes its own temporaries
	// and places the maps all or none, so there is nothing to remove from here
	cleanup := func() {
		if stem != "" && t.host == nil {
			removeClassFiles(stem)
		}
	}
	if !ok {
		cleanup()
		return
	}
	var sum struct {
		Classes []classSummary `json:"classes"`
		Weight  string         `json:"weight"`
	}
	if json.Unmarshal(res.Summary, &sum) != nil || len(sum.Classes) != len(in.Edges)+1 {
		cleanup()
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
	for i, c := range sum.Classes {
		var nd catalog.Dataset
		var code int
		var msg string
		if t.host != nil {
			// the file is on the host, in its derived folder; the dataset is that file there, like a subset written on a host
			nd, code, msg = s.registerRemoteFile(waiting(r.Context()), *t.host, res.Files[i])
			if code != 0 {
				msg = "the width class maps were written on the host (" + path.Base(res.Files[0]) + " and the others of its name, in its derived folder) but class " + strconv.Itoa(c.Index) + " could not be registered: " + msg
			}
		} else {
			nd, code, msg = s.registerPath(waiting(r.Context()), fmt.Sprintf("%s-class%d.fits", stem, c.Index))
		}
		if code != 0 {
			cleanup()
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
	where := ""
	if t.host != nil {
		where = ", written on host " + t.host.Name
	}
	s.Audit.RecordCtx(r.Context(), "ROHSA width class maps", "run", t.run.RunID, fmt.Sprintf("%d classes, weight %s%s", len(out), sum.Weight, where))
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
