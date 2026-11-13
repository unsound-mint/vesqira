package server

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"slices"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Host parity (ADR 0064): a mask made of a file on a host, and a virtual subset of one written as a file, are written on that host, in its
// derived folder, by the host's own copy of the analysis program (hosts.HostRun, hostrun.py). The bytes never cross to this computer; what
// comes back is the program's summary, and the file's identity is read on the host (its SHA-256 by the same hashing path transfers use, its
// size and modification time by the host's worker).

// hostRunAnalysis runs one bounded write on h with the analysis program's options and reads its reply like runAnalysisProgramCtx: a refusal
// is the program's sentence, the corrections in force for the inputs are sent with it and their echo is required. It returns the outcome and
// the resolved path of the file written on the host.
//
// carry is the masks a subset carries (planCarry): their files are written on the host with the result, all or none, and the plan's paths
// are replaced by the resolved paths the host reports, so recordCarried registers them as masks of that host. Nil for none.
func (s *Server) hostRunAnalysis(ctx context.Context, h hosts.Host, op, name string, argv []string, carry *carryPlan) (analysisOutcome, string, int, string) {
	if s.Remote == nil {
		return analysisOutcome{}, "", 503, "remote hosts are not configured"
	}
	if s.Load != nil { // the same heavy slot as a run here: the API waits for the host, and the person sees why
		_, release, err := s.Load.Acquire(ctx, worker.Heavy, analysisLabel(op))
		if err != nil {
			return analysisOutcome{}, "", 499, "request cancelled"
		}
		defer release()
	}
	corrOpt, corrHashes, corrSnap := s.corrArgs(wsCtx(ctx), h.ID, argv)
	if corrOpt != "" {
		argv = append(argv, corrOpt)
	}
	timeout := s.Analysis.Timeout
	out, err := s.Remote.HostRun(ctx, h, op, name, argv, carry.hostOutputs(), timeout)
	switch {
	case ctx.Err() != nil:
		return analysisOutcome{}, "", 499, "request cancelled"
	case errors.Is(err, hosts.ErrHostRunTimeout):
		return analysisOutcome{}, "", 504, "this took longer on the host than an interactive request may (" + timeout.String() + "), so Vesqira stopped waiting and recorded nothing (the program on the host stops before it puts a file in place once nobody is listening); make it from a smaller window of the file (a virtual subset written as a file), or run the work as a job on the host"
	case errors.Is(err, hosts.ErrInvalid):
		return analysisOutcome{}, "", 409, err.Error()
	case errors.Is(err, hosts.ErrHostRunFailed):
		return analysisOutcome{}, "", 502, err.Error()
	case err != nil:
		s.reach.set(h.ID, false)
		return analysisOutcome{}, "", 502, msgHostUnreachable
	}
	s.reach.set(h.ID, true)
	var res struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Summary  json.RawMessage `json:"summary"`
		Software json.RawMessage `json:"software"`
		Applied  []string        `json:"corrections_applied"`
		File     string          `json:"file"`
		Carried  []string        `json:"carried_files"`
		Missing  string          `json:"missing_file"`
		Extra    []string        `json:"extra_files"`
	}
	if json.Unmarshal(out, &res) != nil {
		return analysisOutcome{}, "", 502, "the host answered unexpectedly"
	}
	if !res.OK {
		if res.Error != "" {
			return analysisOutcome{}, "", 422, res.Error
		}
		return analysisOutcome{}, "", 422, "the program could not be run on this file on the host"
	}
	// the host resolves the derived folder (a root reached through a link resolves elsewhere), so the reply names the resolved file; it
	// must be the name asked for, and the host's worker will refuse it later if it is not inside the roots
	if !hostFileNamed(res.File, name) {
		return analysisOutcome{}, "", 502, "the host answered unexpectedly"
	}
	if !carry.adoptHostFiles(res.Carried, res.Missing, path.Dir(res.File)) {
		return analysisOutcome{}, "", 502, "the host answered unexpectedly about the masks it was asked to carry; no result was recorded"
	}
	for _, hh := range corrHashes {
		if !slices.Contains(res.Applied, hh) {
			return analysisOutcome{}, "", 502, "the program on the host did not apply the researcher's corrections (it may be an older version); no result was recorded"
		}
	}
	return analysisOutcome{Summary: res.Summary, Software: res.Software, Corrections: corrSnap, Files: res.Extra}, res.File, 0, ""
}

