package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Decomposition runs of a tool whose profile declares FITS maps (ADR 0068, slice 4). The run is recorded the way a ROHSA fit is
// (rohsa_runs: its folder in dat_path, the crop, the component count) plus the declaration that says how to read the maps, so the
// components panel, the sigma-v diagram and the class maps read it through the same worker calls as any other decomposition.

// importMapsRun records a run whose components are FITS maps in the run's own folder. This computer only: a host's worker does not
// read maps yet. The run record is the profile's: it names the cube the fit was made on and, for a cube-per-parameter layout, how
// many components there are.
func (s *Server) importMapsRun(ctx context.Context, src runSource, prof *runs.Profile, dec *runs.Decomposition, parsed runs.Parsed, record []byte) (runs.Run, int, string) {
	l, ok := src.(localRun)
	if !ok {
		return runs.Run{}, 422, "a decomposition written as FITS maps is imported from this computer only; copy the run's folder here to import it"
	}
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return runs.Run{}, 422, "the run's folder cannot be read"
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	files, err := dec.MapFiles(names)
	if err != nil {
		return runs.Run{}, 422, err.Error()
	}
	info := runs.RohsaInfo{Label: parsed.Name, Notes: []string{}, UnitNotes: map[string]string{}, RegionBasis: "not applicable: the components are FITS maps",
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
	if err := s.Runs.SetROHSA(run.ID, l.dir, info); err != nil {
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

// mapsFingerprint is the size and a stamp of the map files as they are now (name, size, modification time of each), so a map that
// changed never serves old pixels from the cache, the way a .dat's size and time do.
func mapsFingerprint(folder string, files map[string][]string) (size, stamp int64, err error) {
	var all []string
	for _, p := range runs.DecompParams {
		all = append(all, files[p]...)
	}
	sort.Strings(all)
	h := fnv.New64a()
	for _, name := range all {
		fi, err := os.Stat(filepath.Join(folder, name))
		if err != nil {
			return 0, 0, err
		}
		size += fi.Size()
		fmt.Fprintf(h, "%s:%d:%d;", name, fi.Size(), fi.ModTime().UnixNano())
	}
	return size, int64(h.Sum64() >> 1), nil
}
