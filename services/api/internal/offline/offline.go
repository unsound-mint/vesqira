// Package offline keeps the offline store (ADR 0059): bounded, derived replies of the scientific worker (a view, the header cards,
// planes at the step the researcher chose, spectra of saved regions) copied while a host was reachable, so a viewer can go on
// when the VPN drops. It never holds a raw cube. Each reply is a file under the store's directory (0600, in a 0700 folder that
// is not a source root) and an index row that records the file signature it was made from: host, size, modification time and
// the content identity when one was recorded (ADR 0053). The server serves a copy only while that signature is the last one
// seen, says that it is a copy, and never lets the reviewer listener reach it. Nothing is evicted silently: a copy that would
// pass the size limit is refused, and copies leave only by "forget", by a newer preparation of the same file, or with their dataset.
package offline

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// DefaultCap is the limit until the owner sets one: enough for a few hundred preview planes of several cubes, far from a cube.
const DefaultCap int64 = 2 << 30

var ErrNotFound = errors.New("no offline copy")

// OverCap says a copy would pass the limit; nothing was written.
type OverCap struct{ Used, Cap, Need int64 }

func (e *OverCap) Error() string {
	return fmt.Sprintf("the offline store holds %d of its %d bytes; this copy needs %d more. Forget a copy or raise the limit", e.Used, e.Cap, e.Need)
}

// Key is what a stored reply answers: the dataset, the file signature it was read from, and the same fields the plane cache
// is keyed by (operation, channel, preview step, pixel, region description, declared unit).
type Key struct {
	DatasetID, HostID, Size, MTime int64
	Op                             string
	Channel, Step, X, Y            int
	Q, Unit                        string
}

// Entry is an index row.
type Entry struct {
	Key
	ID        int64
	Identity  string // "sha256:..." or "sampled:...", empty when none was recorded when the copy was made
	Bytes     int64
	SHA256    string
	TakenAt   time.Time
	SeenSize  int64
	SeenMTime int64
	SeenAt    time.Time // zero: no live look since the copy was made
	file      string
}

// Stale reports that a live look found the file different from the one this copy was made from.
func (e Entry) Stale() bool {
	return !e.SeenAt.IsZero() && (e.SeenSize != e.Size || e.SeenMTime != e.MTime)
}

type seenKey struct{ ds, host int64 }

type Store struct {
	db  *sql.DB
	dir string

	mu   sync.Mutex // one write at a time, so the size limit is checked against what is really there
	seen sync.Map   // seenKey -> [2]int64{size, mtime}: the last signature written by NoteSeen, to skip repeated writes
}

// Open creates the tables, the 0700 directory, and removes the folder of any dataset that no longer has a row (a delete that
// was interrupted between the catalog and the files).
func Open(db *sql.DB, dir string) (*Store, error) {
	for _, q := range []string{schema.OfflineEntriesTable, schema.OfflineSettingsTable} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	s := &Store{db: db, dir: dir}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if subs, err := os.ReadDir(dir); err == nil {
		for _, d := range subs {
			id, err := strconv.ParseInt(d.Name(), 10, 64)
			if err != nil || !d.IsDir() {
				continue
			}
			var n int
			if db.QueryRow(`SELECT COUNT(*) FROM offline_entries WHERE dataset_id=?`, id).Scan(&n) == nil && n == 0 {
				_ = os.RemoveAll(filepath.Join(dir, d.Name()))
			}
		}
	}
	return s, nil
}

// Cap is the most the store may keep.
func (s *Store) Cap() int64 {
	var c int64
	if s.db.QueryRow(`SELECT cap_bytes FROM offline_settings WHERE id=1`).Scan(&c) != nil {
		return DefaultCap
	}
	return c
}

// SetCap sets the limit. Lowering it below what is kept deletes nothing; the next copy is refused until something is forgotten.
func (s *Store) SetCap(n int64) error {
	if n < 0 {
		return errors.New("invalid: the limit cannot be negative")
	}
	_, err := s.db.Exec(`INSERT INTO offline_settings(id,cap_bytes) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET cap_bytes=excluded.cap_bytes`, n)
	return err
}

// Used is the bytes of every kept file.
func (s *Store) Used() int64 {
	var n int64
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM offline_entries`).Scan(&n)
	return n
}

// Has reports whether a dataset has any copy.
func (s *Store) Has(datasetID int64) bool {
	if s == nil {
		return false
	}
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM offline_entries WHERE dataset_id=?`, datasetID).Scan(&n) == nil && n > 0
}

