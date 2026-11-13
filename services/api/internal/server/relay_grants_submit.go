package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The last step of a standing approval: submitting the draft whose hash was checked, reserved and claimed (ADR 0074 amendment of
// 2026-11-14). Nothing here widens what was checked; every refusal before sbatch frees the jobs-at-once slot.

// maxGrantSubmitTries bounds how many passes a submission refused for "too many active jobs" is tried again before the request is
// left for the key holder.
const maxGrantSubmitTries = 5

var triesRe = regexp.MustCompile(`submit attempt (\d+) of`)

func grantTries(note string) int {
	if m := triesRe.FindStringSubmatch(note); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// submitGranted submits the draft of a request that took its slot and was claimed on the group server. reserved is the script hash
// that was checked, reserved, claimed and audited; the draft is sent only while it still has that hash, and only while the grant
// is not revoked, which is read again here because the pass can be long and revoking does not wait for it.
func (s *Server) submitGranted(ctx context.Context, c *groupClient, l outbox.Link, m relay.Mirrored, g relay.Grant, reserved string, tries int) {
	end := func(note string) { // nothing reached Slurm: the slot goes back, the claimed job stays counted
		_ = s.Relayed.EndUseOf(m.ID)
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateStopped, note)
	}
	switch {
	case m.JobID == nil:
		end("Claimed under the standing approval, but the draft is gone; nothing was submitted.")
	default:
		s.submitGrantedJob(ctx, m, g, reserved, tries, end)
	}
	m, _ = s.Relayed.Get(m.ID)
	s.reportOne(c, l, m)
}

func (s *Server) submitGrantedJob(ctx context.Context, m relay.Mirrored, g relay.Grant, reserved string, tries int, end func(string)) {
	j, err := s.Jobs.Get(*m.JobID)
	switch {
	case err != nil:
		end("Claimed under the standing approval, but the draft is gone; nothing was submitted.")
		return
	case j.State != jobs.StateDraft:
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "") // already on its way: the pass settles the use when the job ends
		return
	case j.ScriptHash != reserved:
		end("The draft changed after it was checked and claimed, so nothing was submitted. The claim names the checked script and cannot be reused: keep the draft as your own job (detach) or decline the request.")
		return
	}
	if cur, err := s.Relayed.GrantByID(g.ID); err != nil || cur.RevokedAt != nil {
		end("The standing approval was revoked before the job was submitted; nothing was submitted.")
		return
	}
	h, err := s.HostDB.Get(j.HostID)
	if err != nil {
		end("Claimed, but the host no longer exists; nothing was submitted.")
		return
	}
	t := true
	_, aerr := s.submitDraft(localCtx(ctx), j, h, reserved, &t)
	if aerr == nil {
		if tries > 0 { // a retry: the request was left pending with its attempt count
			_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "")
		}
		return
	}
	if after, err := s.Jobs.Get(j.ID); err == nil && after.State != jobs.StateDraft {
		// sbatch was reached (the host did not confirm): the job may be queued, so the use stays open until the job ends
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStateUsed, "Claimed under the standing approval, but the submission failed: "+aerr.Msg+" Approve it again yourself.")
		return
	}
	if aerr.Code == 429 && tries+1 < maxGrantSubmitTries {
		_ = s.Relayed.SetGrantState(m.ID, g.ID, relay.GrantStatePending, fmt.Sprintf("Claimed, but %s; submit attempt %d of %d, tried again at the next check.", aerr.Msg, tries+1, maxGrantSubmitTries))
		return
	}
	end("Claimed under the standing approval, but the submission was refused before it reached Slurm: " + aerr.Msg + ". Approve it again yourself.")
}

