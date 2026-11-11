package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Rerun modes (ADR 0054). A draft made with "run again with..." says how it relates to the run it repeats:
//   - strict: the same code revision, no tracked change, the same Manifest.toml and input file as that run observed. It is not
//     submitted when the code folder on the host is not exactly that, and the job stops itself if the folder moves while it waits.
//   - best_effort: the code as it is now; every difference from what the parent observed is shown before submitting.
//   - adapted: changed settings (the earlier behaviour); the settings that differ from the parent are marked.
//
// None of them claims the result will be the same: that depends on things nobody recorded (see the run page).
const (
	rerunStrict   = "strict"
	rerunBest     = "best_effort"
	rerunAdapted  = "adapted"
	hostCheckTime = 45 * time.Second
)

func validRerunMode(m string) bool { return m == rerunStrict || m == rerunBest || m == rerunAdapted }

// baseline is what is known about the code and environment of the run being repeated.
type baseline struct {
	Env      []runs.EnvFact
	Declared string // the revision the run's own metadata declares, without a -dirty suffix
	DeclDirt bool
	Run      runs.Run
}

func (s *Server) parentBaseline(id int64) (baseline, error) {
	r, err := s.Runs.Get(id)
	if err != nil {
		return baseline{}, err
	}
	return baseline{Env: r.Environment, Declared: strings.TrimSuffix(r.GitCommit, "-dirty"), DeclDirt: strings.HasSuffix(r.GitCommit, "-dirty"), Run: r}, nil
}

var fullRev = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// strictRev is the revision a strict rerun insists on, and whether it was observed (verified) or only declared by the run's
// metadata; or why no strict rerun is possible.
func (b baseline) strictRev() (rev, basis, why string) {
	if c, ok := runs.EnvValue(b.Env, "code.commit"); ok {
		switch d, _ := runs.EnvValue(b.Env, "code.dirty"); d {
		case "no":
			return c, runs.EnvVerified, ""
		case "yes":
			return "", "", "the parent run's code had uncommitted changes, which its commit does not describe"
		default:
			return "", "", "whether the parent run's tracked files had changed was not observed, so its commit may not describe its code"
		}
	}
	switch {
	case b.Declared == "":
		return "", "", "the parent run recorded no code revision"
	case b.DeclDirt:
		return "", "", "the parent run declares uncommitted changes, which its commit does not describe"
	case !fullRev.MatchString(b.Declared):
		return "", "", "the parent run declares only a short revision, which cannot be compared exactly"
	}
	return b.Declared, "declared", ""
}

type settingChange struct {
	Key    string  `json:"key"`
	Parent *string `json:"parent"` // null: the parent run did not have this setting
	Now    *string `json:"now"`    // null: the draft does not have it
}

// effectiveSettings are the values a VGT or ROHSA request runs with, as "section.key" entries.
func effectiveSettings(template string, req jobs.VGTRequest) map[string]string {
	if template == "rohsa" {
		return effectiveROHSASettings(req)
	}
	return jobs.EffectiveVGTSettings(req)
}

// settingsChanges compares a draft's settings with those of the job that made the parent run. known is false when the parent
// has no kept request (an imported run, or a job from before settings were kept): nothing is then said to have changed.
func (s *Server) settingsChanges(parentID int64, template string, req jobs.VGTRequest) (ch []settingChange, known bool) {
	if s.Jobs == nil {
		return nil, false
	}
	pj, err := s.Jobs.ForRun(parentID)
	if err != nil || pj.Template != template {
		return nil, false
	}
	preq, ok := pj.ConfigRequest()
	if !ok {
		return nil, false
	}
	a, b := effectiveSettings(template, preq), effectiveSettings(template, req)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		av, aok := a[k]
		bv, bok := b[k]
		if aok && bok && av == bv {
			continue
		}
		c := settingChange{Key: k}
		if aok {
			c.Parent = &av
		}
		if bok {
			c.Now = &bv
		}
		ch = append(ch, c)
	}
	return ch, true
}

