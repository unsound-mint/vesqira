package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
)

// Parameter sweeps (ADR 0048). A sweep is a base job, a grid of explicit values for one to three config settings, and the
// ordinary draft jobs the grid expands to. Creating a sweep submits nothing. The owner reads the scripts (and how they differ
// from the base) and approves them with a concurrency limit; the scheduler below then submits one draft at a time through the
// same submitDraft the Submit button uses, as earlier jobs end.

var sweepNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`) // leaves room for the parameters in each job name (40)

type sweepParamIn struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

type sweepIn struct {
	Name      string         `json:"name"`
	BaseJobID *int64         `json:"baseJobId"`
	BaseRunID *int64         `json:"baseRunId"`
	Params    []sweepParamIn `json:"params"`
	Note      string         `json:"note"`
}

func (in sweepIn) params() []sweeps.Param {
	out := make([]sweeps.Param, 0, len(in.Params))
	for _, p := range in.Params {
		out = append(out, sweeps.Param{Key: p.Key, Label: strings.TrimSpace(p.Label), Values: p.Values})
	}
	return out
}

// sweepPlan is a sweep as it would be created: the base it starts from and every draft, rendered and checked, none stored.
type sweepPlan struct {
	base    jobs.Job
	baseRun *int64
	host    hosts.Host
	params  []sweeps.Param
	combos  []map[string]string
	drafts  []jobs.Job
}

// plan resolves the base, validates the grid and renders every draft before anything is stored, so a bad value in the last
// combination leaves nothing half made.
func (s *Server) plan(ctx context.Context, in sweepIn) (sweepPlan, *apiErr) {
	var p sweepPlan
	if s.Sweeps == nil || s.Jobs == nil || s.HostDB == nil {
		return p, &apiErr{404, "sweeps are not available"}
	}
	if (in.BaseJobID == nil) == (in.BaseRunID == nil) {
		return p, &apiErr{422, "give either baseJobId or baseRunId"}
	}
	var err error
	if in.BaseJobID != nil {
		p.base, err = s.Jobs.Get(*in.BaseJobID)
	} else {
		p.baseRun = in.BaseRunID
		p.base, err = s.Jobs.ForRun(*in.BaseRunID)
		if errors.Is(err, jobs.ErrNotFound) {
			return p, &apiErr{422, "this run was imported, not made by a job here, so how it was set up is not known; sweep from a job"}
		}
	}
	if errors.Is(err, jobs.ErrNotFound) {
		return p, &apiErr{422, "the base job does not exist"}
	}
	if err != nil {
		return p, jobAPIErr(err)
	}
	if p.baseRun == nil {
		p.baseRun = p.base.RunID
	}
	var req jobs.VGTRequest
	var ok bool
	switch p.base.Template {
	case "vgt":
		req, ok = p.base.VGTRequest()
	case "rohsa":
		req, ok = p.base.ROHSARequest()
	}
	if !ok {
		return p, &apiErr{422, "only a VGT or ROHSA job that kept its settings can be swept: its config settings are the parameters (custom scripts, and ROHSA jobs drafted before settings were kept, have none)"}
	}
	rohsa := p.base.Template == "rohsa"
	if !sweepNameRe.MatchString(in.Name) {
		return p, &apiErr{422, "the sweep name may use letters, digits, . _ - (up to 40); it starts each job's name"}
	}
	if utf8.RuneCountInString(in.Note) > jobs.MaxNote {
		return p, &apiErr{422, fmt.Sprintf("the note is at most %d characters", jobs.MaxNote)}
	}
	p.params = in.params()
	if err := sweeps.Validate(p.params); err != nil {
		return p, &apiErr{422, err.Error()}
	}
	if rohsa {
		for i, a := range p.params {
			for _, b := range p.params[i+1:] {
				if jobs.ROHSAOverlap(a.Key, b.Key) {
					return p, &apiErr{422, a.Key + " and " + b.Key + " change the same config lines (lambda sets lambda_amp, lambda_mu and lambda_sig together); sweep one of them"}
				}
			}
		}
	}
	for _, sp := range p.params {
		if sp.Key == "output_dir" || sp.Key == "input_file" {
			return p, &apiErr{422, sp.Key + " is set by Vesqira or is a path; it cannot be swept"}
		}
		for _, v := range sp.Values {
			check := jobs.CheckSetting
			if rohsa {
				check = jobs.CheckROHSASetting
			}
			if err := check(sp.Key, v); err != nil {
				return p, &apiErr{422, err.Error()}
			}
		}
	}
	if p.host, err = s.HostDB.Get(p.base.HostID); err != nil {
		return p, &apiErr{409, "the base job's host no longer exists"}
	}
	if p.combos, err = sweeps.Expand(p.params); err != nil {
		return p, &apiErr{422, err.Error()}
	}
	// the base config is read once (from the text the base job started from, else from its file) and shared by every draft
	cfg := req.BaseConfigText
	if cfg == "" && req.ConfigPath != "" {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text, code, msg := s.remoteText(cctx, p.host, req.ConfigPath)
		cancel()
		if code != 0 {
			return p, &apiErr{code, "config: " + msg}
		}
		cfg = text
	}
	for i, c := range p.combos {
		set := map[string]string{}
		for k, v := range req.Set {
			// a setting of the base job that changes a line a swept setting changes would fight it; the swept value is the draft's
			swept := false
			for _, sp := range p.params {
				swept = swept || (rohsa && jobs.ROHSAOverlap(k, sp.Key)) || k == sp.Key
			}
			if !swept {
				set[k] = v
			}
		}
		for k, v := range c {
			set[k] = v
		}
		name := sweeps.JobName(in.Name, p.params, c, i)
		var b builtJob
		var aerr *apiErr
		if rohsa {
			b, aerr = s.buildROHSA(ctx, p.host, name, rohsaInput{RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, ConfigText: cfg, Set: set,
				CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account})
		} else {
			b, aerr = s.buildVGT(ctx, p.host, name, vgtInput{RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, ConfigText: cfg, Set: set,
				CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account})
		}
		if aerr != nil {
			return p, &apiErr{aerr.Code, name + ": " + aerr.Msg}
		}
		// the request keeps the config's own settings apart from the swept ones, as an ordinary draft does
		why := "Sweep " + in.Name + ": " + sweeps.Label(p.params, c)
		if in.Note != "" {
			why += ". " + in.Note
		}
		if utf8.RuneCountInString(why) > jobs.MaxNote {
			why = string([]rune(why)[:jobs.MaxNote])
		}
		// results are imported by themselves (ADR 0043), for ROHSA too (ADR 0046): the grid shows each run's metrics
		p.drafts = append(p.drafts, jobs.Job{HostID: p.host.ID, Name: name, Template: p.base.Template, Script: b.Script, OutputDir: b.OutputDir, Request: b.Request,
			ParentRunID: p.baseRun, Note: why, AutoImport: true})
	}
	return p, nil
}

type totals struct {
	Jobs            int   `json:"jobs"`
	CPUsEach        int   `json:"cpusEach"` // the largest request of any job
	CPUsTotal       int   `json:"cpusTotal"`
	MemBytesEach    int64 `json:"memBytesEach"`
	MemBytesTotal   int64 `json:"memBytesTotal"`
	TimeLimitEach   int64 `json:"timeLimitSecondsEach"`
	TimeLimitTotal  int64 `json:"timeLimitSecondsTotal"`
	CPUSecondsLimit int64 `json:"cpuSecondsAtLimit"` // sum of CPUs x time limit: the most the requests could use
}

func sumRequested(scripts []string) totals {
	var t totals
	for _, sc := range scripts {
		r := jobs.RequestedOf(sc)
		t.Jobs++
		t.CPUsTotal += r.CPUs
		t.MemBytesTotal += r.MemBytes
		t.TimeLimitTotal += r.TimeSeconds
		t.CPUSecondsLimit += int64(r.CPUs) * r.TimeSeconds
		if r.CPUs > t.CPUsEach {
			t.CPUsEach = r.CPUs
		}
		if r.MemBytes > t.MemBytesEach {
			t.MemBytesEach = r.MemBytes
		}
		if r.TimeSeconds > t.TimeLimitEach {
			t.TimeLimitEach = r.TimeSeconds
		}
	}
	return t
}

// estimate is what the base job measured (ADR 0045), offered as a rough guide: a changed parameter can change the run time and
// the memory a lot, so it is never a prediction and the screen says so.
type estimate struct {
	BaseJobID      int64    `json:"baseJobId"`
	BaseJobName    string   `json:"baseJobName"`
	ElapsedSeconds int64    `json:"elapsedSeconds"`
	MaxRSSBytes    *int64   `json:"maxRssBytes"`
	TotalCPUSec    *float64 `json:"totalCpuSeconds"`
	Basis          string   `json:"basis"`
}

func estimateFrom(base jobs.Job) *estimate {
	if base.Usage == nil {
		return nil
	}
	u := base.Usage
	return &estimate{BaseJobID: base.ID, BaseJobName: base.Name, ElapsedSeconds: u.ElapsedSeconds, MaxRSSBytes: u.MaxRSSBytes, TotalCPUSec: u.TotalCPUSec,
		Basis: "what " + base.Name + " used when it ended, as Slurm reported it; a changed parameter can change time and memory, so this is a guide, not a prediction"}
}

func (s *Server) previewSweep(w http.ResponseWriter, r *http.Request) {
	var in sweepIn
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	p, aerr := s.plan(r.Context(), in)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	type pj struct {
		Name   string            `json:"name"`
		Label  string            `json:"label"`
		Values map[string]string `json:"values"`
	}
	list := make([]pj, 0, len(p.drafts))
	scripts := make([]string, 0, len(p.drafts))
	for i, d := range p.drafts {
		list = append(list, pj{d.Name, sweeps.Label(p.params, p.combos[i]), p.combos[i]})
		scripts = append(scripts, d.Script)
	}
	writeJSON(w, 200, map[string]any{"count": len(list), "max": sweeps.MaxJobs, "baseJob": map[string]any{"id": p.base.ID, "name": p.base.Name, "hostId": p.base.HostID},
		"jobs": list, "totals": sumRequested(scripts), "estimate": estimateFrom(p.base)})
}

func (s *Server) createSweep(w http.ResponseWriter, r *http.Request) {
	var in sweepIn
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	p, aerr := s.plan(r.Context(), in)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	created, err := s.Jobs.CreateDrafts(p.drafts)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	ids := make([]int64, len(created))
	for i, j := range created {
		ids[i] = j.ID
	}
	sw, err := s.Sweeps.Create(sweeps.Sweep{Name: in.Name, HostID: p.host.ID, BaseJobID: p.base.ID, BaseRunID: p.baseRun, Params: p.params, Note: in.Note}, ids, p.combos)
	if err != nil {
		s.Jobs.DeleteDrafts(ids) // nothing refers to the drafts yet
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "sweep created", "sweep", sw.ID, fmt.Sprintf("%d draft jobs from job %d; nothing submitted", len(ids), p.base.ID))
	writeJSON(w, 201, s.sweepView(sw))
}

func (s *Server) sweepFrom(w http.ResponseWriter, r *http.Request) (sweeps.Sweep, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return sweeps.Sweep{}, false
	}
	sw, err := s.Sweeps.Get(id)
	if errors.Is(err, sweeps.ErrNotFound) {
		fail(w, 404, "sweep not found")
		return sw, false
	}
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return sw, false
	}
	return sw, true
}

type memberView struct {
	JobID   int64             `json:"jobId"`
	Name    string            `json:"name"`
	Label   string            `json:"label"`
	Values  map[string]string `json:"values"`
	State   string            `json:"state"`
	Detail  string            `json:"detail"`
	RunID   *int64            `json:"runId"`
	Held    bool              `json:"held"` // a draft whose script changed after the owner approved it: it is not submitted
	Usage   *jobs.Usage       `json:"usage"`
	Request jobs.Requested    `json:"requested"`
}

type sweepOut struct {
	sweeps.Sweep
	Jobs   []memberView   `json:"jobs"`
	Counts map[string]int `json:"counts"`
	// WaitingOn names the jobs that hold the sweep's slots and whose fate is not settled, so a stall is visible:
	// "vgt-dn16 is submitting: Slurm's answer was not seen".
	WaitingOn []string `json:"waitingOn"`
	Pending   int      `json:"pending"`
	// Template is the base job's: "vgt" or "rohsa" (empty when the base job is gone).
	Template string `json:"template"`
}

func (s *Server) sweepView(sw sweeps.Sweep) sweepOut {
	out := sweepOut{Sweep: sw, Jobs: []memberView{}, Counts: map[string]int{}, WaitingOn: []string{}}
	if b, err := s.Jobs.Get(sw.BaseJobID); err == nil {
		out.Template = b.Template
	}
	ms, err := s.Sweeps.Members(sw.ID)
	if err != nil {
		log.Printf("sweeps: %v", err)
		return out
	}
	for _, m := range ms {
		j, err := s.Jobs.Get(m.JobID)
		if err != nil {
			continue
		}
		held := j.State == jobs.StateDraft && sw.State != sweeps.StateDraft && m.ApprovedHash != j.ScriptHash
		out.Jobs = append(out.Jobs, memberView{JobID: j.ID, Name: j.Name, Label: sweeps.Label(sw.Params, m.Values), Values: m.Values, State: j.State, Detail: j.Detail, RunID: j.RunID, Held: held, Usage: j.Usage, Request: jobs.RequestedOf(j.Script)})
		out.Counts[j.State]++
		if j.State == jobs.StateDraft && !held {
			out.Pending++
		}
		if held {
			out.WaitingOn = append(out.WaitingOn, j.Name+" was edited after you approved the sweep, so it is held and will not be submitted")
		}
		if j.State == jobs.StateSubmitting {
			out.WaitingOn = append(out.WaitingOn, j.Name+" is submitting: Slurm's answer was not seen; use Check on the host")
		}
	}
	return out
}

func (s *Server) listSweeps(w http.ResponseWriter, r *http.Request) {
	list, err := s.Sweeps.List()
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := make([]sweepOut, 0, len(list))
	for _, sw := range list {
		out = append(out, s.sweepView(sw))
	}
	writeJSON(w, 200, map[string]any{"sweeps": out})
}

func (s *Server) getSweep(w http.ResponseWriter, r *http.Request) {
	if sw, ok := s.sweepFrom(w, r); ok {
		writeJSON(w, 200, s.sweepView(sw))
	}
}

type reviewJob struct {
	JobID      int64             `json:"jobId"`
	Name       string            `json:"name"`
	Label      string            `json:"label"`
	Values     map[string]string `json:"values"`
	ScriptHash string            `json:"scriptHash"`
	Script     string            `json:"script"`
	Diff       []sweeps.DiffLine `json:"diff"`
	Unexpected int               `json:"unexpected"` // differing lines that are not a swept setting, the output folder or the job name
	Requested  jobs.Requested    `json:"requested"`
	// Sizing is what the owner's own ROHSA sizing table suggests for this draft's crop and Gaussian count (null for VGT):
	// a parameter such as N can cross a row of that table, and every draft requests what the base job requested.
	Sizing *jobs.Sizing `json:"sizing"`
}

// pendingDrafts are the sweep's drafts the owner would be approving now, in grid order.
func (s *Server) pendingDrafts(sw sweeps.Sweep) ([]sweeps.Member, []jobs.Job, error) {
	ms, err := s.Sweeps.Members(sw.ID)
	if err != nil {
		return nil, nil, err
	}
	var pm []sweeps.Member
	var pj []jobs.Job
	for _, m := range ms {
		j, err := s.Jobs.Get(m.JobID)
		if err != nil {
			return nil, nil, err
		}
		if j.State == jobs.StateDraft {
			pm, pj = append(pm, m), append(pj, j)
		}
	}
	return pm, pj, nil
}

// sweepHash identifies the exact scripts being approved: the owner names it when they approve, and approval fails if any
// draft changed in between.
func sweepHash(js []jobs.Job) string {
	h := sha256.New()
	for _, j := range js {
		fmt.Fprintf(h, "%d:%s\n", j.ID, j.ScriptHash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reviewSweep shows what approving would send: every script, how it differs from the base's, and what it requests.
func (s *Server) reviewSweep(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	base, err := s.Jobs.Get(sw.BaseJobID)
	if err != nil {
		fail(w, 409, "the base job no longer exists")
		return
	}
	pm, pj, err := s.pendingDrafts(sw)
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	keys := make([]string, 0, len(sw.Params))
	for _, p := range sw.Params {
		if base.Template == "rohsa" {
			keys = append(keys, jobs.ROHSALineKeys(p.Key)...) // the tied "lambda" is three lines of the config
		} else {
			keys = append(keys, p.Key)
		}
	}
	// Drafts are rendered from the base's settings, never from its script text. A base the owner edited by hand (or one drafted
	// by an older template) therefore differs from every draft in the lines that were edited, which no swept setting explains.
	// The drafts are compared with what the base's settings render, so a line is only "other" when it is unexpected in the
	// draft; the base's own edits are counted once, and the review says they are not carried over.
	ref, baseEdited := base.Script, 0
	if rendered, ok := renderedFromSettings(base); ok && rendered != base.Script {
		ref = rendered
		baseEdited = len(sweeps.Diff(rendered, base.Script, nil)) // lines, "-" and "+" together
	}
	list := make([]reviewJob, 0, len(pj))
	scripts := make([]string, 0, len(pj))
	for i, j := range pj {
		d := sweeps.Diff(ref, j.Script, keys)
		list = append(list, reviewJob{JobID: j.ID, Name: j.Name, Label: sweeps.Label(sw.Params, pm[i].Values), Values: pm[i].Values, ScriptHash: j.ScriptHash,
			Script: j.Script, Diff: d, Unexpected: sweeps.Unexpected(d), Requested: jobs.RequestedOf(j.Script), Sizing: rohsaSizingOf(j)})
		scripts = append(scripts, j.Script)
	}
	note := ""
	if base.Template == "rohsa" {
		note = rohsaConcurrencyNote
	}
	writeJSON(w, 200, map[string]any{"sweep": s.sweepView(sw), "baseJobId": base.ID, "baseName": base.Name, "baseScript": base.Script, "baseEditedLines": baseEdited, "keys": keys, "jobs": list,
		"sweepHash": sweepHash(pj), "totals": sumRequested(scripts), "estimate": estimateFrom(base), "maxConcurrency": sweeps.MaxConcurrency,
		"template": base.Template, "defaultConcurrency": sweeps.DefaultConcurrency, "concurrencyNote": note})
}

// renderedFromSettings is the script the base job's own settings render now, with the base's name and output folder: what the
// base would be had nobody edited its text. False for a job with no kept settings or one that no longer renders.
func renderedFromSettings(base jobs.Job) (string, bool) {
	if base.Template == "rohsa" {
		req, ok := base.ROHSARequest()
		if !ok || req.BaseConfigText == "" {
			return "", false
		}
		text, err := jobs.SetROHSAConfig(req.BaseConfigText, req.Set, base.OutputDir)
		if err != nil {
			return "", false
		}
		script, err := jobs.RenderROHSA(jobs.ROHSASettings{Name: base.Name, RepoDir: req.RepoDir, ConfigPath: req.ConfigPath, Config: text, CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account, ExpectCommit: req.ExpectCommit})
		return script, err == nil
	}
	req, ok := base.VGTRequest()
	if !ok || req.BaseConfigText == "" {
		return "", false
	}
	vals := map[string]string{}
	for k, v := range req.Set {
		vals[k] = v
	}
	vals["output_dir"] = base.OutputDir
	text, err := jobs.SetConfigValues(req.BaseConfigText, vals)
	if err != nil {
		return "", false
	}
	script, err := jobs.RenderVGT(jobs.VGTSettings{Name: base.Name, RepoDir: req.RepoDir, Config: text, CPUs: req.CPUs, Mem: req.Mem, Time: req.Time, Partition: req.Partition, Account: req.Account, ExpectCommit: req.ExpectCommit})
	return script, err == nil
}

// rohsaConcurrencyNote is shown with the review of a ROHSA sweep. The default of one job at a time holds for every sweep; for
// ROHSA there is a known reason, which the owner met on the cluster.
const rohsaConcurrencyNote = "ROHSA jobs share one source folder on the host (rohsa/upstream/ROHSA/src). A job rebuilds the binary there when a source is newer, and a rebuild replaces the binary while another job may be starting it; the script's lock keeps two builds apart but not a build from a start. Run one job at a time unless you know the binary is current. Each fit of a large crop also asks for 100G or more."

// rohsaSizingOf is the owner's sizing table applied to a draft's own config (base text with the sweep's settings), or nil for a
// job that is not a ROHSA job or whose config cannot be read.
func rohsaSizingOf(j jobs.Job) *jobs.Sizing {
	req, ok := j.ROHSARequest()
	if !ok {
		return nil
	}
	cfg, err := jobs.ReadROHSAConfig(req.BaseConfigText)
	if err != nil {
		return nil
	}
	for k, v := range jobs.ROHSAExpand(req.Set) { // the draft's own settings win over the base text
		cfg.Values[k] = v
	}
	size, hasCrop := cfg.CropSize, cfg.HasCrop
	if v, ok := cfg.Values["crop_size"]; ok {
		size, hasCrop = atoi(v), true
	}
	n, hasN := cfg.NGauss, cfg.HasNGauss
	if v, ok := cfg.Values["n_gauss"]; ok {
		n, hasN = atoi(strings.SplitN(v, ",", 2)[0]), true
	}
	z := jobs.SuggestROHSASizing(size, n, hasCrop, hasN)
	return &z
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// submitSweep is the owner's go-ahead: it names the hash of the scripts they read, a concurrency limit and what a failure
// does. Nothing is sent from here beyond what the scheduler then sends one job at a time.
func (s *Server) submitSweep(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		SweepHash     string `json:"sweepHash"`
		Concurrency   *int   `json:"concurrency"` // absent means one at a time
		StopOnFailure bool   `json:"stopOnFailure"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	limit := sweeps.DefaultConcurrency
	if in.Concurrency != nil {
		limit = *in.Concurrency
	}
	if limit < 1 || limit > sweeps.MaxConcurrency {
		fail(w, 422, fmt.Sprintf("the concurrency limit is 1 to %d", sweeps.MaxConcurrency))
		return
	}
	s.sweepMu.Lock()
	_, pj, err := s.pendingDrafts(sw)
	if err == nil && len(pj) == 0 {
		s.sweepMu.Unlock()
		fail(w, 409, "this sweep has no draft left to submit")
		return
	}
	if err != nil {
		s.sweepMu.Unlock()
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if in.SweepHash != sweepHash(pj) {
		s.sweepMu.Unlock()
		fail(w, 409, "the scripts changed since you reviewed them; review the sweep again")
		return
	}
	hashes := map[int64]string{}
	for _, j := range pj {
		hashes[j.ID] = j.ScriptHash
	}
	won, err := s.Sweeps.Approve(sw.ID, limit, in.StopOnFailure, hashes)
	s.sweepMu.Unlock()
	if err != nil {
		if errors.Is(err, sweeps.ErrInvalid) {
			fail(w, 422, err.Error())
		} else {
			log.Printf("sweeps: %v", err)
			fail(w, 500, "internal error")
		}
		return
	}
	if !won {
		fail(w, 409, "this sweep is already running")
		return
	}
	s.Audit.RecordCtx(r.Context(), "sweep approved", "sweep", sw.ID, fmt.Sprintf("%d scripts, at most %d at a time, stop on failure %v, hash %s", len(pj), limit, in.StopOnFailure, in.SweepHash[:12]))
	s.kickSweeps()
	sw, _ = s.Sweeps.Get(sw.ID)
	writeJSON(w, 200, s.sweepView(sw))
}

