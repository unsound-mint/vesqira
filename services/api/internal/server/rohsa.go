package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// ROHSA decomposition runs (ADR 0046): import from the raw .dat and the pipeline's text records, and the per-pixel
// Gaussian components for the spectrum view. Local files only; the index of a large .dat is built in the cache folder.

const maxROHSAProducts = 200

var rohsaDat = regexp.MustCompile(`^rohsa_[A-Za-z0-9._-]+\.dat$`)

// rohsaTarget decides whether an import path names a ROHSA fit: a rohsa_*.dat file, or a folder with exactly one of them
// and no run_metadata.toml. found is false when the path is something else and the VGT import should look at it.
func (s *Server) rohsaTarget(p string) (datReal string, found bool, code int, msg string) {
	if strings.HasSuffix(strings.ToLower(p), ".dat") {
		real, err := s.roots().Resolve(p)
		if err != nil {
			return "", true, 403, err.Error()
		}
		if !rohsaDat.MatchString(filepath.Base(real)) {
			return "", true, 422, "a ROHSA fit is a file named rohsa_<label>.dat"
		}
		return real, true, 0, ""
	}
	if filepath.Base(p) == "run_metadata.toml" {
		return "", false, 0, ""
	}
	dir, err := s.roots().ResolveDir(p)
	if err != nil {
		return "", false, 0, ""
	}
	if _, err := os.Stat(filepath.Join(dir, "run_metadata.toml")); err == nil {
		return "", false, 0, ""
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false, 0, ""
	}
	var dats []string
	for _, e := range ents {
		if !e.IsDir() && rohsaDat.MatchString(e.Name()) {
			dats = append(dats, e.Name())
		}
	}
	switch len(dats) {
	case 0:
		return "", false, 0, ""
	case 1:
		real, err := s.roots().Resolve(filepath.Join(dir, dats[0]))
		if err != nil {
			return "", true, 403, err.Error()
		}
		return real, true, 0, ""
	}
	sort.Strings(dats)
	return "", true, 422, "this folder holds several ROHSA fits (" + strings.Join(dats, ", ") + "); give the .dat to import"
}

