package sweeps

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

// A sweep's state says what the owner has authorised, not how its jobs ended: "draft" nothing was submitted and nothing
// will be; "running" the owner approved the scripts and jobs are submitted as slots free up; "stopped" the owner cancelled,
// or a failure stopped it at their choice (Detail says which); "finished" every job that could be submitted has been.
const (
	StateDraft    = "draft"
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateFinished = "finished"
)

type Sweep struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	HostID        int64      `json:"hostId"`
	BaseJobID     int64      `json:"baseJobId"`
	BaseRunID     *int64     `json:"baseRunId"`
	Params        []Param    `json:"params"`
	Note          string     `json:"note"`
	State         string     `json:"state"`
	Detail        string     `json:"detail"`
	Concurrency   int        `json:"concurrency"`
	StopOnFailure bool       `json:"stopOnFailure"`
	BoardID       *int64     `json:"boardId"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`
}

// Member ties one job to its place in the grid. ApprovedHash is the hash of the script the owner was shown when they
// approved the sweep; the scheduler submits the draft only while its script still has that hash.
type Member struct {
	SweepID      int64             `json:"sweepId"`
	JobID        int64             `json:"jobId"`
	Index        int               `json:"index"`
	Values       map[string]string `json:"values"`
	ApprovedHash string            `json:"-"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.SweepsTables); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const sweepCols = `id,name,host_id,base_job_id,base_run_id,params,note,state,detail,concurrency,stop_on_failure,board_id,created_at,started_at`

func scanSweep(row interface{ Scan(...any) error }) (Sweep, error) {
	var sw Sweep
	var params string
	var baseRun, board, started sql.NullInt64
	var created int64
	var stop int
	if err := row.Scan(&sw.ID, &sw.Name, &sw.HostID, &sw.BaseJobID, &baseRun, &params, &sw.Note, &sw.State, &sw.Detail, &sw.Concurrency, &stop, &board, &created, &started); err != nil {
		return Sweep{}, err
	}
	if err := json.Unmarshal([]byte(params), &sw.Params); err != nil {
		return Sweep{}, err
	}
	sw.StopOnFailure = stop != 0
	sw.CreatedAt = time.Unix(created, 0).UTC()
	if baseRun.Valid {
		sw.BaseRunID = &baseRun.Int64
	}
	if board.Valid {
		sw.BoardID = &board.Int64
	}
	if started.Valid {
		t := time.Unix(started.Int64, 0).UTC()
		sw.StartedAt = &t
	}
	return sw, nil
}

// Create stores a sweep and its members (in grid order, jobIDs[i] holds values[i]) in one transaction.
func (s *Store) Create(sw Sweep, jobIDs []int64, values []map[string]string) (Sweep, error) {
	if len(jobIDs) != len(values) || len(jobIDs) == 0 {
		return Sweep{}, fmt.Errorf("%w: a sweep needs its jobs", ErrInvalid)
	}
	pj, _ := json.Marshal(sw.Params)
	tx, err := s.db.Begin()
	if err != nil {
		return Sweep{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO sweeps(name,host_id,base_job_id,base_run_id,params,note,state,detail,concurrency,stop_on_failure,created_at) VALUES(?,?,?,?,?,?,?,?,1,0,?)`,
		sw.Name, sw.HostID, sw.BaseJobID, sw.BaseRunID, string(pj), sw.Note, StateDraft, "", time.Now().Unix())
	if err != nil {
		return Sweep{}, err
	}
	id, _ := res.LastInsertId()
	for i, jid := range jobIDs {
		vj, _ := json.Marshal(values[i])
		if _, err := tx.Exec(`INSERT INTO sweep_jobs(sweep_id,job_id,idx,vals) VALUES(?,?,?,?)`, id, jid, i, string(vj)); err != nil {
			return Sweep{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Sweep{}, err
	}
	return s.Get(id)
}

func (s *Store) Get(id int64) (Sweep, error) {
	sw, err := scanSweep(s.db.QueryRow(`SELECT `+sweepCols+` FROM sweeps WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Sweep{}, ErrNotFound
	}
	return sw, err
}

func (s *Store) List() ([]Sweep, error) {
	return s.query(`SELECT ` + sweepCols + ` FROM sweeps ORDER BY id DESC`)
}

// Running lists the sweeps the scheduler has work for.
func (s *Store) Running() ([]Sweep, error) {
	return s.query(`SELECT `+sweepCols+` FROM sweeps WHERE state=? ORDER BY id`, StateRunning)
}

func (s *Store) query(q string, args ...any) ([]Sweep, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sweep{}
	for rows.Next() {
		sw, err := scanSweep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}

func (s *Store) Members(sweepID int64) ([]Member, error) {
	rows, err := s.db.Query(`SELECT sweep_id,job_id,idx,vals,approved_hash FROM sweep_jobs WHERE sweep_id=? ORDER BY idx`, sweepID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var vj string
		if err := rows.Scan(&m.SweepID, &m.JobID, &m.Index, &vj, &m.ApprovedHash); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(vj), &m.Values); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OfJob finds the sweep a job belongs to.
func (s *Store) OfJob(jobID int64) (Sweep, Member, bool) {
	var m Member
	var vj string
	err := s.db.QueryRow(`SELECT sweep_id,job_id,idx,vals,approved_hash FROM sweep_jobs WHERE job_id=?`, jobID).Scan(&m.SweepID, &m.JobID, &m.Index, &vj, &m.ApprovedHash)
	if err != nil || json.Unmarshal([]byte(vj), &m.Values) != nil {
		return Sweep{}, Member{}, false
	}
	sw, err := s.Get(m.SweepID)
	return sw, m, err == nil
}

// Approve records the owner's go-ahead: the scripts they were shown (hashes by job id), the concurrency limit and the
// failure policy. Only a sweep that is not running can be approved, so a second click does nothing.
func (s *Store) Approve(id int64, concurrency int, stopOnFailure bool, hashes map[int64]string) (bool, error) {
	if concurrency < 1 || concurrency > MaxConcurrency {
		return false, fmt.Errorf("%w: the concurrency limit is 1 to %d", ErrInvalid, MaxConcurrency)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE sweeps SET state=?, detail='', concurrency=?, stop_on_failure=?, started_at=COALESCE(started_at, ?) WHERE id=? AND state IN (?,?)`,
		StateRunning, concurrency, stopOnFailure, time.Now().Unix(), id, StateDraft, StateStopped)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	for jid, h := range hashes {
		if _, err := tx.Exec(`UPDATE sweep_jobs SET approved_hash=? WHERE sweep_id=? AND job_id=?`, h, id, jid); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// Stop ends the scheduling of a running sweep and says why. It reports whether it changed anything.
func (s *Store) Stop(id int64, detail string) (bool, error) {
	res, err := s.db.Exec(`UPDATE sweeps SET state=?, detail=? WHERE id=? AND state=?`, StateStopped, detail, id, StateRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Finish marks a running sweep whose every submittable job has been submitted.
func (s *Store) Finish(id int64) error {
	_, err := s.db.Exec(`UPDATE sweeps SET state=? WHERE id=? AND state=?`, StateFinished, id, StateRunning)
	return err
}

// SetBoard remembers the board made from the sweep; only the first caller wins.
func (s *Store) SetBoard(id, boardID int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE sweeps SET board_id=? WHERE id=? AND board_id IS NULL`, boardID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClearBoard forgets a board that no longer exists, so a new one can be made.
func (s *Store) ClearBoard(id int64) {
	_, _ = s.db.Exec(`UPDATE sweeps SET board_id=NULL WHERE id=?`, id)
}

// OfBoard finds the sweep a board was made from.
func (s *Store) OfBoard(boardID int64) (Sweep, bool) {
	sw, err := scanSweep(s.db.QueryRow(`SELECT `+sweepCols+` FROM sweeps WHERE board_id=? ORDER BY id DESC LIMIT 1`, boardID))
	return sw, err == nil
}
