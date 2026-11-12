// Package subsets keeps virtual subsets (ADR 0063): a saved window onto a dataset, defined by a spatial box, an optional spatial
// step and a spectral interval, pinned to the identity of the source it was made on. A subset is mutable working state with an
// append-only history: an edit, or a re-pin to a source that changed, is a new version, and a file materialised from a version keeps
// the version it was made from. Nothing here reads a file; the window is resolved by the scientific worker.
package subsets

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	// ErrConflict: the subset has a newer version than the one the person was looking at.
	ErrConflict = errors.New("the subset changed since you looked at it: look again before saving")
)

const (
	MaxPerSource = 200
	MaxStep      = 64
	maxCoord     = 1 << 30
)

// Box is a rectangle of the source's image in whole 0-based pixels, inclusive, the same frame regions use.
type Box struct {
	X0 int `json:"x0"`
	X1 int `json:"x1"`
	Y0 int `json:"y0"`
	Y1 int `json:"y1"`
}

// Channels is a channel range of the source, 0-based and inclusive.
type Channels struct {
	C0 int `json:"c0"`
	C1 int `json:"c1"`
}

// World is a spectral interval stated in world units, with the frame it was stated in (empty: the file's own, which is then
// whatever it declares). It is resolved to channels by the worker, against the WCS in force, by the rule in ADR 0063.
type World struct {
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	Unit  string  `json:"unit"`
	Frame string  `json:"frame,omitempty"`
}

// RegionRef records which saved region a box was taken from. The box is a copy of its bounding box; the region may change later.
type RegionRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Definition is what a subset is: a window onto its source. Step is 1 or more (every Step-th pixel of the box, values untouched).
// A spectral interval is Channels or World, not both; neither means every channel.
type Definition struct {
	Box      *Box       `json:"box,omitempty"`
	Region   *RegionRef `json:"region,omitempty"`
	Step     int        `json:"step"`
	Channels *Channels  `json:"channels,omitempty"`
	World    *World     `json:"world,omitempty"`
}

var unitText = regexp.MustCompile(`^[A-Za-z0-9/*^.() -]{1,20}$`)
var frameText = regexp.MustCompile(`^[A-Za-z0-9-]{1,20}$`)

// Normalised checks the definition as far as it can without the file (the worker checks every bound against the source) and
// returns it with its defaults: a step of 1.
func (d Definition) Normalised() (Definition, error) {
	in := func(v int) bool { return v >= 0 && v < maxCoord }
	if d.Step == 0 {
		d.Step = 1
	}
	if d.Step < 1 || d.Step > MaxStep {
		return d, fmt.Errorf("%w: the step is a whole number from 1 to %d", ErrInvalid, MaxStep)
	}
	if b := d.Box; b != nil && (!in(b.X0) || !in(b.X1) || !in(b.Y0) || !in(b.Y1) || b.X0 > b.X1 || b.Y0 > b.Y1) {
		return d, fmt.Errorf("%w: a box needs whole pixel numbers with the first corner before the second", ErrInvalid)
	}
	if d.Region != nil && d.Box == nil {
		return d, fmt.Errorf("%w: a region reference needs its box", ErrInvalid)
	}
	if c := d.Channels; c != nil && (!in(c.C0) || !in(c.C1) || c.C0 > c.C1) {
		return d, fmt.Errorf("%w: a channel range needs whole channel numbers with the first before the last", ErrInvalid)
	}
	if d.Channels != nil && d.World != nil {
		return d, fmt.Errorf("%w: choose a channel range or a spectral interval, not both", ErrInvalid)
	}
	if w := d.World; w != nil {
		if math.IsNaN(w.Lo) || math.IsInf(w.Lo, 0) || math.IsNaN(w.Hi) || math.IsInf(w.Hi, 0) {
			return d, fmt.Errorf("%w: the spectral interval needs finite bounds", ErrInvalid)
		}
		if !unitText.MatchString(w.Unit) {
			return d, fmt.Errorf("%w: the spectral interval needs its unit (short plain text such as km/s or MHz)", ErrInvalid)
		}
		if w.Frame != "" && !frameText.MatchString(w.Frame) {
			return d, fmt.Errorf("%w: the frame is short plain text such as LSRK", ErrInvalid)
		}
	}
	return d, nil
}

