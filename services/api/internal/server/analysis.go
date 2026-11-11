package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// AnalysisConfig locates the analysis program and where its results go. DerivedDir must be one of the source roots,
// so the files it produces are registered like any other file.
type AnalysisConfig struct {
	Python     string // interpreter, as resolved at startup
	Script     string // analysis.py
	DerivedDir string
	Timeout    time.Duration
}

var analysisOps = map[string]bool{"axial_diff": true, "stats": true, "moment": true, "cutout": true, "slab": true, "align": true, "convolve": true, "scalar_diff": true}

// analysisArgs turns the request's options into command-line options, each validated by the same patterns the job
// template uses, so a value can never become anything but a number or a known word.
type analysisReq struct {
	Op          string            `json:"op"`
	DatasetID   int64             `json:"datasetId"`
	ReferenceID int64             `json:"referenceId"`
	Params      map[string]string `json:"params"`
	// Masks to apply, each with its own explicit policy (ADR 0055): statistics and moment maps, on a file read here.
	Masks []maskUse `json:"masks"`
	// remote analyses become Slurm jobs
	CPUs int    `json:"cpus"`
	Mem  string `json:"mem"`
	Time string `json:"time"`
}

var optionNames = map[string]string{"order": "order", "chanStart": "chan-start", "chanEnd": "chan-end", "threshold": "threshold", "x0": "x0", "x1": "x1", "y0": "y0", "y1": "y1", "method": "method", "targetBmaj": "target-bmaj", "targetBmin": "target-bmin", "targetBpa": "target-bpa"}