// cancelSweep stops the scheduling: drafts not yet submitted stay drafts. A job already queued or running keeps running
// unless the owner asked for it to be cancelled too.
func (s *Server) cancelSweep(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		CancelRunning bool `json:"cancelRunning"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	s.sweepMu.Lock() // not while the scheduler is between its last check and BeginSubmit
	stopped, err := s.Sweeps.Stop(sw.ID, "Cancelled by you. Jobs not yet submitted stay drafts.")
	s.sweepMu.Unlock()
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	// a sweep whose last job is already submitted is "finished" as far as scheduling goes, but its jobs may still be cancelled
	if !stopped && (!in.CancelRunning || sw.State == sweeps.StateDraft) {
		fail(w, 409, "this sweep is not running")
		return
	}
	s.Audit.RecordCtx(r.Context(), "sweep cancelled", "sweep", sw.ID, fmt.Sprintf("cancel running jobs: %v", in.CancelRunning))
	if in.CancelRunning {
		ms, _ := s.Sweeps.Members(sw.ID)
		for _, m := range ms {
			if j, err := s.Jobs.Get(m.JobID); err == nil && isActiveState(j.State) {
				if h, err := s.HostDB.Get(j.HostID); err == nil {
					if aerr := s.cancelSlurm(r.Context(), j, h); aerr != nil {
						log.Printf("sweeps: cancel job %d: %s", j.ID, aerr.Msg)
					}
				}
			}
		}
	}
	sw, _ = s.Sweeps.Get(sw.ID)
	writeJSON(w, 200, s.sweepView(sw))
}

// ---- the scheduler ----

// kickSweeps runs the scheduler in the background. It is called when a sweep is approved, when one of its jobs reaches an
// end state (the winning transition in applyJobStatus), after a "submitting" job is resolved, and on every watcher pass, so a
// restart or a slow host never leaves a sweep waiting for a click.
func (s *Server) kickSweeps() {
	if s.Sweeps == nil || s.Jobs == nil || s.HostDB == nil || s.Remote == nil {
		return
	}
	s.sweepRuns.Add(1)
	go func() {
		defer s.sweepRuns.Done()
		s.advanceSweeps(context.Background())
	}()
}

// waitSweeps blocks until the scheduler runs started so far have finished (tests, shutdown).
func (s *Server) waitSweeps() { s.sweepRuns.Wait() }

func (s *Server) advanceSweeps(ctx context.Context) {
	if s.Sweeps == nil || s.Jobs == nil || s.HostDB == nil || s.Remote == nil {
		return
	}
	running, err := s.Sweeps.Running()
	if err != nil {
		log.Printf("sweeps: %v", err)
		return
	}
	for _, sw := range running {
		if ctx.Err() != nil {
			return
		}
		s.advanceSweep(ctx, sw.ID)
	}
}

// advanceSweep submits the next drafts of one running sweep while fewer than its limit are in flight. One lock serializes
// every run of the scheduler and every cancel, and the sweep's state is read again under it just before each submission:
// BeginSubmit protects a job from being submitted twice, the lock protects the limit.
//
// A job in submitting (Slurm's answer was not seen) or submitted or running holds a slot; an unconfirmed one is never
// resubmitted and never skipped, because it may be queued. A job that failed or was cancelled frees its slot and nothing is
// retried; with "stop on failure" chosen at approval the sweep stops instead. The cluster's own limit (429) means wait.
func (s *Server) advanceSweep(ctx context.Context, id int64) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	skipped := map[int64]bool{}
	for {
		sw, err := s.Sweeps.Get(id)
		if err != nil || sw.State != sweeps.StateRunning {
			return
		}
		ms, err := s.Sweeps.Members(id)
		if err != nil {
			log.Printf("sweeps: %v", err)
			return
		}
		inflight, failed := 0, ""
		var next *sweeps.Member
		var nextJob jobs.Job
		for i, m := range ms {
			j, err := s.Jobs.Get(m.JobID)
			if err != nil {
				continue
			}
			switch j.State {
			case jobs.StateSubmitting, jobs.StateSubmitted, jobs.StateRunning:
				inflight++
			case jobs.StateFailed, jobs.StateCancelled:
				if failed == "" {
					failed = j.Name + " " + j.State
				}
			case jobs.StateDraft:
				held := m.ApprovedHash != j.ScriptHash // edited after the owner approved it
				if next == nil && !held && !skipped[j.ID] {
					next, nextJob = &ms[i], j
				}
			}
		}
		if sw.StopOnFailure && failed != "" {
			if _, err := s.Sweeps.Stop(id, "Stopped because "+failed+", as you chose. Jobs not yet submitted stay drafts."); err == nil {
				s.Audit.RecordCtx(ctx, "sweep stopped", "sweep", id, failed)
			}
			return
		}
		if next == nil {
			_ = s.Sweeps.Finish(id) // nothing is left to submit; jobs in flight carry on
			return
		}
		if inflight >= sw.Concurrency {
			return
		}
		h, err := s.HostDB.Get(nextJob.HostID)
		if err != nil {
			return
		}
		_, aerr := s.submitDraft(ctx, nextJob, h, next.ApprovedHash, nil)
		switch {
		case aerr == nil:
			s.Audit.RecordCtx(ctx, "sweep job submitted", "sweep", id, fmt.Sprintf("job %d (%s)", nextJob.ID, sweeps.Label(sw.Params, next.Values)))
		case aerr.Code == http.StatusTooManyRequests:
			return // too many jobs queued from here: try again on the next trigger
		case aerr.Code == http.StatusBadGateway:
			s.Audit.RecordCtx(ctx, "sweep job unconfirmed", "sweep", id, fmt.Sprintf("job %d: %s", nextJob.ID, aerr.Msg))
			// the job is now "submitting" and holds a slot; the loop counts it
		default:
			log.Printf("sweeps: job %d not submitted: %s", nextJob.ID, aerr.Msg)
			skipped[nextJob.ID] = true
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// ---- the grid: table rows and plot series ----

func (s *Server) sweepRows(sw sweeps.Sweep) ([]sweeps.Row, error) {
	ms, err := s.Sweeps.Members(sw.ID)
	if err != nil {
		return nil, err
	}
	rows := make([]sweeps.Row, 0, len(ms))
	for _, m := range ms {
		j, err := s.Jobs.Get(m.JobID)
		if err != nil {
			continue
		}
		row := sweeps.Row{JobID: j.ID, Name: j.Name, Label: sweeps.Label(sw.Params, m.Values), Values: m.Values, State: j.State, RunID: j.RunID, Metrics: []sweeps.Metric{}}
		if j.RunID != nil && s.Runs != nil {
			if run, err := s.Runs.Get(*j.RunID); err == nil {
				row.RunName = run.Name
				for _, mt := range run.Metrics {
					row.Metrics = append(row.Metrics, sweeps.Metric{Key: mt.Key, Value: mt.Value, Unit: mt.Unit, Uncertainty: mt.Uncertainty, Definition: mt.Definition})
				}
			} else if !errors.Is(err, runs.ErrNotFound) {
				return nil, err
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// sweepGrid answers with the parameters, one row per job (in parameter order) with the metrics its run declared, the metric
// keys with their units, and, when metric is given, the series for that metric against the parameter x. There is no "best"
// column or label: the values are shown and the researcher judges them (AGENTS.md invariant 9).
func (s *Server) sweepGrid(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	rows, err := s.sweepRows(sw)
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	infos := sweeps.Metrics(rows)
	out := map[string]any{"sweepId": sw.ID, "name": sw.Name, "params": sw.Params, "rows": rows, "metrics": infos, "plot": nil, "plotNote": ""}
	if metric := r.URL.Query().Get("metric"); metric != "" {
		x := r.URL.Query().Get("x")
		if x == "" {
			x = sw.Params[0].Key
		}
		plot, why := sweeps.BuildPlot(sw.Params, rows, metric, x)
		if plot != nil {
			out["plot"] = plot
		} else {
			out["plotNote"] = why
		}
	}
	writeJSON(w, 200, out)
}

// ---- the board ----

// boardFromSweep makes one board whose panels follow each job of the sweep, in parameter order. The label ("N = 12, λ = 10")
// is the owner's: boardView fills it from the sweep, and it is in no note, so no snapshot carries it.
func (s *Server) boardFromSweep(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	if s.Boards == nil {
		fail(w, 404, "boards are not available")
		return
	}
	s.sweepBoardMu.Lock()
	defer s.sweepBoardMu.Unlock()
	if sw.BoardID != nil {
		if _, err := s.Boards.Board(*sw.BoardID); err == nil {
			writeJSON(w, 409, map[string]any{"error": "a board was already made from this sweep", "boardId": *sw.BoardID})
			return
		}
		s.Sweeps.ClearBoard(sw.ID) // that board is gone
	}
	title := "Sweep: " + sw.Name
	b, err := s.Boards.CreateBoard(title)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	ms, err := s.Sweeps.Members(sw.ID)
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	for _, m := range ms {
		if err := s.Boards.AddJobItem(b.ID, m.JobID); err != nil {
			s.boardErr(w, err)
			return
		}
	}
	if _, err := s.Sweeps.SetBoard(sw.ID, b.ID); err != nil {
		log.Printf("sweeps: %v", err)
	}
	s.Audit.RecordCtx(r.Context(), "sweep board made", "sweep", sw.ID, fmt.Sprintf("board %d with %d job panels", b.ID, len(ms)))
	nb, _ := s.boardByID(r.Context(), b.ID, false)
	writeJSON(w, 201, nb)
}

// sweepLabel is what boardView adds to a followed job's panel when the job belongs to a sweep.
func (s *Server) sweepLabel(jobID int64) (label string, sweepID *int64) {
	if s.Sweeps == nil {
		return "", nil
	}
	if sw, m, ok := s.Sweeps.OfJob(jobID); ok {
		id := sw.ID
		return sweeps.Label(sw.Params, m.Values), &id
	}
	return "", nil
}
