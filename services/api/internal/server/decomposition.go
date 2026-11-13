package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Decomposition runs of a tool whose profile declares FITS maps (ADR 0068, slice 4). The run is recorded the way a ROHSA fit is
// (rohsa_runs: its folder in dat_path, the crop, the component count) plus the declaration that says how to read the maps, so the
// components panel, the sigma-v diagram and the class maps read it through the same worker calls as any other decomposition.

// importMapsRun records a run whose components are FITS maps in the run's own folder, on this computer or on a host (where the
// host's worker reads them, see rohsaResolve). The run record is the profile's: it names the cube the fit was made on and, for a
// cube-per-parameter layout, how many components there are.
func (s *Server) importMapsRun(ctx context.Context, src runSource, prof *runs.Profile, dec *runs.Decomposition, parsed runs.Parsed, record []byte) (runs.Run, int, string) {
	folder, names, code, msg := src.Folder(ctx)
	if code != 0 {
		return runs.Run{}, code, msg
	}
	files, err := dec.MapFiles(names)
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	info := runs.RohsaInfo{HostID: src.HostID(), Label: parsed.Name, Notes: []string{}, UnitNotes: map[string]string{}, RegionBasis: "not applicable: the components are FITS maps",
		Decomposition: &runs.DecompRecord{Profile: prof.Label(), Files: files}}
	switch {
	case dec.Source.ComponentAxis == nil:
		info.NGauss, info.NGaussBasis = len(files["amplitude"]), "counted from the map files, one per component"
	case runs.RecordInt(record, dec.Components) > 0:
		info.NGauss, info.NGaussBasis = runs.RecordInt(record, dec.Components), "declared by the run record ("+dec.Components+")"
	default:
		info.Notes = append(info.Notes, "The run record does not state the number of components ("+dec.Components+"), so the components cannot be read.")
	}
	if info.NGauss > 64 {
		info.NGauss = 0
		info.Notes = append(info.Notes, "More than 64 components per pixel are not read.")
	}
	// the cube the fit was made on: the file name the record gives, looked for beside the record (the record's own path is the author's machine)
	cropName := filepath.Base(filepath.FromSlash(runs.RecordString(record, dec.Crop)))
	if cropName == "." || cropName == string(filepath.Separator) {
		cropName = ""
	}
	if cropName != "" {
		if d, found, code, msg := src.Register(ctx, cropName); found {
			if code == 502 {
				return runs.Run{}, code, msg // a host that stopped answering is not a run without a crop
			}
			if code != 0 {
				info.Notes = append(info.Notes, cropName+" could not be registered ("+msg+"), so the run has no spectral calibration.")
			} else {
				info.CropDatasetID, info.CropName = &d.ID, cropName
				parsed.InputName, parsed.InputSizeBytes = cropName, d.SizeBytes
			}
		}
	}
	if info.CropDatasetID == nil {
		info.Notes = append(info.Notes, "The cube the fit was made on was not found beside the run record, so this run has no spectral calibration: components are not available.")
	}
	var outs []runs.Output
	seen := map[string]bool{}
	var all []string
	for _, p := range runs.DecompParams {
		all = append(all, files[p]...)
	}
	for _, name := range all {
		if seen[name] || len(outs) >= maxROHSAProducts {
			continue
		}
		seen[name] = true
		d, found, code, msg := src.Register(ctx, name)
		if !found {
			continue
		}
		if code == 502 {
			return runs.Run{}, code, msg
		}
		if code != 0 {
			info.Notes = append(info.Notes, name+" was not linked: "+msg)
			continue
		}
		outs = append(outs, runs.Output{Role: runs.RoleComponentMaps, Product: strings.TrimSuffix(name, filepath.Ext(name)), DatasetID: d.ID, Name: d.Name})
	}
	if err := ctx.Err(); err != nil {
		return runs.Run{}, 502, "the import was interrupted before it finished; nothing was saved"
	}
	run, err := s.Runs.Save(wsCtx(ctx), actorID(ctx), src.Key(), parsed, outs)
	if err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	if err := s.Runs.SetROHSA(run.ID, folder, info); err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	return run, 0, ""
}

