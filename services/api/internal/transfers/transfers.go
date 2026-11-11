// Package transfers is the queue of file and folder transfers between this computer and a host (ADR 0048). It holds only
// what must survive a restart: what was asked, the conflict decision, where the transfer is, and what was verified. The
// work itself (rsync, hashing) is done by the server's runner through internal/hosts.
package transfers

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

var (
	ErrNotFound = errors.New("not found")
	ErrState    = errors.New("the transfer is not in a state that allows this")
)

// States. queued and waiting are runnable (waiting: a retry after a failure, not before NextAt); running and verifying
// hold the host's single slot; conflict waits for the owner; the rest are final.
const (
	StateQueued    = "queued"
	StateWaiting   = "waiting"
	StateRunning   = "running"
	StateVerifying = "verifying"
	StateConflict  = "conflict"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateSkipped   = "skipped"
)

// Kinds of source.
const (
	KindFile = "file"
	KindDir  = "dir"
)

// Policies for a name that already exists at the destination. Nothing but PolicyReplace ever overwrites.
const (
	PolicyAsk      = "ask"       // an identical file counts as already there; a different one waits for the owner
	PolicySkip     = "skip"      // leave what is there, transfer nothing
	PolicyKeepBoth = "keep_both" // transfer under a new name ("name (2).ext")
	PolicyReplace  = "replace"   // the owner said to overwrite
)

// Verification outcomes. Empty until the transfer has been checked.
const (
	Verified   = "verified"
	Unverified = "unverified"
)

// Verification methods.
const (
	MethodSHA256   = "sha256"         // both ends hashed the file
	MethodChecksum = "rsync-checksum" // a folder: rsync compared every file's contents, not sha256
)

func Terminal(state string) bool {
	return state == StateDone || state == StateFailed || state == StateCancelled || state == StateSkipped
}

// Transfer is one queue entry. SourcePath is the file or folder to read (on the host for a download, here for an upload),
// DestDir the existing folder it goes into (here for a download, on the host for an upload).
type Transfer struct {
	ID          int64      `json:"id"`
	HostID      int64      `json:"hostId"`
	Direction   string     `json:"direction"`
	Kind        string     `json:"kind"` // file | dir | "" until the source has been looked at
	SourcePath  string     `json:"sourcePath"`
	DestDir     string     `json:"destDir"`
	Name        string     `json:"name"`      // wanted name at the destination
	FinalName   string     `json:"finalName"` // chosen when the first attempt starts; never changes afterwards (the partial file is keyed by it)
	DestPath    string     `json:"destPath"`  // DestDir/(FinalName or Name)
	Policy      string     `json:"policy"`
	State       string     `json:"state"`
	Detail      string     `json:"detail"`
	TotalBytes  int64      `json:"totalBytes"` // 0: not known
	DoneBytes   int64      `json:"doneBytes"`
	RateBps     int64      `json:"rateBps"`
	EtaSeconds  int64      `json:"etaSeconds"` // -1: not known
	BwlimitKBps int        `json:"bwlimitKBps"`
	Compress    bool       `json:"compress"`
	Checksum    bool       `json:"checksum"` // compare contents on the next attempt (set when a retry follows a mismatch)
	Attempts    int        `json:"attempts"`
	Fails       int        `json:"fails"` // failures in a row without progress; the backoff grows with it
	NextAt      *time.Time `json:"nextAttemptAt"`
	Warning     string     `json:"warning"`
	Verif       Verif      `json:"verification"`
	DatasetID   *int64     `json:"datasetId"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// Verif is what was checked after the bytes arrived. Status is empty until then.
type Verif struct {
	Status       string `json:"status"`
	Method       string `json:"method"`
	Note         string `json:"note"`
	SourceSHA256 string `json:"sourceSha256"`
	DestSHA256   string `json:"destSha256"`
	Bytes        int64  `json:"bytes"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.TransfersTable); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const cols = `id,host_id,direction,kind,source_path,dest_dir,name,final_name,policy,state,detail,total_bytes,done_bytes,rate_bps,eta_s,
	bwlimit_kbps,compress,checksum,attempts,fails,next_at,warning,verif_status,verif_method,verif_note,src_sha,dst_sha,verif_bytes,dataset_id,
	created_at,started_at,finished_at`

