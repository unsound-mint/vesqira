package relay

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The group server's store of inboxes and requests. Every statement names the workspace (internal/arch guard 5); every
// transition is one guarded UPDATE, so of two racing callers exactly one wins, and the server clock decides expiry inside the
// same statement that claims or withdraws (a claim never depends on an expiry sweep having run).

var (
	ErrNotFound = errors.New("not found")
	// ErrNoInbox: the addressee publishes no inbox in this workspace, or it is paused.
	ErrNoInbox = errors.New("this person does not take requests in this workspace now")
)

// LimitError is a rate limit refusal.
type LimitError struct{ Msg string }

func (e LimitError) Error() string { return e.Msg }

// ConflictError is a transition the request's state does not allow; State is the state it is in.
type ConflictError struct {
	State string
	Msg   string
}

func (e ConflictError) Error() string { return e.Msg }

// Store is the group server's side.
type Store struct {
	db  *sql.DB
	Now func() time.Time
}

// Open creates the tables when they are missing (schema 33 adds them to an upgraded catalog).
func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.RelayTables); err != nil {
		return nil, err
	}
	return &Store{db: db, Now: time.Now}, nil
}

func (s *Store) now() int64 { return s.Now().Unix() }

// Actor is who made a transition and how: an account through a session, a token (with its id and the instance it named), or
// the server clock.
type Actor struct {
	ID       string
	Via      string // session | token | clock
	TokenID  string
	Instance string
}

// InboxRow is a published inbox.
type InboxRow struct {
	Account   string     `json:"account"`
	Inbox     Inbox      `json:"inbox"`
	UpdatedAt time.Time  `json:"updatedAt"`
	PolledAt  *time.Time `json:"polledAt,omitempty"`
}

// PutInbox records what a key holder's computer publishes (the caller has run CheckInbox).
func (s *Store) PutInbox(ws workspace.ID, account string, a Actor, in Inbox) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO relay_inboxes(workspace_id, account_id, inbox, paused, instance, token_id, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, account_id) DO UPDATE SET inbox=excluded.inbox, paused=excluded.paused, instance=excluded.instance,
		token_id=excluded.token_id, updated_at=excluded.updated_at`, ws, account, string(b), in.Paused, a.Instance, a.TokenID, s.now())
	return err
}

func scanInbox(row interface{ Scan(...any) error }) (InboxRow, error) {
	var r InboxRow
	var raw string
	var upd int64
	var polled sql.NullInt64
	if err := row.Scan(&r.Account, &raw, &upd, &polled); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(raw), &r.Inbox); err != nil {
		return r, err
	}
	r.UpdatedAt = time.Unix(upd, 0).UTC()
	if polled.Valid {
		t := time.Unix(polled.Int64, 0).UTC()
		r.PolledAt = &t
	}
	return r, nil
}

// Inboxes lists the workspace's published inboxes.
func (s *Store) Inboxes(ws workspace.ID) ([]InboxRow, error) {
	rows, err := s.db.Query(`SELECT account_id, inbox, updated_at, polled_at FROM relay_inboxes WHERE workspace_id=? ORDER BY updated_at DESC`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxRow{}
	for rows.Next() {
		r, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InboxOf returns one account's inbox in the workspace.
func (s *Store) InboxOf(ws workspace.ID, account string) (InboxRow, error) {
	r, err := scanInbox(s.db.QueryRow(`SELECT account_id, inbox, updated_at, polled_at FROM relay_inboxes WHERE workspace_id=? AND account_id=?`, ws, account))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Approval is the approval record a claim leaves.
type Approval struct {
	Instance    string            `json:"-"`
	ScriptHash  string            `json:"scriptHash,omitempty"`
	SweepHash   string            `json:"sweepHash,omitempty"`
	Kept        []Combo           `json:"kept,omitempty"`
	Concurrency int               `json:"concurrency,omitempty"`
	Params      map[string]string `json:"params"`
	Resources   Resources         `json:"resources"`
	Label       string            `json:"label"`
	BaseChosen  bool              `json:"baseChosen"`
	EditedLines int               `json:"editedLines"`
	AutoPush    bool              `json:"autoPush"`
	At          time.Time         `json:"at"`
}

// Row is one request as the server keeps it.
type Row struct {
	ID          int64          `json:"id"`
	Requester   string         `json:"requester"`
	Addressee   string         `json:"addressee"`
	Ask         Ask            `json:"ask"`
	BaseItemID  *int64         `json:"baseItemId,omitempty"`
	State       string         `json:"state"`
	Change      int64          `json:"-"`
	Seq         int64          `json:"-"`
	CreatedAt   time.Time      `json:"createdAt"`
	ExpiresAt   time.Time      `json:"expiresAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	Approval    *Approval      `json:"approval,omitempty"`
	Code        string         `json:"code"`
	Exit        string         `json:"exit"`
	Counts      map[string]int `json:"counts"`
	RunHash     string         `json:"runHash"`
	SubmittedAt *time.Time     `json:"submittedAt,omitempty"`
	RunningAt   *time.Time     `json:"runningAt,omitempty"`
	EndedAt     *time.Time     `json:"endedAt,omitempty"`
	ReportedAt  *time.Time     `json:"reportedAt,omitempty"`
	DeclineNote string         `json:"declineNote"`
	ClosedNote  string         `json:"closedNote"`
}

