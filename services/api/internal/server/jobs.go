package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Jobs act on the researcher's own cluster account. The owner's loopback API is the only principal that exists, so a
// submission needs no extra approval, but it always carries the hash of the exact script the owner was shown. When
// another principal can call these routes (reviewers, other users) they must be refused or put behind an explicit
// owner approval first; see ADR 0013.

func (s *Server) jobFrom(w http.ResponseWriter, r *http.Request) (jobs.Job, hosts.Host, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return jobs.Job{}, hosts.Host{}, false
	}
	j, err := s.Jobs.Get(id)
	if errors.Is(err, jobs.ErrNotFound) {
		fail(w, 404, "job not found")
		return j, hosts.Host{}, false
	}
	if err != nil {
		fail(w, 500, "internal error")
		return j, hosts.Host{}, false
	}
	h, err := s.HostDB.Get(j.HostID)
	if err != nil {
		fail(w, 409, "the job's host no longer exists")
		return j, h, false
	}
	return j, h, true
}

func (s *Server) jobErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrInvalid):
		fail(w, 422, err.Error())
	case errors.Is(err, jobs.ErrNotFound):
		fail(w, 404, "job not found")
	default:
		log.Printf("jobs: %v", err)
		fail(w, 500, "internal error")
	}
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	js, err := s.Jobs.List()
	if err != nil {
		s.jobErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": js})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, _, ok := s.jobFrom(w, r)
	if ok {
		writeJSON(w, 200, j)
	}
}

// hostText reads a config (or any small text) from a host so a job can start from a run's own configuration.
func (s *Server) hostText(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, code, msg := s.remoteText(ctx, h, r.URL.Query().Get("path"))
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 200, map[string]string{"text": text})
}

// apiErr is a refusal with the status the handler answers it with.
type apiErr struct {
	Code int
	Msg  string
}

func (e *apiErr) Error() string { return e.Msg }

func (s *Server) failAPI(w http.ResponseWriter, e *apiErr) { fail(w, e.Code, e.Msg) }

// jobAPIErr turns a jobs-package error into the refusal a handler would answer.
func jobAPIErr(err error) *apiErr {
	switch {
	case errors.Is(err, jobs.ErrInvalid):
		return &apiErr{422, err.Error()}
	case errors.Is(err, jobs.ErrNotFound):
		return &apiErr{404, "job not found"}
	}
	log.Printf("jobs: %v", err)
	return &apiErr{500, "internal error"}
}

// vgtInput is everything a VGT draft is rendered from.
type vgtInput struct {
	RepoDir, ConfigPath, ConfigText string
	Set                             map[string]string
	CPUs                            int
	Mem, Time, Partition, Account   string
	// RerunMode and ExpectRev come from "run again with..." (ADR 0054); ExpectRev is set for a strict rerun, and for a relayed run
	// request's draft, which insists on its base run's commit (relayPin).
	RerunMode, ExpectRev string
	// InputRerun marks a draft that runs another run's configuration on a new cube; HashInput makes its script hash the input.
	InputRerun *jobs.InputRerun
	HashInput  bool
}

type builtJob struct{ Script, OutputDir, Request string }

