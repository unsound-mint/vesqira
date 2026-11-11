package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Relative orientation (ADR 0047): a declared orientation map against a point catalog with a declared angle (mode A) or against
// another declared orientation map on the same grid (mode B). The worker (relorient.py) computes; this file checks that both
// sides are declared well enough to be turned into the B-field orientation, runs it as an analysis on this computer and keeps
// the result like any analysis result: a registered file (the per-source table for mode A, the |Δψ| map for mode B) with a
// derived record holding every number, the parameters and the seed.

// relOrientParams are the parameters a person may set; nil means the stated default (relorient.check_params).
type relOrientParams struct {
	RadiusArcmin *float64 `json:"radiusArcmin"`
	MinPixels    *int     `json:"minPixels"`
	NShifts      *int     `json:"nShifts"`
	MinShiftDeg  *float64 `json:"minShiftDeg"`
	KeepFraction *float64 `json:"keepFraction"`
	Seed         *int64   `json:"seed"`
	Bins         *int     `json:"bins"`
	// Optional catalog row filter P/σP >= SnrMin, off unless both columns (declared value columns of the catalog) are named.
	SnrColumn      *string  `json:"snrColumn"`
	SnrSigmaColumn *string  `json:"snrSigmaColumn"`
	SnrMin         *float64 `json:"snrMin"`
}

type relOrientReq struct {
	DatasetID int64           `json:"datasetId"` // the orientation map
	OtherID   int64           `json:"otherId"`   // a catalog (mode A) or a second orientation map (mode B)
	Params    relOrientParams `json:"params"`
	// Masks of the map and (for two maps) of the second one, each with its own policy (ADR 0055). The weight policy is accepted here:
	// the statistic already has weights.
	Masks      []maskUse `json:"masks"`
	OtherMasks []maskUse `json:"otherMasks"`
}

// worker turns the parameters into the worker's names, refusing values outside the ranges the worker accepts (it checks again).
func (p relOrientParams) worker(catalogMode bool) (map[string]any, string) {
	out := map[string]any{}
	num := func(name string, v *float64, lo, hi float64) string {
		if v == nil {
			return ""
		}
		if math.IsNaN(*v) || *v < lo || *v > hi {
			return fmt.Sprintf("%s must be between %g and %g", name, lo, hi)
		}
		return ""
	}
	whole := func(name string, v *int, lo, hi int) string {
		if v != nil && (*v < lo || *v > hi) {
			return fmt.Sprintf("%s must be a whole number between %d and %d", name, lo, hi)
		}
		return ""
	}
	for _, msg := range []string{
		num("minShiftDeg", p.MinShiftDeg, 0, 180), num("keepFraction", p.KeepFraction, 0.05, 1),
		whole("nShifts", p.NShifts, 0, 20000), whole("bins", p.Bins, 2, 90),
	} {
		if msg != "" {
			return nil, msg
		}
	}
	if p.Seed != nil && *p.Seed < 0 {
		return nil, "seed must be a non-negative whole number"
	}
	if catalogMode {
		if msg := num("radiusArcmin", p.RadiusArcmin, 1e-3, 600); msg != "" {
			return nil, msg
		}
		if msg := whole("minPixels", p.MinPixels, 1, 1000000); msg != "" {
			return nil, msg
		}
		named := func(v *string) bool { return v != nil && *v != "" }
		if named(p.SnrColumn) != named(p.SnrSigmaColumn) || (p.SnrMin != nil && !named(p.SnrColumn)) {
			return nil, "the P/σP filter needs both columns: choose the column holding P and the column holding σP"
		}
		if named(p.SnrColumn) {
			if *p.SnrColumn == *p.SnrSigmaColumn {
				return nil, "the P/σP filter needs two different columns"
			}
			if msg := num("snrMin", p.SnrMin, 0, 1e6); msg != "" {
				return nil, msg
			}
			out["snr_column"], out["snr_sigma_column"] = *p.SnrColumn, *p.SnrSigmaColumn
			if p.SnrMin != nil {
				out["snr_min"] = *p.SnrMin
			}
		}
		if p.RadiusArcmin != nil {
			out["radius_arcmin"] = *p.RadiusArcmin
		}
		if p.MinPixels != nil {
			out["min_pixels"] = *p.MinPixels
		}
	} else if p.RadiusArcmin != nil || p.MinPixels != nil || p.SnrColumn != nil || p.SnrSigmaColumn != nil || p.SnrMin != nil {
		return nil, "radiusArcmin, minPixels and the P/σP filter apply to a comparison with a catalog only"
	}
	if p.NShifts != nil {
		out["n_shifts"] = *p.NShifts
	}
	if p.MinShiftDeg != nil {
		out["min_shift_deg"] = *p.MinShiftDeg
	}
	if p.KeepFraction != nil {
		out["keep_fraction"] = *p.KeepFraction
	}
	if p.Seed != nil {
		out["seed"] = *p.Seed
	}
	if p.Bins != nil {
		out["bins"] = *p.Bins
	}
	return out, ""
}