const rowCols = `id, created_by, addressee, label, template, params, resources, grid, base_item_id, note, state, change, seq, created_at,
	expires_at, updated_at, claim_instance, script_hash, sweep_hash, approved, approved_at, code, exit_code, counts, run_hash,
	submitted_at, running_at, ended_at, reported_at, decline_note, closed_note`

func ptime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

func scanRow(row interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var params, res, grid, approved, counts, inst, sh, swh string
	var base, apAt, sub, run, end, rep sql.NullInt64
	var created, expires, updated int64
	err := row.Scan(&r.ID, &r.Requester, &r.Addressee, &r.Ask.Label, &r.Ask.Template, &params, &res, &grid, &base, &r.Ask.Note, &r.State,
		&r.Change, &r.Seq, &created, &expires, &updated, &inst, &sh, &swh, &approved, &apAt, &r.Code, &r.Exit, &counts, &r.RunHash,
		&sub, &run, &end, &rep, &r.DeclineNote, &r.ClosedNote)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(params), &r.Ask.Params)
	_ = json.Unmarshal([]byte(res), &r.Ask.Resources)
	_ = json.Unmarshal([]byte(grid), &r.Ask.Grid)
	_ = json.Unmarshal([]byte(counts), &r.Counts)
	if r.Ask.Params == nil {
		r.Ask.Params = map[string]string{}
	}
	if r.Ask.Grid == nil {
		r.Ask.Grid = []GridKey{}
	}
	if r.Counts == nil {
		r.Counts = map[string]int{}
	}
	if base.Valid {
		r.BaseItemID = &base.Int64
	}
	r.CreatedAt, r.ExpiresAt, r.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), time.Unix(updated, 0).UTC()
	if approved != "" {
		var a Approval
		if json.Unmarshal([]byte(approved), &a) == nil {
			a.Instance = inst
			if t := ptime(apAt); t != nil {
				a.At = *t
			}
			r.Approval = &a
		}
	}
	r.SubmittedAt, r.RunningAt, r.EndedAt, r.ReportedAt = ptime(sub), ptime(run), ptime(end), ptime(rep)
	return r, nil
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

