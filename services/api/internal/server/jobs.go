package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"path"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
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

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HostID   int64  `json:"hostId"`
		Template string `json:"template"`
		Name     string `json:"name"`
		Script   string `json:"script"` // custom template
		VGT      struct {
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
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	h, err := s.HostDB.Get(in.HostID)
	if err != nil {
		fail(w, 404, "host not found")
		return
	}
	job := jobs.Job{HostID: h.ID, Name: in.Name, Template: in.Template}
	switch in.Template {
	case "custom":
		job.Script = in.Script
	case "vgt":
		cfg := in.VGT.ConfigText
		if cfg == "" && in.VGT.ConfigPath != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			text, code, msg := s.remoteText(ctx, h, in.VGT.ConfigPath)
			cancel()
			if code != 0 {
				fail(w, code, "config: "+msg)
				return
			}
			cfg = text
		}
		var suffix [4]byte
		_, _ = rand.Read(suffix[:])
		// results go inside the host's source roots so Vesqira can read them afterwards
		out := path.Join(h.Roots[0], "vesqira_runs", in.Name+"-"+hex.EncodeToString(suffix[:]))
		vals := map[string]string{}
		for k, v := range in.VGT.Set {
			vals[k] = v
		}
		vals["output_dir"] = out
		cfg, err = jobs.SetConfigValues(cfg, vals)
		if err != nil {
			s.jobErr(w, err)
			return
		}
		script, err := jobs.RenderVGT(jobs.VGTSettings{Name: in.Name, RepoDir: in.VGT.RepoDir, Config: cfg, CPUs: in.VGT.CPUs, Mem: in.VGT.Mem, Time: in.VGT.Time, Partition: in.VGT.Partition, Account: in.VGT.Account})
		if err != nil {
			s.jobErr(w, err)
			return
		}
		job.Script, job.OutputDir = script, out
		if !h.InRoots(out) {
			fail(w, 422, "the output folder must be inside a source root of the host")
			return
		}
	default:
		fail(w, 400, "template must be vgt or custom")
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
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if j.State != jobs.StateDraft {
		fail(w, 409, "only a draft can be submitted")
		return
	}
	if in.ScriptHash != j.ScriptHash {
		fail(w, 409, "the script changed since you reviewed it; review the current script and submit again")
		return
	}
	if n, err := s.Jobs.Active(); err == nil && n >= jobs.MaxActive {
		fail(w, 429, "too many jobs are already queued or running from here")
		return
	}
	cmd, err := jobs.SubmitCommand(j.WorkDir)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	// Record the attempt first: from here on the record says "submitting" until Slurm's answer is seen, so a timeout
	// or a second click cannot submit the same job twice.
	if won, err := s.Jobs.BeginSubmit(j.ID); err != nil {
		s.jobErr(w, err)
		return
	} else if !won {
		fail(w, 409, "this job is already being submitted")
		return
	}
	out, err := s.Remote.RunShell(r.Context(), h, cmd, []byte(j.Script), 45*time.Second)
	var id string
	if err == nil {
		id, err = jobs.ParseSubmit(string(out))
	}
	if err != nil {
		_ = s.Jobs.NoteSubmitting(j.ID, "Slurm's answer was not seen ("+err.Error()+"). The job may already be queued: use Check on the host.")
		fail(w, 502, "the host did not confirm the submission ("+err.Error()+"). The job may already be queued, so it is not submitted again: use Check on the host.")
		return
	}
	if err := s.Jobs.MarkSubmitted(j.ID, id); err != nil {
		s.jobErr(w, err)
		return
	}
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
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
	switch {
	case found:
		return s.Jobs.MarkSubmitted(j.ID, id)
	case reopen:
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
		cmd, err := jobs.StatusCommand(j.SlurmID)
		if err != nil {
			s.jobErr(w, err)
			return
		}
		out, err := s.Remote.RunShell(r.Context(), h, cmd, nil, 30*time.Second)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		if state, detail := jobs.ParseStatus(string(out)); state != "" {
			_ = s.Jobs.SetState(j.ID, state, detail)
		} else {
			_ = s.Jobs.SetState(j.ID, j.State, detail)
		}
	}
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	if j.SlurmID == "" || (j.State != jobs.StateSubmitted && j.State != jobs.StateRunning) {
		fail(w, 409, "only a queued or running job can be cancelled")
		return
	}
	cmd, err := jobs.CancelCommand(j.SlurmID)
	if err != nil {
		s.jobErr(w, err)
		return
	}
	if _, err := s.Remote.RunShell(r.Context(), h, cmd, nil, 30*time.Second); err != nil {
		fail(w, 502, err.Error())
		return
	}
	_ = s.Jobs.SetState(j.ID, j.State, "cancel requested")
	j, _ = s.Jobs.Get(j.ID)
	writeJSON(w, 200, j)
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
	if j.Template == "analysis" {
		d, code, msg := s.registerAnalysisJob(ctx, j, hostRef{h})
		if code != 0 {
			fail(w, code, msg)
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
	_ = s.Jobs.LinkRun(j.ID, run.ID)
	writeJSON(w, 201, s.runView(run))
}