// Window is the definition as the worker's window object (services/scientific-worker/subset.py): absent keys mean the whole axis.
func (d Definition) Window() map[string]any {
	w := map[string]any{}
	if d.Step > 1 {
		w["step"] = d.Step
	}
	if b := d.Box; b != nil {
		w["x0"], w["x1"], w["y0"], w["y1"] = b.X0, b.X1, b.Y0, b.Y1
	}
	if c := d.Channels; c != nil {
		w["c0"], w["c1"] = c.C0, c.C1
	}
	if x := d.World; x != nil {
		world := map[string]any{"lo": x.Lo, "hi": x.Hi, "unit": x.Unit}
		if x.Frame != "" {
			world["frame"] = x.Frame
		}
		w["world"] = world
	}
	return w
}

// Version is one row of a subset's history: a definition, the reason for it, and the source file it was pinned to.
type Version struct {
	Version int        `json:"version"`
	Kind    string     `json:"kind"` // create | edit | repin
	Name    string     `json:"name"`
	Reason  string     `json:"reason"`
	Def     Definition `json:"definition"`
	// The source file this version was pinned to: its registered size and modification time, and its recorded content identity
	// (sha256:… or sampled:…, empty when none was recorded).
	SourceSize     int64     `json:"sourceSize"`
	SourceModified time.Time `json:"sourceModifiedAt"`
	SourceIdentity string    `json:"sourceIdentity"`
	Author         string    `json:"author"`
	At             time.Time `json:"at"`
}

