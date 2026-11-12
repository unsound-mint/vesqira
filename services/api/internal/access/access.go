// Package access holds the access labels of datasets (ADR 0069) and works out the label a figure or a derived result carries.
//
// A label is a declaration by the researcher about whether the data may be shown to others: "none" (released, the default),
// "collaboration" (inside the collaboration only) or "unreleased" (not validated or not approved yet). Labels are ordered
// unreleased > collaboration > none, and what a figure or a derived dataset carries is the most restrictive label among its
// sources, read from the dependency edges recorded in the catalog at the time it is asked for, never copied.
//
// The label is mutable working state with an append-only history (who, when, why). It is not a permission: it decides what the
// publishing code asks about or refuses, and it is only as true as the person who set it (a file's bytes say nothing about it).
package access

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Label is an access label. The zero value is None.
type Label string

const (
	None          Label = "none"
	Collaboration Label = "collaboration"
	Unreleased    Label = "unreleased"
)

// Labels lists every label from least to most restrictive.
func Labels() []Label { return []Label{None, Collaboration, Unreleased} }

// Name is the label as a person reads it.
func (l Label) Name() string {
	switch l {
	case Collaboration:
		return "collaboration only"
	case Unreleased:
		return "unreleased"
	}
	return "released"
}

// Rank orders the labels: unreleased (2) above collaboration only (1) above none (0). An unknown label ranks above all of
// them, so a value nobody understands is never treated as released.
func (l Label) Rank() int {
	switch l {
	case None, "":
		return 0
	case Collaboration:
		return 1
	case Unreleased:
		return 2
	}
	return 3
}

// Valid reports whether l is one of Labels.
func (l Label) Valid() bool { return l.Rank() < 3 }

// Parse reads a label, refusing anything but the three names. The empty string is None.
func Parse(s string) (Label, error) {
	l := Label(strings.TrimSpace(s))
	if l == "" {
		return None, nil
	}
	if !l.Valid() {
		return None, fmt.Errorf("the access label is one of none, collaboration or unreleased")
	}
	return l, nil
}