// nextChange is the cursor value of the next transition in the workspace: one more than its highest. The writer is one connection
// in one transaction, so two transitions never share a value.
func nextChange(tx execer, ws workspace.ID) (int64, error) {
	var c int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(change), 0) + 1 FROM run_requests WHERE workspace_id=?`, ws).Scan(&c)
	return c, err
}

func (s *Store) event(tx execer, id int64, a Actor, ev, detail string) error {
	_, err := tx.Exec(`INSERT INTO run_request_events(request_id, at, actor, via, token_id, instance, event, detail) VALUES(?,?,?,?,?,?,?,?)`,
		id, s.now(), a.ID, a.Via, a.TokenID, a.Instance, ev, detail)
	return err
}

func getRow(tx execer, ws workspace.ID, id int64) (Row, error) {
	r, err := scanRow(tx.QueryRow(`SELECT `+rowCols+` FROM run_requests WHERE workspace_id=? AND id=?`, ws, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Get returns one request of the workspace.
func (s *Store) Get(ws workspace.ID, id int64) (Row, error) { return getRow(s.db, ws, id) }

// List returns the workspace's requests, newest first (at most 500).
func (s *Store) List(ws workspace.ID) ([]Row, error) {
	if err := s.ExpireDue(ws); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM run_requests WHERE workspace_id=? ORDER BY id DESC LIMIT 500`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BaseRef is the pushed run a request names as its base, as the store finds it in the workspace.
type BaseRef struct {
	Origin, Hash, Title, PushedBy, Instance string
}

// BaseOf finds a pushed run item of the workspace (any lineage head or superseded version; archived ones too, which stay readable).
func (s *Store) BaseOf(ws workspace.ID, itemID int64) (BaseRef, error) {
	var b BaseRef
	err := s.db.QueryRow(`SELECT origin, hash, title, created_by, instance FROM pushed_items WHERE workspace_id=? AND id=? AND kind='run'`, ws, itemID).
		Scan(&b.Origin, &b.Hash, &b.Title, &b.PushedBy, &b.Instance)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// RunItem finds the pushed run line a request's results became, by its hash, among what the addressee pushed to the workspace.
func (s *Store) RunItem(ws workspace.ID, addressee, hash string) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM pushed_items WHERE workspace_id=? AND hash=? AND kind='run' AND created_by=?`, ws, hash, addressee).Scan(&id)
	return id, err == nil
}

// File stores a new request after checking it against the addressee's inbox (not paused) and the rate limits, in one transaction.
func (s *Store) File(ws workspace.ID, requester, addressee string, ask Ask, baseItemID *int64, days int, a Actor) (Row, error) {
	if days == 0 {
		days = DefaultExpiryDays
	}
	if days < 1 || days > MaxExpiryDays {
		return Row{}, refuse("a request expires after 1 to %d days", MaxExpiryDays)
	}
	if ask.Params == nil {
		ask.Params = map[string]string{}
	}
	if ask.Grid == nil {
		ask.Grid = []GridKey{}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, err
	}
	defer tx.Rollback()
	in, err := scanInbox(tx.QueryRow(`SELECT account_id, inbox, updated_at, polled_at FROM relay_inboxes WHERE workspace_id=? AND account_id=?`, ws, addressee))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && in.Inbox.Paused) {
		return Row{}, ErrNoInbox
	}
	if err != nil {
		return Row{}, err
	}
	if err := CheckAsk(in.Inbox, ask); err != nil {
		return Row{}, err
	}
	now := s.now()
	var open, day int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM run_requests WHERE workspace_id=? AND created_by=? AND addressee=? AND state IN ('requested','waiting_review') AND expires_at>?`,
		ws, requester, addressee, now).Scan(&open); err != nil {
		return Row{}, err
	}
	if open >= MaxOpenPerPair {
		return Row{}, LimitError{fmt.Sprintf("you have %d open requests to this person; wait for an answer or withdraw one", open)}
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM run_requests WHERE workspace_id=? AND created_by=? AND created_at>?`, ws, requester, now-86400).Scan(&day); err != nil {
		return Row{}, err
	}
	if day >= MaxFiledPerDay {
		return Row{}, LimitError{fmt.Sprintf("at most %d requests a day; try again tomorrow", MaxFiledPerDay)}
	}
	ch, err := nextChange(tx, ws)
	if err != nil {
		return Row{}, err
	}
	params, _ := json.Marshal(ask.Params)
	res, _ := json.Marshal(ask.Resources)
	grid, _ := json.Marshal(ask.Grid)
	r, err := tx.Exec(`INSERT INTO run_requests(workspace_id, created_by, addressee, label, template, params, resources, grid, base_item_id, note, state,
		change, created_at, expires_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ws, requester, addressee, ask.Label, ask.Template, string(params), string(res), string(grid), baseItemID, ask.Note, StateRequested,
		ch, now, now+int64(days)*86400, now)
	if err != nil {
		return Row{}, err
	}
	id, _ := r.LastInsertId()
	if err := s.event(tx, id, a, "filed", fmt.Sprintf("%s on %q, expires in %d days", ask.Template, ask.Label, days)); err != nil {
		return Row{}, err
	}
	row, err := getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	return row, tx.Commit()
}