// rerunCheckJSON is what Vesqira can say about a draft before it is submitted.
type rerunCheckJSON struct {
	Mode        string `json:"mode"`
	ParentRunID int64  `json:"parentRunId"`
	// CodeChecked is true when the host answered the read of the code folder. Adapted drafts do not ask.
	CodeChecked bool                 `json:"codeChecked"`
	Differences []runs.EnvDifference `json:"differences"`
	Settings    []settingChange      `json:"settings"`
	// SettingsKnown is false when the parent run's settings were not kept, so no change can be marked.
	SettingsKnown bool `json:"settingsKnown"`
	// Blocking: a strict rerun would be refused now. Messages say, in sentences, what differs or could not be checked.
	Blocking bool     `json:"blocking"`
	Messages []string `json:"messages"`
	Notes    []string `json:"notes"`
}

const afterRunNote = "Julia version, loaded modules, node and CPU are known only when the job runs; they are compared on the run page afterwards."

// rerunCheck reads the code folder on the host (read-only; see jobs.EnvProbe) and compares it with what the parent run
// observed. It never changes anything.
func (s *Server) rerunCheck(ctx context.Context, j jobs.Job, h hosts.Host) (rerunCheckJSON, *apiErr) {
	mode := j.RerunMode()
	if j.ParentRunID == nil || mode == "" {
		return rerunCheckJSON{}, &apiErr{409, "this job does not repeat a run"}
	}
	req, ok := j.ConfigRequest()
	if !ok {
		return rerunCheckJSON{}, &apiErr{409, "this job has no kept settings to check"}
	}
	out := rerunCheckJSON{Mode: mode, ParentRunID: *j.ParentRunID, Differences: []runs.EnvDifference{}, Settings: []settingChange{}, Messages: []string{}, Notes: []string{}}
	out.Settings, out.SettingsKnown = s.settingsChanges(*j.ParentRunID, j.Template, req)
	if out.Settings == nil {
		out.Settings = []settingChange{}
	}
	if !out.SettingsKnown {
		out.Notes = append(out.Notes, "The parent run's settings were not kept (it was imported), so changed settings cannot be marked.")
	}
	if mode == rerunAdapted {
		return out, nil
	}
	base, err := s.parentBaseline(*j.ParentRunID)
	if err != nil {
		return out, &apiErr{404, "the run this job repeats does not exist"}
	}
	cfg, _ := jobs.EmbeddedConfig(j.Script)
	input, _ := jobs.ConfigPaths(cfg)
	if !jobs.ValidPath(input) { // a relative or unusual path is not interpolated; the facts that need it come back unresolved
		input = ""
	}
	cmd, script, err := jobs.EnvProbe(req.RepoDir, input)
	if err != nil {
		return out, jobAPIErr(err)
	}
	probe, err := s.Remote.RunShell(ctx, h, cmd, []byte(script), hostCheckTime)
	if err != nil {
		out.Messages = append(out.Messages, "the code folder could not be read on the host ("+err.Error()+")")
		out.Blocking = mode == rerunStrict
		out.Notes = append(out.Notes, afterRunNote)
		return out, nil
	}
	out.CodeChecked = true
	nowFacts := runs.ParseEnvLines(string(probe))
	if _, ok := runs.EnvValue(base.Env, "input.sampled_fp"); ok && input != "" {
		nowFacts = append(nowFacts, s.nowInputFingerprint(ctx, h, input))
	}
	diffs, msgs, blocking := compareToNow(base, nowFacts, mode == rerunStrict)
	out.Differences, out.Messages, out.Blocking = append(out.Differences, diffs...), append(out.Messages, msgs...), blocking
	out.Notes = append(out.Notes, afterRunNote)
	return out, nil
}

