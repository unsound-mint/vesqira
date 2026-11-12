package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
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
	f, err := sourceroot.OpenFile(real)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, runs.MaxMetadataBytes+1)) // the file may have grown since the size check
	if err != nil || int64(len(b)) > runs.MaxMetadataBytes {
		return nil
	}
	return b
}

type fileUnit struct {
	Bunit *string `json:"bunit"`
}

// importROHSA registers the crop and the product maps found where the pipeline puts them, and records the run. The .dat is
// not registered as a dataset (it is not FITS); the run keeps its location server-side and the viewer reads it through
// the worker, on the machine that holds it. The crop is an input: it is linked to the run, never listed as one of its outputs.
func (s *Server) importROHSA(ctx context.Context, src rohsaSource) (runs.Run, int, string) {
	datReal := src.Dat()
	base := path.Base(filepath.ToSlash(datReal))
	stem := strings.TrimSuffix(base, ".dat")
	suffix := strings.TrimPrefix(stem, "rohsa_")
	folder := path.Base(path.Dir(filepath.ToSlash(datReal)))
	parsed, err := runs.ParseROHSA(
		src.Near(ctx, stem+"_run_trace.toml"), src.Near(ctx, stem+"_products_trace.toml"), src.Near(ctx, "parameters_"+suffix+".txt"),
		stem, folder)
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	info := parsed.Info
	info.HostID = src.HostID()
	if info.RegionBasis != "declared" {
		info.Notes = append(info.Notes, "No trace names the region, so the crop is looked for under the folder's name; that name is the only link between the fit and the file.")
	}
	// the crop: <region>.fits beside the .dat, the way the pipeline names it
	if info.Region != "" && !strings.ContainsAny(info.Region, `/\`) {
		cropName := info.Region + ".fits"
		if d, found, code, msg := src.Register(ctx, cropName); found {
			if code != 0 {
				info.Notes = append(info.Notes, cropName+" could not be registered ("+msg+"), so the run has no spectral calibration.")
			} else {
				info.CropDatasetID, info.CropName = &d.ID, cropName
				parsed.InputName, parsed.InputSizeBytes = cropName, d.SizeBytes
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
		rel := path.Join("rohsa", parsed.ProductLabel, sub.folder)
		for _, name := range src.Fits(ctx, rel) {
			if len(outs) >= maxROHSAProducts {
				info.Notes = append(info.Notes, fmt.Sprintf("Only the first %d product files were linked.", maxROHSAProducts))
				break
			}
			d, found, code, msg := src.Register(ctx, path.Join(rel, name)) // a link out of the roots is skipped
			if !found {
				continue
			}
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
	// A host that stopped answering (ssh dropped, the deadline passed, the caller left) is not a host with no trace, no crop or no
	// maps. Nothing is saved: a run built from silence would overwrite a good one under the same key, and an automatic import
	// would link and announce it and never try again.
	if code, msg := src.Failed(); code != 0 {
		return runs.Run{}, code, msg
	}
	if err := ctx.Err(); err != nil {
		return runs.Run{}, 502, "the import was interrupted before it finished; nothing was saved"
	}
	run, err := s.Runs.Save(src.Key(), parsed.Parsed, outs)
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

// remoteImportTimeout bounds one import from a host: a fit's product tree is a few dozen small headers, each read there.
const remoteImportTimeout = 10 * time.Minute

// importROHSAFromHost imports a ROHSA fit that lives on a host: the .dat, or the folder holding it (or one fit just below).
// The host's worker reads the small records (trace, namelist) and the FITS headers; the .dat is not read at all.
func (s *Server) importROHSAFromHost(w http.ResponseWriter, r *http.Request, hostID int64, p string) {
	if s.HostDB == nil || s.Remote == nil {
		fail(w, 503, "remote hosts are not configured")
		return
	}
	h, err := s.HostDB.Get(hostID)
	if err != nil {
		fail(w, 404, "host not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteImportTimeout)
	defer cancel()
	dat, code, msg := s.rohsaTargetRemote(ctx, h, p)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	run, code, msg := s.importROHSA(ctx, newRemoteROHSA(s, h, dat))
	if code != 0 {
		fail(w, code, msg)
		return
	}
	s.Audit.RecordCtx(r.Context(), "rohsa run imported from host", "run", run.ID, h.Name+": "+dat)
	writeJSON(w, 201, s.runView(run))
}

// importROHSAJob imports the fit a finished ROHSA job left in its output folder on its host: the one rohsa_*.dat found in the
// folder or just below it (the pipeline writes <output>/<region>/...). The same importer as an import by path, so the run is
// the one an owner would get by hand; a folder with no fit or several is refused with the reason and nothing is guessed.
func (s *Server) importROHSAJob(ctx context.Context, j jobs.Job, h hosts.Host) (runs.Run, int, string) {
	dat, code, msg := s.rohsaTargetRemote(ctx, h, j.OutputDir)
	if code != 0 {
		if code == 403 {
			msg = "the job's output folder is not available on the host"
		}
		return runs.Run{}, code, msg
	}
	run, code, msg := s.importROHSA(ctx, newRemoteROHSA(s, h, dat))
	if code == 0 {
		s.Audit.RecordCtx(ctx, "rohsa run imported from host", "run", run.ID, h.Name+": "+dat)
	}
	return run, code, msg
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
	for _, r := range all {
		if r.Info.CropDatasetID == nil {
			continue
		}
		crop, err := s.Store.Get(*r.Info.CropDatasetID)
		if err != nil {
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
			why, err := compat.Grids(effectiveHeader(d), effectiveHeader(crop))
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

// rohsaTask is a validated request: the dataset is on the run's grid, the crop and the .dat are authorized and unchanged,
// on this machine or on the one host that holds both.
type rohsaTask struct {
	run      runs.RohsaRun
	crop     catalog.Dataset
	cropReal string
	datReal  string
	// datSize and datMtime are the .dat as it is now (stat, here or on the host): part of the cache key, so a changed fit
	// never serves old pixels.
	datSize, datMtime int64
	host              *hosts.Host // nil for a fit on this machine
	key               cacheKey
}

func (s *Server) rohsaResolve(w http.ResponseWriter, r *http.Request, runID int64) (rohsaTask, bool) {
	var t rohsaTask
	d, ok := s.datasetByPath(w, r)
	if !ok {
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
	if rr.Info.HostID == 0 && s.CacheDir == "" {
		fail(w, 503, "the cache folder for ROHSA indexes is not configured")
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
	if crop.HostID != rr.Info.HostID {
		// the crop gives the grid and the spectral axis and the .dat holds the fit: the worker reads both, so both are on one machine
		fail(w, 409, "the run's crop and its fit are not on the same machine")
		return t, false
	}
	cropReal, key, status, msg := s.authorize(r.Context(), crop)
	if status != 0 {
		fail(w, status, msg)
		return t, false
	}
	t = rohsaTask{run: rr, crop: crop, cropReal: cropReal, key: key}
	if rr.Info.HostID == 0 {
		datReal, err := s.roots().Resolve(rr.DatPath)
		if err != nil {
			fail(w, 403, err.Error())
			return t, false
		}
		t.datReal = datReal
		if fi, err := os.Stat(datReal); err == nil {
			t.datSize, t.datMtime = fi.Size(), fi.ModTime().UnixNano()
		}
		return t, true
	}
	if s.HostDB == nil || s.Remote == nil {
		fail(w, 503, "remote hosts are not configured")
		return t, false
	}
	h, err := s.HostDB.Get(rr.Info.HostID)
	if err != nil || !h.InRoots(rr.DatPath) {
		fail(w, 403, "not available")
		return t, false
	}
	rep, err := s.Remote.Do(r.Context(), h, map[string]any{"op": "stat", "path": rr.DatPath}) // the host's worker enforces its roots
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		fail(w, 403, "not available")
		return t, false
	case errors.Is(err, context.Canceled):
		return t, false
	case err != nil:
		fail(w, 502, "the host is not reachable (is the VPN up?)")
		return t, false
	}
	var st struct {
		Size    int64 `json:"size"`
		MtimeNS int64 `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &st) != nil {
		fail(w, 502, "the host answered unexpectedly")
		return t, false
	}
	t.datReal, t.datSize, t.datMtime, t.host = rr.DatPath, st.Size, st.MtimeNS, &h
	return t, true
}