func (a analysisReq) options() (map[string]string, error) {
	out := map[string]string{}
	for k, v := range a.Params {
		name, ok := optionNames[k]
		if !ok {
			return nil, fmt.Errorf("unknown option %q", k)
		}
		out[name] = v
	}
	// validate through the job template's own rules
	if _, err := jobs.RenderAnalysis(jobs.AnalysisSettings{Name: "check", Python: "python3", ScriptDir: "$HOME/.cache/vesqira/0123456789abcdef", Op: a.Op,
		Input: "/a/b.fits", Reference: "/a/c.fits", OutDir: "/a/out", Args: out, CPUs: 1, Mem: "1G", Time: "00:01:00"}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) analysisDeps() bool { return s.Analysis != nil && s.Derived != nil }

// analyze runs an analysis on a file on this machine, in its own process with a time limit, and registers the result
// as a new dataset with its provenance. A file on another host is analysed there, as a Slurm job (see analysisJob).
func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	var in analysisReq
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if !analysisOps[in.Op] {
		fail(w, 400, "unknown analysis")
		return
	}
	opts, err := in.options()
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	d, code, msg := s.imageDataset(in.DatasetID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if d.HostID != 0 {
		fail(w, 409, "this file lives on another host: create the analysis as a job there (Analyse on the host), so it runs through Slurm and not on the login node")
		return
	}
	real, _, status, m := s.authorize(r.Context(), d)
	if status != 0 {
		fail(w, status, m)
		return
	}
	ax, code, msg := s.axialAnalysis(r.Context(), in.Op, d, false)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	args := []string{"-P", s.Analysis.Script, in.Op, "--input", real}
	sources := []int64{d.ID}
	if ax != nil {
		args = append(args, ax.args...)
	}
	var maskSafe []map[string]any // what the result's provenance keeps of the masks applied: no ids, no paths
	if len(in.Masks) > 0 {
		if in.Op != "stats" && in.Op != "moment" {
			fail(w, 422, "masks are applied by statistics, moment maps and relative orientation; this analysis does not take them")
			return
		}
		specs, safe, _, code, msg := s.maskSpecs(in.Masks, d, false)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		args = append(args, "--masks", specs)
		maskSafe = safe
	}
	// A unit the person declared for a file with no BUNIT goes with the request, so the result says it, labelled as theirs. The
	// worker uses it only when the file has no unit of its own. The "=" form keeps a unit that starts with "-" a value.
	declared := map[string]string{}
	if u := declaredUnitOf(d); u != "" && in.Op != "stats" && in.Op != "axial_diff" { // an orientation map has no brightness unit
		args = append(args, "--declared-unit="+u)
		declared["input"] = u
	}
	if in.Op == "align" || in.Op == "axial_diff" || in.Op == "scalar_diff" {
		ref, code, msg := s.imageDataset(in.ReferenceID)
		if code != 0 {
			fail(w, 422, "reference: "+msg)
			return
		}
		if in.Op == "axial_diff" {
			// both maps must be declared axial (unit known) and share the grid: a difference is only meaningful then
			ia, ib := d.Interpretation, ref.Interpretation
			if ia == nil || ib == nil || ia.Kind != "axial_orientation" || ib.Kind != "axial_orientation" {
				fail(w, 422, "both maps must be declared as axial orientation maps (with a unit) before they can be differenced")
				return
			}
			if why, err := compat.Grids(d.Header, ref.Header); err != nil || len(why) > 0 {
				writeJSON(w, 409, map[string]any{"error": "the maps are not known to share a grid, so a pixel-by-pixel difference is not meaningful", "reasons": why})
				return
			}
			args = append(args, "--unit", ia.Unit, "--unit-ref", ib.Unit)
		}
		if in.Op == "scalar_diff" {
			// angles are periodic: a map declared axial has its own difference, and a plain subtraction would be wrong for it
			if (d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation") || (ref.Interpretation != nil && ref.Interpretation.Kind == "axial_orientation") {
				fail(w, 422, "a map declared as an axial orientation map needs the axial difference, which is taken modulo 180 degrees")
				return
			}
			if why, err := compat.Grids(d.Header, ref.Header); err != nil || len(why) > 0 {
				writeJSON(w, 409, map[string]any{"error": "the maps are not known to share a grid, so a pixel-by-pixel difference is not meaningful", "reasons": why})
				return
			}
		}
		if ref.HostID != 0 {
			fail(w, 409, "the reference lives on another host; both files must be on the same machine to be aligned")
			return
		}
		rreal, _, status, m := s.authorize(r.Context(), ref)
		if status != 0 {
			fail(w, status, "reference: "+m)
			return
		}
		args = append(args, "--reference", rreal)
		sources = append(sources, ref.ID)
		if u := declaredUnitOf(ref); u != "" && in.Op == "scalar_diff" {
			args = append(args, "--declared-unit-ref="+u)
			declared["reference"] = u
		}
	}
	for k, v := range opts {
		args = append(args, "--"+k, v)
	}
	var outPath string
	if in.Op != "stats" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		outPath = filepath.Join(s.Analysis.DerivedDir, fmt.Sprintf("%s-%d-%s.fits", in.Op, d.ID, hex.EncodeToString(b[:])))
		args = append(args, "--out", outPath)
	}
	relPath := ""
	if ax != nil {
		relPath = strings.TrimSuffix(outPath, ".fits") + "-reliability.fits"
		args = append(args, "--rel-out", relPath)
	}
	res, code, msg := s.runAnalysisProgram(r, args)
	if code != 0 {
		if outPath != "" {
			_ = os.Remove(outPath)
		}
		if relPath != "" {
			_ = os.Remove(relPath)
		}
		fail(w, code, msg)
		return
	}
	if in.Op == "stats" {
		writeJSON(w, 200, map[string]any{"summary": res.Summary, "software": derived.CleanSoftware(res.Software)})
		return
	}
	nd, code, msg := s.registerPath(r.Context(), outPath)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	pm := map[string]any{"options": opts}
	if len(maskSafe) > 0 {
		pm["masks"] = maskSafe // the reviewer sees these (type, rule, policy, tier, fingerprint); the summary keeps the ids for the owner
	}
	if len(declared) > 0 {
		pm["declaredUnits"] = declared // offered to the worker; whether it was used (the file may state its own unit) is in the summary's unit_source
	}
	params, _ := json.Marshal(pm)
	if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: in.Op, Params: params, Sources: sources, Summary: res.Summary, Software: res.Software}); err != nil {
		log.Printf("derived: %v", err)
	}
	nd = s.finishAxialResult(analysisOutcome{res.Summary, res.Software}, nd, ax, relPath, func(p string) (catalog.Dataset, int, string) { return s.registerPath(r.Context(), p) }, in.Op, sources, params)
	writeJSON(w, 201, map[string]any{"dataset": nd, "summary": res.Summary})
}