// readNear reads an optional text record next to the .dat; absent or unreadable is nil (a record that is not there is not invented).
func (s *Server) readNear(dir, name string) []byte {
	real, err := s.roots().Resolve(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	if fi, err := os.Stat(real); err != nil || fi.Size() > runs.MaxMetadataBytes {
		return nil
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return nil
	}
	return b
}

type fileUnit struct {
	Bunit *string `json:"bunit"`
}

// importROHSA registers the crop and the product maps found where the pipeline puts them, and records the run. The .dat is
// not registered as a dataset (it is not FITS); the run keeps its location server-side and the viewer reads it through
// the worker. The crop is an input: it is linked to the run, never listed as one of its outputs.
func (s *Server) importROHSA(ctx context.Context, datReal string) (runs.Run, int, string) {
	dir, base := filepath.Dir(datReal), filepath.Base(datReal)
	stem := strings.TrimSuffix(base, ".dat")
	suffix := strings.TrimPrefix(stem, "rohsa_")
	parsed, err := runs.ParseROHSA(
		s.readNear(dir, stem+"_run_trace.toml"), s.readNear(dir, stem+"_products_trace.toml"), s.readNear(dir, "parameters_"+suffix+".txt"),
		stem, filepath.Base(dir))
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	info := parsed.Info
	if info.RegionBasis != "declared" {
		info.Notes = append(info.Notes, "No trace names the region, so the crop is looked for under the folder's name; that name is the only link between the fit and the file.")
	}
	// the crop: <region>.fits beside the .dat, the way the pipeline names it
	if info.Region != "" && !strings.ContainsAny(info.Region, `/\`) {
		cropName := info.Region + ".fits"
		if real, err := s.roots().Resolve(filepath.Join(dir, cropName)); err == nil {
			d, code, msg := s.registerPath(ctx, real)
			if code != 0 {
				info.Notes = append(info.Notes, cropName+" could not be registered ("+msg+"), so the run has no spectral calibration.")
			} else {
				info.CropDatasetID, info.CropName = &d.ID, cropName
				parsed.InputName = cropName
				if fi, err := os.Stat(real); err == nil {
					parsed.InputSizeBytes = fi.Size()
				}
			}
		}
	}
	if info.CropDatasetID == nil {
		info.Notes = append(info.Notes, "The crop FITS the fit used ("+info.Region+".fits) was not found beside the .dat, so this run has no spectral calibration: component curves and velocities are not available.")
	}
	// the product tree: <dir>/rohsa/<label>/{maps,cubes}/*.fits, each a dataset linked by the name the pipeline gave it
	var outs []runs.Output
	seen := map[string]bool{}
	for _, sub := range []struct{ folder, prefix string }{{"maps", ""}, {"cubes", "cube_"}} {
		pdir, err := s.roots().ResolveDir(filepath.Join(dir, "rohsa", parsed.ProductLabel, sub.folder))
		if err != nil {
			continue
		}
		ents, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".fits") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if len(outs) >= maxROHSAProducts {
				info.Notes = append(info.Notes, fmt.Sprintf("Only the first %d product files were linked.", maxROHSAProducts))
				break
			}
			real, err := s.roots().Resolve(filepath.Join(pdir, name)) // a link out of the roots is skipped
			if err != nil {
				continue
			}
			d, code, msg := s.registerPath(ctx, real)
			if code != 0 {
				info.Notes = append(info.Notes, name+" was not linked: "+msg)
				continue
			}
			role := sub.prefix + strings.TrimSuffix(strings.TrimPrefix(name, parsed.ProductLabel+"_"), ".fits")
			if seen[role] {
				continue
			}
			seen[role] = true
			outs = append(outs, runs.Output{Role: role, DatasetID: d.ID, Name: d.Name})
			if note := s.proposeProductUnit(d, role); note != "" {
				info.UnitNotes[role] = note
			}
		}
	}
	if len(outs) == 0 {
		info.Notes = append(info.Notes, "No product maps were found under rohsa/"+parsed.ProductLabel+"/ beside the .dat.")
	}
	run, err := s.Runs.Save(datReal, parsed.Parsed, outs)
	if err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	if err := s.Runs.SetROHSA(run.ID, datReal, info); err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	return run, 0, ""
}

// proposeProductUnit never changes what a file says. When the file has no BUNIT, a unit from the product's name is declared as
// the run adapter's proposal (shown labelled, replaceable by the person, never over theirs). When the file has a BUNIT that the
// name contradicts, the file's value stands and the conflict is returned as a note: the pipeline copies the crop's BUNIT onto
// every map, so a map named *_kms can say K.
func (s *Server) proposeProductUnit(d catalog.Dataset, role string) string {
	unit, basis, ok := runs.ProposeUnit(role)
	if !ok {
		return ""
	}
	var h fileUnit
	_ = json.Unmarshal(d.Header, &h)
	if h.Bunit != nil {
		if runs.SameUnit(*h.Bunit, unit) {
			return ""
		}
		return fmt.Sprintf("The file says %s; its name suggests %s. The file's value is the one shown and used.", *h.Bunit, unit)
	}
	cur, err := s.Store.Get(d.ID)
	if err != nil {
		return ""
	}
	if cur.UnitDeclaration != nil && cur.UnitDeclaration.DeclaredBy != "rohsa-run-adapter" {
		return "" // the person's own declaration is never replaced
	}
	if err := s.Store.SetUnitDeclaration(cur, unit, "rohsa-run-adapter"); err != nil {
		log.Printf("runs: %v", err)
		return ""
	}
	return basis + "; the file has no BUNIT"
}

// ---- the viewer's side ----

type rohsaRunView struct {
	RunID         int64   `json:"runId"`
	Name          string  `json:"name"`
	NGauss        int     `json:"nGauss"`
	NGaussBasis   string  `json:"nGaussBasis"`
	CropDatasetID int64   `json:"cropDatasetId"`
	CropName      string  `json:"cropName"`
	CropBunit     *string `json:"cropBunit"`
	IsCrop        bool    `json:"isCrop"`
	Usable        bool    `json:"usable"`
	Refusal       *string `json:"refusal"`
}

// rohsaRunsFor lists the ROHSA runs whose components can be drawn over d: d is the run's crop, or exactly on its grid. A run
// whose crop has the same axis lengths but a different grid is listed with the reason it is refused; unrelated files list nothing.
func (s *Server) rohsaRunsFor(d catalog.Dataset) ([]rohsaRunView, error) {
	all, err := s.Runs.AllROHSA()
	if err != nil {
		return nil, err
	}
	out := []rohsaRunView{}
	if d.HostID != 0 {
		return out, nil
	}
	for _, r := range all {
		if r.Info.CropDatasetID == nil {
			continue
		}
		crop, err := s.Store.Get(*r.Info.CropDatasetID)
		if err != nil || crop.HostID != 0 {
			continue
		}
		v := rohsaRunView{RunID: r.RunID, Name: r.RunName, NGauss: r.Info.NGauss, NGaussBasis: r.Info.NGaussBasis, CropDatasetID: crop.ID, CropName: crop.Name, IsCrop: crop.ID == d.ID}
		var h fileUnit
		_ = json.Unmarshal(crop.Header, &h)
		v.CropBunit = h.Bunit
		var refusal string
		switch {
		case r.Info.NGauss == 0:
			refusal = "the run does not declare how many Gaussians each pixel has"
		case v.IsCrop:
		default:
			why, err := compat.Grids(d.Header, crop.Header)
			switch {
			case err != nil:
				refusal = "the grids could not be compared"
			case len(why) > 0:
				if !sameAxisLengths(d.Header, crop.Header) {
					continue
				}
				refusal = "this cube is not known to be on the run's grid: " + why[0]
			}
		}
		if refusal != "" {
			v.Refusal = &refusal
		}
		v.Usable = refusal == ""
		out = append(out, v)
	}
	return out, nil
}

func sameAxisLengths(a, b json.RawMessage) bool {
	var ha, hb struct {
		Axes []struct {
			Length int `json:"length"`
		} `json:"axes"`
	}
	if json.Unmarshal(a, &ha) != nil || json.Unmarshal(b, &hb) != nil || len(ha.Axes) != len(hb.Axes) {
		return false
	}
	for i := range ha.Axes {
		if ha.Axes[i].Length != hb.Axes[i].Length {
			return false
		}
	}
	return true
}

func (s *Server) datasetByPath(w http.ResponseWriter, r *http.Request) (catalog.Dataset, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return catalog.Dataset{}, false
	}
	d, err := s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return d, false
	}
	if err != nil {
		log.Printf("rohsa: %v", err)
		fail(w, 500, "internal error")
		return d, false
	}
	return d, true
}

// rohsaRuns answers which decomposition runs can be shown over this dataset.
func (s *Server) rohsaRuns(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetByPath(w, r)
	if !ok {
		return
	}
	rs, err := s.rohsaRunsFor(d)
	if err != nil {
		log.Printf("rohsa: %v", err)
		fail(w, 500, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"runs": rs})
}

// rohsaTask is a validated request: the dataset is on the run's grid, the crop and the .dat are authorized and unchanged.
type rohsaTask struct {
	run      runs.RohsaRun
	crop     catalog.Dataset
	cropReal string
	datReal  string
	key      cacheKey
}

func (s *Server) rohsaResolve(w http.ResponseWriter, r *http.Request, runID int64) (rohsaTask, bool) {
	var t rohsaTask
	d, ok := s.datasetByPath(w, r)
	if !ok {
		return t, false
	}
	if s.CacheDir == "" {
		fail(w, 503, "the cache folder for ROHSA indexes is not configured")
		return t, false
	}
	rr, err := s.Runs.ROHSA(runID)
	if errors.Is(err, runs.ErrNotFound) {
		fail(w, 404, "ROHSA run not found")
		return t, false
	}
	if err != nil {
		log.Printf("rohsa: %v", err)
		fail(w, 500, "internal error")
		return t, false
	}
	if rr.Info.NGauss == 0 || rr.Info.CropDatasetID == nil {
		fail(w, 409, "this run has no usable crop or Gaussian count")
		return t, false
	}
	views, err := s.rohsaRunsFor(d)
	if err != nil {
		fail(w, 500, "internal error")
		return t, false
	}
	for _, v := range views {
		if v.RunID == runID && !v.Usable {
			writeJSON(w, 409, map[string]any{"error": *v.Refusal, "reasons": []string{*v.Refusal}})
			return t, false
		}
	}
	usable := false
	for _, v := range views {
		usable = usable || (v.RunID == runID && v.Usable)
	}
	if !usable {
		fail(w, 409, "this dataset is not on the run's grid")
		return t, false
	}
	crop, err := s.Store.Get(*rr.Info.CropDatasetID)
	if err != nil {
		fail(w, 404, "the run's crop is no longer registered")
		return t, false
	}
	cropReal, key, status, msg := s.authorize(r.Context(), crop)
	if status != 0 {
		fail(w, status, msg)
		return t, false
	}
	datReal, err := s.roots().Resolve(rr.DatPath)
	if err != nil {
		fail(w, 403, err.Error())
		return t, false
	}
	return rohsaTask{run: rr, crop: crop, cropReal: cropReal, datReal: datReal, key: key}, true
}

func (s *Server) rohsaDo(w http.ResponseWriter, r *http.Request, req map[string]any) (worker.Reply, bool) {
	svc := s.planesFor(0)
	if svc == nil {
		fail(w, 503, "the scientific worker is not configured")
		return worker.Reply{}, false
	}
	rep, err := svc.Do(r.Context(), req)
	var re *worker.RequestError
	switch {
	case err == nil:
		return rep, true
	case errors.As(err, &re):
		code := map[string]int{"bad_request": 400, "forbidden": 403, "index_not_ready": 409}[re.Code]
		if code == 0 {
			code = 422
		}
		fail(w, code, re.Message)
	case errors.Is(err, context.Canceled):
	case errors.Is(err, worker.ErrTimeout):
		fail(w, 504, "the scientific worker timed out")
	default:
		log.Printf("worker: %v", err)
		fail(w, 502, "the scientific worker is unavailable")
	}
	return worker.Reply{}, false
}

func (t rohsaTask) request(op string, s *Server) map[string]any {
	return map[string]any{"op": op, "path": t.cropReal, "dat_path": t.datReal, "cache_dir": s.CacheDir, "n_gauss": t.run.Info.NGauss}
}

// rohsaIndex advances (or confirms) the index of the run's .dat. Each call works for a bounded time, so a very large file is
// indexed over several calls and the page shows the progress. The answer is the index's state, not a scientific result.
func (s *Server) rohsaIndex(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID int64 `json:"runId"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	t, ok := s.rohsaResolve(w, r, in.RunID)
	if !ok {
		return
	}
	req := t.request("rohsa_index", s)
	req["budget_s"] = 15
	rep, ok := s.rohsaDo(w, r, req)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

// rohsaComponents answers with the Gaussians of one pixel as a frame: the JSON head (components, axis, conventions) and the
// float64 curves.
func (s *Server) rohsaComponents(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if err != nil {
		fail(w, 400, "run must be an integer")
		return
	}
	x, ok := index(w, r, "x")
	if !ok {
		return
	}
	y, ok := index(w, r, "y")
	if !ok {
		return
	}
	t, ok := s.rohsaResolve(w, r, runID)
	if !ok {
		return
	}
	var datSize, datMtime int64
	if fi, err := os.Stat(t.datReal); err == nil {
		datSize, datMtime = fi.Size(), fi.ModTime().UnixNano()
	}
	k := t.key
	k.op, k.x, k.y, k.q = "rohsa_pixel", x, y, fmt.Sprintf("%d,%d,%d", runID, datSize, datMtime)
	if rep, hit := s.Cache.get(k); hit {
		writeFrame(w, rep)
		return
	}
	req := t.request("rohsa_pixel", s)
	req["x"], req["y"] = x, y
	rep, ok := s.rohsaDo(w, r, req)
	if !ok {
		return
	}
	s.Cache.put(k, rep)
	writeFrame(w, rep)
}