// ExpireDue marks the open requests of the workspace whose expiry has passed as expired, each with its own cursor value, so the
// key holder's next pull says so. Claims and withdrawals check the clock themselves; this only makes the state visible.
func (s *Store) ExpireDue(ws workspace.ID) error {
	now := s.now()
	rows, err := s.db.Query(`SELECT id FROM run_requests WHERE workspace_id=? AND state IN ('requested','waiting_review') AND expires_at<=?`, ws, now)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := s.expire(ws, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) expire(ws workspace.ID, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := expireIn(s, tx, ws, id); err != nil {
		return err
	}
	return tx.Commit()
}

// expireIn expires one open request whose time has passed, inside tx. It reports whether it did.
func expireIn(s *Store, tx execer, ws workspace.ID, id int64) (bool, error) {
	ch, err := nextChange(tx, ws)
	if err != nil {
		return false, err
	}
	now := s.now()
	res, err := tx.Exec(`UPDATE run_requests SET state=?, change=?, updated_at=? WHERE workspace_id=? AND id=? AND state IN ('requested','waiting_review') AND expires_at<=?`,
		StateExpired, ch, now, ws, id, now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, s.event(tx, id, Actor{ID: "system", Via: "clock"}, "expired", "")
}

// Page is the pull: requests addressed to the account in the workspace whose change is after the cursor, in change order, at most
// MaxPage. It records the poll on the inbox (shown as "<name>'s computer last checked at").
func (s *Store) Page(ws workspace.ID, addressee string, after int64) ([]Row, error) {
	if err := s.ExpireDue(ws); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE relay_inboxes SET polled_at=? WHERE workspace_id=? AND account_id=?`, s.now(), ws, addressee); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM run_requests WHERE workspace_id=? AND addressee=? AND change>? ORDER BY change LIMIT ?`, ws, addressee, after, MaxPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Polled is when the account's computer last pulled in the workspace.
func (s *Store) Polled(ws workspace.ID, account string) *time.Time {
	var p sql.NullInt64
	if s.db.QueryRow(`SELECT polled_at FROM relay_inboxes WHERE workspace_id=? AND account_id=?`, ws, account).Scan(&p) != nil {
		return nil
	}
	return ptime(p)
}

// conflict explains why a transition did not apply, from the state the request is in now.
func conflict(r Row, now int64, what string) error {
	st := r.State
	if IsOpen(st) && r.ExpiresAt.Unix() <= now {
		st = StateExpired
	}
	msg := fmt.Sprintf("this request is %s, so it cannot be %s", words(st), what)
	return ConflictError{State: st, Msg: msg}
}

func words(state string) string {
	switch state {
	case StateReview:
		return "waiting for review"
	case StateRequested:
		return "not yet fetched by the key holder's computer"
	}
	return state
}

// Review moves a request to "waiting for review": the key holder's computer rendered its draft. Idempotent.
func (s *Store) Review(ws workspace.ID, id int64, addressee string, a Actor) (Row, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, err
	}
	defer tx.Rollback()
	r, err := getRow(tx, ws, id)
	if err != nil || r.Addressee != addressee {
		return Row{}, ErrNotFound
	}
	if r.State == StateReview {
		return r, nil
	}
	ch, err := nextChange(tx, ws)
	if err != nil {
		return Row{}, err
	}
	now := s.now()
	res, err := tx.Exec(`UPDATE run_requests SET state=?, change=?, updated_at=? WHERE workspace_id=? AND id=? AND addressee=? AND state=? AND expires_at>?`,
		StateReview, ch, now, ws, id, addressee, StateRequested, now)
	if err != nil {
		return Row{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := expireIn(s, tx, ws, id); err != nil {
			return Row{}, err
		}
		_ = tx.Commit()
		return Row{}, conflict(r, now, "reviewed")
	}
	if err := s.event(tx, id, a, "review", "draft rendered"); err != nil {
		return Row{}, err
	}
	r, err = getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	return r, tx.Commit()
}

func sameClaim(ap *Approval, c Claim) bool {
	return ap != nil && ap.Instance == c.Instance && ap.ScriptHash == c.ScriptHash && ap.SweepHash == c.SweepHash
}

// Claim is the one-winner approval: it succeeds only while the request waits for review and has not expired by the server's
// clock, in the one UPDATE that records it. The same claim again (request, hash, instance) answers the recorded one; any other
// claim on an approved request is refused.
func (s *Store) Claim(ws workspace.ID, id int64, addressee string, a Actor, c Claim) (Row, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, false, err
	}
	defer tx.Rollback()
	r, err := getRow(tx, ws, id)
	if err != nil || r.Addressee != addressee {
		return Row{}, false, ErrNotFound
	}
	if err := CheckClaim(r.Ask, c); err != nil {
		return Row{}, false, err
	}
	if r.State != StateRequested && r.State != StateReview && r.State != StateExpired && r.State != StateWithdrawn && r.State != StateDeclined {
		if sameClaim(r.Approval, c) {
			return r, false, nil
		}
		return Row{}, false, ConflictError{State: r.State, Msg: "this request was already approved with another script or from another computer"}
	}
	now := s.now()
	ap := Approval{ScriptHash: c.ScriptHash, SweepHash: c.SweepHash, Kept: c.Kept, Concurrency: c.Concurrency, Params: c.Params,
		Resources: c.Resources, Label: c.Label, BaseChosen: c.BaseChosen, EditedLines: c.EditedLines, AutoPush: c.AutoPush}
	if ap.Params == nil {
		ap.Params = map[string]string{}
	}
	apb, _ := json.Marshal(ap)
	ch, err := nextChange(tx, ws)
	if err != nil {
		return Row{}, false, err
	}
	res, err := tx.Exec(`UPDATE run_requests SET state=?, change=?, updated_at=?, claim_instance=?, script_hash=?, sweep_hash=?, approved=?, approved_at=?
		WHERE workspace_id=? AND id=? AND addressee=? AND state=? AND expires_at>?`,
		StateApproved, ch, now, c.Instance, c.ScriptHash, c.SweepHash, string(apb), now, ws, id, addressee, StateReview, now)
	if err != nil {
		return Row{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := expireIn(s, tx, ws, id); err != nil {
			return Row{}, false, err
		}
		_ = tx.Commit()
		return Row{}, false, conflict(r, now, "approved")
	}
	detail := "script sha256 " + c.ScriptHash
	if c.SweepHash != "" {
		detail = fmt.Sprintf("sweep sha256 %s, %d combinations kept, at most %d at a time", c.SweepHash, len(c.Kept), c.Concurrency)
	}
	if c.EditedLines > 0 {
		detail += fmt.Sprintf(", edited by hand: %d lines", c.EditedLines)
	}
	if err := s.event(tx, id, a, "approved", detail); err != nil {
		return Row{}, false, err
	}
	r, err = getRow(tx, ws, id)
	if err != nil {
		return Row{}, false, err
	}
	return r, true, tx.Commit()
}

// Report applies a status the key holder's computer reports, monotonically: a sequence number not above the last is ignored, a
// state below the current one is ignored (a late "running" never overwrites an end state), and an end state is final, though a
// later report may still add the counts or the pushed run line. It reports whether anything changed.
func (s *Store) Report(ws workspace.ID, id int64, addressee string, a Actor, rep Report) (Row, bool, error) {
	if err := CheckReport(rep); err != nil {
		return Row{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, false, err
	}
	defer tx.Rollback()
	r, err := getRow(tx, ws, id)
	if err != nil || r.Addressee != addressee {
		return Row{}, false, ErrNotFound
	}
	if rank(r.State) == 0 {
		return Row{}, false, conflict(r, s.now(), "reported on before it is approved")
	}
	if r.Approval == nil || r.Approval.Instance != rep.Instance {
		return Row{}, false, ConflictError{State: r.State, Msg: "only the computer that approved this request reports on it"}
	}
	if rep.Seq <= r.Seq {
		return r, false, nil
	}
	state := rep.State
	if rank(state) < rank(r.State) || (rank(r.State) == 4 && state != r.State) {
		state = r.State // ignored: never lower, never change an end
	}
	code, exit := rep.Code, rep.Exit
	if state != rep.State {
		code, exit = r.Code, r.Exit
	}
	counts := r.Counts
	if rep.Counts != nil {
		counts = rep.Counts
	}
	cb, _ := json.Marshal(counts)
	runHash := r.RunHash
	if runHash == "" && rep.RunHash != "" {
		runHash = rep.RunHash
	}
	now := s.now()
	var sub, run, end any
	if rank(state) >= 2 {
		sub = now
	}
	if rank(state) >= 3 && state != StateUnknown {
		run = now
	}
	if rank(state) == 4 {
		end = now
	}
	res, err := tx.Exec(`UPDATE run_requests SET state=?, code=?, exit_code=?, counts=?, run_hash=?, seq=?, updated_at=?, reported_at=?, reported_instance=?,
		submitted_at=COALESCE(submitted_at, ?), running_at=COALESCE(running_at, ?), ended_at=COALESCE(ended_at, ?)
		WHERE workspace_id=? AND id=? AND seq<?`,
		state, code, exit, string(cb), runHash, rep.Seq, now, now, rep.Instance, sub, run, end, ws, id, rep.Seq)
	if err != nil {
		return Row{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, false, nil
	}
	changed := state != r.State || code != r.Code || runHash != r.RunHash || string(cb) != mustJSON(r.Counts)
	if changed {
		detail := state
		if code != "" {
			detail += " (" + code + ")"
		}
		if exit != "" {
			detail += " exit " + exit
		}
		if runHash != r.RunHash {
			detail += ", run line " + runHash
		}
		if err := s.event(tx, id, a, "status", detail); err != nil {
			return Row{}, false, err
		}
	}
	r, err = getRow(tx, ws, id)
	if err != nil {
		return Row{}, false, err
	}
	return r, changed, tx.Commit()
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Decline is the addressee's no, with a note, while the request is open.
func (s *Store) Decline(ws workspace.ID, id int64, addressee string, a Actor, note string) (Row, error) {
	if err := PlainText(note, "the note"); err != nil {
		return Row{}, err
	}
	return s.close(ws, id, a, StateDeclined, "declined", note, func(r Row) bool { return r.Addressee == addressee }, false)
}

// Withdraw is the requester's (or a Maintainer's) withdrawal, only before approval and before expiry by the server's clock.
func (s *Store) Withdraw(ws workspace.ID, id int64, a Actor, any bool) (Row, error) {
	return s.close(ws, id, a, StateWithdrawn, "withdrawn", "", func(r Row) bool { return any || r.Requester == a.ID }, true)
}

// ErrNotYours: the actor may see the request but not act on it.
var ErrNotYours = errors.New("only the person who filed this request, a Maintainer or an Owner can do that")

func (s *Store) close(ws workspace.ID, id int64, a Actor, state, ev, note string, may func(Row) bool, checkClock bool) (Row, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, err
	}
	defer tx.Rollback()
	r, err := getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	if !may(r) {
		if state == StateDeclined {
			return Row{}, ErrNotFound // a relay token sees only what is addressed to its account
		}
		return Row{}, ErrNotYours
	}
	ch, err := nextChange(tx, ws)
	if err != nil {
		return Row{}, err
	}
	now := s.now()
	clock := int64(-1 << 62)
	if checkClock {
		clock = now
	}
	res, err := tx.Exec(`UPDATE run_requests SET state=?, change=?, updated_at=?, decline_note=? WHERE workspace_id=? AND id=? AND state IN ('requested','waiting_review') AND expires_at>?`,
		state, ch, now, note, ws, id, clock)
	if err != nil {
		return Row{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := expireIn(s, tx, ws, id); err != nil {
			return Row{}, err
		}
		_ = tx.Commit()
		return Row{}, conflict(r, now, ev)
	}
	if err := s.event(tx, id, a, ev, note); err != nil {
		return Row{}, err
	}
	r, err = getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	return r, tx.Commit()
}

// AddNote adds a follow-up note (the requester's or a Maintainer's), which the key holder's next pull carries.
func (s *Store) AddNote(ws workspace.ID, id int64, a Actor, any bool, text string) (Row, error) {
	if err := PlainText(text, "the note"); err != nil {
		return Row{}, err
	}
	if text == "" {
		return Row{}, refuse("write the note")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Row{}, err
	}
	defer tx.Rollback()
	r, err := getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	if !any && r.Requester != a.ID {
		return Row{}, ErrNotYours
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM run_request_events WHERE request_id=? AND event='note'`, id).Scan(&n); err != nil {
		return Row{}, err
	}
	if n >= MaxNotes {
		return Row{}, LimitError{fmt.Sprintf("a request takes at most %d follow-up notes", MaxNotes)}
	}
	ch, err := nextChange(tx, ws)
	if err != nil {
		return Row{}, err
	}
	if _, err := tx.Exec(`UPDATE run_requests SET change=?, updated_at=? WHERE workspace_id=? AND id=?`, ch, s.now(), ws, id); err != nil {
		return Row{}, err
	}
	if err := s.event(tx, id, a, "note", text); err != nil {
		return Row{}, err
	}
	r, err = getRow(tx, ws, id)
	if err != nil {
		return Row{}, err
	}
	return r, tx.Commit()
}

// Event is one entry of a request's record.
type Event struct {
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Via      string    `json:"via"`
	TokenID  string    `json:"-"`
	Instance string    `json:"-"`
	Event    string    `json:"event"`
	Detail   string    `json:"detail"`
}

// Events returns a request's record, oldest first; the caller has found the request in its workspace.
func (s *Store) Events(ws workspace.ID, id int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT e.at, e.actor, e.via, e.token_id, e.instance, e.event, e.detail FROM run_request_events e
		JOIN run_requests r ON r.id=e.request_id WHERE r.workspace_id=? AND e.request_id=? ORDER BY e.id`, ws, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&at, &e.Actor, &e.Via, &e.TokenID, &e.Instance, &e.Event, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// CloseAccount applies ADR 0066's account change when an account leaves the workspace: its inbox closes; open requests addressed
// to it are declined with the sentence given; approved ones keep their last status, marked "no further reports expected"; its
// own open requests are withdrawn.
func (s *Store) CloseAccount(ws workspace.ID, account, sentence string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM relay_inboxes WHERE workspace_id=? AND account_id=?`, ws, account); err != nil {
		return err
	}
	type move struct {
		id    int64
		state string
	}
	var moves []move
	rows, err := tx.Query(`SELECT id, CASE WHEN addressee=? THEN 'declined' ELSE 'withdrawn' END FROM run_requests
		WHERE workspace_id=? AND (addressee=? OR created_by=?) AND state IN ('requested','waiting_review')`, account, ws, account, account)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m move
		if rows.Scan(&m.id, &m.state) == nil {
			moves = append(moves, m)
		}
	}
	rows.Close()
	sys := Actor{ID: "system", Via: "membership"}
	for _, m := range moves {
		ch, err := nextChange(tx, ws)
		if err != nil {
			return err
		}
		note := ""
		if m.state == StateDeclined {
			note = sentence
		}
		if _, err := tx.Exec(`UPDATE run_requests SET state=?, change=?, updated_at=?, decline_note=? WHERE workspace_id=? AND id=?`, m.state, ch, s.now(), note, ws, m.id); err != nil {
			return err
		}
		if err := s.event(tx, m.id, sys, m.state, note); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE run_requests SET closed_note='no further reports expected' WHERE workspace_id=? AND addressee=? AND state IN ('approved','submitted','running')`, ws, account); err != nil {
		return err
	}
	return tx.Commit()
}
