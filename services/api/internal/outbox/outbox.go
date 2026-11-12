// Package outbox is the local instance's side of the link to a group server (ADR 0066 sections 1 and 2): which server it is
// linked to, under which random instance id and origin-key secret, and the durable queue of frozen items waiting to be pushed.
// A queued item keeps the exact bytes it will send until the server confirms them, so a retry after a lost answer resends the
// same bytes and lands on the same content hash. Its pictures wait as files in a folder beside the catalog, never in rows.
//
// The tables are local-only (internal/arch): the group server never mounts the routes that reach them.
package outbox

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ErrNotLinked means no group server is linked.
var ErrNotLinked = errors.New("this computer is not linked to a group server")

// Queue states, and what the UI says for each.
const (
	Waiting = "waiting" // not confirmed yet: "waiting: <why>"
	Sent    = "sent"    // the server confirmed it: "pushed"
	Refused = "refused" // the server refused it for a reason a retry cannot change: "refused: <sentence>"
)

// Store is the link and the queue on the local catalog, and the folder queued pictures wait in.
type Store struct {
	db  *sql.DB
	dir string
	Now func() time.Time
}

// Open creates the tables when they are missing (schema 32 adds them to an upgraded catalog). dir holds queued pictures (0700).
func Open(db *sql.DB, dir string) (*Store, error) {
	if _, err := db.Exec(schema.OutboxTables); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dir, Now: time.Now}, nil
}

// Link is the linked group server.
type Link struct {
	Origin    string    `json:"origin"`
	Instance  string    `json:"instance"`
	Secret    []byte    `json:"-"`
	Workspace string    `json:"workspace"`
	LinkedAt  time.Time `json:"linkedAt"`
}

func random16() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// Link returns the linked server, or ErrNotLinked.
func (s *Store) Link() (Link, error) {
	var l Link
	var secret string
	var at int64
	err := s.db.QueryRow(`SELECT origin, instance, secret, workspace, linked_at FROM group_link WHERE id=1`).Scan(&l.Origin, &l.Instance, &secret, &l.Workspace, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotLinked
	}
	if err != nil {
		return Link{}, err
	}
	l.Secret, err = hex.DecodeString(secret)
	l.LinkedAt = time.Unix(at, 0).UTC()
	return l, err
}

// SetLink records the linked server. The first link makes a random 128-bit instance id and a 256-bit origin-key secret; linking
// again to the same origin keeps them (the lineages continue), and linking to another origin starts a new instance and empties
// the queue, so nothing frozen for one server is sent to another.
func (s *Store) SetLink(origin, workspaceName string) (Link, error) {
	old, err := s.Link()
	if err != nil && !errors.Is(err, ErrNotLinked) {
		return Link{}, err
	}
	if err == nil && old.Origin == origin {
		_, err := s.db.Exec(`UPDATE group_link SET workspace=? WHERE id=1`, workspaceName)
		old.Workspace = workspaceName
		return old, err
	}
	if err == nil {
		if err := s.clearQueue(); err != nil {
			return Link{}, err
		}
	}
	secret := append(random16(), random16()...)
	l := Link{Origin: origin, Instance: hex.EncodeToString(random16()), Secret: secret, Workspace: workspaceName, LinkedAt: time.Unix(s.Now().Unix(), 0).UTC()}
	_, err = s.db.Exec(`INSERT INTO group_link(id, origin, instance, secret, workspace, linked_at) VALUES(1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET origin=excluded.origin, instance=excluded.instance, secret=excluded.secret, workspace=excluded.workspace, linked_at=excluded.linked_at`,
		l.Origin, l.Instance, hex.EncodeToString(secret), l.Workspace, l.LinkedAt.Unix())
	return l, err
}

// Unlink empties the queue (the caller deletes the token file, which is what makes this computer unlinked). The server's address,
// the instance id, the origin-key secret and the lineages are kept, so linking again to the same server continues the same
// lineages instead of starting duplicates; linking to another server replaces them (SetLink). Local work is untouched; what was
// pushed stays on the server.
func (s *Store) Unlink() error {
	if _, err := s.db.Exec(`DELETE FROM push_queue`); err != nil {
		return err
	}
	_ = os.RemoveAll(s.dir)
	return nil
}