// buildVGT renders a VGT job from its settings: the config text (read from the host unless given), the editable settings
// applied through the whitelist, a fresh output folder inside the host's source roots, the script, and the request kept so a
// rerun can start from an exact copy. Nothing is stored. createJob and sweeps both call it, so a sweep's drafts are rendered by
// exactly the code that renders any other draft.
func (s *Server) buildVGT(ctx context.Context, h hosts.Host, name string, in vgtInput) (builtJob, *apiErr) {
	cfg := in.ConfigText
	if cfg == "" && in.ConfigPath != "" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text, code, msg := s.remoteText(ctx, h, in.ConfigPath)
		cancel()
		if code != 0 {
			return builtJob{}, &apiErr{code, "config: " + msg}
		}
		cfg = text
	}
	if len(h.Roots) == 0 {
		return builtJob{}, &apiErr{422, "the host has no source folder to write results into"}
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	// results go inside the host's source roots so Vesqira can read them afterwards
	out := path.Join(h.Roots[0], "vesqira_runs", name+"-"+hex.EncodeToString(suffix[:]))
	baseCfg := cfg
	vals := map[string]string{}
	for k, v := range in.Set {
		vals[k] = v
	}
	vals["output_dir"] = out
	cfg, err := jobs.SetConfigValues(cfg, vals)
	if err != nil {
		return builtJob{}, jobAPIErr(err)
	}
	script, err := jobs.RenderVGT(jobs.VGTSettings{Name: name, RepoDir: in.RepoDir, Config: cfg, CPUs: in.CPUs, Mem: in.Mem, Time: in.Time, Partition: in.Partition, Account: in.Account, ExpectCommit: in.ExpectRev, HashInput: in.HashInput || in.InputRerun != nil})
	if err != nil {
		return builtJob{}, jobAPIErr(err)
	}
	// The base text and the overrides are kept apart so a rerun can change one setting on the same base, without
	// reading a file that may have changed on the host since.
	rq, _ := json.Marshal(jobs.VGTRequest{RepoDir: in.RepoDir, ConfigPath: in.ConfigPath, BaseConfigText: baseCfg, Set: in.Set,
		CPUs: in.CPUs, Mem: in.Mem, Time: in.Time, Partition: in.Partition, Account: in.Account, RerunMode: in.RerunMode, ExpectCommit: in.ExpectRev, InputRerun: in.InputRerun, HashInput: in.HashInput || in.InputRerun != nil})
	if !h.InRoots(out) {
		return builtJob{}, &apiErr{422, "the output folder must be inside a source root of the host"}
	}
	return builtJob{Script: script, OutputDir: out, Request: string(rq)}, nil
}

// rohsaInput is everything a ROHSA draft is rendered from.
type rohsaInput struct {
	RepoDir, ConfigPath, ConfigText string
	Set                             map[string]string
	CPUs                            int
	Mem, Time, Partition, Account   string
	RerunMode, ExpectRev            string
	InputRerun                      *jobs.InputRerun
	HashInput                       bool
}

