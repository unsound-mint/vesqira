package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// "Run this configuration on another cube" (ADR 0014 amendment, issue 121). A draft is rendered from a recorded run's settings,
// never from its script text (the rule sweeps follow, ADR 0048): the config text the job started from, its overrides, the code
// folder and the resources are kept, and the input file is the one thing replaced, by a cube already in the catalog on the same
// host. The draft says what was carried and what changed, and it is refused, in a sentence, when the recorded run kept no
// settings or when a setting cannot hold for the new cube. Nothing is guessed, converted or silently adjusted.
//
// The routes are part of the jobs block (local mode only; ADR 0050 and 0066 leave jobs unmounted on a group server).

type inputRerunIn struct {
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
	// RerunMode is best_effort (the default: the code as it is on the host now, every difference from the source run's
	// observed code listed before submitting) or adapted. Strict is refused: it insists on the same input file.
	RerunMode string `json:"rerunMode"`
}

type summaryValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type summaryChange struct {
	Key    string  `json:"key"`
	Parent *string `json:"parent"`
	Now    *string `json:"now"`
}

// inputRerunSummary is what a person reads before the draft is made, and what the created draft returns: what was carried over
// from the recorded run and what changed.
type inputRerunSummary struct {
	SourceRunID      int64                   `json:"sourceRunId"`
	SourceRunName    string                  `json:"sourceRunName"`
	SourceJobID      int64                   `json:"sourceJobId"`
	Template         string                  `json:"template"`
	HostID           int64                   `json:"hostId"`
	Mode             string                  `json:"mode"`
	Name             string                  `json:"name"`
	SourceConfigHash string                  `json:"sourceConfigHash"`
	ConfigHash       string                  `json:"configHash"`
	Input            jobs.RerunInputIdentity `json:"input"`
	PreviousInput    string                  `json:"previousInput"`
	Changed          []summaryChange         `json:"changed"`
	Carried          []summaryValue          `json:"carried"`
	// SourceEditedLines counts the lines of the source job's script that were edited by hand; the draft is rendered from its
	// recorded settings, so those edits are not carried over.
	SourceEditedLines int      `json:"sourceEditedLines"`
	Notes             []string `json:"notes"`
}

type inputRerunPlan struct {
	job     jobs.Job
	host    hosts.Host
	summary inputRerunSummary
	draft   jobs.Job
}

func refuse(format string, a ...any) *apiErr { return &apiErr{422, fmt.Sprintf(format, a...)} }

// unitOf is the data unit a dataset's catalogued header states, or the researcher's declaration when it states none.
func unitOf(d catalog.Dataset) (string, bool) {
	var h fileUnit
	_ = json.Unmarshal(effectiveHeader(d), &h)
	if h.Bunit != nil && strings.TrimSpace(*h.Bunit) != "" {
		return strings.TrimSpace(*h.Bunit), true
	}
	if d.UnitDeclaration != nil && !d.UnitDeclaration.Stale && d.UnitDeclaration.Unit != "" {
		return d.UnitDeclaration.Unit, true
	}
	return "", false
}