// compareToNow lines the facts the parent observed up against what the host shows now. Blocking is meaningful for a strict
// rerun: a revision that differs or cannot be read, tracked changes, or a Manifest.toml or input file that differ.
func compareToNow(base baseline, now []runs.EnvFact, strict bool) ([]runs.EnvDifference, []string, bool) {
	var diffs []runs.EnvDifference
	var msgs []string
	blocking := false
	block := func(m string) {
		msgs = append(msgs, m)
		blocking = blocking || strict
	}
	// the revision: observed by the parent, or declared by its metadata
	want, basis, _ := base.strictRev()
	if want == "" {
		want, basis = base.Declared, "declared"
		if v, ok := runs.EnvValue(base.Env, "code.commit"); ok {
			want, basis = v, runs.EnvVerified
		}
	}
	nowRev, haveRev := runs.EnvValue(now, "code.commit")
	d := runs.EnvDifference{Key: "code.commit", Label: "Code commit", Parent: want, This: nowRev}
	switch {
	case !haveRev:
		d.Result, d.Why = "unknown", "the code folder could not be read as a git checkout on the host"
		block("the code folder's revision could not be read on the host (" + factReason(now, "code.commit") + ")")
	case want == "":
		d.Result, d.Why = "unknown", "the parent run recorded no revision"
		msgs = append(msgs, "the parent run recorded no code revision, so the revision cannot be compared")
	case nowRev == want:
		d.Result = "matched"
	default:
		d.Result, d.Blocking = "differed", true
		block(fmt.Sprintf("the code folder is at %s, the parent run used %s", short12(nowRev), short12(want)))
	}
	if basis == "declared" && want != "" {
		d.Why = strings.TrimSpace(d.Why + " The parent's revision is declared by its own metadata, not observed.")
	}
	diffs = append(diffs, d)

	dirty, haveDirty := runs.EnvValue(now, "code.dirty")
	dd := runs.EnvDifference{Key: "code.dirty", Label: "Tracked files changed since that commit", Parent: valueOr(base.Env, "code.dirty"), This: dirty}
	switch {
	case !haveDirty:
		dd.Result, dd.Why = "unknown", "git status could not be read on the host"
		block("whether tracked files in the code folder have changed could not be read on the host")
	case dirty == "yes":
		dd.Result, dd.Blocking = "differed", true
		block("tracked files in the code folder have uncommitted changes")
	default:
		dd.Result = "matched"
	}
	diffs = append(diffs, dd)

	// The input file is compared by its sampled fingerprint when both sides have one: equal says the sampled bytes are equal
	// (a file only touched is then not a difference), different says the file differs. Without one on both sides the size
	// and modification time decide, as before.
	byFP := false
	fpMatched := false
	for _, ed := range runs.CompareEnv(base.Env, now) {
		if ed.Key == "input.sampled_fp" && ed.Result != "unknown" {
			byFP, fpMatched = true, ed.Result == "matched"
			if !fpMatched {
				block("the input file's sampled fingerprint differs from the one recorded for the parent run (the file's contents are not the same)")
			}
			diffs = append(diffs, ed)
		}
	}
	for _, ed := range runs.CompareEnv(base.Env, now) {
		switch ed.Key {
		case "julia.manifest_sha256", "input.size_bytes", "input.mtime_epoch":
			if ed.Result == "differed" && byFP && fpMatched {
				ed.Blocking = false
				ed.Why = "the sampled fingerprint is equal, so this is not counted as a difference (a file that was touched or copied)"
			} else if ed.Result == "differed" && byFP {
				ed.Blocking = true // already blocked on the fingerprint; the size or time is shown with it
			} else if ed.Result == "differed" {
				block(ed.Label + " differs: parent " + ed.Parent + ", now " + ed.This)
			}
			diffs = append(diffs, ed)
		}
	}
	if _, ok := runs.EnvValue(base.Env, "input.sampled_fp"); ok && !byFP {
		msgs = append(msgs, "the input file's fingerprint could not be measured now ("+factReason(now, "input.sampled_fp")+"), so its size and modification time are compared instead")
	}
	return diffs, msgs, blocking
}