// runAnalysisProgram runs the analysis program (python and args) in its own process group, bounded by the configured timeout and
// killed with its children when the request ends, and reads its one JSON reply. code is the HTTP status to answer with when it
// is not 0; the caller removes any output it named.
func (s *Server) runAnalysisProgram(r *http.Request, args []string) (analysisOutcome, int, string) {
	if s.Load != nil { // heavy class: wait for a slot, visible to the person who asked (workload.go)
		label := "analysis"
		if len(args) > 2 {
			label = analysisLabel(args[2])
		}
		_, release, err := s.Load.Acquire(r.Context(), worker.Heavy, label)
		if err != nil {
			return analysisOutcome{}, 499, "request cancelled"
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.Analysis.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Analysis.Python, args...)
	cmd.Env = worker.BaseEnv()
	if id := RequestID(r.Context()); id != "" { // already validated as a plain token
		cmd.Env = append(cmd.Env, "VESQIRA_REQUEST_ID="+id)
	}
	cmd.Stderr = worker.StderrTail.Writer(nil)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &limited{w: &out, max: 1 << 20}
	runErr := cmd.Run()
	if ctx.Err() != nil {
		if r.Context().Err() != nil {
			return analysisOutcome{}, 499, "request cancelled"
		}
		return analysisOutcome{}, 504, "the analysis took too long and was stopped"
	}
	var res struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Summary  json.RawMessage `json:"summary"`
		Software json.RawMessage `json:"software"`
	}
	if json.Unmarshal(out.Bytes(), &res) != nil || runErr != nil || !res.OK {
		if res.Error != "" {
			return analysisOutcome{}, 422, res.Error
		}
		return analysisOutcome{}, 422, "the analysis could not be run on this file"
	}
	return analysisOutcome{res.Summary, res.Software}, 0, ""
}

type limited struct {
	w   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.max {
		return 0, errors.New("output limit exceeded")
	}
	return l.w.Write(p)
}

