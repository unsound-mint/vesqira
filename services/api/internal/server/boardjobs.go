package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/vgtlog"
)

// A board panel can follow a job. The jobs store, the hosts and the job log live here in the server layer; the boards
// package only ever receives plain facts (name, state) and never learns where a job runs.

const (
	progressTTL     = 30 * time.Second // a job's log is read at most this often
	progressTimeout = 5 * time.Second  // a board response never waits longer than this for logs
)

// progressCache remembers what a job's log said, including "nothing", so a board that is polled does not open an ssh
// session per request. The zero value is ready to use.
type progressCache struct {
	mu   sync.Mutex
	m    map[int64]progressEntry
	read func(ctx context.Context, j jobs.Job) (string, error) // replaced in tests; nil reads through the job's host
}

type progressEntry struct {
	at time.Time
	p  *vgtlog.Progress
}

func (c *progressCache) get(id int64) (progressEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	return e, ok
}

func (c *progressCache) put(id int64, p *vgtlog.Progress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[int64]progressEntry{}
	}
	c.m[id] = progressEntry{at: time.Now(), p: p}
}

func jobIsActive(state string) bool {
	return state == jobs.StateSubmitted || state == jobs.StateRunning
}

// logProgress returns the job's progress from its log, from the cache when it is fresh. With live false it never
// reads from the host.
func (s *Server) logProgress(ctx context.Context, j jobs.Job, live bool) *vgtlog.Progress {
	if e, ok := s.progress.get(j.ID); ok && (!live || time.Since(e.at) < progressTTL) {
		return e.p
	}
	if !live || j.SlurmID == "" || ctx.Err() != nil {
		return nil
	}
	var out string
	var err error
	if s.progress.read != nil {
		out, err = s.progress.read(ctx, j)
	} else if s.HostDB != nil && s.Remote != nil {
		out, err = s.readLogViaHost(ctx, j)
	} else {
		return nil
	}
	if err != nil { // on timeout or a host that cannot be reached the panel simply shows no progress
		return nil
	}
	var p *vgtlog.Progress
	if pr, ok := vgtlog.ParseTail(out); ok {
		p = &pr
	}
	s.progress.put(j.ID, p)
	return p
}

func (s *Server) readLogViaHost(ctx context.Context, j jobs.Job) (string, error) {
	h, err := s.HostDB.Get(j.HostID)
	if err != nil {
		return "", err
	}
	cmd, err := jobs.LogCommand(j.WorkDir, j.SlurmID)
	if err != nil {
		return "", err
	}
	out, err := s.Remote.RunShell(ctx, h, cmd, nil, progressTimeout)
	return string(out), err
}

// boardView fills the job panels of a board with the job's current state. live lets it read logs of active jobs (the
// board fetch does; the replies to edits only use what is cached, so an edit never waits on a host).
func (s *Server) boardView(ctx context.Context, b boards.Board, live bool) boards.Board {
	ctx, cancel := context.WithTimeout(ctx, progressTimeout)
	defer cancel()
	now := time.Now()
	for i := range b.Items {
		it := &b.Items[i]
		if it.Kind != boards.KindJob {
			continue
		}
		id := it.Job.ID
		info := boards.JobInfo{ID: id, Name: "Run", State: "unknown"} // a job that is gone is shown as such, not hidden
		if s.Jobs != nil {
			if j, err := s.Jobs.Get(id); err == nil {
				info = boards.JobInfo{ID: id, Name: j.Name, State: j.State, SubmittedAt: j.SubmittedAt, FinishedAt: j.FinishedAt, RunID: j.RunID}
				if jobIsActive(j.State) {
					if p := s.logProgress(ctx, j, live); p != nil {
						jp := &boards.JobProgress{Done: p.Done, Total: p.Total}
						if j.SubmittedAt != nil {
							if eta, ok := vgtlog.ETA(*p, *j.SubmittedAt, now); ok {
								jp.ETASeconds = &eta
							}
						}
						info.Progress = jp
					}
				}
			}
		}
		info.Label, info.SweepID = s.sweepLabel(id)
		it.Job = &info
	}
	return b
}

func (s *Server) boardByID(ctx context.Context, id int64, live bool) (boards.Board, error) {
	b, err := s.Boards.Board(id)
	if err != nil {
		return b, err
	}
	return s.boardView(ctx, b, live), nil
}

// jobSnapshots collects the only two facts about each followed job that a snapshot may carry.
func (s *Server) jobSnapshots(b boards.Board) map[int64]boards.JobSnapshot {
	out := map[int64]boards.JobSnapshot{}
	for _, it := range b.Items {
		if it.Kind != boards.KindJob || s.Jobs == nil {
			continue
		}
		if j, err := s.Jobs.Get(it.Job.ID); err == nil {
			out[it.Job.ID] = boards.JobSnapshot{Name: j.Name, State: j.State}
		}
	}
	return out
}

// boardPin turns a job panel into the figure that was made from its result.
func (s *Server) boardPin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	pos, ok := pathInt(w, r, "pos")
	if !ok {
		return
	}
	var in struct {
		FigureID int64 `json:"figureId"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if err := s.Boards.PinItem(id, int(pos), in.FigureID); err != nil {
		s.boardErr(w, err)
		return
	}
	s.Audit.Record("job panel pinned", "board", id, "panel "+strconv.FormatInt(pos, 10)+" is now figure "+strconv.FormatInt(in.FigureID, 10))
	b, err := s.boardByID(r.Context(), id, false)
	if err != nil && !errors.Is(err, boards.ErrNotFound) {
		log.Printf("boards: %v", err)
	}
	writeJSON(w, 200, b)
}
