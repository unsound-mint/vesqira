package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// A cross-run plot saved as a board figure (ADR 0065). It is a figure of its own kind: no dataset, and a description of the runs
// it was drawn from instead of a scene. The description (cross-run/1) names run ids, names and values, so like a scene it is
// owner-only: it lives in figures.scene, is never copied into a snapshot or a report, and the reviewer-safe provenance holds only
// the query, the axes, the number of runs and the conversion. The server writes the description from its own reading of the runs;
// the browser only says which runs, which axes and which conversion, and a conversion is accepted only when the worker, asked
// again, gives the factor the browser used.

const (
	// CrossRunSchema is the schema of the owner-only description in figures.scene.
	CrossRunSchema  = "cross-run/1"
	maxCrossRunRuns = 5000
	maxCrossRunWord = 400
)

type crossRunValue struct {
	Value           float64  `json:"value"`
	Unit            string   `json:"unit"`
	Uncertainty     *float64 `json:"uncertainty"`
	UncertaintyKind string   `json:"uncertaintyKind"`
	Definition      string   `json:"definition"`
}

type crossRunMember struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Y is what the run recorded for the result, never replaced by a conversion.
	Y crossRunValue `json:"y"`
	// X is the parameter's text or the result's number at the time of saving.
	X string `json:"x"`
	// Converted is the number the plot drew, when the run was converted into the target unit.
	Converted *struct {
		Value       float64  `json:"value"`
		Uncertainty *float64 `json:"uncertainty"`
	} `json:"converted,omitempty"`
}

type crossRunFrom struct {
	Unit   string  `json:"unit"`
	Factor float64 `json:"factor"`
	Count  int     `json:"count"`
}

type crossRunConversion struct {
	To     string         `json:"to"`
	Method string         `json:"method"`
	From   []crossRunFrom `json:"from"`
}

type crossRunSpec struct {
	Schema     string              `json:"schema"`
	Query      string              `json:"query"`
	Y          string              `json:"y"`
	X          string              `json:"x"`
	XKind      string              `json:"xKind"`
	By         string              `json:"by"`
	Conversion *crossRunConversion `json:"conversion,omitempty"`
	Runs       []crossRunMember    `json:"runs"`
}

// safeRunPlot is the reviewer-safe account of a cross-run plot: the words and the counts, never a run id, name or value.
type safeRunPlot struct {
	Query      string              `json:"query"`
	Y          string              `json:"y"`
	X          string              `json:"x"`
	XKind      string              `json:"xKind"`
	By         string              `json:"by,omitempty"`
	Runs       int                 `json:"runs"`
	Conversion *crossRunConversion `json:"conversion,omitempty"`
}

type crossRunRequest struct {
	Title      string  `json:"title"`
	Caption    string  `json:"caption"`
	PreviewPNG string  `json:"previewPng"`
	Query      string  `json:"query"`
	Y          string  `json:"y"`
	X          string  `json:"x"`
	XKind      string  `json:"xKind"`
	By         string  `json:"by"`
	RunIDs     []int64 `json:"runIds"`
	Conversion *struct {
		To      string `json:"to"`
		Entries []struct {
			RunID  int64   `json:"runId"`
			Factor float64 `json:"factor"`
		} `json:"entries"`
	} `json:"conversion"`
	RedrawnFrom *int64 `json:"redrawnFrom"`
}

func oneLineText(s string, max int) bool {
	return utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, func(r rune) bool { return r < 32 || r == 127 })
}

func lowerEq(a, b string) bool { return strings.EqualFold(a, b) }

// createRunFigure is POST /api/figures/runs.
func (s *Server) createRunFigure(w http.ResponseWriter, r *http.Request) {
	var in crossRunRequest
	if !body(w, r, 12<<20, &in, true) {
		return
	}
	if in.XKind != "param" && in.XKind != "metric" {
		fail(w, 400, "xKind must be param or metric")
		return
	}
	for _, t := range []string{in.Query, in.Y, in.X, in.By} {
		if !oneLineText(t, maxCrossRunWord) {
			fail(w, 400, "the query and the axis names must be one line of at most 400 characters")
			return
		}
	}
	if in.Y == "" || in.X == "" {
		fail(w, 400, "a plot needs a result (y) and something to draw it against (x)")
		return
	}
	if len(in.RunIDs) == 0 || len(in.RunIDs) > maxCrossRunRuns {
		fail(w, 400, "a figure of a cross-run plot needs between 1 and 5000 runs")
		return
	}
	png, err := base64.StdEncoding.DecodeString(in.PreviewPNG)
	if err != nil {
		fail(w, 400, "previewPng must be base64")
		return
	}
	spec, code, msg := s.crossRunSpecOf(r.Context(), in)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	var linkRev string
	var identity json.RawMessage // the only owner-side record a cross-run plot keeps beside its description: the figure it redraws
	if in.RedrawnFrom != nil {
		orig, err := s.Boards.Figure(wsOf(r), *in.RedrawnFrom)
		if err != nil || orig.Kind != boards.KindCrossRun {
			fail(w, 400, "a cross-run plot can only be drawn again from a cross-run plot")
			return
		}
		linkRev = orig.Revision
		identity, _ = json.Marshal(figureIdentity{Schema: "figure-identity/1", Datasets: []figureDatasetIdentity{}, RedrawnFrom: &redrawnFrom{FigureID: orig.ID, Revision: orig.Revision}})
	}
	sceneJSON, err := json.Marshal(spec)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	prov, err := crossRunProvenance(spec, time.Now())
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	f, err := s.Boards.CreateFigure(wsOf(r), actorID(r.Context()), boards.NewFigure{Title: in.Title, Caption: in.Caption, Provenance: prov, Scene: sceneJSON, Identity: identity, RedrawnFrom: linkRev}, png)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 201, f)
}