// analysisJob drafts a Slurm job that runs the analysis on the host where the file lives. The script is reviewed and
// submitted like any other job; its results come back through "register" as a derived dataset.
func (s *Server) analysisJob(w http.ResponseWriter, r *http.Request) {
	var in analysisReq
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if !analysisOps[in.Op] {
		fail(w, 400, "unknown analysis")
		return
	}
	opts, err := in.options()
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	d, code, msg := s.imageDataset(in.DatasetID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if in.Op == "scalar_diff" {
		fail(w, 409, "a difference is computed on this computer, from two maps registered here")
		return
	}
	if in.Op == "axial_diff" {
		fail(w, 409, "an axial difference is computed on this computer, from two maps registered here")
		return
	}
	if d.HostID == 0 {
		fail(w, 409, "this file is on this computer: run the analysis directly")
		return
	}
	if len(in.Masks) > 0 {
		fail(w, 409, "masks are applied to files on this computer; an analysis job on a host does not take them yet")
		return
	}
	ax, code, msg := s.axialAnalysis(r.Context(), in.Op, d, true)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	h, err := s.HostDB.Get(d.HostID)
	if err != nil {
		fail(w, 404, "host not found")
		return
	}
	sources := []int64{d.ID}
	refPath := ""
	if in.Op == "align" {
		ref, code, msg := s.imageDataset(in.ReferenceID)
		if code != 0 {
			fail(w, 422, "reference: "+msg)
			return
		}
		if ref.HostID != d.HostID {
			fail(w, 409, "both files must be on the same host to be aligned there")
			return
		}
		refPath = ref.Path
		sources = append(sources, ref.ID)
	}
	if in.CPUs == 0 {
		in.CPUs, in.Mem, in.Time = 4, "16G", "02:00:00"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.Remote.Deploy(ctx, h); err != nil { // the analysis program must be on the host before a job can run it
		fail(w, 502, err.Error())
		return
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("%s-%d", in.Op, d.ID)
	out := path.Join(h.Roots[0], "vesqira_derived", name+"-"+hex.EncodeToString(b[:]))
	set := jobs.AnalysisSettings{Name: name, Python: h.Python, ScriptDir: s.Remote.RemoteDir(), Op: in.Op, Input: d.Path, Reference: refPath,
		OutDir: out, Args: opts, CPUs: in.CPUs, Mem: in.Mem, Time: in.Time}
	pm := map[string]any{"op": in.Op, "options": opts, "sources": sources}
	if ax != nil { // the same code path as a run here: the worker gets the declaration and writes the companion reliability map
		set.AxialUnit, set.AxialFrame, set.Weights = ax.unit, ax.frame, ax.weights
		pm["axial"] = axialParams{Unit: ax.unit, Frame: ax.interp.Frame, Quantity: ax.interp.Quantity}
	}
	script, err := jobs.RenderAnalysis(set)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	params, _ := json.Marshal(pm)
	job, err := s.Jobs.CreateDraft(jobs.Job{HostID: h.ID, Name: name, Template: "analysis", Script: script, OutputDir: out, Params: string(params)})
	if err != nil {
		s.jobErr(w, err)
		return
	}
	writeJSON(w, 201, job)
}

// registerAnalysisJob brings a finished analysis job's result.fits (and its result.json) in as a derived dataset.
func (s *Server) registerAnalysisJob(ctx context.Context, j jobs.Job, h hostRef) (catalog.Dataset, int, string) {
	text, code, msg := s.remoteText(ctx, h.Host, path.Join(j.OutputDir, "result.json"))
	if code != 0 {
		return catalog.Dataset{}, code, "result.json: " + msg
	}
	var res struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Summary  json.RawMessage `json:"summary"`
		Software json.RawMessage `json:"software"`
	}
	if json.Unmarshal([]byte(text), &res) != nil || !res.OK {
		return catalog.Dataset{}, 422, "the analysis did not finish successfully on the host"
	}
	var p struct {
		Op      string            `json:"op"`
		Options map[string]string `json:"options"`
		Sources []int64           `json:"sources"`
		Axial   *axialParams      `json:"axial"`
	}
	_ = json.Unmarshal([]byte(j.Params), &p)
	if p.Op == "stats" {
		return catalog.Dataset{}, 409, "a statistics job has no file to register: read its result"
	}
	d, code, msg := s.registerRemoteFile(ctx, h.Host, path.Join(j.OutputDir, "result.fits"))
	if code != 0 {
		return catalog.Dataset{}, code, msg
	}
	params, _ := json.Marshal(map[string]any{"options": p.Options})
	if err := s.Derived.Save(derived.Record{DatasetID: d.ID, Op: p.Op, Params: params, Sources: p.Sources, Summary: res.Summary, Software: res.Software}); err != nil {
		log.Printf("derived: %v", err)
	}
	if p.Axial != nil {
		in := catalog.Interpretation{Kind: "axial_orientation", Unit: p.Axial.Unit, Frame: p.Axial.Frame, Quantity: p.Axial.Quantity}
		reg := func(fp string) (catalog.Dataset, int, string) { return s.registerRemoteFile(ctx, h.Host, fp) }
		d = s.finishAxialResult(analysisOutcome{res.Summary, res.Software}, d, &axialRun{interp: in}, remoteRelPath(j.OutputDir), reg, p.Op, p.Sources, params)
	}
	return d, 0, ""
}

// derivedSummary is the reviewer-safe description of how a dataset was made, or nil.
func (s *Server) derivedSummary(datasetID int64) *derived.Summary {
	if s.Derived == nil {
		return nil
	}
	rec, err := s.Derived.Get(datasetID)
	if err != nil {
		return nil
	}
	var sum struct {
		Policy string `json:"policy"`
	}
	_ = json.Unmarshal(rec.Summary, &sum)
	names := []string{}
	for _, id := range rec.Sources {
		if d, err := s.Store.Get(id); err == nil {
			names = append(names, d.Name)
		}
	}
	return &derived.Summary{Op: rec.Op, Params: rec.Params, Sources: names, Policy: sum.Policy, Software: rec.Software, CreatedAt: rec.CreatedAt}
}

func (s *Server) getDerived(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	rec, err := s.Derived.Get(id)
	if err != nil {
		fail(w, 404, "not a derived dataset")
		return
	}
	writeJSON(w, 200, rec)
}

// jobResult returns an analysis job's result.json (statistics have no file; every analysis prints its summary there).
func (s *Server) jobResult(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.Template != "analysis" || j.OutputDir == "" {
		fail(w, 409, "this job has no analysis result")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, code, msg := s.remoteText(ctx, h, path.Join(j.OutputDir, "result.json"))
	if code != 0 {
		fail(w, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(text))
}
