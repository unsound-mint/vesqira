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

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Decomposition runs of a tool whose profile declares FITS maps (ADR 0068, slice 4). The run is recorded the way a ROHSA fit is
// (rohsa_runs: its folder in dat_path, the crop, the component count) plus the declaration that says how to read the maps, so the
// components panel, the sigma-v diagram and the class maps read it through the same worker calls as any other decomposition.

// importDecompRun records a run whose profile declares components, on this computer or (FITS maps only) on a host, where the host's
// worker reads them (see rohsaResolve). The run record is the profile's: it names the cube the fit was made on and, where the
// components are one cube per parameter or a table, how many there are. Whatever the profile's outputs table names is registered as
// well, under its roles. Everything the profile does not declare is said, not guessed.
func (s *Server) importDecompRun(ctx context.Context, src runSource, prof *runs.Profile, dec *runs.Decomposition, parsed runs.Parsed, record []byte) (runs.Run, int, string) {
	folder, names, code, msg := src.Folder(ctx)
	if code != 0 {
		return runs.Run{}, code, msg
	}
	info := runs.RohsaInfo{HostID: src.HostID(), Label: parsed.Name, Notes: []string{}, UnitNotes: map[string]string{}, RegionBasis: "not applicable: read from the run's profile",
		Decomposition: &runs.DecompRecord{Profile: prof.Label()}}
	if prof.Origin != nil {
		info.Decomposition.ProfileSHA = prof.Origin.SHA
	}
	var all []string // the map files to register
	location := folder
	if dec.Source.Kind == runs.SourceFitsMaps {
		files, err := dec.MapFiles(names)
		if err != nil {
			return runs.Run{}, 422, err.Error()
		}
		info.Decomposition.Files = files
		info.RegionBasis = "not applicable: the components are FITS maps"
		if dec.Source.ComponentAxis == nil {
			info.NGauss, info.NGaussBasis = len(files["amplitude"]), "counted from the map files, one per component"
		}
		for _, p := range runs.DecompParams {
			all = append(all, files[p]...)
		}
	} else {
		l, ok := src.(localRun)
		if !ok {
			return runs.Run{}, 422, "a component table a profile declares is imported from this computer only; copy the run's folder here to import it"
		}
		table, why := tableFileOf(prof, names, src.RecordFile(), parsed.Name)
		if why != "" {
			return runs.Run{}, 422, why
		}
		real, err := l.s.roots(wsCtx(ctx)).Resolve(filepath.Join(l.dir, table))
		if err != nil {
			return runs.Run{}, 403, err.Error()
		}
		location = real
		info.RegionBasis = "not applicable: the components are a table"
	}
	if dec.Source.Kind == runs.SourceTable || dec.Source.ComponentAxis != nil {
		if n := runs.RecordInt(record, dec.Components); n > 0 {
			info.NGauss, info.NGaussBasis = n, "declared by the run record ("+dec.Components+")"
		} else if dec.Components == "" {
			info.Notes = append(info.Notes, "The profile does not say which key of the run record holds the number of components, so the components cannot be read.")
		} else {
			info.Notes = append(info.Notes, "The run record does not state the number of components ("+dec.Components+"), so the components cannot be read.")
		}
	}
	if info.NGauss > 64 {
		info.NGauss = 0
		info.Notes = append(info.Notes, "More than 64 components per pixel are not read.")
	}
	// the cube the fit was made on: the file name the record gives, looked for beside the record (the record's own path is the author's machine)
	cropName := ""
	if dec.Crop != "" {
		cropName = filepath.Base(filepath.FromSlash(runs.RecordString(record, dec.Crop)))
		if cropName == "." || cropName == string(filepath.Separator) {
			cropName = ""
		}
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
	byRole := map[string]catalog.Dataset{}
	if prof.Outputs != nil {
		var code int
		var msg string
		if outs, byRole, code, msg = s.registerRecordOutputs(ctx, src, prof, parsed); code != 0 {
			return runs.Run{}, code, msg
		}
	}
	seen := map[string]bool{}
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
	if err := s.Runs.SetROHSA(run.ID, location, info); err != nil {
		log.Printf("runs: %v", err)
		return runs.Run{}, 500, "internal error"
	}
	s.proposeOrientation(ctx, prof, byRole)
	return run, 0, ""
}

// tableFileOf finds the profile's component table in the names of the run's folder: the one file the profile's component_table role
// names, with the run's stem and label filled in. A profile with no such role, no match and several matches each get a sentence.
func tableFileOf(prof *runs.Profile, names []string, record, label string) (string, string) {
	if _, ok := prof.Files[runs.RoleComponentTable]; !ok {
		return "", "the tool profile " + prof.Label() + " declares its components as a table but names no component_table file, so the table cannot be found"
	}
	vars := map[string]string{"stem": prof.Stem(record), "label": label}
	var hits []string
	for _, n := range names {
		if prof.IsFile(runs.RoleComponentTable, n, vars) {
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	switch len(hits) {
	case 0:
		return "", "no file the profile " + prof.Label() + " names as the component table was found in the run's folder"
	case 1:
		return hits[0], ""
	}
	return "", "the run's folder holds several files the profile " + prof.Label() + " names as the component table (" + strings.Join(hits, ", ") + "); keep one"
}

// decompositionRequest is what the worker is sent about how to read a run's components: the declaration of the profile the run was
// imported with, and for FITS maps the file names in its folder. A run stored before slice 4 has no record and is read as the ROHSA
// profile reads a .dat. A run whose profile this server does not have is refused with the name, not read some other way.
func (s *Server) decompositionRequest(ws workspace.ID, rr runs.RohsaRun) (map[string]any, string) {
	reg := s.profilesAt(ws, rr.DatPath) // the program's profiles and those of the project the run's files are in
	prof := reg.ByName("ROHSA")
	if rec := rr.Info.Decomposition; rec != nil {
		if prof = reg.ByLabel(rec.Profile); prof == nil {
			if reg = s.refreshProfilesAt(ws, rr.DatPath, false); reg != nil {
				prof = reg.ByLabel(rec.Profile)
			}
		}
		if prof == nil {
			return nil, "this run was read with the tool profile " + rec.Profile + ", which this server has not loaded (a project profile is read from the project's .vesqira/profiles/ folder)"
		}
		// A profile from a project folder is the researcher's file, and the declaration is read from it at every request: a file edited
		// since the import, with the same name and version, would read the run's numbers differently without a trace.
		if rec.ProfileSHA != "" && (prof.Origin == nil || prof.Origin.SHA != rec.ProfileSHA) {
			return nil, "the project's profile file for " + rec.Profile + " has changed since this run was imported, so its components are not read with a different declaration; import the run again, or restore the file"
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
