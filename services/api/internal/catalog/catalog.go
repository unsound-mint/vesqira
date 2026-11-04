// Package catalog stores dataset references and the header facts the worker declared.
// Scientific data stays in the original files; only references and metadata live in SQLite.
package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Dataset struct {
	ID         int64           `json:"id"`
	Path       string          `json:"path"`
	Name       string          `json:"name"`
	SizeBytes  int64           `json:"sizeBytes"`
	ModifiedAt time.Time       `json:"modifiedAt"`
	AddedAt    time.Time       `json:"addedAt"`
	Header     json.RawMessage `json:"header"`
	// HostID is 0 for a file on this machine, otherwise the remote host the file lives on (see package hosts).
	HostID int64 `json:"hostId"`
	// Interpretation is what the user declared about the data. The file itself declares none of it.
	Interpretation *Interpretation `json:"interpretation"`
}

// Interpretation is a user-declared overlay on a dataset (never inferred from values, never written to the file).
type Interpretation struct {
	Kind          string    `json:"kind"` // "axial_orientation"
	Unit          string    `json:"unit"` // "deg" or "rad", required
	ReliabilityID *int64    `json:"reliabilityId"`
	DeclaredBy    string    `json:"declaredBy"` // "user" or the name of an adapter, e.g. "vgt-run-adapter"
	DeclaredAt    time.Time `json:"declaredAt"`
	// Stale is true when the file's registered size or modification time differs from what it was when the user
	// made the declaration: the declaration may no longer describe these bytes.
	Stale bool `json:"stale"`
}

type Store struct{ db *sql.DB }

var ErrNotFound = errors.New("dataset not found")

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS datasets(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		modified_at INTEGER NOT NULL, -- Unix nanoseconds
		added_at INTEGER NOT NULL,
		header TEXT NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS interpretations(
			dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			unit TEXT NOT NULL,
			reliability_id INTEGER,
			declared_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			modified_ns INTEGER NOT NULL DEFAULT 0)`)
	}
	if err == nil {
		// older catalogs lack the column that records who made a declaration; adding it twice is harmless
		_, _ = db.Exec(`ALTER TABLE interpretations ADD COLUMN declared_by TEXT NOT NULL DEFAULT 'user'`)
		_, _ = db.Exec(`ALTER TABLE datasets ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the shared SQLite handle so other stores (figures, boards, review) live in the same file.
func (s *Store) DB() *sql.DB { return s.db }

// Upsert registers a path or refreshes its metadata. The path is the identity of a reference;
// it does not prove bytewise identity, so size and mtime are stored for later comparison.
func (s *Store) Upsert(d Dataset) (Dataset, error) {
	key := pathKey(d.HostID, d.Path)
	_, err := s.db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,host_id) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET name=excluded.name,size_bytes=excluded.size_bytes,modified_at=excluded.modified_at,header=excluded.header`,
		key, d.Name, d.SizeBytes, d.ModifiedAt.UnixNano(), time.Now().Unix(), string(d.Header), d.HostID)
	if err != nil {
		return Dataset{}, err
	}
	got, err := s.byPath(key)
	if err != nil {
		return Dataset{}, err
	}
	return s.Get(got.ID)
}

// pathKey keeps one unique key per (host, path): a path on another machine is not the same file as the same path here.
func pathKey(host int64, p string) string {
	if host == 0 {
		return p
	}
	return fmt.Sprintf("h%d:%s", host, p)
}

func scan(row interface{ Scan(...any) error }) (Dataset, error) {
	var d Dataset
	var mod, add int64
	var hdr string
	if err := row.Scan(&d.ID, &d.Path, &d.Name, &d.SizeBytes, &mod, &add, &hdr, &d.HostID); err != nil {
		return Dataset{}, err
	}
	if d.HostID != 0 {
		d.Path = strings.TrimPrefix(d.Path, fmt.Sprintf("h%d:", d.HostID))
	}
	d.ModifiedAt, d.AddedAt, d.Header = time.Unix(0, mod).UTC(), time.Unix(add, 0).UTC(), json.RawMessage(hdr)
	return d, nil
}

const cols = `id,path,name,size_bytes,modified_at,added_at,header,host_id`

func (s *Store) byPath(p string) (Dataset, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE path=?`, p))
}

func (s *Store) Get(id int64) (Dataset, error) {
	d, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Interpretation, err = s.interpretation(id, d)
	return d, err
}

func (s *Store) interpretation(id int64, d Dataset) (*Interpretation, error) {
	var in Interpretation
	var rel sql.NullInt64
	var at, size, mod int64
	err := s.db.QueryRow(`SELECT kind,unit,reliability_id,declared_at,size_bytes,modified_ns,declared_by FROM interpretations WHERE dataset_id=?`, id).Scan(&in.Kind, &in.Unit, &rel, &at, &size, &mod, &in.DeclaredBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rel.Valid {
		in.ReliabilityID = &rel.Int64
	}
	in.DeclaredAt = time.Unix(at, 0).UTC()
	in.Stale = size != d.SizeBytes || mod != d.ModifiedAt.UnixNano()
	return &in, nil
}

// SetInterpretation records what the user declared. Callers validate the kind, unit and companion first.
func (s *Store) SetInterpretation(d Dataset, in Interpretation) error {
	id := d.ID
	var rel any
	if in.ReliabilityID != nil {
		rel = *in.ReliabilityID
	}
	by := in.DeclaredBy
	if by == "" {
		by = "user"
	}
	_, err := s.db.Exec(`INSERT INTO interpretations(dataset_id,kind,unit,reliability_id,declared_at,size_bytes,modified_ns,declared_by) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(dataset_id) DO UPDATE SET kind=excluded.kind,unit=excluded.unit,reliability_id=excluded.reliability_id,declared_at=excluded.declared_at,
			size_bytes=excluded.size_bytes,modified_ns=excluded.modified_ns,declared_by=excluded.declared_by`,
		id, in.Kind, in.Unit, rel, time.Now().Unix(), d.SizeBytes, d.ModifiedAt.UnixNano(), by)
	return err
}

func (s *Store) ClearInterpretation(id int64) error {
	_, err := s.db.Exec(`DELETE FROM interpretations WHERE dataset_id=?`, id)
	return err
}

func (s *Store) List() ([]Dataset, error) {
	rows, err := s.db.Query(`SELECT ` + cols + ` FROM datasets ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dataset{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if out[i].Interpretation, err = s.interpretation(out[i].ID, out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