// decompositionRequest is what the worker is sent about how to read a run's components: the declaration of the profile the run was
// imported with, and for FITS maps the file names in its folder. A run stored before slice 4 has no record and is read as the ROHSA
// profile reads a .dat. A run whose profile this server does not have is refused with the name, not read some other way.
func (s *Server) decompositionRequest(rr runs.RohsaRun) (map[string]any, string) {
	reg := s.profiles()
	prof := reg.ByName("ROHSA")
	if rec := rr.Info.Decomposition; rec != nil {
		if prof = reg.ByLabel(rec.Profile); prof == nil {
			return nil, "this run was read with the tool profile " + rec.Profile + ", which this server has not loaded"
		}
	}
	dec := reg.DecompositionOf(prof)
	if dec == nil {
		return nil, "the tool profile " + prof.Label() + " declares no components"
	}
	out := map[string]any{"declaration": dec.Declaration()}
	if rec := rr.Info.Decomposition; rec != nil && rec.Files != nil {
		out["files"] = rec.Files
	}
	return out, ""
}

// mapsRecordOnHost looks for the run record of a tool that declares FITS maps at an import path on a host: the record file itself, or
// a folder that holds one. It reads the folder through the host's worker (inside the roots it enforces) and returns the run source
// for that folder; false when the path is not such a run, so the caller keeps its own answer.
func (s *Server) mapsRecordOnHost(ctx context.Context, h hosts.Host, p string) (remoteRun, bool) {
	dir, only := p, ""
	for _, f := range s.profiles().RecordFiles(runs.ReaderRunMetadata) {
		if path.Base(p) == f {
			dir, only = path.Dir(p), f
		}
	}
	real, names, _, err := s.remoteList(ctx, h, dir)
	if err != nil || real == "" {
		return remoteRun{}, false
	}
	for _, name := range names {
		if only != "" && name != only {
			continue
		}
		prof := s.profiles().ByRecordFile(name, runs.ReaderRunMetadata)
		if dec := s.profiles().DecompositionOf(prof); prof != nil && dec != nil && dec.Source.Kind == runs.SourceFitsMaps {
			return remoteRun{s: s, h: h, dir: real, file: name}, true
		}
	}
	return remoteRun{}, false
}

// mapFileNames are the distinct map files of a run, sorted: the one list the fingerprint is taken over, here or on a host.
func mapFileNames(files map[string][]string) []string {
	var all []string
	for _, p := range runs.DecompParams {
		all = append(all, files[p]...)
	}
	sort.Strings(all)
	return slices.Compact(all)
}

// mapsFingerprint is the size and a stamp of the map files as they are now (name, size, modification time of each, from stat in the
// order of mapFileNames), so a map that changed never serves old pixels from the cache, the way a .dat's size and time do.
func mapsFingerprint(files map[string][]string, stat func(names []string) (sizes, mtimes []int64, err error)) (size, stamp int64, err error) {
	names := mapFileNames(files)
	sizes, mtimes, err := stat(names)
	if err != nil {
		return 0, 0, err
	}
	h := fnv.New64a()
	for i, name := range names {
		size += sizes[i]
		fmt.Fprintf(h, "%s:%d:%d;", name, sizes[i], mtimes[i])
	}
	return size, int64(h.Sum64() >> 1), nil
}

// localMapStat stats the map files of a run's folder on this computer.
func localMapStat(folder string) func([]string) ([]int64, []int64, error) {
	return func(names []string) (sizes, mtimes []int64, err error) {
		for _, name := range names {
			fi, err := os.Stat(filepath.Join(folder, name))
			if err != nil {
				return nil, nil, err
			}
			sizes, mtimes = append(sizes, fi.Size()), append(mtimes, fi.ModTime().UnixNano())
		}
		return sizes, mtimes, nil
	}
}

var errRemoteRefused = errors.New("not available")

// remoteMapStat stats the map files of a run's folder on a host in one request: the host's worker authorizes each path against its
// roots. A file it refuses is errRemoteRefused; a host that does not answer is the transport's error.
func (s *Server) remoteMapStat(ctx context.Context, h hosts.Host, folder string) func([]string) ([]int64, []int64, error) {
	return func(names []string) ([]int64, []int64, error) {
		paths := make([]string, len(names))
		for i, n := range names {
			paths[i] = path.Join(folder, n)
		}
		rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "stat_many", "paths": paths})
		var re *worker.RequestError
		switch {
		case errors.As(err, &re):
			return nil, nil, errRemoteRefused
		case err != nil:
			return nil, nil, err
		}
		var st struct {
			Files []struct {
				Size    int64 `json:"size"`
				MtimeNS int64 `json:"mtime_ns"`
			} `json:"files"`
		}
		if json.Unmarshal(rep.Head, &st) != nil || len(st.Files) != len(names) {
			return nil, nil, errors.New("the host answered unexpectedly")
		}
		var sizes, mtimes []int64
		for _, f := range st.Files {
			sizes, mtimes = append(sizes, f.Size), append(mtimes, f.MtimeNS)
		}
		return sizes, mtimes, nil
	}
}