// nowInputFingerprint measures the input file's sampled fingerprint on the host as it is now, as a fact (unresolved with the
// reason when it cannot be).
func (s *Server) nowInputFingerprint(ctx context.Context, h hosts.Host, path string) runs.EnvFact {
	f := runs.EnvFact{Key: "input.sampled_fp"}
	fp, _, _, err := s.hostFingerprint(ctx, h, path)
	if err != nil {
		f.Status, f.Reason = runs.EnvUnresolved, err.Error()
		return f
	}
	f.Status, f.Value = runs.EnvVerified, fp
	return f
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func valueOr(facts []runs.EnvFact, key string) string {
	v, _ := runs.EnvValue(facts, key)
	return v
}

func factReason(facts []runs.EnvFact, key string) string {
	for _, f := range facts {
		if f.Key == key && f.Reason != "" {
			return f.Reason
		}
	}
	return "no answer"
}

// getRerunCheck is GET /api/jobs/{id}/rerun-check.
func (s *Server) getRerunCheck(w http.ResponseWriter, r *http.Request) {
	j, h, ok := s.jobFrom(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*hostCheckTime)
	defer cancel()
	out, aerr := s.rerunCheck(ctx, j, h)
	if aerr != nil {
		s.failAPI(w, aerr)
		return
	}
	writeJSON(w, 200, out)
}

// gateRerun runs before a draft is submitted. A strict draft is refused unless the code folder on the host is the parent's;
// a best effort draft is submitted, and what differed is kept in the audit line. Both are checked here, on the server, not
// only on the screen.
func (s *Server) gateRerun(ctx context.Context, j jobs.Job, h hosts.Host) (note string, aerr *apiErr) {
	mode := j.RerunMode()
	if j.ParentRunID == nil || (mode != rerunStrict && mode != rerunBest) {
		return "", nil
	}
	chk, aerr := s.rerunCheck(ctx, j, h)
	if aerr != nil {
		if mode == rerunStrict {
			return "", &apiErr{aerr.Code, "strict rerun not submitted: " + aerr.Msg}
		}
		return "", nil
	}
	if mode == rerunStrict && chk.Blocking {
		return "", &apiErr{409, "strict rerun not submitted, nothing was sent: " + strings.Join(chk.Messages, "; ") + ". Use best effort to run the code as it is now, or restore the folder to the parent's state."}
	}
	if len(chk.Messages) > 0 {
		return mode + " rerun, differences: " + strings.Join(chk.Messages, "; "), nil
	}
	return mode + " rerun, no difference found in what could be read before the job", nil
}

// rerunFor validates the mode of a new draft and, for a strict one, the revision it insists on. It returns the mode to store
// ("" when the draft repeats nothing).
func (s *Server) rerunFor(template string, parentID *int64, mode string) (string, string, *apiErr) {
	if parentID == nil {
		if mode != "" {
			return "", "", &apiErr{422, "a rerun mode needs the run it repeats"}
		}
		return "", "", nil
	}
	if template != "vgt" && template != "rohsa" {
		if mode != "" {
			return "", "", &apiErr{422, "rerun modes apply to VGT and ROHSA drafts"}
		}
		return "", "", nil
	}
	if mode == "" {
		mode = rerunAdapted
	}
	if !validRerunMode(mode) {
		return "", "", &apiErr{422, "the rerun mode is strict, best_effort or adapted"}
	}
	if mode != rerunStrict {
		return mode, "", nil
	}
	base, err := s.parentBaseline(*parentID)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			return "", "", &apiErr{422, "the run this job repeats does not exist"}
		}
		log.Printf("jobs: %v", err)
		return "", "", &apiErr{500, "internal error"}
	}
	rev, _, why := base.strictRev()
	if rev == "" {
		return "", "", &apiErr{422, "a strict rerun is not possible: " + why + ". Use best effort or adapted."}
	}
	return mode, rev, nil
}

// refuseStrictSettings: a strict rerun repeats the run as it was. When the parent's settings are known and the draft's differ,
// the draft is refused with the list of what differs; adapted is the mode for changed settings.
func (s *Server) refuseStrictSettings(j jobs.Job) *apiErr {
	if j.RerunMode() != rerunStrict || j.ParentRunID == nil {
		return nil
	}
	req, ok := j.ConfigRequest()
	if !ok {
		return nil
	}
	ch, known := s.settingsChanges(*j.ParentRunID, j.Template, req)
	if !known || len(ch) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ch))
	for _, c := range ch {
		keys = append(keys, c.Key)
	}
	return &apiErr{422, "a strict rerun repeats the run with its own settings, and these differ: " + strings.Join(keys, ", ") + ". Use adapted to change settings."}
}