func (k Key) name() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%d|%s|%d|%d|%d|%d|%s|%s", k.HostID, k.Size, k.MTime, k.Op, k.Channel, k.Step, k.X, k.Y, k.Q, k.Unit)))
	return hex.EncodeToString(sum[:16]) + ".bin"
}

func (s *Store) path(rel string) string { return filepath.Join(s.dir, filepath.FromSlash(rel)) }

// Put keeps one reply. head and payload are stored as the worker returned them. A copy of the same key is replaced.
func (s *Store) Put(k Key, identity string, head, payload []byte, at time.Time) (Entry, error) {
	if s.dir == "" {
		return Entry{}, errors.New("the offline store has no directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body := make([]byte, 4, 4+len(head)+len(payload))
	binary.LittleEndian.PutUint32(body, uint32(len(head)))
	body = append(append(body, head...), payload...)
	var replaced int64
	_ = s.db.QueryRow(`SELECT COALESCE((SELECT bytes FROM offline_entries WHERE dataset_id=? AND host_id=? AND size=? AND mtime=? AND op=? AND channel=? AND step=? AND x=? AND y=? AND q=? AND unit=?),0)`,
		k.DatasetID, k.HostID, k.Size, k.MTime, k.Op, k.Channel, k.Step, k.X, k.Y, k.Q, k.Unit).Scan(&replaced)
	if used, limit := s.Used()-replaced, s.Cap(); used+int64(len(body)) > limit {
		return Entry{}, &OverCap{Used: used, Cap: limit, Need: int64(len(body))}
	}
	rel := strconv.FormatInt(k.DatasetID, 10) + "/" + k.name()
	if err := os.MkdirAll(filepath.Dir(s.path(rel)), 0o700); err != nil {
		return Entry{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path(rel)), ".tmp-*")
	if err != nil {
		return Entry{}, err
	}
	_, werr := tmp.Write(body)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.path(rel))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return Entry{}, werr
	}
	sum := sha256.Sum256(body)
	e := Entry{Key: k, Identity: identity, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), TakenAt: at.UTC(), file: rel}
	_, err = s.db.Exec(`INSERT INTO offline_entries(dataset_id,host_id,size,mtime,identity,op,channel,step,x,y,q,unit,file,bytes,sha256,taken_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dataset_id,host_id,size,mtime,op,channel,step,x,y,q,unit) DO UPDATE SET identity=excluded.identity, file=excluded.file, bytes=excluded.bytes,
		sha256=excluded.sha256, taken_at=excluded.taken_at, seen_size=0, seen_mtime=0, seen_at=0`,
		k.DatasetID, k.HostID, k.Size, k.MTime, identity, k.Op, k.Channel, k.Step, k.X, k.Y, k.Q, k.Unit, rel, e.Bytes, e.SHA256, e.TakenAt.UnixNano())
	if err != nil {
		_ = os.Remove(s.path(rel))
		return Entry{}, err
	}
	s.ForgetSeen(k.DatasetID, k.HostID)
	return e, nil
}

const entryCols = `id,dataset_id,host_id,size,mtime,identity,op,channel,step,x,y,q,unit,file,bytes,sha256,taken_at,seen_size,seen_mtime,seen_at`

func scan(r interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var at, seen int64
	err := r.Scan(&e.ID, &e.DatasetID, &e.HostID, &e.Size, &e.MTime, &e.Identity, &e.Op, &e.Channel, &e.Step, &e.X, &e.Y, &e.Q, &e.Unit, &e.file, &e.Bytes, &e.SHA256, &at, &e.SeenSize, &e.SeenMTime, &seen)
	e.TakenAt = time.Unix(0, at).UTC()
	if seen != 0 {
		e.SeenAt = time.Unix(0, seen).UTC()
	}
	return e, err
}

