package server

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Watching jobs while the app runs (ADR 0043). Until now a job's state was read only when someone pressed Refresh or had
// the job open. The watcher reads it on a schedule through the same path (applyJobStatus), so an end state is noticed
// once, announced once, and its results imported once, whichever caller sees it first.

const (
	// MinWatchInterval is the shortest time between status calls to one host. A cluster's login node is shared: one
	// squeue and one sacct per host per interval, however many jobs are active there.
	MinWatchInterval = 60 * time.Second
	maxWatchBackoff  = 15 * time.Minute
	watchTick        = 15 * time.Second
	// owedEvery is how often a finished job's owed run record is retried on a host that has no active job.
	owedEvery = 5 * time.Minute
	// unknownAfter is how long a job may be missing from squeue and sacct before the owner is told its end is unknown.
	unknownAfter = 15 * time.Minute
	owedPerPass  = 3
)

type hostWatch struct {
	next     time.Time // no status call to this host before this time
	nextOwed time.Time
	fails    int
}

// jobWatcher is the schedule: per host, when it may be called next and how many calls in a row failed.
type jobWatcher struct {
	interval time.Duration
	hosts    map[int64]*hostWatch
}

func newJobWatcher(interval time.Duration) *jobWatcher {
	if interval < MinWatchInterval {
		interval = MinWatchInterval
	}
	return &jobWatcher{interval: interval, hosts: map[int64]*hostWatch{}}
}

func (w *jobWatcher) host(id int64) *hostWatch {
	hw := w.hosts[id]
	if hw == nil {
		hw = &hostWatch{}
		w.hosts[id] = hw
	}
	return hw
}

// backoff is the wait after fails failures in a row: the interval doubled once per failure consecutive failure, up to a quarter of an hour: a host behind a VPN that is down
// is asked rarely, and the first success after it returns resets the schedule.
func (w *jobWatcher) backoff(fails int) time.Duration {
	d := w.interval
	for i := 0; i < fails && d < maxWatchBackoff; i++ {
		d *= 2
	}
	if d > maxWatchBackoff {
		d = maxWatchBackoff
	}
	return d
}

// WatchJobs follows queued and running jobs until ctx ends. It starts with a pass at once (a job that ended while the app
// was closed is noticed on start) and does nothing at all, not even a host call, when no job is active.
func (s *Server) WatchJobs(ctx context.Context, interval time.Duration) {
	if s.Jobs == nil || s.HostDB == nil || s.Remote == nil {
		return
	}
	ctx = localCtx(ctx) // local-only feature: never mounted on the group server (ADR 0050 owner answer 3)
	w := newJobWatcher(interval)
	t := time.NewTicker(watchTick)
	defer t.Stop()
	for {
		s.watchOnce(ctx, w, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.reach.wakeCh(): // a VPN connect succeeded: look at once
		}
	}
}

func isActiveState(state string) bool {
	return state == jobs.StateSubmitted || state == jobs.StateRunning
}