func (s *Store) clearQueue() error {
	if _, err := s.db.Exec(`DELETE FROM push_queue`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM push_lineage`); err != nil {
		return err
	}
	_ = os.RemoveAll(s.dir)
	return nil
}

// Entry is one queued item as the UI shows it.
type Entry struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	LocalID   int64      `json:"localId"` // this computer's id of the board, figure, run or report; never sent
	Title     string     `json:"title"`
	Hash      string     `json:"hash"`
	State     string     `json:"state"`
	Message   string     `json:"message"`
	Attempts  int        `json:"attempts"`
	NextAt    *time.Time `json:"nextAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
}

const entryCols = `id, kind, local_id, title, hash, state, message, attempts, next_at, created_at, sent_at`

func scanEntry(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var next, created int64
	var sent sql.NullInt64
	if err := sc.Scan(&e.ID, &e.Kind, &e.LocalID, &e.Title, &e.Hash, &e.State, &e.Message, &e.Attempts, &next, &created, &sent); err != nil {
		return Entry{}, err
	}
	e.CreatedAt = time.Unix(created, 0).UTC()
	if e.State == Waiting && next > 0 {
		t := time.Unix(next, 0).UTC()
		e.NextAt = &t
	}
	if sent.Valid {
		t := time.Unix(sent.Int64, 0).UTC()
		e.SentAt = &t
	}
	return e, nil
}

// Enqueue queues a frozen item: body is the exact request body, assets the cleaned pictures it shows. An item already waiting
// with the same hash, or the last confirmed push of the same object with the same hash, is answered as it is: nothing is queued
// twice. A newer freeze of the same object replaces one still waiting (the server would supersede it at once anyway).
func (s *Store) Enqueue(ws workspace.ID, kind string, localID int64, origin, title, hash string, body []byte, assets map[string][]byte) (Entry, error) {
	// only the object's newest entry counts: content put back to an earlier version (A, B, then A) is queued again, so the server
	// makes A current again; the same content as the newest waiting or confirmed entry is that entry
	var id int64
	var state, last string
	err := s.db.QueryRow(`SELECT id, state, hash FROM push_queue WHERE workspace_id=? AND kind=? AND local_id=? AND state IN ('waiting','sent') ORDER BY id DESC LIMIT 1`, ws, kind, localID).Scan(&id, &state, &last)
	if err == nil && last == hash {
		return s.entry(id)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Entry{}, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Entry{}, err
	}
	shas := []string{}
	for sha, b := range assets {
		p := filepath.Join(s.dir, sha)
		if _, err := os.Stat(p); err != nil {
			if err := os.WriteFile(p, b, 0o600); err != nil {
				return Entry{}, err
			}
		}
		shas = append(shas, sha)
	}
	list, _ := json.Marshal(shas)
	now := s.Now().Unix()
	if _, err := s.db.Exec(`DELETE FROM push_queue WHERE workspace_id=? AND kind=? AND local_id=? AND state='waiting'`, ws, kind, localID); err != nil {
		return Entry{}, err
	}
	res, err := s.db.Exec(`INSERT INTO push_queue(workspace_id,kind,local_id,origin,hash,title,body,assets,state,message,created_at,next_at) VALUES(?,?,?,?,?,?,?,?,'waiting','queued',?,?)`,
		ws, kind, localID, origin, hash, title, string(body), string(list), now, now)
	if err != nil {
		return Entry{}, err
	}
	id, _ = res.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO push_lineage(origin, workspace_id, kind, local_id) VALUES(?,?,?,?) ON CONFLICT(origin) DO NOTHING`, origin, ws, kind, localID); err != nil {
		return Entry{}, err
	}
	s.prune()
	return s.entry(id)
}

func (s *Store) entry(id int64) (Entry, error) {
	return scanEntry(s.db.QueryRow(`SELECT `+entryCols+` FROM push_queue WHERE id=?`, id))
}

// List returns the queue of ws, newest first (at most 200).
func (s *Store) List(ws workspace.ID) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT `+entryCols+` FROM push_queue WHERE workspace_id=? ORDER BY id DESC LIMIT 200`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Last returns the newest entry of one local object, if any.
func (s *Store) Last(ws workspace.ID, kind string, localID int64) (Entry, bool) {
	e, err := scanEntry(s.db.QueryRow(`SELECT `+entryCols+` FROM push_queue WHERE workspace_id=? AND kind=? AND local_id=? ORDER BY id DESC LIMIT 1`, ws, kind, localID))
	return e, err == nil
}

// Due is one waiting item ready to send: its frozen body and the hashes of its pictures.
type Due struct {
	Entry
	Body   []byte
	Assets []string
}

// Due lists the waiting items whose time has come, oldest first. The rows are read and closed before anything is sent.
func (s *Store) Due(now time.Time) ([]Due, error) {
	rows, err := s.db.Query(`SELECT `+entryCols+`, body, assets FROM push_queue WHERE state='waiting' AND next_at<=? ORDER BY id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var d Due
		var next, created int64
		var sent sql.NullInt64
		var body, assets string
		if err := rows.Scan(&d.ID, &d.Kind, &d.LocalID, &d.Title, &d.Hash, &d.State, &d.Message, &d.Attempts, &next, &created, &sent, &body, &assets); err != nil {
			return nil, err
		}
		d.Body = []byte(body)
		_ = json.Unmarshal([]byte(assets), &d.Assets)
		out = append(out, d)
	}
	return out, rows.Err()
}

// NextWake is the earliest time a waiting item is due, or zero when none waits.
func (s *Store) NextWake() time.Time {
	var n sql.NullInt64
	_ = s.db.QueryRow(`SELECT MIN(next_at) FROM push_queue WHERE state='waiting'`).Scan(&n)
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0)
}

// Asset returns a queued picture's bytes.
func (s *Store) Asset(sha string) ([]byte, error) {
	if len(sha) != 64 {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.dir, sha))
}

// MarkSent records the server's confirmation and the new head of the object's lineage.
func (s *Store) MarkSent(id int64, serverID int64) error {
	now := s.Now().Unix()
	if _, err := s.db.Exec(`UPDATE push_queue SET state='sent', message='', sent_at=?, server_id=? WHERE id=? AND state='waiting'`, now, serverID, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE push_lineage SET head_hash=(SELECT hash FROM push_queue WHERE id=?) WHERE origin=(SELECT origin FROM push_queue WHERE id=?)`, id, id); err != nil {
		return err
	}
	s.prune()
	return nil
}

// MarkRefused stops an item with the server's sentence; it is never retried.
func (s *Store) MarkRefused(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE push_queue SET state='refused', message=? WHERE id=? AND state='waiting'`, msg, id)
	s.prune()
	return err
}

// MarkRetry keeps an item waiting with why, and when it is tried again.
func (s *Store) MarkRetry(id int64, msg string, next time.Time) error {
	_, err := s.db.Exec(`UPDATE push_queue SET message=?, attempts=attempts+1, next_at=? WHERE id=? AND state='waiting'`, msg, next.Unix(), id)
	return err
}

// ResetWaits makes every waiting item due now: a success, or the person asking, resets the backoff (ADR 0066 retries).
func (s *Store) ResetWaits() error {
	_, err := s.db.Exec(`UPDATE push_queue SET next_at=? WHERE state='waiting' AND next_at>?`, s.Now().Unix(), s.Now().Unix())
	return err
}

// Origin returns the local object an origin key stands for (slice 5 resolves a relayed request's base through it).
func (s *Store) Origin(origin string) (kind string, localID int64, err error) {
	err = s.db.QueryRow(`SELECT kind, local_id FROM push_lineage WHERE origin=?`, origin).Scan(&kind, &localID)
	return
}

// prune deletes queued pictures no waiting item still needs.
func (s *Store) prune() {
	rows, err := s.db.Query(`SELECT assets FROM push_queue WHERE state='waiting'`)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for rows.Next() {
		var a string
		var list []string
		if rows.Scan(&a) == nil && json.Unmarshal([]byte(a), &list) == nil {
			for _, x := range list {
				keep[x] = true
			}
		}
	}
	rows.Close()
	ents, _ := os.ReadDir(s.dir)
	for _, e := range ents {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}
