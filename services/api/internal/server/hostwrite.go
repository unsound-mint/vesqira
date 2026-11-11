package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
)

// Writing on a host is the one place Vesqira changes a file that is not on this computer (ADR 0039). It writes two
// kinds of file, through hosts.Manager.WriteFile only: .vesqira/project.toml in a project folder, after the same
// confirmation a local project needs, and run.json next to the outputs of a job it submitted. Neither replaces a file
// Vesqira did not write; both are audited, including a refusal.

// projectMarker starts the first line of every project.toml Vesqira writes on a host. The file is replaced only while its
// first line starts with it, so a project file the researcher wrote, or edited by taking the line out, is left alone.
const projectMarker = "# Written by Vesqira"

func hasProjectMarker(b []byte) bool {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	return bytes.HasPrefix(line, []byte(projectMarker))
}

func withProjectMarker(text string) string {
	if hasProjectMarker([]byte(text)) {
		return text
	}
	return projectMarker + " (this line lets Vesqira update the file later; delete it to keep the file entirely yours).\n" + text
}

func projectFileHostPath(p projects.Project) string {
	return path.Join(p.Path, discover.ProjectFilePath)
}

const projectFileDir = ".vesqira"

// hostProjectFile reads the project file on the host, exactly as the bytes are (the write that follows is pinned to their
// sha256). A file that cannot be read from inside the folder is "does not exist" (the write that follows is refused with
// the real reason); one too large to check counts as present and foreign; an unreachable host is an error.
func (s *Server) hostProjectFile(ctx context.Context, h hosts.Host, p projects.Project) (raw []byte, exists bool, err error) {
	b, ex, err := s.Remote.ReadExisting(ctx, h, p.Path, projectFileDir, "project.toml")
	var wr *hosts.WriteRefused
	switch {
	case errors.As(err, &wr) && wr.Reason == "toolarge":
		return nil, true, nil
	case errors.As(err, &wr):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return b, ex, nil
}

// expectOf is the pin for a write that replaces what was just read.
func expectOf(raw []byte, exists bool) string {
	if !exists {
		return hosts.ExpectAbsent
	}
	return hosts.SHA256Hex(raw)
}

// writeHostProjectFile writes text (marker added) as the project's file on its host and audits the attempt. expect pins
// the file the text was built from (see hosts.WriteSpec.Expect), so an edit made on the host meanwhile is not lost.
func (s *Server) writeHostProjectFile(ctx context.Context, h hosts.Host, p projects.Project, text, expect string) error {
	data := []byte(strings.TrimRight(withProjectMarker(text), "\n") + "\n")
	err := s.Remote.WriteFile(ctx, h, hosts.WriteSpec{Root: p.Path, Rel: projectFileDir, Name: "project.toml", Marker: projectMarker, Create: true, Data: data, Expect: expect})
	if err != nil {
		s.Audit.Record("project file write on host refused", "project", p.ID, h.Name+": "+err.Error())
		return err
	}
	s.Audit.Record("project file written on host", "project", p.ID, h.Name+": "+projectFileHostPath(p))
	return nil
}

// appendOnHost adds an answer block to the project's file on its host, creating it from the proposal when asked.
// It never fails the answer for a reason on the host: the answer stays in the catalog and the note says why the file
// did not change. Only a block that would break the file is an error.
func (s *Server) appendOnHost(ctx context.Context, p projects.Project, block string, create bool, proposed string) (written bool, note string, err error) {
	kept := "; the answer is kept in the catalog"
	if s.HostDB == nil || s.Remote == nil {
		return false, "not written to the project: remote hosts are not configured" + kept, nil
	}
	h, herr := s.HostDB.Get(p.HostID)
	if herr != nil {
		return false, "not written to the project: the host of this project was removed" + kept, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, exists, rerr := s.hostProjectFile(ctx, h, p)
	if rerr != nil {
		return false, "not written to the project: the host could not be reached (" + rerr.Error() + ")" + kept, nil
	}
	if !exists && !create {
		return false, "not written to the project: there is no .vesqira/project.toml yet" + kept, nil
	}
	if exists && !hasProjectMarker(raw) {
		return false, "not written to the project: project.toml on the host was not written by Vesqira, so it is left as it is" + kept, nil
	}
	base := string(raw)
	if !exists {
		base = withProjectMarker(proposed)
	}
	out, aerr := discover.Append([]byte(base), block)
	if aerr != nil {
		return false, "", aerr
	}
	if werr := s.writeHostProjectFile(ctx, h, p, string(out), expectOf(raw, exists)); werr != nil {
		return false, "not written to the project: " + werr.Error() + kept, nil
	}
	return true, "", nil
}

// writeProjectOnHost is the project-file route for a project on a host: the same confirmation as a local project
// (write: true is checked by the caller), the same replace rule, and the host's refusal of a foreign file is a 409.
func (s *Server) writeProjectOnHost(w http.ResponseWriter, r *http.Request, p projects.Project, given string) {
	if s.HostDB == nil || s.Remote == nil {
		fail(w, 501, "remote hosts are not configured")
		return
	}
	h, err := s.HostDB.Get(p.HostID)
	if err != nil {
		fail(w, 409, "the host of this project was removed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cur, exists, rerr := s.hostProjectFile(ctx, h, p)
	if rerr != nil {
		fail(w, 502, "the host could not be reached ("+rerr.Error()+"); nothing was written")
		return
	}
	if exists && !hasProjectMarker(cur) {
		fail(w, 409, "project.toml on the host was not written by Vesqira, so it is not replaced; edit it there, or delete it to let Vesqira write one")
		return
	}
	text := given
	if text == "" {
		if exists {
			fail(w, 409, "project.toml already exists; send its new text to replace it")
			return
		}
		text = s.storedSummaryOf(p).Proposed
		if text == "" {
			fail(w, 409, "scan the project first: there is no proposed file yet")
			return
		}
	}
	if _, err := discover.ParseProjectFile([]byte(text)); err != nil {
		fail(w, 422, err.Error())
		return
	}
	if err := s.writeHostProjectFile(ctx, h, p, text, expectOf(cur, exists)); err != nil {
		var wr *hosts.WriteRefused
		if errors.As(err, &wr) {
			fail(w, 409, err.Error())
		} else {
			fail(w, 502, "the host did not write the file ("+err.Error()+")")
		}
		return
	}
	s.startScan(p) // read it back through the host: the file is the layout from now on
	writeJSON(w, 200, map[string]any{"written": true, "path": discover.ProjectFilePath, "replaced": exists})
}

// ---- run records ----

// isRunRecord is the rule for "this run.json is one Vesqira wrote": it is a JSON object whose top-level "vesqira" block has
// "record": "vesqira-run-record". A run.json that merely mentions the marker, or is not JSON, is someone else's.
func isRunRecord(b []byte) bool {
	var d struct {
		Vesqira struct {
			Record string `json:"record"`
		} `json:"vesqira"`
	}
	return json.Unmarshal(b, &d) == nil && d.Vesqira.Record == jobs.RunRecordMarker
}

// recordRunAsync writes (or rewrites) the run record of a job in the background, so the answer to the researcher does not
// wait for ssh round trips. The record is marked owed first and stays owed until it is written or refused for good, so
// the next refresh retries what a failure, a missing output folder or an unreachable host left undone.
func (s *Server) recordRunAsync(jobID int64, h hosts.Host, ev jobs.StateEvidence) {
	if s.Remote == nil || s.Jobs == nil {
		return
	}
	_ = s.Jobs.SetRecordOwed(jobID, true)
	s.records.Add(1)
	go func() {
		defer s.records.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		s.recordRun(ctx, jobID, h, ev)
	}()
}

// waitRecords blocks until the background record writes started so far have finished (tests, shutdown).
func (s *Server) waitRecords() { s.records.Wait() }

func (s *Server) recordLock(id int64) *sync.Mutex {
	m, _ := s.recordLocks.LoadOrStore(id, new(sync.Mutex))
	return m.(*sync.Mutex)
}

// recordRun writes the run record of a job next to its outputs on its host. It is best effort by design: the job is
// already in Slurm, so a refusal or an unreachable host is audited and shown in the job's detail line, never turned into a
// failed submission. It never creates the job's output folder: while the folder is missing and the job is not finished,
// the record stays owed silently; a refusal that retrying cannot cure ends the debt.
func (s *Server) recordRun(ctx context.Context, jobID int64, h hosts.Host, ev jobs.StateEvidence) {
	lock := s.recordLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	j, err := s.Jobs.Get(jobID)
	if err != nil {
		return
	}
	settle := func() { _ = s.Jobs.SetRecordOwed(j.ID, false) }
	if j.OutputDir == "" {
		// custom jobs name no output folder: there is nothing to write next to. (A ROHSA job's record sits in the output folder
		// above the fit; discovery merges the two into one run, see discover.mergeJobRecords.)
		settle()
		return
	}
	note := func(why string, forGood bool) {
		s.Audit.Record("run record not written", "job", j.ID, h.Name+": "+why)
		_ = s.Jobs.AppendDetail(j.ID, "; run.json not written next to the outputs ("+why+")")
		if forGood {
			settle()
		}
	}
	root, rel, ok := splitRoot(h, j.OutputDir)
	if !ok {
		note("the output folder is not inside a source root", true)
		return
	}
	j.State, j.Detail = ev.Value, ev.Detail
	data, err := jobs.RunRecord(j, h.Name, s.runInputs(j), ev, time.Now())
	if err != nil {
		note(err.Error(), true)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	err = s.Remote.WriteFile(ctx, h, hosts.WriteSpec{Root: root, Rel: rel, Name: jobs.RunRecordName, Marker: jobs.RunRecordMarker, Data: data, Recognise: isRunRecord})
	var wr *hosts.WriteRefused
	switch {
	case err == nil:
		settle()
		s.Audit.Record("run record written", "job", j.ID, h.Name+": "+path.Join(j.OutputDir, jobs.RunRecordName)+" ("+ev.Value+")")
	case errors.As(err, &wr) && wr.Reason == "absent":
		if isTerminal(ev.Value) {
			note("the job never created its output folder", true)
		} // else: the job has not started writing yet; the next refresh tries again
	case errors.As(err, &wr):
		switch wr.Reason {
		case "link", "notdir", "escape", "notfile", "foreign", "toolarge", "root", "readonly", "nosha256":
			note(err.Error(), true) // what is there will not change by itself: no retry
		default:
			note(err.Error(), false) // changed meanwhile, truncated, a temporary folder that could not be made: retried
		}
	default:
		note(err.Error(), false) // the host could not be reached: retried on the next refresh
	}
}

// splitRoot finds the source root of the host that holds dir and the folder below it.
func splitRoot(h hosts.Host, dir string) (root, rel string, ok bool) {
	if !h.InRoots(dir) {
		return "", "", false
	}
	best := ""
	for _, r := range h.Roots {
		r = strings.TrimRight(r, "/")
		if strings.HasPrefix(dir, r+"/") && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, strings.TrimPrefix(dir, best+"/"), true
}

// runInputs names the catalog files an analysis job was asked to read. They are references to paths on the host.
func (s *Server) runInputs(j jobs.Job) []jobs.RunInput {
	var p struct {
		Sources []int64 `json:"sources"`
	}
	if j.Params == "" || json.Unmarshal([]byte(j.Params), &p) != nil || s.Store == nil {
		return nil
	}
	var out []jobs.RunInput
	for _, id := range p.Sources {
		if d, code, _ := s.dataset(id); code == 0 {
			out = append(out, jobs.RunInput{DatasetID: d.ID, Name: d.Name, Path: d.Path, Source: "the catalog entry the job was drafted from; the file was not hashed"})
		}
	}
	return out
}

func isTerminal(state string) bool {
	return state == jobs.StateCompleted || state == jobs.StateFailed || state == jobs.StateCancelled
}