// orientationSide checks that a map can enter a relative-orientation comparison and returns the worker options that describe
// it (prefix "" or "ref-"). Every missing declaration is named; nothing is assumed.
func (s *Server) orientationSide(r *http.Request, d catalog.Dataset, prefix, which string) ([]string, int, string) {
	in := d.Interpretation
	if in == nil || in.Kind != "axial_orientation" {
		return nil, 422, which + " is not declared as an axial orientation map: declare its unit, frame and what the angle is first"
	}
	if in.Stale {
		return nil, 409, which + ": the file changed since it was declared as an orientation map; declare it again"
	}
	var missing []string
	if in.Frame == nil {
		missing = append(missing, "the angle's frame (pixel or sky)")
	}
	if in.Quantity == nil {
		missing = append(missing, "what the angle is (B-field, gradient, dust polarization or starlight polarization)")
	} else if *in.Quantity == "unspecified" {
		missing = append(missing, "what the angle is (it is declared as 'unspecified')")
	}
	if len(missing) > 0 {
		return nil, 422, which + ": " + strings.Join(missing, " and ") + " must be declared before it can be compared with another tracer; the file does not say it"
	}
	if d.HostID != 0 {
		return nil, 409, which + " lives on another host: relative orientation runs on this computer only for now; copy the map here or compare it on the host"
	}
	if n := nonDegenerateAxes(d.Header); n != 2 {
		return nil, 422, which + " is not a single plane"
	}
	real, _, status, msg := s.authorize(r.Context(), d)
	if status != 0 {
		return nil, status, which + ": " + msg
	}
	args := []string{"--" + prefix + "axial-unit", in.Unit, "--" + prefix + "axial-frame", *in.Frame, "--" + prefix + "axial-quantity", *in.Quantity}
	if prefix == "" {
		args = append([]string{"--input", real}, args...)
	} else {
		args = append([]string{"--reference", real}, args...)
	}
	if in.ReliabilityID != nil {
		p, code, msg := s.reliabilityPath(r.Context(), d, false)
		if code != 0 {
			return nil, code, which + ": " + msg
		}
		args = append(args, "--"+prefix+"weights", p)
	}
	return args, 0, ""
}

// relativeOrientation is POST /api/analysis/relative-orientation.
func (s *Server) relativeOrientation(w http.ResponseWriter, r *http.Request) {
	var in relOrientReq
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	d, code, msg := s.imageDataset(in.DatasetID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if in.OtherID == in.DatasetID {
		fail(w, 422, "choose another dataset to compare with")
		return
	}
	other, code, msg := s.dataset(in.OtherID)
	if code != 0 {
		fail(w, code, "the other dataset: "+msg)
		return
	}
	catalogMode := other.Kind == catalog.KindCatalog
	params, bad := in.Params.worker(catalogMode)
	if bad != "" {
		fail(w, 400, bad)
		return
	}
	args, code, msg := s.orientationSide(r, d, "", "the map")
	if code != 0 {
		fail(w, code, msg)
		return
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	stem := filepath.Join(s.Analysis.DerivedDir, fmt.Sprintf("relorient-%d-%d-%s", d.ID, other.ID, hex.EncodeToString(b[:])))
	op, outPath, tablePath := "relorient_maps", stem+".fits", stem+"-table.csv"
	if catalogMode {
		op, outPath, tablePath = "relorient_catalog", stem+".csv", ""
		if other.HostID != 0 {
			fail(w, 409, "the catalog lives on another host; catalogs are read on this computer only")
			return
		}
		decl, code, msg := declarationArg(other)
		if code != 0 {
			fail(w, code, "the catalog: "+msg)
			return
		}
		if decl["angle"] == nil {
			fail(w, 422, "the catalog has no angle column declared: declare which column is the position angle, its unit and what it measures")
			return
		}
		creal, _, status, msg := s.authorize(r.Context(), other)
		if status != 0 {
			fail(w, status, "the catalog: "+msg)
			return
		}
		dj, _ := json.Marshal(decl)
		args = append(args, "--catalog", creal, "--declaration", string(dj))
	} else {
		if why, err := compat.Grids(d.Header, other.Header); err != nil || len(why) > 0 {
			writeJSON(w, 409, map[string]any{"error": "the maps are not known to share a grid, so pixels cannot be compared one to one: align one onto the other first (align resamples orientation maps as angles)", "reasons": why})
			return
		}
		ra, code, msg := s.orientationSide(r, other, "ref-", "the second map")
		if code != 0 {
			fail(w, code, msg)
			return
		}
		args = append(args, ra...)
		args = append(args, "--table-out", tablePath)
	}
	var maskSafe, otherSafe []map[string]any
	if len(in.Masks) > 0 || len(in.OtherMasks) > 0 {
		if catalogMode {
			fail(w, 422, "masks are applied to pixels of two maps; a comparison with a catalog does not take them yet")
			return
		}
		specs, safe, _, code, msg := s.maskSpecs(in.Masks, d, true)
		if code != 0 {
			fail(w, code, "the map: "+msg)
			return
		}
		if specs != "" {
			args = append(args, "--masks", specs)
		}
		ospecs, osafe, _, code, msg := s.maskSpecs(in.OtherMasks, other, true)
		if code != 0 {
			fail(w, code, "the second map: "+msg)
			return
		}
		if ospecs != "" {
			args = append(args, "--ref-masks", ospecs)
		}
		maskSafe, otherSafe = safe, osafe
	}
	pj, _ := json.Marshal(params)
	full := append([]string{"-P", s.Analysis.Script, op}, args...)
	full = append(full, "--params", string(pj), "--out", outPath)
	res, code, msg := s.runAnalysisProgram(r, full)
	cleanup := func() {
		_ = os.Remove(outPath)
		if tablePath != "" {
			_ = os.Remove(tablePath)
		}
	}
	if code != 0 {
		cleanup()
		fail(w, code, msg)
		return
	}
	nd, code, msg := s.registerPath(r.Context(), outPath)
	if code != 0 {
		cleanup()
		fail(w, code, msg)
		return
	}
	// The parameters as the worker used them (defaults filled in, seed included) are in the summary; params keep what was asked.
	recParams := map[string]any{"options": params, "mode": map[bool]string{true: "map_catalog", false: "map_map"}[catalogMode]}
	if k := relorientConstruction(catalogMode, res.Summary, time.Now()); k != nil { // Vesqira ran the null, so it records how it was built
		recParams["controls"] = runs.Controls{Construction: k}
	}
	if len(maskSafe) > 0 || len(otherSafe) > 0 {
		recParams["masks"] = map[string]any{"map": maskSafe, "other": otherSafe}
	}
	rec, _ := json.Marshal(recParams)
	if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: op, Params: rec, Sources: []int64{d.ID, other.ID}, Summary: res.Summary, Software: res.Software}); err != nil {
		log.Printf("derived: %v", err)
	}
	s.Audit.Record("relative orientation", "dataset", nd.ID, d.Name+" with "+other.Name)
	writeJSON(w, 201, map[string]any{"dataset": nd, "summary": res.Summary})
}