// crossRunSpecOf reads the runs the request names and writes the description from the catalog's own rows. A run that is not in the
// catalog, a run without the result, or an x that is not a parameter or result of the run is refused: a figure never names what
// the catalog does not hold.
func (s *Server) crossRunSpecOf(ctx context.Context, in crossRunRequest) (crossRunSpec, int, string) {
	spec := crossRunSpec{Schema: CrossRunSchema, Query: in.Query, Y: in.Y, X: in.X, XKind: in.XKind, By: in.By, Runs: []crossRunMember{}}
	if s.Runs == nil {
		return spec, 503, "runs are not available on this server"
	}
	all, err := s.Runs.List(wsCtx(ctx))
	if err != nil {
		log.Printf("cross-run figure: %v", err)
		return spec, 500, "internal error"
	}
	byID := make(map[int64]runs.Run, len(all))
	for _, rn := range all {
		byID[rn.ID] = rn
	}
	ids := append([]int64(nil), in.RunIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			return spec, 400, "a run is named twice"
		}
		rn, ok := byID[id]
		if !ok {
			return spec, 400, fmt.Sprintf("run %d is not in the catalog", id)
		}
		var ym *runs.Metric
		for k := range rn.Metrics {
			if lowerEq(rn.Metrics[k].Key, in.Y) {
				ym = &rn.Metrics[k]
			}
		}
		if ym == nil || ym.Value == nil || math.IsNaN(*ym.Value) || math.IsInf(*ym.Value, 0) {
			return spec, 400, fmt.Sprintf("run %q did not record %s, so it cannot be in the plot", rn.Name, in.Y)
		}
		m := crossRunMember{ID: rn.ID, Name: rn.Name, Y: crossRunValue{Value: *ym.Value, Unit: ym.Unit, Uncertainty: ym.Uncertainty, UncertaintyKind: ym.UncertaintyKind, Definition: ym.Definition}}
		found := false
		if in.XKind == "param" {
			for _, p := range rn.Params {
				if lowerEq(p.Key, in.X) {
					m.X, found = p.Value, p.Value != ""
				}
			}
		} else {
			for _, xm := range rn.Metrics {
				if lowerEq(xm.Key, in.X) && xm.Value != nil {
					m.X, found = strconv.FormatFloat(*xm.Value, 'g', -1, 64), true
				}
			}
		}
		if !found {
			return spec, 400, fmt.Sprintf("run %q did not record %s, so it cannot be in the plot", rn.Name, in.X)
		}
		spec.Runs = append(spec.Runs, m)
	}
	if in.Conversion != nil {
		if code, msg := s.verifyConversion(ctx, &spec, in); code != 0 {
			return spec, code, msg
		}
	}
	return spec, 0, ""
}