// Subset is a subset with its current version.
type Subset struct {
	ID        int64     `json:"id"`
	SourceID  int64     `json:"sourceId"`
	CreatedAt time.Time `json:"createdAt"`
	Current   Version   `json:"current"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.SubsetsTables); err != nil {
		return nil, err
	}
	if err := schema.Scope(db, "subsets"); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const versionCols = `version,kind,name,reason,definition,source_size,source_mtime_ns,source_identity,author,created_at`

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var def string
	var mod, at int64
	if err := row.Scan(&v.Version, &v.Kind, &v.Name, &v.Reason, &def, &v.SourceSize, &mod, &v.SourceIdentity, &v.Author, &at); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(def), &v.Def); err != nil {
		return v, err
	}
	v.SourceModified, v.At = time.Unix(0, mod).UTC(), time.Unix(at, 0).UTC()
	return v, nil
}

func insertVersion(tx *sql.Tx, id int64, v Version) error {
	raw, _ := json.Marshal(v.Def)
	_, err := tx.Exec(`INSERT INTO subset_versions(subset_id,`+versionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, v.Version, v.Kind, v.Name, v.Reason, string(raw), v.SourceSize, v.SourceModified.UnixNano(), v.SourceIdentity, v.Author, time.Now().Unix())
	return err
}

// Create makes a new subset of source with v as its first version (the version number and kind are set here).
func (s *Store) Create(ws workspace.ID, by string, sourceID int64, v Version) (Subset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Subset{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM subsets WHERE source_id=? AND workspace_id=?`, sourceID, ws).Scan(&n); err != nil {
		return Subset{}, err
	}
	if n >= MaxPerSource {
		return Subset{}, fmt.Errorf("%w: at most %d subsets per dataset", ErrInvalid, MaxPerSource)
	}
	res, err := tx.Exec(`INSERT INTO subsets(source_id,created_at,workspace_id,created_by) VALUES(?,?,?,?)`, sourceID, time.Now().Unix(), ws, by)
	if err != nil {
		return Subset{}, err
	}
	id, _ := res.LastInsertId()
	v.Version, v.Kind = 1, "create"
	if err := insertVersion(tx, id, v); err != nil {
		return Subset{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subset{}, err
	}
	return s.Get(ws, id)
}

// AddVersion appends v to subset id as its next version. expect is the version the caller was looking at: a newer one is ErrConflict.
func (s *Store) AddVersion(ws workspace.ID, id int64, expect int, kind string, v Version) (Subset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Subset{}, err
	}
	defer tx.Rollback()
	var cur sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM subset_versions WHERE subset_id=? AND subset_id IN (SELECT id FROM subsets WHERE workspace_id=?)`, id, ws).Scan(&cur); err != nil {
		return Subset{}, err
	}
	if !cur.Valid {
		return Subset{}, ErrNotFound
	}
	if int(cur.Int64) != expect {
		return Subset{}, ErrConflict
	}
	v.Version, v.Kind = expect+1, kind
	if err := insertVersion(tx, id, v); err != nil {
		return Subset{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subset{}, err
	}
	return s.Get(ws, id)
}

// currentRow selects the workspace's subsets with their latest version; the caller appends conditions with AND and its arguments after ws.
func currentRow(ws workspace.ID) (string, []any) {
	return `SELECT s.id,s.source_id,s.created_at,v.version,v.kind,v.name,v.reason,v.definition,v.source_size,v.source_mtime_ns,v.source_identity,v.author,v.created_at
	FROM subsets s JOIN subset_versions v ON v.subset_id=s.id AND v.version=(SELECT MAX(version) FROM subset_versions WHERE subset_id=s.id)
	WHERE s.workspace_id=?`, []any{ws}
}

func scanSubset(row interface{ Scan(...any) error }) (Subset, error) {
	var sub Subset
	var created int64
	var def string
	var mod, at int64
	v := &sub.Current
	if err := row.Scan(&sub.ID, &sub.SourceID, &created, &v.Version, &v.Kind, &v.Name, &v.Reason, &def, &v.SourceSize, &mod, &v.SourceIdentity, &v.Author, &at); err != nil {
		return sub, err
	}
	if err := json.Unmarshal([]byte(def), &v.Def); err != nil {
		return sub, err
	}
	sub.CreatedAt, v.SourceModified, v.At = time.Unix(created, 0).UTC(), time.Unix(0, mod).UTC(), time.Unix(at, 0).UTC()
	return sub, nil
}

func (s *Store) Get(ws workspace.ID, id int64) (Subset, error) {
	q, args := currentRow(ws)
	sub, err := scanSubset(s.db.QueryRow(q+` AND s.id=?`, append(args, id)...))
	if errors.Is(err, sql.ErrNoRows) {
		return sub, ErrNotFound
	}
	return sub, err
}

// List is every subset with its current version, oldest first; sourceID 0 lists all of them.
func (s *Store) List(ws workspace.ID, sourceID int64) ([]Subset, error) {
	q, args := currentRow(ws)
	if sourceID != 0 {
		q, args = q+` AND s.source_id=?`, append(args, sourceID)
	}
	rows, err := s.db.Query(q+` ORDER BY s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subset{}
	for rows.Next() {
		sub, err := scanSubset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// Versions is the whole history of a subset, oldest first.
func (s *Store) Versions(ws workspace.ID, id int64) ([]Version, error) {
	rows, err := s.db.Query(`SELECT `+versionCols+` FROM subset_versions WHERE subset_id=? AND subset_id IN (SELECT id FROM subsets WHERE workspace_id=?) ORDER BY version`, id, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete forgets a subset and its history. Files materialised from it are ordinary datasets and stay.
func (s *Store) Delete(ws workspace.ID, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM subset_versions WHERE subset_id=? AND subset_id IN (SELECT id FROM subsets WHERE workspace_id=?)`, id, ws); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM subsets WHERE id=? AND workspace_id=?`, id, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