// Get reads one copy. The file must still be the bytes that were written; a missing or altered file removes the row and is
// reported as not found, so a damaged copy is never served.
func (s *Store) Get(k Key) (e Entry, head, payload []byte, err error) {
	e, err = scan(s.db.QueryRow(`SELECT `+entryCols+` FROM offline_entries WHERE dataset_id=? AND host_id=? AND size=? AND mtime=? AND op=? AND channel=? AND step=? AND x=? AND y=? AND q=? AND unit=?`,
		k.DatasetID, k.HostID, k.Size, k.MTime, k.Op, k.Channel, k.Step, k.X, k.Y, k.Q, k.Unit))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, nil, nil, ErrNotFound
	}
	if err != nil {
		return Entry{}, nil, nil, err
	}
	body, rerr := os.ReadFile(s.path(e.file))
	sum := sha256.Sum256(body)
	if rerr != nil || hex.EncodeToString(sum[:]) != e.SHA256 || len(body) < 4 {
		s.mu.Lock()
		_, _ = s.db.Exec(`DELETE FROM offline_entries WHERE id=?`, e.ID)
		_ = os.Remove(s.path(e.file))
		s.mu.Unlock()
		return Entry{}, nil, nil, ErrNotFound
	}
	n := int(binary.LittleEndian.Uint32(body))
	if 4+n > len(body) {
		return Entry{}, nil, nil, ErrNotFound
	}
	return e, body[4 : 4+n], body[4+n:], nil
}

// Entries lists a dataset's copies (all of them when datasetID is 0), oldest first.
func (s *Store) Entries(datasetID int64) ([]Entry, error) {
	q, args := `SELECT `+entryCols+` FROM offline_entries`, []any{}
	if datasetID != 0 {
		q, args = q+` WHERE dataset_id=?`, append(args, datasetID)
	}
	rows, err := s.db.Query(q+` ORDER BY dataset_id, op, step, channel, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Forget removes every copy of a dataset, rows first and then the files, and says how many and how many bytes.
func (s *Store) Forget(datasetID int64) (n int, freed int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM offline_entries WHERE dataset_id=?`, datasetID).Scan(&n, &freed)
	if _, err = s.db.Exec(`DELETE FROM offline_entries WHERE dataset_id=?`, datasetID); err != nil {
		return 0, 0, err
	}
	s.RemoveFiles(datasetID)
	s.seen.Range(func(k, _ any) bool {
		if k.(seenKey).ds == datasetID {
			s.seen.Delete(k)
		}
		return true
	})
	return n, freed, nil
}

// RemoveFiles removes a dataset's folder. Lifecycle calls it after the dataset's rows are gone.
func (s *Store) RemoveFiles(datasetID int64) {
	if s == nil || s.dir == "" || datasetID <= 0 {
		return
	}
	_ = os.RemoveAll(filepath.Join(s.dir, strconv.FormatInt(datasetID, 10)))
}

// Supersede removes the copies of a dataset on one host that were made from another file signature than the current one: a
// newer preparation replaces what a changed file made stale.
func (s *Store) Supersede(datasetID, hostID, size, mtime int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, file FROM offline_entries WHERE dataset_id=? AND host_id=? AND (size<>? OR mtime<>?)`, datasetID, hostID, size, mtime)
	if err != nil {
		return err
	}
	type old struct {
		id   int64
		file string
	}
	var olds []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.file); err != nil {
			rows.Close()
			return err
		}
		olds = append(olds, o)
	}
	rows.Close()
	for _, o := range olds {
		if _, err := s.db.Exec(`DELETE FROM offline_entries WHERE id=?`, o.id); err != nil {
			return err
		}
		_ = os.Remove(s.path(o.file))
	}
	return nil
}

// NoteSeen records what a live look found on the host for a dataset's file. Copies made from another signature become stale
// (they are kept, so the researcher can see that they are, but never served); copies of this signature are fresh again. The
// common case, the same signature as the last note, writes nothing.
func (s *Store) NoteSeen(datasetID, hostID, size, mtime int64) {
	if s == nil || !s.Has(datasetID) {
		return
	}
	k := seenKey{datasetID, hostID}
	if v, ok := s.seen.Load(k); ok && v.([2]int64) == [2]int64{size, mtime} {
		return
	}
	if _, err := s.db.Exec(`UPDATE offline_entries SET seen_size=?, seen_mtime=?, seen_at=? WHERE dataset_id=? AND host_id=?`, size, mtime, time.Now().UnixNano(), datasetID, hostID); err == nil {
		s.seen.Store(k, [2]int64{size, mtime})
	}
}

// ForgetSeen drops the in-memory shortcut of NoteSeen for a dataset, after its copies were replaced.
func (s *Store) ForgetSeen(datasetID, hostID int64) { s.seen.Delete(seenKey{datasetID, hostID}) }
