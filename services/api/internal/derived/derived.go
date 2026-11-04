// Package derived records how a dataset was made from others: the operation, its parameters, its sources and the
// software that did it. A derived file is new data with its own provenance; it never replaces an input.
package derived

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type Record struct {
	DatasetID int64           `json:"datasetId"`
	Op        string          `json:"op"`
	Params    json.RawMessage `json:"params"`
	Sources   []int64         `json:"sources"`
	Summary   json.RawMessage `json:"summary"`
	Software  json.RawMessage `json:"software"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Summary is what travels with a figure: no ids, no paths; sources by file name.
type Summary struct {
	Op        string          `json:"op"`
	Params    json.RawMessage `json:"params"`
	Sources   []string        `json:"sources"`
	Policy    string          `json:"policy"`
	Software  json.RawMessage `json:"software"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS derived(
		dataset_id INTEGER PRIMARY KEY, op TEXT NOT NULL, params TEXT NOT NULL, sources TEXT NOT NULL,
		summary TEXT NOT NULL, software TEXT NOT NULL, created_at INTEGER NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Save(r Record) error {
	if r.Params == nil {
		r.Params = json.RawMessage(`{}`)
	}
	if r.Summary == nil {
		r.Summary = json.RawMessage(`{}`)
	}
	if r.Software == nil {
		r.Software = json.RawMessage(`{}`)
	}
	src, _ := json.Marshal(r.Sources)
	_, err := s.db.Exec(`INSERT INTO derived(dataset_id,op,params,sources,summary,software,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(dataset_id) DO UPDATE SET op=excluded.op,params=excluded.params,sources=excluded.sources,summary=excluded.summary,software=excluded.software`,
		r.DatasetID, r.Op, string(r.Params), string(src), string(r.Summary), string(r.Software), time.Now().Unix())
	return err
}

func (s *Store) Get(datasetID int64) (Record, error) {
	var r Record
	var p, src, sum, sw string
	var at int64
	err := s.db.QueryRow(`SELECT dataset_id,op,params,sources,summary,software,created_at FROM derived WHERE dataset_id=?`, datasetID).Scan(&r.DatasetID, &r.Op, &p, &src, &sum, &sw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	r.Params, r.Summary, r.Software = json.RawMessage(p), json.RawMessage(sum), json.RawMessage(sw)
	_ = json.Unmarshal([]byte(src), &r.Sources)
	r.CreatedAt = time.Unix(at, 0).UTC()
	return r, nil
}