// watchOnce is one pass at time now. Jobs still in "submitting" are not polled: whether Slurm holds them is a decision for
// the owner (Check on the host, or reopen as a draft), and a lookup there is never silent.
func (s *Server) watchOnce(ctx context.Context, w *jobWatcher, now time.Time) {
	js, err := s.Jobs.Watchable(now)
	if err != nil {
		log.Printf("jobs: watch: %v", err)
		return
	}
	type work struct{ active, owed []jobs.Job }
	byHost := map[int64]*work{}
	for _, j := range js {
		g := byHost[j.HostID]
		if g == nil {
			g = &work{}
			byHost[j.HostID] = g
		}
		if isActiveState(j.State) {
			g.active = append(g.active, j)
		} else {
			g.owed = append(g.owed, j)
		}
	}
	ids := make([]int64, 0, len(byHost))
	for id := range byHost {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	for _, hostID := range ids {
		if ctx.Err() != nil {
			return
		}
		g, hw := byHost[hostID], w.host(hostID)
		h, err := s.HostDB.Get(hostID)
		if err != nil {
			continue // the host was removed: nothing to ask
		}
		if s.reach.takeKick(hostID) { // the VPN is back: the backoff of the time it was down no longer applies
			hw.next, hw.fails = now, 0
		}
		due := !now.Before(hw.next)
		if len(g.active) > 0 && due {
			s.watchHost(ctx, w, hw, h, g.active, now)
		}
		if len(g.owed) > 0 && due && !now.Before(hw.nextOwed) {
			hw.nextOwed = now.Add(owedEvery)
			for i, j := range g.owed {
				if i == owedPerPass {
					break
				}
				s.retryOwedRecord(j, h)
			}
		}
	}
	s.advanceSweeps(ctx) // a restart, or a job that ended while nobody looked, must not leave a sweep waiting
}

// watchHost makes the one status call for a host and applies each job's answer through the shared refresh path.
func (s *Server) watchHost(ctx context.Context, w *jobWatcher, hw *hostWatch, h hosts.Host, active []jobs.Job, now time.Time) {
	slurm := make([]string, 0, len(active))
	for _, j := range active {
		slurm = append(slurm, j.SlurmID)
	}
	if _, err := jobs.BatchStatusCommand(slurm); err != nil { // the ids are checked before anything is run
		hw.next = now.Add(w.interval)
		log.Printf("jobs: watch %s: %v", h.Name, err)
		return
	}
	hw.next = now.Add(w.interval) // the slot is taken before the call, so a slow answer cannot cause a second call
	st, err := s.hostStatus(ctx, h, slurm, now)
	if err != nil {
		s.reach.set(h.ID, false)
		hw.fails++
		hw.next = now.Add(w.backoff(hw.fails))
		if hw.fails == 1 {
			log.Printf("jobs: %s did not answer (%v); asking again in %s, then less often", h.Name, err, hw.next.Sub(now))
		}
		return
	}
	s.reach.set(h.ID, true)
	if hw.fails > 0 {
		log.Printf("jobs: %s answers again", h.Name)
	}
	hw.fails = 0
	for _, j := range active {
		s.applyJobStatus(j, h, st[j.SlurmID], now)
	}
}

// applyJobStatus records what Slurm reported for a queued or running job. It is the one path for the Refresh button, the
// page's timer and the background watcher. A job Slurm does not list keeps its state: not found is not failed. The
// update is guarded by the job's state, so only the caller that moves it to an end state announces it.
func (s *Server) applyJobStatus(j jobs.Job, h hosts.Host, st jobs.Status, obtained time.Time) {
	if st.State == "" {
		_, _ = s.Jobs.ApplyStatus(j.ID, j.State, st.Detail)
		if j.SubmittedAt != nil && obtained.Sub(*j.SubmittedAt) > unknownAfter {
			s.announce(j, h, jobs.Status{Slurm: ""}, notify.KindUnknown)
		}
		return
	}
	won, err := s.Jobs.ApplyStatus(j.ID, st.State, st.Detail)
	if err != nil || !won {
		return // another caller already moved this job on, and it handles the record and the announcement
	}
	ended := isTerminal(st.State)
	if ended && st.Usage != nil {
		// what the scheduler recorded the job used, kept with the job; only the caller that saw the end stores it
		if err := s.Jobs.SetUsage(j.ID, *st.Usage); err != nil {
			log.Printf("jobs: usage of job %d: %v", j.ID, err)
		}
	}
	// the record says how the job ended; and it is retried while it is still owed (folder not there yet, host down)
	if j.RecordOwed || ended {
		s.recordRunAsync(j.ID, h, jobs.ReportedEvidence(st.State, st.Detail, obtained))
	}
	if !ended {
		return
	}
	kind := notify.KindFailed
	switch {
	case st.State == jobs.StateCompleted:
		kind = notify.KindCompleted
	case st.State == jobs.StateCancelled:
		kind = notify.KindCancelled
	case strings.HasPrefix(st.Slurm, "TIMEOUT"):
		kind = notify.KindTimeout
	}
	s.announce(j, h, st, kind)
	if st.State == jobs.StateCompleted && j.AutoImport {
		s.autoImportAsync(j, h)
	}
	s.kickSweeps() // a slot may be free for the next job of a sweep
}

// retryOwedRecord writes the run record of a finished job again, from what was stored when its end was read.
func (s *Server) retryOwedRecord(j jobs.Job, h hosts.Host) {
	detail, _, _ := strings.Cut(j.Detail, "; run.json not written")
	asOf := time.Now()
	if j.FinishedAt != nil {
		asOf = *j.FinishedAt
	}
	s.recordRunAsync(j.ID, h, jobs.ReportedEvidence(j.State, detail, asOf))
}

// announce records the end of a job once. The words say what Slurm reported; completion is never called success, because a
// finished job has not been checked (AGENTS.md invariant 9).
func (s *Server) announce(j jobs.Job, h hosts.Host, st jobs.Status, kind string) {
	if s.Notes == nil {
		return
	}
	var title, body string
	exit := ""
	if st.ExitCode != "" {
		exit = " (exit " + st.ExitCode + ")"
	}
	where := " on " + h.Name
	switch kind {
	case notify.KindCompleted:
		title = fmt.Sprintf("Job %s finished%s — results not yet checked", j.Name, exit)
		body = "Slurm reports COMPLETED" + where + ". That means the job ran to its end, not that its result is valid."
		if j.AutoImport {
			body += " Its results will be imported as a run now; importing reads the files and checks nothing."
		} else {
			body += " Open the job to import its results."
		}
	case notify.KindTimeout:
		title = fmt.Sprintf("Job %s stopped at its time limit%s", j.Name, exit)
		body = "Slurm reports TIMEOUT" + where + ". Anything it wrote before the limit may be incomplete."
	case notify.KindCancelled:
		title = fmt.Sprintf("Job %s was cancelled", j.Name)
		body = "Slurm reports " + orWord(st.Slurm, "CANCELLED") + where + "."
	case notify.KindUnknown:
		title = fmt.Sprintf("Job %s: Slurm no longer lists it, how it ended is not known", j.Name)
		body = "Neither squeue nor sacct" + where + " reports Slurm job " + j.SlurmID + ". Vesqira has not marked it finished or failed: check the host's log and output folder."
	default:
		title = fmt.Sprintf("Job %s failed%s", j.Name, exit)
		body = "Slurm reports " + orWord(st.Slurm, "FAILED") + where + ". The job's log may say why."
	}
	id := j.ID
	key := fmt.Sprintf("job:%d:end", j.ID)
	if kind == notify.KindUnknown {
		key = fmt.Sprintf("job:%d:unknown", j.ID)
	}
	// // local-only feature: never mounted on the group server (ADR 0050 owner answer 3)
	if _, err := s.Notes.Add(workspace.Local, authz.LocalOwnerID, key, notify.Notification{Kind: kind, Title: title, Body: body, JobID: &id}); err != nil {
		log.Printf("jobs: notification: %v", err)
	}
}

func orWord(w, fallback string) string {
	if w == "" {
		return fallback
	}
	return w
}

// autoImportAsync imports a completed job's results in the background, once. It is tracked so tests and shutdown can wait.
func (s *Server) autoImportAsync(j jobs.Job, h hosts.Host) {
	if s.Runs == nil || s.Store == nil {
		return
	}
	s.imports.Add(1)
	go func() {
		defer s.imports.Done()
		limit := 90 * time.Second
		if j.Template == "rohsa" {
			limit = remoteImportTimeout // a fit's product tree is registered file by file on the host
		}
		// local-only feature: never mounted on the group server (ADR 0050 owner answer 3)
		ctx, cancel := context.WithTimeout(localCtx(context.Background()), limit)
		defer cancel()
		s.autoImport(ctx, j.ID, h)
	}()
}

// waitImports blocks until the automatic imports started so far have finished (tests, shutdown).
func (s *Server) waitImports() { s.imports.Wait() }

// autoImport runs the same import as the "Import results as a run" button (importFrom: run_metadata.toml and the output
// files next to it, or for a ROHSA job the one fit found under its output folder; no other way of finding a run) and links the run to the job. The claim in the store lets exactly one
// attempt happen: a failure becomes one notification with the reason and is not retried; the button stays available.
func (s *Server) autoImport(ctx context.Context, jobID int64, h hosts.Host) {
	won, err := s.Jobs.ClaimAutoImport(jobID)
	if err != nil || !won {
		return
	}
	j, err := s.Jobs.Get(jobID)
	if err != nil {
		return
	}
	id := j.ID
	var run runs.Run
	var code int
	var msg string
	if j.Template == "rohsa" {
		run, code, msg = s.importROHSAJob(ctx, j, h)
	} else if _, record, why := s.jobImport(j.Template); why != "" {
		code, msg = 422, why
	} else {
		run, code, msg = s.importFrom(ctx, remoteRun{s: s, h: h, dir: j.OutputDir, file: record})
	}
	if code != 0 {
		s.Audit.RecordCtx(ctx, "run import failed", "job", j.ID, h.Name+": "+msg)
		_, _ = s.Notes.Add(wsCtx(ctx), actorID(ctx), fmt.Sprintf("job:%d:import", j.ID), notify.Notification{Kind: notify.KindImportFailed, JobID: &id,
			Title: fmt.Sprintf("Results of job %s were not imported", j.Name),
			Body:  msg + ". It was tried once and is not retried; open the job to import them by hand."})
		return
	}
	run = s.linkJobRun(ctx, j, h, run)
	s.Audit.RecordCtx(ctx, "run imported automatically", "job", j.ID, h.Name+": "+j.OutputDir)
	rid := run.ID
	_, _ = s.Notes.Add(wsCtx(ctx), actorID(ctx), fmt.Sprintf("job:%d:import", j.ID), notify.Notification{Kind: notify.KindImported, JobID: &id, RunID: &rid,
		Title: fmt.Sprintf("Results of job %s imported as a run", j.Name),
		Body:  "The files were registered from the output folder. Nothing was checked: review the run before relying on it."})
}
