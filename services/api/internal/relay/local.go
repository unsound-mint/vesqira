package relay

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The key holder's local side: the inbox this computer publishes (with the host each label stands for, which never leaves it),
// the pull's cursor and backoff, and the mirror of each request with what this computer made of it. Local-only tables
// (internal/arch): the group server never mounts the routes that reach them.

// Local states of a mirrored request.
const (
	LocalReceived = "received" // passed this computer's validation; no draft yet
	LocalRefused  = "refused"  // failed this computer's validation; declined with the reason
	LocalDraft    = "draft"    // rendered into a job or a sweep, waiting for the key holder
	LocalApproved = "approved" // claimed on the server and handed to submitDraft or the sweep scheduler
	LocalDeclined = "declined"
	LocalDetached = "detached" // the key holder kept the draft as a job of their own; nothing is reported for it
	LocalClosed   = "closed"   // withdrawn, expired or declined on the server before approval
)

// MaxOpenLocal bounds the requests waiting here (received or drafted): a compromised server ignores its own rate limits, so this
// computer refuses more than it can sensibly review.
const MaxOpenLocal = 100

// Local is the key holder's store.
type Local struct {
	db  *sql.DB
	Now func() time.Time
}

// OpenLocal creates the tables when they are missing (schema 33 adds them to an upgraded catalog).
func OpenLocal(db *sql.DB) (*Local, error) {
	if _, err := db.Exec(schema.RelayedTables); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema.RelayGrantTables); err != nil {
		return nil, err
	}
	if err := schema.RelayGrantColumns(db); err != nil {
		return nil, err
	}
	return &Local{db: db, Now: time.Now}, nil
}

func (l *Local) now() int64 { return l.Now().Unix() }

// LocalState is this computer's inbox and pull state for the linked server.
type LocalState struct {
	Origin     string           `json:"origin"`
	Configured bool             `json:"configured"` // the key holder has set up an inbox on this computer
	Inbox      Inbox            `json:"inbox"`
	Hosts      map[string]int64 `json:"hosts"` // label -> host id, this computer only
	Published  bool             `json:"published"`
	Cursor     int64            `json:"cursor"`
	PolledAt   *time.Time       `json:"polledAt,omitempty"`
	AnsweredAt *time.Time       `json:"answeredAt,omitempty"`
	Failures   int              `json:"failures"`
	Message    string           `json:"message"`
}

// State returns the inbox and pull state; a row recorded for another server keeps the inbox the key holder typed, but its cursor
// starts again and the inbox is published again.
func (l *Local) State(origin string) (LocalState, error) {
	st := LocalState{Origin: origin, Inbox: Inbox{Labels: []Label{}}, Hosts: map[string]int64{}}
	var o, inbox, hosts string
	var pub bool
	var polled, answered sql.NullInt64
	err := l.db.QueryRow(`SELECT origin, inbox, hosts, published, cursor, polled_at, answered_at, failures, message FROM relay_local WHERE id=1`).
		Scan(&o, &inbox, &hosts, &pub, &st.Cursor, &polled, &answered, &st.Failures, &st.Message)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Configured = true
	_ = json.Unmarshal([]byte(inbox), &st.Inbox)
	_ = json.Unmarshal([]byte(hosts), &st.Hosts)
	if st.Inbox.Labels == nil {
		st.Inbox.Labels = []Label{}
	}
	if st.Hosts == nil {
		st.Hosts = map[string]int64{}
	}
	if o != origin {
		st.Cursor, st.Failures, st.Message = 0, 0, ""
		return st, nil
	}
	st.Published, st.PolledAt, st.AnsweredAt = pub, ptime(polled), ptime(answered)
	return st, nil
}

// SetInbox records the inbox the key holder typed and its label-to-host map; it is published at the next pull.
func (l *Local) SetInbox(origin string, in Inbox, hosts map[string]int64) error {
	ib, _ := json.Marshal(in)
	hb, _ := json.Marshal(hosts)
	_, err := l.db.Exec(`INSERT INTO relay_local(id, origin, inbox, hosts, published, cursor) VALUES(1,?,?,?,0,0)
		ON CONFLICT(id) DO UPDATE SET inbox=excluded.inbox, hosts=excluded.hosts, published=0,
		cursor=CASE WHEN relay_local.origin=excluded.origin THEN relay_local.cursor ELSE 0 END, origin=excluded.origin`, origin, string(ib), string(hb))
	return err
}