func intSetting(eff map[string]string, keys ...string) (int, bool) {
	for _, k := range keys {
		if v, ok := eff[k]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func arcsec(c compat.Cube) string {
	return fmt.Sprintf("%.4g x %.4g arcsec", c.PixelArcsec[0], c.PixelArcsec[1])
}

// inputCompat judges the recorded settings against the new cube. Refusals are settings that cannot hold for it (the pipeline
// itself would stop, or the setting would mean nothing); notes are things to read, never conversions.
func inputCompat(template string, eff map[string]string, cube compat.Cube, newUnit string, haveNew bool, oldCube *compat.Cube, oldUnit string, haveOld bool) (refusals, notes []string) {
	side := min(cube.NX, cube.NY)
	if template == "rohsa" {
		if crop, ok := intSetting(eff, "crop_size"); ok && crop > 0 {
			if crop > side {
				refusals = append(refusals, fmt.Sprintf("crop_size is %d pixels and the new cube is %d x %d pixels on the sky (the crop must fit inside it)", crop, cube.NX, cube.NY))
			} else {
				if x0, ok := intSetting(eff, "crop_x0"); ok && (x0 < 1 || x0 > cube.NX-crop+1) {
					refusals = append(refusals, fmt.Sprintf("crop_x0 = %d with crop_size %d does not fit inside the new cube's width of %d pixels (x0 counts from 1)", x0, crop, cube.NX))
				}
				if y0, ok := intSetting(eff, "crop_y0"); ok && (y0 < 1 || y0 > cube.NY-crop+1) {
					refusals = append(refusals, fmt.Sprintf("crop_y0 = %d with crop_size %d does not fit inside the new cube's height of %d pixels (y0 counts from 1)", y0, crop, cube.NY))
				}
			}
		}
	} else {
		if dn, ok := intSetting(eff, "vgt.dn", "dn"); ok && (dn > cube.NX || dn > cube.NY) {
			refusals = append(refusals, fmt.Sprintf("dn is %d pixels and the new cube is %d x %d pixels on the sky (VGT.jl needs dn no larger than either side)", dn, cube.NX, cube.NY))
		}
		if ker, ok := intSetting(eff, "vgt.ker", "ker"); ok && ker > side {
			refusals = append(refusals, fmt.Sprintf("ker is %d pixels and the new cube's smaller side is %d pixels (a smoothing radius larger than the image means nothing)", ker, side))
		}
		if noise, ok := eff["noise.noise"]; ok {
			switch {
			case haveNew && haveOld && !runs.SameUnit(newUnit, oldUnit):
				refusals = append(refusals, fmt.Sprintf("noise = %s is in the data's unit, which was %s for the recorded run's input and is %s for the new cube; a noise level is not converted between units here", noise, oldUnit, newUnit))
			case !haveNew || !haveOld:
				notes = append(notes, fmt.Sprintf("noise = %s is in the data's unit and was carried over unchanged; the unit could not be compared because it is not stated for %s.", noise, whichUnknown(haveOld, haveNew)))
			}
		}
	}
	if oldCube == nil {
		if cube.HasScale {
			notes = append(notes, fmt.Sprintf("The previous input is not in the catalog, so its pixel scale is not known here; window sizes are in pixels and were kept as pixels. The new cube's pixels are %s.", arcsec(cube)))
		} else {
			notes = append(notes, "Window sizes are in pixels and were kept as pixels; the pixel scale of the new cube is not declared in degrees in its header, so what they cover on the sky is not known.")
		}
		return refusals, notes
	}
	switch same, known := compat.SamePixelScale(*oldCube, cube); {
	case !known:
		notes = append(notes, "The pixel scale of one of the two cubes is not declared in degrees in its header, so whether the window sizes cover the same angle on the sky could not be compared; they were kept as pixels.")
	case !same:
		notes = append(notes, fmt.Sprintf("The pixel scale differs: the previous input has %s per pixel, the new cube %s. dn, ker and the crop are in pixels and were kept as pixels, so they cover a different angle on the sky; nothing was converted.", arcsec(*oldCube), arcsec(cube)))
	}
	if oldCube.SpecType != cube.SpecType || oldCube.SpecUnit != cube.SpecUnit {
		notes = append(notes, fmt.Sprintf("The spectral axis differs: %s (%s) before, %s (%s) now.", oldCube.SpecType, unitOrNone(oldCube.SpecUnit), cube.SpecType, unitOrNone(cube.SpecUnit)))
	}
	if oldCube.NV != cube.NV {
		notes = append(notes, fmt.Sprintf("The new cube has %d channels; the previous input had %d. Settings given in channels were kept as channels.", cube.NV, oldCube.NV))
	}
	return refusals, notes
}

func whichUnknown(haveOld, haveNew bool) string {
	switch {
	case !haveOld && !haveNew:
		return "either cube"
	case !haveOld:
		return "the previous input"
	}
	return "the new cube"
}

func unitOrNone(u string) string {
	if u == "" {
		return "unit not stated"
	}
	return u
}

// planInputRerun resolves the recorded run, the chosen cube and every check, and renders the draft. Nothing is stored.
func (s *Server) planInputRerun(ctx context.Context, ws workspace.ID, runID int64, in inputRerunIn) (inputRerunPlan, *apiErr) {
	var p inputRerunPlan
	if s.Runs == nil || s.localJobs() == nil || s.HostDB == nil || s.Store == nil {
		return p, &apiErr{404, "jobs are not available"}
	}
	run, err := s.Runs.Get(ws, runID)
	if errors.Is(err, runs.ErrNotFound) {
		return p, &apiErr{404, "run not found"}
	}
	if err != nil {
		log.Printf("runs: %v", err)
		return p, &apiErr{500, "internal error"}
	}
	if run.Kind != "vgt" && run.Kind != "rohsa" {
		return p, refuse("a configuration can be run on another cube for VGT and ROHSA runs only; this run's kind is %s", run.Kind)
	}
	p.job, err = s.localJobs().ForRun(runID)
	if errors.Is(err, jobs.ErrNotFound) {
		return p, refuse("this run was imported, not made by a job here, so no configuration was kept to repeat; draft a job from it with \"run again with...\" and state the settings yourself")
	}
	if err != nil {
		return p, jobAPIErr(err)
	}
	req, ok := p.job.ConfigRequest()
	if !ok || p.job.Template != run.Kind || strings.TrimSpace(req.BaseConfigText) == "" {
		return p, refuse("this run's job was recorded without its settings (it ran a script or a config by path), so there is no recorded configuration to repeat")
	}
	template := p.job.Template
	srcHash, ok := jobs.ConfigHash(template, req)
	if !ok {
		return p, refuse("the recorded configuration could not be read back (its overrides no longer apply to its config text), so it is not repeated")
	}
	if p.host, err = s.HostDB.Get(p.job.HostID); err != nil {
		return p, &apiErr{409, "the host this run's job ran on no longer exists"}
	}
	mode := in.RerunMode
	if mode == "" {
		mode = rerunBest
	}
	if mode == rerunStrict {
		return p, refuse("a strict rerun insists on the same input file as the run it repeats, and this repeats the configuration on a different one; use best_effort (the code as it is on the host now, with every difference listed) or adapted")
	}
	if mode, _, aerr := s.rerunFor(ws, template, &runID, mode); aerr != nil {
		return p, aerr
	} else if mode == "" {
		return p, &apiErr{500, "internal error"}
	}
	if in.DatasetID == 0 {
		return p, refuse("choose the cube to run the configuration on")
	}
	ds, err := s.Store.Get(ws, in.DatasetID)
	if errors.Is(err, catalog.ErrNotFound) {
		return p, refuse("the chosen dataset does not exist")
	}
	if err != nil {
		log.Printf("runs: %v", err)
		return p, &apiErr{500, "internal error"}
	}
	switch {
	case ds.Archived:
		return p, refuse("the chosen dataset is archived")
	case ds.Kind != "" && ds.Kind != "fits":
		return p, refuse("the chosen dataset is a %s, not a FITS cube", ds.Kind)
	case ds.HostID == 0:
		return p, refuse("the chosen cube is on this computer, and the job reads its input on the host %s; register the cube on that host (or transfer it there) first", p.host.Name)
	case ds.HostID != p.host.ID:
		return p, refuse("the chosen cube is on another host than the one the recorded run ran on (%s); a job reads only files on its own host", p.host.Name)
	case !jobs.ValidPath(ds.Path) || !p.host.InRoots(ds.Path):
		return p, refuse("the chosen cube's path is not an absolute path without spaces or quotes inside the host's source folders, so a job cannot be given it")
	}
	prev := jobs.InputPathOf(template, req.BaseConfigText)
	// The script runs from the code folder, so a relative input in the recorded config is read from there: looked up and compared
	// as that absolute path (the pipelines' own configs use relative paths).
	prevAbs, relNote := prev, ""
	if prev != "" && !path.IsAbs(prev) && jobs.ValidPath(req.RepoDir) {
		prevAbs = path.Join(req.RepoDir, prev)
		relNote = fmt.Sprintf("The recorded input was the relative path %s, which the script reads from the code folder (%s), so it was taken to be %s.", prev, req.RepoDir, prevAbs)
	}
	if prevAbs == ds.Path {
		return p, refuse("the chosen cube is the file the recorded run already used; to repeat the run on the same input use \"run again with...\"")
	}
	eff := effectiveSettings(template, req)
	if template == "vgt" {
		for k, v := range jobs.EffectiveVGTSettings(req) {
			if strings.HasSuffix(k, "weight_file") && strings.TrimSpace(v) != "" {
				return p, refuse("the recorded configuration names a weight file (%s = %s), which belongs to the old cube; only the input is replaced here, so the old weight cube is not carried onto a new input", k, v)
			}
		}
	}
	cube, why := compat.ReadCube(effectiveHeader(ds))
	if why != "" {
		return p, refuse("the chosen cube is not one the pipeline can read: %s", why)
	}
	// The previous input, when it is in the catalog, lets the pixel scale, spectral axis and unit be compared.
	var oldCube *compat.Cube
	var oldUnit string
	var haveOld bool
	if prevAbs != "" && path.IsAbs(prevAbs) {
		if id, found, _ := s.Store.LocationOwner(ws, p.job.HostID, prevAbs); found {
			if od, err := s.Store.Get(ws, id); err == nil {
				if c, why := compat.ReadCube(effectiveHeader(od)); why == "" {
					oldCube = &c
				}
				oldUnit, haveOld = unitOf(od)
			}
		}
	}
	newUnit, haveNew := unitOf(ds)
	refusals, notes := inputCompat(template, eff, cube, newUnit, haveNew, oldCube, oldUnit, haveOld)
	if relNote != "" {
		notes = append([]string{relNote}, notes...)
	}
	if len(refusals) > 0 {
		return p, refuse("the recorded settings do not fit the chosen cube (%s): %s", ds.Name, strings.Join(refusals, "; "))
	}
	newBase, err := jobs.SetInputFile(template, req.BaseConfigText, ds.Path)
	if err != nil {
		return p, jobAPIErr(err)
	}
	next := req
	next.BaseConfigText = newBase
	newHash, ok := jobs.ConfigHash(template, next)
	if !ok || newHash != srcHash {
		log.Printf("runs: configuration hash changed by swapping the input of run %d", runID)
		return p, &apiErr{500, "internal error"}
	}
	ident := jobs.RerunInputIdentity{DatasetID: ds.ID, Name: ds.Name, HostID: ds.HostID, Path: ds.Path, SizeBytes: ds.SizeBytes, ModifiedAt: ds.ModifiedAt.Unix()}
	if locs, err := s.Store.Locations(ws, ds.ID); err == nil {
		if l, ok := primaryOf(locs); ok && l.SizeBytes == ds.SizeBytes {
			if l.Sampled != nil {
				ident.SampledFP = l.Sampled.Value
			}
			if l.Full != nil {
				ident.FullSHA256 = l.Full.Value
			}
		}
	}
	if ident.FullSHA256 == "" {
		notes = append(notes, "The catalog has no full SHA-256 for this cube. The job will hash the file itself before it starts (one pass over the file on the compute node) and the run will record that value.")
	}
	name := in.Name
	if name == "" {
		name = inputRerunName(run.Name, ds.Name)
	}
	if utf8.RuneCountInString(name) > 40 {
		return p, refuse("the job name is at most 40 characters")
	}
	rec := &jobs.InputRerun{SourceRunID: runID, SourceConfigHash: srcHash, ConfigHash: newHash, PreviousInput: prev, Input: ident, Notes: notes}
	var b builtJob
	var aerr *apiErr
	if template == "rohsa" {
		b, aerr = s.buildROHSA(ctx, p.host, name, rohsaInput{RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, ConfigText: newBase, Set: req.Set,
			CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account, RerunMode: mode, InputRerun: rec})
	} else {
		b, aerr = s.buildVGT(ctx, p.host, name, vgtInput{RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, ConfigText: newBase, Set: req.Set,
			CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account, RerunMode: mode, InputRerun: rec})
	}
	if aerr != nil {
		return p, aerr
	}
	sum := inputRerunSummary{SourceRunID: runID, SourceRunName: run.Name, SourceJobID: p.job.ID, Template: template, HostID: p.host.ID, Mode: mode, Name: name,
		SourceConfigHash: srcHash, ConfigHash: newHash, Input: ident, PreviousInput: prev, Changed: []summaryChange{}, Carried: []summaryValue{}, Notes: notes}
	if prev != "" {
		sum.Changed = append(sum.Changed, summaryChange{Key: "input", Parent: strp(prev), Now: strp(ds.Path)})
	} else {
		sum.Changed = append(sum.Changed, summaryChange{Key: "input", Now: strp(ds.Path)})
	}
	keys := make([]string, 0, len(eff))
	for k := range eff {
		if base := k[strings.LastIndex(k, ".")+1:]; !pathKeysForSummary[base] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		sum.Carried = append(sum.Carried, summaryValue{k, eff[k]})
	}
	sum.Carried = append(sum.Carried,
		summaryValue{"code.repo_dir", req.RepoDir},
		summaryValue{"resources.cpus", strconv.Itoa(req.CPUs)}, summaryValue{"resources.mem", req.Mem}, summaryValue{"resources.time", req.Time})
	if req.Partition != "" {
		sum.Carried = append(sum.Carried, summaryValue{"resources.partition", req.Partition})
	}
	if req.Account != "" {
		sum.Carried = append(sum.Carried, summaryValue{"resources.account", req.Account})
	}
	if rendered, ok := renderedFromSettings(p.job); ok && rendered != p.job.Script {
		sum.SourceEditedLines = len(sweeps.Diff(rendered, p.job.Script, nil)) // lines, "-" and "+" together: the count the sweep review gives
		sum.Notes = append(sum.Notes, fmt.Sprintf("The source job's script differs from what its recorded settings render now by %d lines (edited by hand, or rendered by an earlier template). The draft is rendered from the recorded settings, so those differences are not carried over.", sum.SourceEditedLines))
	}
	if ident.SampledFP == "" && ident.FullSHA256 == "" {
		sum.Notes = append(sum.Notes, "No fingerprint is recorded for this cube in the catalog yet.")
	}
	p.summary = sum
	p.draft = jobs.Job{HostID: p.host.ID, Name: name, Template: template, Script: b.Script, OutputDir: b.OutputDir, Request: b.Request, ParentRunID: &runID,
		Note: fmt.Sprintf("Configuration of run %d (%s) on %s", runID, run.Name, ds.Name), AutoImport: autoImports(template)}
	if utf8.RuneCountInString(p.draft.Note) > jobs.MaxNote {
		p.draft.Note = string([]rune(p.draft.Note)[:jobs.MaxNote])
	}
	return p, nil
}

var pathKeysForSummary = map[string]bool{"input_file": true, "input": true, "output_dir": true}

// inputRerunName suggests a job name (letters, digits, . _ -, at most 40) from the run and the cube.
func inputRerunName(runName, dsName string) string {
	clean := func(s string, n int) string {
		s = strings.TrimSuffix(s, ".fits")
		s = strings.Trim(notNameChar.ReplaceAllString(s, "-"), "-")
		if len(s) > n {
			s = strings.TrimRight(s[:n], "-")
		}
		return s
	}
	a, b := clean(runName, 18), clean(dsName, 40-len("-on-")-1)
	if a == "" {
		a = "run"
	}
	if b == "" {
		b = "cube"
	}
	out := a + "-on-" + b
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	return out
}

// previewInputRerun is GET /api/runs/{id}/rerun-on-input?datasetId=N: what the draft would carry and change, or why it is
// refused. Nothing is stored.
func (s *Server) previewInputRerun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	ds, _ := strconv.ParseInt(r.URL.Query().Get("datasetId"), 10, 64)
	p, aerr := s.planInputRerun(r.Context(), wsOf(r), id, inputRerunIn{DatasetID: ds, Name: r.URL.Query().Get("name"), RerunMode: r.URL.Query().Get("rerunMode")})
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	writeJSON(w, 200, p.summary)
}

// createInputRerun is POST /api/runs/{id}/rerun-on-input: the draft, stored, with the summary. Nothing is submitted.
func (s *Server) createInputRerun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in inputRerunIn
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	p, aerr := s.planInputRerun(r.Context(), wsOf(r), id, in)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	created, err := s.Jobs.CreateDraft(p.draft)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	s.Audit.RecordCtx(r.Context(), "job drafted on another cube", "job", created.ID,
		fmt.Sprintf("configuration %s of run %d on dataset %d (%s)", p.summary.ConfigHash[:12], id, p.summary.Input.DatasetID, p.summary.Input.Name))
	writeJSON(w, 201, map[string]any{"job": created, "summary": p.summary})
}
