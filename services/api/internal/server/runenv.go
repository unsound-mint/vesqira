package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// What a job observed about where it ran (ADR 0054). The job's own script writes vesqira-env.json next to its outputs; the
// API reads it when the run is imported and keeps the facts on the run, each verified or unresolved with its reason.

// readJobEnvironment reads the environment file of a finished VGT or ROHSA job from its host. read is false when the host
// could not be asked (nothing should be replaced then); otherwise facts always has every known key.
func (s *Server) readJobEnvironment(ctx context.Context, j jobs.Job, h hosts.Host) (facts []runs.EnvFact, read bool) {
	unresolved := runs.UnresolvedEnv
	if s.Remote == nil || j.OutputDir == "" || j.SlurmID == "" {
		return unresolved("no environment file is expected for this job"), true
	}
	root, rel, ok := splitRoot(h, j.OutputDir)
	if !ok {
		return unresolved("the output folder is not inside a source root, so no file was read"), true
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	b, exists, err := s.Remote.ReadExisting(ctx, h, root, rel, runs.EnvFile)
	var wr *hosts.WriteRefused
	switch {
	case errors.As(err, &wr) && wr.Reason == "toolarge":
		return unresolved("the environment file is larger than expected, so it was not read"), true
	case errors.As(err, &wr):
		return unresolved("the environment file could not be read safely (a link, or not a plain file)"), true
	case err != nil:
		return nil, false
	case !exists:
		return unresolved("no vesqira-env.json next to the outputs: the job did not get to write it, or it was submitted before environments were recorded"), true
	}
	return runs.ParseEnv(b, j.SlurmID), true
}

// linkJobRun links the run an import made to its job and, for a VGT or ROHSA job, keeps what the job observed. It returns the
// run as stored. An unreachable host leaves an earlier record as it was.
func (s *Server) linkJobRun(ctx context.Context, j jobs.Job, h hosts.Host, run runs.Run) runs.Run {
	_ = s.Jobs.LinkRun(j.ID, run.ID)
	if j.Template != "vgt" && j.Template != "rohsa" {
		return run
	}
	facts, read := s.readJobEnvironment(ctx, j, h)
	if !read {
		return run
	}
	facts = s.inputFingerprint(ctx, j, h, facts)
	if err := s.Runs.SetEnvironment(run.ID, facts); err != nil {
		log.Printf("runs: environment: %v", err)
		return run
	}
	if r, err := s.Runs.Get(run.ID); err == nil {
		return r
	}
	return run
}

// parentComparison says, for a run made with "run again with...", which of the facts both runs observed matched and which
// differed. A fact one side did not observe is unknown, never matched.
type parentComparison struct {
	ParentRunID    int64                `json:"parentRunId"`
	Mode           string               `json:"mode"` // strict | best_effort | adapted, or "" when the draft did not record one
	ParentObserved bool                 `json:"parentObserved"`
	ThisObserved   bool                 `json:"thisObserved"`
	Facts          []runs.EnvDifference `json:"facts"`
}

func observed(facts []runs.EnvFact) bool {
	for _, f := range facts {
		if f.Status == runs.EnvVerified && f.Key != "slurm.job_id" {
			return true
		}
	}
	return false
}

// compareWithParent builds the comparison for the run page, or nil for a run that repeats nothing.
func (s *Server) compareWithParent(r runs.Run, parentID *int64, mode string) *parentComparison {
	if parentID == nil {
		return nil
	}
	p, err := s.Runs.Get(*parentID)
	if err != nil {
		return nil
	}
	return &parentComparison{ParentRunID: p.ID, Mode: mode, ParentObserved: observed(p.Environment), ThisObserved: observed(r.Environment), Facts: runs.CompareEnv(p.Environment, r.Environment)}
}

// setRunControls is PUT /api/runs/{id}/controls: the researcher states how the run was smoothed, how its noise was modelled and
// how a control was built. Blocks that are sent replace the stored ones; a block sent as null is cleared. Every block is stored
// as declared by the researcher, whatever the request says about its basis or author.
func (s *Server) setRunControls(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in runs.Controls
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	if _, err := s.Runs.Get(id); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			fail(w, 404, "run not found")
		} else {
			log.Printf("runs: %v", err)
			fail(w, 500, "internal error")
		}
		return
	}
	if err := in.Declare(time.Now()); err != nil {
		fail(w, 422, err.Error())
		return
	}
	if err := s.Runs.SetControls(id, &in); err != nil {
		log.Printf("runs: controls: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("run controls declared", "run", id, controlsAudit(in))
	run, _ := s.Runs.Get(id)
	writeJSON(w, 200, s.runView(run))
}

func controlsAudit(c runs.Controls) string {
	d := ""
	add := func(n string) {
		if d != "" {
			d += ", "
		}
		d += n
	}
	if c.Smoothing != nil {
		add("smoothing")
	}
	if c.Noise != nil {
		add("noise model")
	}
	if c.Construction != nil {
		add("control construction")
	}
	if d == "" {
		return "cleared"
	}
	return d
}

// inputFingerprint adds the sampled fingerprint (ADR 0053, sampled/1) of the job's input file to what the job observed.
// The job's script cannot compute it (it is a shell fragment on a compute node, and the worker's modules are not at a path it
// knows), so Vesqira measures it at import: from the dataset's own record when that location has one for the same size and
// modification time, otherwise by asking the host's worker. It is kept only when the file's size and time still equal what
// the job observed when it started, which is evidence (not proof: a file can change between samples or keep size and time)
// that these are the bytes the job read. Otherwise the fact is unresolved and says why.
func (s *Server) inputFingerprint(ctx context.Context, j jobs.Job, h hosts.Host, facts []runs.EnvFact) []runs.EnvFact {
	set := func(f runs.EnvFact) []runs.EnvFact {
		for i := range facts {
			if facts[i].Key == f.Key {
				f.Label = facts[i].Label
				facts[i] = f
			}
		}
		return facts
	}
	no := func(why string) []runs.EnvFact {
		return set(runs.EnvFact{Key: "input.sampled_fp", Status: runs.EnvUnresolved, Reason: why})
	}
	sizeS, ok1 := runs.EnvValue(facts, "input.size_bytes")
	mtS, ok2 := runs.EnvValue(facts, "input.mtime_epoch")
	if !ok1 || !ok2 {
		if !observed(facts) { // nothing of this file was trusted (another job's, or none): keep the reason already given
			return facts
		}
		return no("the job did not record the input file's size and time, so a later measurement cannot be tied to it")
	}
	cfg, _ := jobs.EmbeddedConfig(j.Script)
	path, _ := jobs.ConfigPaths(cfg)
	if !jobs.ValidPath(path) {
		return no("the input file is not a plain absolute path")
	}
	size, err1 := strconv.ParseInt(sizeS, 10, 64)
	mt, err2 := strconv.ParseInt(mtS, 10, 64)
	if err1 != nil || err2 != nil {
		return no("the job's size or time was not a number")
	}
	if s.Store != nil {
		if id, found, _ := s.Store.LocationOwner(h.ID, path); found {
			locs, _ := s.Store.Locations(id)
			for _, l := range locs {
				if l.HostID == h.ID && l.Path == path && l.Sampled != nil && l.SizeBytes == size && l.ModifiedAt.Unix() == mt {
					return set(runs.EnvFact{Key: "input.sampled_fp", Status: runs.EnvVerified, Value: l.Sampled.Value})
				}
			}
		}
	}
	fp, gotSize, gotMtimeNS, err := s.hostFingerprint(ctx, h, path)
	if err != nil {
		return no("the input file could not be measured on the host (" + err.Error() + ")")
	}
	if gotSize != size || gotMtimeNS/1e9 != mt {
		return no("the input file has changed since the job started (its size or time differs), so its fingerprint says nothing about what the job read")
	}
	return set(runs.EnvFact{Key: "input.sampled_fp", Status: runs.EnvVerified, Value: fp})
}

// hostFingerprint asks the host's worker for the sampled fingerprint of a file inside the host's source roots.
func (s *Server) hostFingerprint(ctx context.Context, h hosts.Host, path string) (fp string, size, mtimeNS int64, err error) {
	if s.Remote == nil || !h.InRoots(path) {
		return "", 0, 0, errors.New("not available")
	}
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "fingerprint", "path": path})
	if err != nil {
		return "", 0, 0, errors.New("the host's worker did not answer")
	}
	var r struct {
		Sampled string `json:"sampled"`
		Size    int64  `json:"size"`
		MtimeNS int64  `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &r) != nil || r.Sampled == "" {
		return "", 0, 0, errors.New("the host answered unexpectedly")
	}
	return r.Sampled, r.Size, r.MtimeNS, nil
}