// Max returns the more restrictive of two labels.
func Max(a, b Label) Label {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Restricted reports whether the label limits sharing at all.
func (l Label) Restricted() bool { return l.Rank() > 0 }

// ParseSet reads a comma-separated list of labels (the policy column), keeping each restrictive label once, in rank order.
func ParseSet(s string) []Label {
	var out []Label
	for _, l := range Labels() {
		if !l.Restricted() {
			continue
		}
		for _, f := range strings.Split(s, ",") {
			if Label(strings.TrimSpace(f)) == l {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// Blocks reports whether l is among the labels a workspace has made block publishing.
func Blocks(blocked []Label, l Label) bool {
	for _, b := range blocked {
		if b == l && l.Restricted() {
			return true
		}
	}
	return false
}

// Change is one record of a dataset's history.
type Change struct {
	ID        int64     `json:"id"`
	DatasetID int64     `json:"datasetId"`
	Name      string    `json:"name"` // the dataset's name when the label was set
	Label     Label     `json:"label"`
	Previous  Label     `json:"previous"`
	Reason    string    `json:"reason"`
	Author    string    `json:"author"`
	At        time.Time `json:"at"`
}

// Store reads and writes the history.
type Store struct{ db *sql.DB }

// Open creates the history table when the catalog has none.
func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.AccessTable); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

// ErrInvalid marks a refusal the caller can show as it is.
var ErrInvalid = errors.New("invalid")

// Set records a new label for a dataset and returns the one it replaced. Setting the label already in force records nothing.
// name is the dataset's name now, kept with the row so the history reads after the dataset has left the catalog.
func (s *Store) Set(ws workspace.ID, datasetID int64, name string, label Label, reason, author string) (previous Label, changed bool, err error) {
	if !label.Valid() {
		return None, false, fmt.Errorf("%w: unknown label", ErrInvalid)
	}
	if len([]rune(reason)) > 500 {
		return None, false, fmt.Errorf("%w: the reason is at most 500 characters", ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return None, false, err
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow(`SELECT label FROM dataset_access WHERE workspace_id=? AND dataset_id=? ORDER BY id DESC LIMIT 1`, ws, datasetID).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		prev, err = string(None), nil
	}
	if err != nil {
		return None, false, err
	}
	if Label(prev) == label {
		return label, false, nil
	}
	if _, err := tx.Exec(`INSERT INTO dataset_access(workspace_id,dataset_id,dataset_name,label,previous,reason,author,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		ws, datasetID, name, string(label), prev, strings.TrimSpace(reason), author, time.Now().Unix()); err != nil {
		return None, false, err
	}
	return Label(prev), true, tx.Commit()
}

// History lists a dataset's changes, newest first.
func (s *Store) History(ws workspace.ID, datasetID int64) ([]Change, error) {
	rows, err := s.db.Query(`SELECT id,dataset_id,dataset_name,label,previous,reason,author,created_at FROM dataset_access WHERE workspace_id=? AND dataset_id=? ORDER BY id DESC`, ws, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		var at int64
		if err := rows.Scan(&c.ID, &c.DatasetID, &c.Name, &c.Label, &c.Previous, &c.Reason, &c.Author, &at); err != nil {
			return nil, err
		}
		c.At = time.Unix(at, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Source is a labelled dataset a result depends on.
type Source struct {
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
	Label     Label  `json:"label"`
}

// Effective is the label a dataset, figure or run carries: the most restrictive among its own and its sources'. Via lists the
// labelled datasets that bring a label in (the dataset itself included when it has one), most restrictive first; it is empty
// when the label is none. It names datasets, so it is owner-side only and never goes into a snapshot or a push.
type Effective struct {
	Label Label    `json:"label"`
	Via   []Source `json:"via,omitempty"`
}

// AccessLabel is the label as a string, for the code that freezes a board and must not import this package.
func (e Effective) AccessLabel() string { return string(e.Label) }

// View is the labels and dependency edges of one workspace, loaded once so that a list of figures costs one read. When nothing in
// the workspace is labelled, which is the common case, the edges are never read.
type View struct {
	own     map[int64]Source  // current label of each labelled dataset
	derived map[int64][]int64 // derived dataset -> the datasets it was made from
	runOut  map[int64][]int64 // run -> its output datasets
}

// View loads the workspace's labels and, when any exists, the edges it needs to propagate them.
func (s *Store) View(ws workspace.ID) (*View, error) {
	v := &View{own: map[int64]Source{}}
	rows, err := s.db.Query(`SELECT a.dataset_id, a.dataset_name, a.label FROM dataset_access a
		WHERE a.workspace_id=? AND a.id=(SELECT MAX(b.id) FROM dataset_access b WHERE b.workspace_id=a.workspace_id AND b.dataset_id=a.dataset_id)`, ws)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.DatasetID, &src.Name, &src.Label); err != nil {
			rows.Close()
			return nil, err
		}
		if src.Label.Restricted() {
			v.own[src.DatasetID] = src
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // the catalog uses one connection: finish this read before the next
	if len(v.own) == 0 {
		return v, nil
	}
	v.derived = map[int64][]int64{}
	drows, err := s.db.Query(`SELECT d.dataset_id, d.sources FROM derived d JOIN datasets ds ON ds.id=d.dataset_id WHERE ds.workspace_id=?`, ws)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var id int64
		var raw string
		if err := drows.Scan(&id, &raw); err != nil {
			drows.Close()
			return nil, err
		}
		var src []int64
		if json.Unmarshal([]byte(raw), &src) == nil && len(src) > 0 {
			v.derived[id] = src
		}
	}
	if err := drows.Err(); err != nil {
		drows.Close()
		return nil, err
	}
	drows.Close()
	v.runOut = map[int64][]int64{}
	orows, err := s.db.Query(`SELECT o.run_id, o.dataset_id FROM run_outputs o JOIN runs r ON r.id=o.run_id WHERE r.workspace_id=?`, ws)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") { // a catalog whose runs store was never opened has no outputs to follow
			return v, nil
		}
		return nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var run, ds int64
		if err := orows.Scan(&run, &ds); err != nil {
			return nil, err
		}
		v.runOut[run] = append(v.runOut[run], ds)
	}
	return v, orows.Err()
}

// Datasets is the effective label of a set of datasets: the most restrictive among them and everything they were made from.
// Dependencies are followed through the derived records, with a visited set so a cycle cannot loop.
func (v *View) Datasets(ids ...int64) Effective {
	if v == nil || len(v.own) == 0 {
		return Effective{Label: None}
	}
	seen := map[int64]bool{}
	via := map[int64]Source{}
	var walk func(id int64)
	walk = func(id int64) {
		if seen[id] {
			return
		}
		seen[id] = true
		if src, ok := v.own[id]; ok {
			via[id] = src
		}
		for _, from := range v.derived[id] {
			walk(from)
		}
	}
	for _, id := range ids {
		walk(id)
	}
	e := Effective{Label: None}
	for _, s := range via {
		e.Label = Max(e.Label, s.Label)
		e.Via = append(e.Via, s)
	}
	sort.Slice(e.Via, func(i, j int) bool {
		a, b := e.Via[i], e.Via[j]
		if a.Label.Rank() != b.Label.Rank() {
			return a.Label.Rank() > b.Label.Rank()
		}
		return a.DatasetID < b.DatasetID
	})
	return e
}

// Dataset is Datasets for one dataset.
func (v *View) Dataset(id int64) Effective { return v.Datasets(id) }

// Own is the label set on the dataset itself (None when unlabelled), apart from what it inherits.
func (v *View) Own(id int64) Label {
	if v == nil {
		return None
	}
	if s, ok := v.own[id]; ok {
		return s.Label
	}
	return None
}

// Run is the effective label of a run: the most restrictive among its output datasets.
func (v *View) Run(id int64) Effective {
	if v == nil {
		return Effective{Label: None}
	}
	return v.Datasets(v.runOut[id]...)
}

// Labelled lists every dataset that carries a label, its own or an inherited one, for the badges of a catalog list.
func (v *View) Labelled() map[int64]Effective {
	out := map[int64]Effective{}
	if v == nil || len(v.own) == 0 {
		return out
	}
	for id := range v.own {
		out[id] = v.Dataset(id)
	}
	for id := range v.derived {
		if e := v.Dataset(id); e.Label.Restricted() {
			out[id] = e
		}
	}
	return out
}

// Figure is the effective label of a figure from the edges it records: its source dataset, every dataset its scene and
// identity record name (overlays, reliability maps, channel-grid members) and, for a cross-run plot, the outputs of the runs it
// plots. A figure saved before those records existed has only its source dataset, which is all the catalog can say.
func (v *View) Figure(datasetID *int64, scene, identity json.RawMessage) Effective {
	if v == nil || len(v.own) == 0 {
		return Effective{Label: None}
	}
	var ids []int64
	if datasetID != nil {
		ids = append(ids, *datasetID)
	}
	ids = append(ids, DatasetRefs(scene)...)
	ids = append(ids, DatasetRefs(identity)...)
	for _, run := range RunRefs(scene) {
		ids = append(ids, v.runOut[run]...)
	}
	return v.Datasets(ids...)
}

// DatasetRefs collects every dataset id a scene/1, channel-grid or figure-identity document names, by key: "datasetId",
// "reliabilityId", and the arrays "datasets" and "overlays" of ids. Reading by key rather than by schema keeps a new field
// of a later scene version from slipping a dataset past the label.
func DatasetRefs(raw json.RawMessage) []int64 {
	var doc any
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []int64
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "datasetId", "reliabilityId":
					if f, ok := val.(float64); ok && f > 0 {
						out = append(out, int64(f))
					}
				case "datasets", "overlays":
					if arr, ok := val.([]any); ok {
						for _, e := range arr {
							if f, ok := e.(float64); ok && f > 0 {
								out = append(out, int64(f))
							}
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	return out
}

// RunRefs collects the run ids of a cross-run plot's description (runs[].id).
func RunRefs(raw json.RawMessage) []int64 {
	var doc struct {
		Runs []struct {
			ID int64 `json:"id"`
		} `json:"runs"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []int64
	for _, r := range doc.Runs {
		if r.ID > 0 {
			out = append(out, r.ID)
		}
	}
	return out
}