// verifyConversion asks the worker again for every conversion the browser applied, per recorded unit, and records the worker's
// numbers. The browser's factor must be the worker's, and everything converted must be defined the same way as something recorded
// in the target unit: a result defined differently is never merged (ADR 0062).
func (s *Server) verifyConversion(ctx context.Context, spec *crossRunSpec, in crossRunRequest) (int, string) {
	c := in.Conversion
	if c.To == "" || len(c.To) > maxConvertUnit || len(c.Entries) == 0 {
		return 400, "a conversion needs its target unit and the runs it converted"
	}
	svc := s.planesOnRaw(0)
	if svc == nil {
		return 503, "the scientific worker is not configured"
	}
	idx := make(map[int64]int, len(spec.Runs))
	for i, m := range spec.Runs {
		idx[m.ID] = i
	}
	native := map[string]bool{} // definitions that some run recorded in the target unit itself
	conv := map[int64]float64{}
	for _, e := range c.Entries {
		if _, ok := idx[e.RunID]; !ok || conv[e.RunID] != 0 || math.IsNaN(e.Factor) || e.Factor <= 0 {
			return 400, "a conversion names a run that is not in the plot, twice, or without a factor"
		}
		conv[e.RunID] = e.Factor
	}
	for _, m := range spec.Runs {
		if _, c2 := conv[m.ID]; !c2 && m.Y.Unit == c.To && m.Y.Definition != "" {
			native[m.Y.Definition] = true
		}
	}
	groups := map[string][]int{}
	for id := range conv {
		i := idx[id]
		m := spec.Runs[i]
		if m.Y.Unit == "" || m.Y.Unit == c.To || m.Y.Definition == "" || !native[m.Y.Definition] {
			return 400, fmt.Sprintf("run %q cannot be converted into %s: its unit, or the way its result is defined, does not match a result recorded in %s", m.Name, c.To, c.To)
		}
		groups[m.Y.Unit] = append(groups[m.Y.Unit], i)
	}
	units := make([]string, 0, len(groups))
	for u := range groups {
		units = append(units, u)
	}
	sort.Strings(units)
	out := crossRunConversion{To: c.To}
	for _, unit := range units {
		members := groups[unit]
		sort.Ints(members)
		vals := make([]convertItem, len(members))
		for k, i := range members {
			v := spec.Runs[i].Y.Value
			vals[k] = convertItem{Value: &v, Uncertainty: spec.Runs[i].Y.Uncertainty, From: unit}
		}
		rep, err := svc.Do(ctx, map[string]any{"op": "convert_values", "to": c.To, "values": vals})
		var re *worker.RequestError
		switch {
		case errors.As(err, &re):
			return 422, re.Message
		case err != nil:
			log.Printf("cross-run figure conversion: %v", err)
			return 502, "the scientific worker is unavailable"
		}
		var res struct {
			Refused bool                `json:"refused"`
			Reason  string              `json:"reason"`
			To      string              `json:"to"`
			Factors map[string]float64  `json:"factors"`
			Results []convertItemResult `json:"results"`
			Method  string              `json:"method"`
		}
		if json.Unmarshal(rep.Head, &res) != nil || (!res.Refused && len(res.Results) != len(members)) {
			return 502, "the worker answered unexpectedly"
		}
		if res.Refused {
			return 422, "not converted: " + res.Reason
		}
		f := res.Factors[unit]
		for _, i := range members {
			if want := conv[spec.Runs[i].ID]; math.Abs(want-f) > 1e-12*math.Abs(f) {
				return 409, "the conversion the plot used does not match the worker's; draw the plot again and save it"
			}
		}
		for k, i := range members {
			spec.Runs[i].Converted = &struct {
				Value       float64  `json:"value"`
				Uncertainty *float64 `json:"uncertainty"`
			}{res.Results[k].Value, res.Results[k].Uncertainty}
		}
		out.Method = res.Method
		out.From = append(out.From, crossRunFrom{Unit: unit, Factor: f, Count: len(members)})
	}
	spec.Conversion = &out
	return 0, ""
}

// crossRunProvenance is the reviewer-safe provenance of a cross-run plot. It uses the shape every figure's provenance has, so
// boards, reports and the review read it unchanged, and says plainly that there is no file: the figure was drawn from run records.
// It carries the query words, the axes, the count and the conversion; no run id, name or value.
func crossRunProvenance(spec crossRunSpec, at time.Time) (json.RawMessage, error) {
	var pr Provenance
	pr.Source.Name = fmt.Sprintf("cross-run plot of %d runs", len(spec.Runs))
	pr.Source.ModifiedAt = at.UTC()
	pr.Source.Identity = "no file: drawn from the run records in the catalog, as they were when it was saved"
	pr.Source.Header = safeHeader{Axes: []safeAxis{}}
	pr.Tiers = []safeTier{}
	pr.View = ViewParams{Kind: "runplot"}
	pr.RunPlot = &safeRunPlot{Query: spec.Query, Y: spec.Y, X: spec.X, XKind: spec.XKind, By: spec.By, Runs: len(spec.Runs), Conversion: spec.Conversion}
	pr.Statements = []string{
		fmt.Sprintf("%s drawn against %s for %d runs that match %s.", spec.Y, spec.X, len(spec.Runs), queryText(spec.Query)),
		"Every number is what its run declared; Vesqira neither computes nor verifies run results.",
		"Runs are not ranked and no run is called best.",
	}
	if c := spec.Conversion; c != nil {
		for _, f := range c.From {
			pr.Statements = append(pr.Statements, fmt.Sprintf("%d runs recorded in %s are shown converted to %s (x %s), by plain unit conversion with no equivalency; what each run recorded is kept by the author.", f.Count, f.Unit, c.To, strconv.FormatFloat(f.Factor, 'g', 8, 64)))
		}
	}
	return json.Marshal(pr)
}

func queryText(q string) string {
	if strings.TrimSpace(q) == "" {
		return "no filter (all runs)"
	}
	return "the filter \"" + q + "\""
}