// buildROHSA renders a ROHSA job the way buildVGT renders a VGT one: the config text (read from the host unless given), the
// editable settings applied through the ROHSA whitelist, a fresh output folder inside the host's source roots that Vesqira
// sets (so a repeated run never writes over an earlier one), the script with the config embedded, and the request kept so a
// rerun or a sweep can start from an exact copy. Nothing is stored.
func (s *Server) buildROHSA(ctx context.Context, h hosts.Host, name string, in rohsaInput) (builtJob, *apiErr) {
	cfg := in.ConfigText
	if cfg == "" && in.ConfigPath != "" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text, code, msg := s.remoteText(ctx, h, in.ConfigPath)
		cancel()
		if code != 0 {
			return builtJob{}, &apiErr{code, "config: " + msg}
		}
		cfg = text
	}
	if cfg == "" {
		return builtJob{}, &apiErr{422, "a ROHSA job starts from a config: give the config's path on the host"}
	}
	if len(h.Roots) == 0 {
		return builtJob{}, &apiErr{422, "the host has no source folder to write results into"}
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	out := path.Join(h.Roots[0], "vesqira_runs", name+"-"+hex.EncodeToString(suffix[:]))
	if !h.InRoots(out) {
		return builtJob{}, &apiErr{422, "the output folder must be inside a source root of the host"}
	}
	text, err := jobs.SetROHSAConfig(cfg, in.Set, out)
	if err != nil {
		return builtJob{}, jobAPIErr(err)
	}
	script, err := jobs.RenderROHSA(jobs.ROHSASettings{Name: name, RepoDir: in.RepoDir, ConfigPath: in.ConfigPath, Config: text, CPUs: in.CPUs, Mem: in.Mem, Time: in.Time, Partition: in.Partition, Account: in.Account, ExpectCommit: in.ExpectRev, HashInput: in.HashInput || in.InputRerun != nil})
	if err != nil {
		return builtJob{}, jobAPIErr(err)
	}
	rq, _ := json.Marshal(jobs.VGTRequest{RepoDir: in.RepoDir, ConfigPath: in.ConfigPath, BaseConfigText: cfg, Set: in.Set,
		CPUs: in.CPUs, Mem: in.Mem, Time: in.Time, Partition: in.Partition, Account: in.Account, RerunMode: in.RerunMode, ExpectCommit: in.ExpectRev, InputRerun: in.InputRerun, HashInput: in.HashInput || in.InputRerun != nil})
	return builtJob{Script: script, OutputDir: out, Request: string(rq)}, nil
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HostID   int64  `json:"hostId"`
		Template string `json:"template"`
		Name     string `json:"name"`
		Script   string `json:"script"` // custom template
		Note     string `json:"note"`   // why the owner makes this job
		// AutoImport: import the results as a run by itself when the job completes. Default: on for VGT, off otherwise.
		AutoImport *bool `json:"autoImport"`
		// ParentRunID names the run this draft repeats with changes ("run again with...").
		ParentRunID *int64 `json:"parentRunId"`
		// RerunMode is strict, best_effort or adapted (the default) for a draft that has a ParentRunID (ADR 0054).
		RerunMode string `json:"rerunMode"`
		VGT       struct {
			RepoDir    string            `json:"repoDir"`
			ConfigPath string            `json:"configPath"` // a config on the host, inside its roots
			ConfigText string            `json:"configText"`
			Set        map[string]string `json:"set"`
			CPUs       int               `json:"cpus"`
			Mem        string            `json:"mem"`
			Time       string            `json:"time"`
			Partition  string            `json:"partition"`
			Account    string            `json:"account"`
		} `json:"vgt"`
		ROHSA struct {
			RepoDir    string            `json:"repoDir"`
			ConfigPath string            `json:"configPath"`
			ConfigText string            `json:"configText"`
			Set        map[string]string `json:"set"`
			CPUs       int               `json:"cpus"`
			Mem        string            `json:"mem"`
			Time       string            `json:"time"`
			Partition  string            `json:"partition"`
			Account    string            `json:"account"`
		} `json:"rohsa"`
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	h, err := s.HostDB.Get(in.HostID)
	if err != nil {
		fail(w, 404, "host not found")
		return
	}
	if utf8.RuneCountInString(in.Note) > jobs.MaxNote {
		fail(w, 422, fmt.Sprintf("the note is at most %d characters", jobs.MaxNote))
		return
	}
	job := jobs.Job{HostID: h.ID, Name: in.Name, Template: in.Template, Note: in.Note, AutoImport: autoImports(in.Template)}
	if in.AutoImport != nil {
		if *in.AutoImport && !autoImports(in.Template) {
			fail(w, 422, "results are imported automatically for VGT and ROHSA jobs only")
			return
		}
		job.AutoImport = *in.AutoImport
	}
	if in.ParentRunID != nil {
		if s.Runs == nil {
			fail(w, 422, "the run this job repeats does not exist")
			return
		}
		if _, err := s.Runs.Get(wsOf(r), *in.ParentRunID); err != nil {
			if errors.Is(err, runs.ErrNotFound) {
				fail(w, 422, "the run this job repeats does not exist")
			} else {
				log.Printf("jobs: %v", err)
				fail(w, 500, "internal error")
			}
			return
		}
		job.ParentRunID = in.ParentRunID
	}
	rerunMode, expectRev, aerr := s.rerunFor(wsOf(r), in.Template, in.ParentRunID, in.RerunMode)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	switch in.Template {
	case "custom":
		job.Script = in.Script
	case "vgt":
		v, aerr := s.buildVGT(r.Context(), h, in.Name, vgtInput{RepoDir: in.VGT.RepoDir, ConfigPath: in.VGT.ConfigPath, ConfigText: in.VGT.ConfigText, Set: in.VGT.Set,
			CPUs: in.VGT.CPUs, Mem: in.VGT.Mem, Time: in.VGT.Time, Partition: in.VGT.Partition, Account: in.VGT.Account, RerunMode: rerunMode, ExpectRev: expectRev})
		if aerr != nil {
			s.failAPI(w, aerr)
			return
		}
		job.Script, job.OutputDir, job.Request = v.Script, v.OutputDir, v.Request
	case "rohsa":
		v, aerr := s.buildROHSA(r.Context(), h, in.Name, rohsaInput{RepoDir: in.ROHSA.RepoDir, ConfigPath: in.ROHSA.ConfigPath, ConfigText: in.ROHSA.ConfigText, Set: in.ROHSA.Set,
			CPUs: in.ROHSA.CPUs, Mem: in.ROHSA.Mem, Time: in.ROHSA.Time, Partition: in.ROHSA.Partition, Account: in.ROHSA.Account, RerunMode: rerunMode, ExpectRev: expectRev})
		if aerr != nil {
			s.failAPI(w, aerr)
			return
		}
		job.Script, job.OutputDir, job.Request = v.Script, v.OutputDir, v.Request
	default:
		fail(w, 400, "template must be vgt, rohsa or custom")
		return
	}
	if aerr := s.refuseStrictSettings(job); aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	created, err := s.Jobs.CreateDraft(job)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	writeJSON(w, 201, created)
}