var tableFileName = regexp.MustCompile(`^relorient-[0-9]+-[0-9]+-[0-9a-f]{8}(-table)?\.csv$`)

// relativeOrientationTable is GET /api/datasets/{id}/relative-orientation.csv: the per-sample table of a relative-orientation
// result as a download. Only a file this program wrote into the derived folder, named by the result's own record, is served.
func (s *Server) relativeOrientationTable(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	rec, err := s.Derived.Get(id)
	if err != nil || (rec.Op != "relorient_catalog" && rec.Op != "relorient_maps") {
		fail(w, 404, "not a relative-orientation result")
		return
	}
	var sum struct {
		Table struct {
			File   *string `json:"file"`
			Reason *string `json:"reason"`
		} `json:"table"`
	}
	_ = json.Unmarshal(rec.Summary, &sum)
	if sum.Table.File == nil {
		why := "this result has no table"
		if sum.Table.Reason != nil {
			why = *sum.Table.Reason
		}
		fail(w, 404, why)
		return
	}
	name := *sum.Table.File
	if !tableFileName.MatchString(name) {
		fail(w, 404, "this result has no table")
		return
	}
	f, err := os.Open(filepath.Join(s.Analysis.DerivedDir, name))
	if err != nil {
		fail(w, 404, "the table file is no longer in the derived folder")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	st, err := f.Stat()
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// relorientConstruction is how the chance-level null of a relative-orientation result was built, read from the summary the worker
// returned: the method it names and the parameters it says it used (defaults filled in, seed included). It is verified because
// Vesqira ran the analysis and records what the worker reported, and only when the null actually ran: a summary whose null was
// skipped, or that does not say, yields nil and nothing is claimed.
func relorientConstruction(catalogMode bool, summary json.RawMessage, now time.Time) *runs.Construction {
	var sum struct {
		Parameters map[string]json.Number `json:"parameters"`
		Null       struct {
			Method  string          `json:"method"`
			Skipped json.RawMessage `json:"skipped"`
			Kept    *int            `json:"kept"`
		} `json:"null"`
	}
	if json.Unmarshal(summary, &sum) != nil || sum.Null.Method == "" || (len(sum.Null.Skipped) > 0 && string(sum.Null.Skipped) != "null") {
		return nil
	}
	k := &runs.Construction{Kind: "shifted_map_null", Params: []runs.ControlParam{}, Note: sum.Null.Method,
		Stated: runs.Stated{Basis: runs.BasisVerified, By: runs.ByVesqira, At: now.UTC().Format(time.RFC3339)}}
	if catalogMode {
		k.Kind = "other" // the catalog null moves the source pattern over the map, not a map over a map
	}
	for _, name := range []string{"n_shifts", "min_shift_deg", "keep_fraction", "seed"} {
		if v, ok := sum.Parameters[name]; ok {
			k.Params = append(k.Params, runs.ControlParam{Key: name, Value: v.String()})
		}
	}
	if sum.Null.Kept != nil {
		k.Params = append(k.Params, runs.ControlParam{Key: "shifts_kept", Value: strconv.Itoa(*sum.Null.Kept)})
	}
	return k
}