func ts(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0).UTC()
	return &t
}

func scan(row interface{ Scan(...any) error }) (Transfer, error) {
	var t Transfer
	var compress, checksum int
	var next, ds, started, finished sql.NullInt64
	var created int64
	err := row.Scan(&t.ID, &t.HostID, &t.Direction, &t.Kind, &t.SourcePath, &t.DestDir, &t.Name, &t.FinalName, &t.Policy, &t.State, &t.Detail,
		&t.TotalBytes, &t.DoneBytes, &t.RateBps, &t.EtaSeconds, &t.BwlimitKBps, &compress, &checksum, &t.Attempts, &t.Fails, &next, &t.Warning,
		&t.Verif.Status, &t.Verif.Method, &t.Verif.Note, &t.Verif.SourceSHA256, &t.Verif.DestSHA256, &t.Verif.Bytes, &ds, &created, &started, &finished)
	if err != nil {
		return Transfer{}, err
	}
	t.Compress, t.Checksum = compress != 0, checksum != 0
	t.NextAt, t.StartedAt, t.FinishedAt = ts(next), ts(started), ts(finished)
	if ds.Valid {
		t.DatasetID = &ds.Int64
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	name := t.FinalName
	if name == "" {
		name = t.Name
	}
	t.DestPath = strings.TrimRight(t.DestDir, "/") + "/" + name
	return t, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Create queues a transfer. The caller has validated and authorised its paths.
func (s *Store) Create(t Transfer) (Transfer, error) {
	if t.Policy == "" {
		t.Policy = PolicyAsk
	}
	res, err := s.db.Exec(`INSERT INTO transfers(host_id,direction,kind,source_path,dest_dir,name,policy,state,total_bytes,bwlimit_kbps,compress,warning,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.HostID, t.Direction, t.Kind, t.SourcePath, t.DestDir, t.Name, t.Policy, StateQueued, t.TotalBytes, t.BwlimitKBps,
		b2i(t.Compress), t.Warning, time.Now().Unix())
	if err != nil {
		return Transfer{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (Transfer, error) {
	t, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM transfers WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	return t, err
}

// List returns the newest first, at most limit (default 200).
func (s *Store) List(limit int) ([]Transfer, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+cols+` FROM transfers ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Transfer{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Due lists the runnable transfers whose time has come, oldest first, and Busy the hosts whose slot is taken.
func (s *Store) Due(now time.Time) ([]Transfer, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM transfers WHERE state IN ('queued','waiting') AND (next_at IS NULL OR next_at<=?) ORDER BY id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Busy() (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT host_id FROM transfers WHERE state IN ('running','verifying')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Claim moves a runnable transfer to running, counting the attempt. Only one caller wins.
func (s *Store) Claim(id int64, now time.Time) (bool, error) {
	res, err := s.db.Exec(`UPDATE transfers SET state='running', attempts=attempts+1, next_at=NULL, rate_bps=0, eta_s=-1,
		started_at=COALESCE(started_at,?) WHERE id=? AND state IN ('queued','waiting')`, now.Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Plan records what the first attempt found: the kind, the size if known, the name chosen after the conflict check. The
// final name is written once; a later attempt keeps it (the unfinished file is named after it).
func (s *Store) Plan(id int64, kind string, total int64, finalName string) error {
	_, err := s.db.Exec(`UPDATE transfers SET kind=?, total_bytes=CASE WHEN ?>0 THEN ? ELSE total_bytes END, final_name=CASE WHEN final_name='' THEN ? ELSE final_name END WHERE id=?`,
		kind, total, total, finalName, id)
	return err
}

func (s *Store) SetWarning(id int64, w string) error {
	_, err := s.db.Exec(`UPDATE transfers SET warning=? WHERE id=?`, w, id)
	return err
}

// Progress stores a reading while running. It never touches a transfer that is no longer running.
func (s *Store) Progress(id, done, total, rate, eta int64) error {
	_, err := s.db.Exec(`UPDATE transfers SET done_bytes=?, total_bytes=CASE WHEN ?>0 THEN ? ELSE total_bytes END, rate_bps=?, eta_s=? WHERE id=? AND state='running'`,
		done, total, total, rate, eta, id)
	return err
}

// Move changes the state with a detail, only from one of the given states. It reports whether it did.
func (s *Store) Move(id int64, from []string, to, detail string) (bool, error) {
	return s.update(id, from, to, detail, nil, -1)
}

// Retry parks a failed attempt until next. fails is the new count in a row.
func (s *Store) Retry(id int64, from []string, next time.Time, fails int, detail string) (bool, error) {
	return s.update(id, from, StateWaiting, detail, &next, fails)
}

func (s *Store) update(id int64, from []string, to, detail string, next *time.Time, fails int) (bool, error) {
	q := `UPDATE transfers SET state=?, detail=?`
	args := []any{to, detail}
	if next != nil {
		q += `, next_at=?`
		args = append(args, next.Unix())
	}
	if fails >= 0 {
		q += `, fails=?`
		args = append(args, fails)
	}
	if Terminal(to) {
		q += `, finished_at=?, rate_bps=0, eta_s=-1`
		args = append(args, time.Now().Unix())
	}
	q += ` WHERE id=? AND state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(from)), ",") + `)`
	args = append(args, id)
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Finish ends a transfer (done, failed, skipped, cancelled) with its verification, from the given states.
func (s *Store) Finish(id int64, from []string, to, detail string, v Verif) (bool, error) {
	ok, err := s.update(id, from, to, detail, nil, 0)
	if err != nil || !ok {
		return ok, err
	}
	_, err = s.db.Exec(`UPDATE transfers SET verif_status=?, verif_method=?, verif_note=?, src_sha=?, dst_sha=?, verif_bytes=? WHERE id=?`,
		v.Status, v.Method, v.Note, v.SourceSHA256, v.DestSHA256, v.Bytes, id)
	return true, err
}

// SetPolicy records the owner's decision on a conflict and queues the transfer again.
func (s *Store) SetPolicy(id int64, policy string) (bool, error) {
	res, err := s.db.Exec(`UPDATE transfers SET policy=?, state='queued', detail='', next_at=NULL WHERE id=? AND state='conflict'`, policy, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Requeue puts a failed or cancelled transfer (or a finished one that could not be verified) back in the queue; the
// partial file, if any, is resumed. With checksum the next attempt compares contents instead of trusting size and time.
func (s *Store) Requeue(id int64, checksum bool) (bool, error) {
	res, err := s.db.Exec(`UPDATE transfers SET state='queued', detail='', next_at=NULL, fails=0, checksum=?, finished_at=NULL,
		verif_status='', verif_method='', verif_note='', src_sha='', dst_sha='', verif_bytes=0
		WHERE id=? AND (state IN ('failed','cancelled') OR (state='done' AND verif_status='unverified'))`, b2i(checksum), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Recover is run once at start: a transfer that was running or verifying when the app closed goes back to the queue, to
// resume from its partial file. It returns how many.
func (s *Store) Recover() (int, error) {
	res, err := s.db.Exec(`UPDATE transfers SET state='queued', next_at=NULL, rate_bps=0, eta_s=-1,
		detail='Vesqira was closed during this transfer; it resumes from the partial file' WHERE state IN ('running','verifying')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LinkDataset records the dataset a finished download was registered as.
func (s *Store) LinkDataset(id, dataset int64) error {
	_, err := s.db.Exec(`UPDATE transfers SET dataset_id=? WHERE id=?`, dataset, id)
	return err
}

// Remove forgets a finished transfer's record. Files are never touched.
func (s *Store) Remove(id int64) error {
	res, err := s.db.Exec(`DELETE FROM transfers WHERE id=? AND state IN ('done','failed','cancelled','skipped')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := s.Get(id); gerr != nil {
			return gerr
		}
		return ErrState
	}
	return nil
}