func (s *Server) editJobScript(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Script string `json:"script"`
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	if s.relayedDraft(id) && s.Sweeps != nil {
		if _, _, inSweep := s.Sweeps.OfJob(id); inSweep {
			// ADR 0066 section 3: a relayed sweep is approved once over drafts rendered from the base and never edited by hand
			fail(w, 409, "the drafts of a requested sweep are not edited by hand; draft the request again with other values, or decline it")
			return
		}
	}
	if err := s.Jobs.SetScript(id, in.Script); err != nil {
		s.jobErr(w, err)
		return
	}
	j, _ := s.Jobs.Get(id)
	writeJSON(w, 200, j)
}

// submitJob sends exactly the stored script, and only if the caller names its hash: what was shown is what runs.
func (s *Server) submitJob(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		ScriptHash string `json:"scriptHash"`
		// AutoImport, when present, sets the job's option before it is submitted (the draft's own value otherwise).
		AutoImport *bool `json:"autoImport"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if s.relayedDraft(j.ID) {
		fail(w, 409, "this draft was rendered for a run request: approve it from Run requests, which claims it on the group server first, or keep it as your own job there")
		return
	}
	out, aerr := s.submitDraft(r.Context(), j, h, in.ScriptHash, in.AutoImport)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	writeJSON(w, 200, out)
}

// submitDraft is the one way a draft reaches Slurm, for the Submit button and for a sweep's scheduler alike. It sends the
// stored script only if scriptHash is its hash, records the attempt before sbatch runs (BeginSubmit has one winner, so a
// double click, a retry or a racing scheduler cannot submit one job twice), and never retries after an unconfirmed answer.
func (s *Server) submitDraft(ctx context.Context, j jobs.Job, h hosts.Host, scriptHash string, autoImport *bool) (jobs.Job, *apiErr) {
	if j.State != jobs.StateDraft {
		return j, &apiErr{409, "only a draft can be submitted"}
	}
	if autoImport != nil && *autoImport && !autoImports(j.Template) {
		return j, &apiErr{422, "results are imported automatically for VGT and ROHSA jobs only"}
	}
	if scriptHash != j.ScriptHash {
		return j, &apiErr{409, "the script changed since you reviewed it; review the current script and submit again"}
	}
	if n, err := s.Jobs.Active(); err == nil && n >= jobs.MaxActive {
		return j, &apiErr{429, "too many jobs are already queued or running from here"}
	}
	cmd, err := jobs.SubmitCommand(j.WorkDir)
	if err != nil {
		return j, jobAPIErr(err)
	}
	// A strict rerun is refused here, before anything is recorded or sent, when the code folder is not the parent's.
	rerunNote, aerr := s.gateRerun(ctx, j, h)
	if aerr != nil {
		return j, aerr
	}
	if autoImport != nil && *autoImport != j.AutoImport { // after every refusal above: a refused submit changes nothing
		if err := s.Jobs.SetAutoImport(j.ID, *autoImport); err != nil {
			return j, jobAPIErr(err)
		}
	}
	// Record the attempt first: from here on the record says "submitting" until Slurm's answer is seen, so a timeout
	// or a second click cannot submit the same job twice.
	if won, err := s.Jobs.BeginSubmit(j.ID, j.ScriptHash); err != nil {
		return j, jobAPIErr(err)
	} else if !won {
		return j, &apiErr{409, "this job is already being submitted, or its script changed since you reviewed it"}
	}
	out, err := s.Remote.RunShell(ctx, h, cmd, []byte(j.Script), 45*time.Second)
	answered := time.Now()
	var id string
	if err == nil {
		id, err = jobs.ParseSubmit(string(out))
	}
	if err != nil {
		_ = s.Jobs.NoteSubmitting(j.ID, "Slurm's answer was not seen ("+err.Error()+"). The job may already be queued: use Check on the host.")
		return j, &apiErr{502, "the host did not confirm the submission (" + err.Error() + "). The job may already be queued, so it is not submitted again: use Check on the host."}
	}
	if err := s.Jobs.MarkSubmitted(j.ID, id); err != nil {
		return j, jobAPIErr(err)
	}
	detail := "Slurm job " + id + ", script sha256 " + j.ScriptHash[:12]
	if rerunNote != "" {
		detail += "; " + rerunNote
	}
	s.Audit.RecordCtx(ctx, "job submitted", "job", j.ID, detail)
	j, _ = s.Jobs.Get(j.ID)
	// Settings are known now. The record is written in the background (the answer does not wait for ssh), and only once the
	// job has created its output folder; a refresh completes it and adds the end state.
	s.recordRunAsync(j.ID, h, jobs.SubmittedEvidence(id, answered))
	return j, nil
}

// reconcile looks for the Slurm job behind a record stuck in "submitting". found is meaningful only when err is nil:
// a failed lookup says nothing about whether the job exists.
func (s *Server) reconcile(ctx context.Context, j jobs.Job, h hosts.Host) (id string, found bool, err error) {
	cmd, err := jobs.LookupCommand(j.WorkDir)
	if err != nil {
		return "", false, err
	}
	out, err := s.Remote.RunShell(ctx, h, cmd, nil, 30*time.Second)
	if err != nil {
		return "", false, err
	}
	id, found = jobs.ParseLookup(string(out), j.WorkDir)
	return id, found, nil
}

// checkSubmitting resolves an unconfirmed submission when the host can answer, and says what it found.
func (s *Server) checkSubmitting(ctx context.Context, j jobs.Job, h hosts.Host, reopen bool) error {
	id, found, err := s.reconcile(ctx, j, h)
	if err != nil {
		return err
	}
	defer s.kickSweeps() // an unconfirmed submission was a slot a sweep could not use; it is settled now
	seen := time.Now()
	switch {
	case found:
		s.Audit.RecordCtx(ctx, "job found on the host", "job", j.ID, "Slurm job "+id)
		if err := s.Jobs.MarkSubmitted(j.ID, id); err != nil {
			return err
		}
		s.recordRunAsync(j.ID, h, jobs.FoundEvidence(id, seen))
		return nil
	case reopen:
		s.Audit.RecordCtx(ctx, "job reopened as draft", "job", j.ID, "the host holds no such job")
		return s.Jobs.Reopen(j.ID)
	default:
		return s.Jobs.NoteSubmitting(j.ID, "Slurm does not list this job (checked "+time.Now().Format("15:04")+"). If you are sure it was not submitted, reopen it as a draft.")
	}
}

func (s *Server) reopenJob(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.State != jobs.StateSubmitting {
		fail(w, 409, "only a submission without an answer can be reopened")
		return
	}
	if err := s.checkSubmitting(r.Context(), j, h, true); err != nil {
		fail(w, 502, "the host could not be checked ("+err.Error()+"); nothing was changed")
		return
	}
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
}

func (s *Server) refreshJob(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.State == jobs.StateSubmitting {
		if err := s.checkSubmitting(r.Context(), j, h, false); err != nil {
			fail(w, 502, "the host could not be checked ("+err.Error()+")")
			return
		}
		j, _ = s.Jobs.Get(j.ID)
		writeJSON(w, 200, j)
		return
	}
	if j.SlurmID != "" && (j.State == jobs.StateSubmitted || j.State == jobs.StateRunning) {
		if _, err := jobs.StatusCommand(j.SlurmID); err != nil {
			s.jobErr(w, err)
			return
		}
		st, err := s.hostStatus(r.Context(), h, []string{j.SlurmID}, time.Now())
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		s.applyJobStatus(j, h, st[j.SlurmID], time.Now())
	} else if j.RecordOwed && j.SlurmID != "" && isTerminal(j.State) {
		s.retryOwedRecord(j, h)
	}
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if aerr := s.cancelSlurm(r.Context(), j, h); aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
}

// cancelSlurm asks the host to cancel a queued or running job and notes it.
func (s *Server) cancelSlurm(ctx context.Context, j jobs.Job, h hosts.Host) *apiErr {
	if j.SlurmID == "" || (j.State != jobs.StateSubmitted && j.State != jobs.StateRunning) {
		return &apiErr{409, "only a queued or running job can be cancelled"}
	}
	cmd, err := jobs.CancelCommand(j.SlurmID)
	if err != nil {
		return jobAPIErr(err)
	}
	if _, err := s.Remote.RunShell(ctx, h, cmd, nil, 30*time.Second); err != nil {
		return &apiErr{502, err.Error()}
	}
	s.Audit.RecordCtx(ctx, "job cancel requested", "job", j.ID, "Slurm job "+j.SlurmID)
	_, _ = s.Jobs.ApplyStatus(j.ID, j.State, "cancel requested") // guarded: never over an end state a refresh just read
	return nil
}

func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.SlurmID == "" {
		writeJSON(w, 200, map[string]string{"log": ""})
		return
	}
	cmd, err := jobs.LogCommand(j.WorkDir, j.SlurmID)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	out, err := s.Remote.RunShell(r.Context(), h, cmd, nil, 30*time.Second)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"log": string(out)})
}

type hostRef struct{ hosts.Host }

// autoImports says which templates can have their results imported by themselves when the job completes (ADR 0043): the two
// whose output folder holds a run Vesqira knows how to read, VGT (run_metadata.toml) and ROHSA (the fit, ADR 0046).
func autoImports(template string) bool { return template == "vgt" || template == "rohsa" }

// registerJobOutputs imports the finished job's run folder from its host and links the Run to the job.
func (s *Server) registerJobOutputs(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.OutputDir == "" {
		fail(w, 409, "this job has no known output folder")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if j.Template == "rohsa" {
		rctx, rcancel := context.WithTimeout(r.Context(), remoteImportTimeout) // the same bound as the automatic import
		defer rcancel()
		run, code, msg := s.importROHSAJob(rctx, j, h)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		run = s.linkJobRun(rctx, j, h, run)
		writeJSON(w, 201, s.runView(wsOf(r), run))
		return
	}
	if j.Template == "analysis" {
		d, note, code, msg := s.registerAnalysisJob(ctx, j, hostRef{h})
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if note != "" {
			writeJSON(w, 201, struct {
				catalog.Dataset
				MasksNote string `json:"masksNote"`
			}{d, note})
			return
		}
		writeJSON(w, 201, d)
		return
	}
	run, code, msg := s.importFrom(ctx, remoteRun{s: s, h: h, dir: j.OutputDir})
	if code != 0 {
		fail(w, code, msg)
		return
	}
	run = s.linkJobRun(ctx, j, h, run)
	writeJSON(w, 201, s.runView(wsOf(r), run))
}