func (s *Server) rohsaDo(w http.ResponseWriter, r *http.Request, t rohsaTask, req map[string]any) (worker.Reply, bool) {
	svc := s.planesFor(t.crop.ID) // the host's worker for a crop on a host, this machine's otherwise
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
		if t.host != nil {
			fail(w, 502, "the host is not reachable (is the VPN up?)")
		} else {
			fail(w, 502, "the scientific worker is unavailable")
		}
	}
	return worker.Reply{}, false
}

// request is what the worker is asked. A host's worker is never told where to cache: it keeps the index in its own account's
// cache folder (plane_worker.remote_cache_dir), so no request can make it write anywhere else.
func (t rohsaTask) request(op string, s *Server) map[string]any {
	req := map[string]any{"op": op, "path": t.cropReal, "dat_path": t.datReal, "n_gauss": t.run.Info.NGauss}
	if t.host == nil {
		req["cache_dir"] = s.CacheDir
	}
	return req
}

// rohsaIndex advances (or confirms) the index of the run's .dat. Each call works for a bounded time, so a very large file is
// indexed over several calls and the page shows the progress. The answer is the index's state, not a scientific result.
// For a fit on a host the index is built there: the .dat is streamed once on the host, the sidecar goes to the account's
// cache folder on the host, and only this small state travels. That write is audited when an index starts and when it settles.
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
	rep, ok := s.rohsaDo(w, r, t, req)
	if !ok {
		return
	}
	var head map[string]json.RawMessage
	if err := json.Unmarshal(rep.Head, &head); err != nil {
		fail(w, 502, "the worker answered unexpectedly")
		return
	}
	flag := func(k string) bool { var b bool; _ = json.Unmarshal(head[k], &b); return b }
	if t.host != nil {
		var file, state string
		_ = json.Unmarshal(head["cache_file"], &file)
		_ = json.Unmarshal(head["state"], &state)
		if flag("created") {
			s.Audit.RecordCtx(r.Context(), "rohsa index started on host", "run", in.RunID, t.host.Name+": index written under "+path.Dir(file)+" (a rebuildable cache, never in the data's folder) for "+t.datReal)
		}
		if flag("settled") {
			s.Audit.RecordCtx(r.Context(), "rohsa index "+state+" on host", "run", in.RunID, t.host.Name+": "+path.Base(file))
		}
		head["where"] = json.RawMessage(`"host"`)
	} else {
		head["where"] = json.RawMessage(`"here"`)
	}
	for _, k := range []string{"cache_file", "created", "settled"} { // a location on the host and bookkeeping are not the page's
		delete(head, k)
	}
	out, err := json.Marshal(head)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

// rohsaComponents answers with the Gaussians of one pixel as a frame: the JSON head (components, axis, conventions) and the
// float64 curves. On a host the pixel is one seek in the index there and N short lines of the .dat.
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
	k := t.key
	k.op, k.x, k.y, k.q = "rohsa_pixel", x, y, fmt.Sprintf("%d,%d,%d", runID, t.datSize, t.datMtime)
	if rep, hit := s.Cache.get(k); hit {
		writeFrame(w, rep)
		return
	}
	req := t.request("rohsa_pixel", s)
	req["x"], req["y"] = x, y
	rep, ok := s.rohsaDo(w, r, t, req)
	if !ok {
		return
	}
	s.Cache.put(k, rep)
	writeFrame(w, rep)
}