// retryGrantSubmits tries again the submissions that were refused for too many active jobs.
func (s *Server) retryGrantSubmits(ctx context.Context, c *groupClient, l outbox.Link) {
	ms, err := s.Relayed.Resubmits(l.Origin)
	if err != nil {
		return
	}
	for i, m := range ms {
		if i == maxTellsPerPass || ctx.Err() != nil {
			return
		}
		g, err := s.Relayed.GrantByID(m.GrantID)
		if err != nil || m.ScriptHash == "" {
			_ = s.Relayed.EndUseOf(m.ID)
			_ = s.Relayed.SetGrantState(m.ID, m.GrantID, relay.GrantStateStopped, "The standing approval is gone; nothing was submitted.")
			continue
		}
		s.submitGranted(ctx, c, l, m, g, m.ScriptHash, grantTries(m.GrantNote))
	}
}

// ---- the host, asked during a pass ----

var errHostDown = errors.New("the host did not answer earlier in this pass")

// grantHostDo asks the host's worker, giving up after the timeout. A host that did not answer (as opposed to refusing) is marked
// down for the rest of the pass, so one unreachable host costs one timeout, not one for each request.
func (s *Server) grantHostDo(ctx context.Context, h hosts.Host, req map[string]any, timeout time.Duration) (worker.Reply, error) {
	if s.grantDown[h.ID] {
		return worker.Reply{}, errHostDown
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rep, err := s.Remote.Do(ctx, h, req)
	var re *worker.RequestError
	if err != nil && !errors.As(err, &re) && s.grantDown != nil {
		s.grantDown[h.ID] = true
	}
	return rep, err
}

// juliaDepotName is the depot the shipped templates give a run inside its code folder (JULIA_DEPOT_PATH in run_profiles.json).
const juliaDepotName = ".julia-vesqira"

// codeFolderRefusal asks the host who can write the code folders and the Julia depot inside them. A person who can write there
// can plant code the key holder's account then runs, which no check of the script sees. It returns the sentence naming the path
// and its mode when one is writable by group or others; answered is false when the host did not.
func (s *Server) codeFolderRefusal(ctx context.Context, h hosts.Host, repoDirs []string) (why string, answered bool) {
	if len(repoDirs) == 0 {
		return "", true
	}
	if s.Remote == nil {
		return "", false
	}
	var ps []string
	for _, d := range repoDirs {
		ps = append(ps, d, path.Join(d, juliaDepotName))
	}
	rep, err := s.grantHostDo(ctx, h, map[string]any{"op": "modes", "paths": ps}, 60*time.Second)
	if err != nil {
		var re *worker.RequestError
		if errors.As(err, &re) {
			return fmt.Sprintf("the host %s could not say who can write the code folder (it said: %s)", h.Name, re.Message), true
		}
		return "", false
	}
	var out struct {
		Paths []struct {
			Path       string `json:"path"`
			Exists     bool   `json:"exists"`
			Mode       string `json:"mode"`
			GroupWrite bool   `json:"group_write"`
			OtherWrite bool   `json:"other_write"`
		} `json:"paths"`
	}
	if json.Unmarshal(rep.Head, &out) != nil || len(out.Paths) != len(ps) {
		return "the host answered unexpectedly when asked who can write the code folder", true
	}
	for _, p := range out.Paths {
		switch {
		case p.GroupWrite && p.OtherWrite:
			return fmt.Sprintf("%s on %s is writable by group and others (mode %s)", p.Path, h.Name, p.Mode), true
		case p.GroupWrite:
			return fmt.Sprintf("%s on %s is writable by its group (mode %s)", p.Path, h.Name, p.Mode), true
		case p.OtherWrite:
			return fmt.Sprintf("%s on %s is writable by others (mode %s)", p.Path, h.Name, p.Mode), true
		}
	}
	return "", true
}

// grantRepoDirs are the code folders the key holder's own runs of a template on a host used (at most four): the folders a request
// under a new grant can start from.
func (s *Server) grantRepoDirs(hostID int64, template string) []string {
	if s.Jobs == nil {
		return nil
	}
	js, err := s.Jobs.List()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, j := range js {
		if j.HostID != hostID || j.Template != template || len(out) == 4 {
			continue
		}
		if req, ok := j.ConfigRequest(); ok && req.RepoDir != "" && jobs.ValidPath(req.RepoDir) && !seen[req.RepoDir] {
			seen[req.RepoDir] = true
			out = append(out, req.RepoDir)
		}
	}
	return out
}