// MarkPublished records that the server accepted the inbox as it is now.
func (l *Local) MarkPublished(origin string) error {
	_, err := l.db.Exec(`UPDATE relay_local SET published=1 WHERE id=1 AND origin=?`, origin)
	return err
}

// Answered records a pull the server answered: the new cursor, and the backoff reset.
func (l *Local) Answered(origin string, cursor int64) error {
	now := l.now()
	_, err := l.db.Exec(`UPDATE relay_local SET cursor=?, polled_at=?, answered_at=?, failures=0, message='' WHERE id=1 AND origin=?`, cursor, now, now, origin)
	return err
}

// Failed records a pull the server did not answer (or answered with something refused whole), for the backoff.
func (l *Local) Failed(origin, msg string) error {
	_, err := l.db.Exec(`UPDATE relay_local SET polled_at=?, failures=failures+1, message=? WHERE id=1 AND origin=?`, l.now(), msg, origin)
	return err
}

// Mirrored is one request as this computer holds it.
type Mirrored struct {
	ID          int64       `json:"id"`
	ServerID    int64       `json:"serverId"`
	Request     Request     `json:"request"`
	ServerState string      `json:"serverState"`
	LocalState  string      `json:"localState"`
	Refusal     string      `json:"refusal"`
	JobID       *int64      `json:"jobId,omitempty"`
	SweepID     *int64      `json:"sweepId,omitempty"`
	BaseRunID   *int64      `json:"baseRunId,omitempty"`
	BaseChosen  bool        `json:"baseChosen"`
	Draft       DraftValues `json:"draft"`
	ScriptHash  string      `json:"scriptHash"`
	AutoPush    bool        `json:"autoPush"`
	RunHash     string      `json:"runHash"`
	Seq         int64       `json:"seq"`
	Reported    string      `json:"reported"`
	// GrantID is the standing approval the request was submitted under (ADR 0074), set once used; GrantState is what this computer
	// decided about the grant the request named ("" not yet, waiting, pending, used, manual) and GrantNote why it waits for you.
	GrantID     string      `json:"grantId"`
	GrantState  string      `json:"grantState"`
	GrantNote   string      `json:"grantNote"`
	ReceivedAt  time.Time   `json:"receivedAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	origin      string
	workspaceID workspace.ID
}

const mirrorCols = `id, workspace_id, origin, server_id, request, server_state, local_state, refusal, job_id, sweep_id, base_run_id, base_chosen, draft,
	script_hash, auto_push, run_hash, seq, reported, received_at, updated_at, grant_id, grant_state, grant_note`

func scanMirror(row interface{ Scan(...any) error }) (Mirrored, error) {
	var m Mirrored
	var raw, draft string
	var job, sweep, base sql.NullInt64
	var rec, upd int64
	err := row.Scan(&m.ID, &m.workspaceID, &m.origin, &m.ServerID, &raw, &m.ServerState, &m.LocalState, &m.Refusal, &job, &sweep, &base, &m.BaseChosen, &draft,
		&m.ScriptHash, &m.AutoPush, &m.RunHash, &m.Seq, &m.Reported, &rec, &upd, &m.GrantID, &m.GrantState, &m.GrantNote)
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(raw), &m.Request)
	_ = json.Unmarshal([]byte(draft), &m.Draft)
	for _, p := range []struct {
		v   sql.NullInt64
		dst **int64
	}{{job, &m.JobID}, {sweep, &m.SweepID}, {base, &m.BaseRunID}} {
		if p.v.Valid {
			x := p.v.Int64
			*p.dst = &x
		}
	}
	m.ReceivedAt, m.UpdatedAt = time.Unix(rec, 0).UTC(), time.Unix(upd, 0).UTC()
	return m, nil
}

// Origin is the server the request came from.
func (m Mirrored) Origin() string { return m.origin }

// Get returns one mirrored request.
func (l *Local) Get(id int64) (Mirrored, error) {
	m, err := scanMirror(l.db.QueryRow(`SELECT `+mirrorCols+` FROM relayed_requests WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// List returns the requests from the linked server, newest first.
func (l *Local) List(origin string) ([]Mirrored, error) {
	return l.query(`SELECT `+mirrorCols+` FROM relayed_requests WHERE origin=? ORDER BY id DESC LIMIT 500`, origin)
}

// Reportable are the approved requests whose status may still change.
func (l *Local) Reportable(origin string) ([]Mirrored, error) {
	return l.query(`SELECT `+mirrorCols+` FROM relayed_requests WHERE origin=? AND local_state=? ORDER BY id`, origin, LocalApproved)
}

// Pending are requests to tell the server about: received ones whose draft is rendered but the review not yet confirmed, and
// refused ones not yet declined there.
func (l *Local) Pending(origin string) ([]Mirrored, error) {
	return l.query(`SELECT `+mirrorCols+` FROM relayed_requests WHERE origin=? AND ((local_state=? AND server_state='requested') OR (local_state=? AND server_state IN ('requested','waiting_review'))) ORDER BY id`,
		origin, LocalDraft, LocalRefused)
}

func (l *Local) query(q string, args ...any) ([]Mirrored, error) {
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mirrored{}
	for rows.Next() {
		m, err := scanMirror(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Bounds on what one group server can make this computer store: a compromised server can invent request ids at will.
const (
	// MaxRefusedKept refused requests keep their content; past it a refused request is stored as its id, state and reason only.
	MaxRefusedKept = 200
	// MaxRowsLocal is the most requests of one server this computer stores in any state; past it a pass stops with ErrFull.
	MaxRowsLocal = 5000
)

// ErrFull is a server that sent more requests than this computer keeps.
var ErrFull = fmt.Errorf("%w: this computer already stores %d requests from this group server and takes no more; ask its operator why so many arrived", ErrInvalid, MaxRowsLocal)

func (l *Local) count(origin, cond string, args ...any) int {
	var n int
	_ = l.db.QueryRow(`SELECT COUNT(*) FROM relayed_requests WHERE origin=? `+cond, append([]any{origin}, args...)...).Scan(&n)
	return n
}

func (l *Local) openCount(origin string) int {
	var n int
	_ = l.db.QueryRow(`SELECT COUNT(*) FROM relayed_requests WHERE origin=? AND local_state IN (?,?)`, origin, LocalReceived, LocalDraft).Scan(&n)
	return n
}

// Upsert records a request the server answered. A new one is stored as received, or refused with the reason this computer's
// validation gave (refusal). A known one takes the server's state and notes; its ask is fixed at the first receipt, and a
// request whose ask changed on the server since is refused, never followed. A request closed on the server before approval
// closes here. It reports whether the row is new.
func (l *Local) Upsert(origin string, req Request, refusal string) (Mirrored, bool, error) {
	now := l.now()
	old, err := scanMirror(l.db.QueryRow(`SELECT `+mirrorCols+` FROM relayed_requests WHERE origin=? AND server_id=?`, origin, req.ID))
	if errors.Is(err, sql.ErrNoRows) {
		if l.count(origin, "") >= MaxRowsLocal {
			return Mirrored{}, false, ErrFull
		}
		state := LocalReceived
		if !IsOpen(req.State) { // already approved, declined, withdrawn or expired (a relink pulls history from cursor 0)
			state, refusal = LocalClosed, ""
		} else if refusal == "" && l.openCount(origin) >= MaxOpenLocal {
			refusal = "this computer already holds 100 requests waiting for review"
		}
		if refusal != "" && state == LocalReceived {
			state = LocalRefused
		}
		b, _ := json.Marshal(req)
		if state == LocalRefused && l.count(origin, "AND local_state=?", LocalRefused) >= MaxRefusedKept {
			b, _ = json.Marshal(Request{ID: req.ID, Change: req.Change, State: req.State, Notes: []FollowUp{}})
			refusal += fmt.Sprintf(" (stored without its content: this computer keeps the content of %d refused requests)", MaxRefusedKept)
		}
		res, err := l.db.Exec(`INSERT INTO relayed_requests(workspace_id, origin, server_id, request, server_state, local_state, refusal, received_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, workspace.Local, origin, req.ID, string(b), req.State, state, refusal, now, now)
		if err != nil {
			return Mirrored{}, false, err
		}
		id, _ := res.LastInsertId()
		m, err := l.Get(id)
		return m, true, err
	}
	if err != nil {
		return Mirrored{}, false, err
	}
	keep := old.Request
	keep.State, keep.Change, keep.Notes, keep.ExpiresAt = req.State, req.Change, req.Notes, req.ExpiresAt
	state, why := old.LocalState, old.Refusal
	if !reflect.DeepEqual(normalAsk(old.Request.Ask), normalAsk(req.Ask)) || !reflect.DeepEqual(old.Request.Base, req.Base) || old.Request.Requester != req.Requester || old.Request.RequesterID != req.RequesterID {
		if state == LocalReceived || state == LocalDraft {
			state, why = LocalRefused, "the request changed on the server after this computer received it; it is not followed"
		}
	}
	if refusal != "" && (state == LocalReceived || state == LocalDraft) {
		state, why = LocalRefused, refusal
	}
	if !IsOpen(req.State) && (state == LocalReceived || state == LocalDraft) {
		state = LocalClosed
	}
	b, _ := json.Marshal(keep)
	if _, err := l.db.Exec(`UPDATE relayed_requests SET request=?, server_state=?, local_state=?, refusal=?, updated_at=? WHERE id=?`,
		string(b), req.State, state, why, now, old.ID); err != nil {
		return Mirrored{}, false, err
	}
	m, err := l.Get(old.ID)
	return m, false, err
}

func normalAsk(a Ask) Ask {
	if a.Params == nil {
		a.Params = map[string]string{}
	}
	if len(a.Grid) == 0 {
		a.Grid = nil
	}
	return a
}

// SetServerState records the state the server answered to one of this computer's calls.
func (l *Local) SetServerState(id int64, state string) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET server_state=?, updated_at=? WHERE id=?`, state, l.now(), id)
	return err
}

// DraftValues are what a draft was rendered with: the request's values with the key holder's changes, the resources and the label.
// They are what the claim sends as the final parameters.
type DraftValues struct {
	Params    map[string]string `json:"params"`
	Resources Resources         `json:"resources"`
	Label     string            `json:"label"`
}

// SetDraft records the job or sweep a request was rendered into, the base it started from and the values it was rendered with.
func (l *Local) SetDraft(id int64, jobID, sweepID *int64, baseRunID int64, chosen bool, d DraftValues) error {
	b, _ := json.Marshal(d)
	res, err := l.db.Exec(`UPDATE relayed_requests SET local_state=?, job_id=?, sweep_id=?, base_run_id=?, base_chosen=?, draft=?, updated_at=? WHERE id=? AND local_state IN (?,?)`,
		LocalDraft, jobID, sweepID, baseRunID, chosen, string(b), l.now(), id, LocalReceived, LocalDraft)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return refuse("this request can no longer be drafted")
	}
	return nil
}

// SetApproved records a successful claim: the hash approved and whether the run line is pushed when the run is imported.
func (l *Local) SetApproved(id int64, hash string, autoPush bool) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET local_state=?, server_state=?, script_hash=?, auto_push=?, updated_at=? WHERE id=?`,
		LocalApproved, StateApproved, hash, autoPush, l.now(), id)
	return err
}

// SetLocalState moves a request to declined, detached or closed.
func (l *Local) SetLocalState(id int64, state string) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET local_state=?, updated_at=? WHERE id=?`, state, l.now(), id)
	return err
}

// NextSeq reserves the next status sequence number of a request.
func (l *Local) NextSeq(id int64) (int64, error) {
	if _, err := l.db.Exec(`UPDATE relayed_requests SET seq=seq+1 WHERE id=?`, id); err != nil {
		return 0, err
	}
	var s int64
	err := l.db.QueryRow(`SELECT seq FROM relayed_requests WHERE id=?`, id).Scan(&s)
	return s, err
}

// SetReported records what the server accepted last (a key of state, code, counts and run line), so it is not sent again.
func (l *Local) SetReported(id int64, key string) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET reported=?, updated_at=? WHERE id=?`, key, l.now(), id)
	return err
}

// SetRunHash records the hash of the run line queued for push.
func (l *Local) SetRunHash(id int64, hash string) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET run_hash=? WHERE id=?`, hash, id)
	return err
}
