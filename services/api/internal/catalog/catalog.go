// Package catalog stores dataset references and the header facts the worker declared.
// Scientific data stays in the original files; only references and metadata live in SQLite.
package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
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
		modified_at INTEGER NOT NULL,
		added_at INTEGER NOT NULL,
		header TEXT NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Upsert registers a path or refreshes its metadata. The path is the identity of a reference;
// it does not prove bytewise identity, so size and mtime are stored for later comparison.
func (s *Store) Upsert(d Dataset) (Dataset, error) {
	_, err := s.db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET name=excluded.name,size_bytes=excluded.size_bytes,modified_at=excluded.modified_at,header=excluded.header`,
		d.Path, d.Name, d.SizeBytes, d.ModifiedAt.Unix(), time.Now().Unix(), string(d.Header))
	if err != nil {
		return Dataset{}, err
	}
	return s.byPath(d.Path)
}

func scan(row interface{ Scan(...any) error }) (Dataset, error) {
	var d Dataset
	var mod, add int64
	var hdr string
	if err := row.Scan(&d.ID, &d.Path, &d.Name, &d.SizeBytes, &mod, &add, &hdr); err != nil {
		return Dataset{}, err
	}
	d.ModifiedAt, d.AddedAt, d.Header = time.Unix(mod, 0).UTC(), time.Unix(add, 0).UTC(), json.RawMessage(hdr)
	return d, nil
}

const cols = `id,path,name,size_bytes,modified_at,added_at,header`

func (s *Store) byPath(p string) (Dataset, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE path=?`, p))
}

func (s *Store) Get(id int64) (Dataset, error) {
	d, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, ErrNotFound
	}
	return d, err
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
	return out, rows.Err()
}