// hostFileNamed reports whether p is the resolved absolute path of a file called name.
func hostFileNamed(p, name string) bool {
	return path.Base(p) == name && path.IsAbs(p) && path.Clean(p) == p
}

// hostOutputs is what a subset written on a host asks the host to write besides the result: the carried masks (read on the host, where
// they live) and the result's own no-value mask. The names are the final names of the files in the host's derived folder, a fixed shape
// hostrun.py checks again.
func (p *carryPlan) hostOutputs() *hosts.HostOutputs {
	if p == nil || (len(p.carried) == 0 && p.missingOut == "") {
		return nil
	}
	o := &hosts.HostOutputs{}
	for _, c := range p.carried {
		o.Carry = append(o.Carry, hosts.HostCarry{Name: filepath.Base(c.out), Path: c.mask.Path, Type: c.mask.Type})
	}
	if p.missingOut != "" {
		o.Missing = filepath.Base(p.missingOut)
	}
	return o
}

// adoptHostFiles replaces the plan's local file names by the resolved paths the host reports for them, once each is the file asked for in the
// same folder as the result. It is false when the reply does not match what was asked, and the plan is then left as it was.
func (p *carryPlan) adoptHostFiles(carried []string, missing, folder string) bool {
	if p == nil {
		return len(carried) == 0 && missing == ""
	}
	if len(carried) != len(p.carried) || (p.missingOut != "") != (missing != "") {
		return false
	}
	for i, c := range p.carried {
		if !hostFileNamed(carried[i], filepath.Base(c.out)) || path.Dir(carried[i]) != folder {
			return false
		}
	}
	if missing != "" && (!hostFileNamed(missing, filepath.Base(p.missingOut)) || path.Dir(missing) != folder) {
		return false
	}
	for i := range p.carried {
		p.carried[i].out = carried[i]
	}
	if missing != "" {
		p.missingOut = missing
	}
	return true
}

// hostFileIdentity reads a file's identity on its host: SHA-256 and size by the host's hashing path (hosts.StatPath), and the size and
// modification time in nanoseconds by the host's worker, which is what every later freshness check compares.
func (s *Server) hostFileIdentity(ctx context.Context, h hosts.Host, p string) (digest string, size, mtimeNS int64, err error) {
	st, err := s.Remote.StatPath(ctx, h, path.Dir(p), path.Base(p), true)
	if err != nil {
		return "", 0, 0, err
	}
	if st.Kind != "file" || len(st.SHA) != 64 {
		return "", 0, 0, errors.New("the file written on the host is not a plain file")
	}
	size, mtimeNS, err = s.hostStat(ctx, h, p)
	if err != nil {
		return "", 0, 0, err
	}
	if size != st.Size {
		return "", 0, 0, errors.New("the file on the host changed while its identity was read")
	}
	return st.SHA, size, mtimeNS, nil
}

// hostStat asks the host's worker for a file's size and modification time (nanoseconds). The worker enforces the roots.
func (s *Server) hostStat(ctx context.Context, h hosts.Host, p string) (size, mtimeNS int64, err error) {
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "stat", "path": p})
	if err != nil {
		return 0, 0, err
	}
	var st struct {
		Size    int64 `json:"size"`
		MtimeNS int64 `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &st) != nil {
		return 0, 0, errors.New("the host answered unexpectedly")
	}
	return st.Size, st.MtimeNS, nil
}

// hostOf is the host a dataset or a mask lives on, or a refusal sentence.
func (s *Server) hostOf(id int64) (hosts.Host, int, string) {
	if s.HostDB == nil || s.Remote == nil {
		return hosts.Host{}, 503, "remote hosts are not configured"
	}
	h, err := s.HostDB.Get(id)
	if err != nil {
		return hosts.Host{}, 404, "host not found"
	}
	return h, 0, ""
}
