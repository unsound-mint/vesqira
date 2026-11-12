// Package history keeps the viewer's exploration history (ADR 0057): per project, a bounded list of meaningful scene states
// (a dataset opened, a channel held, a region drawn, an overlay added, a link changed), each with the step it was reached
// from. "Continue from here" restores a step's scene and the next step is recorded as a new branch from it; nothing later is
// deleted. It is mutable working state, not evidence: the oldest steps are pruned past Keep, and a step holds only a scene
// (what was on screen), never pixels or values. Dataset ids are the owner's; the reviewer listener has no route to this store.
package history

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// Keep is how many steps a project keeps; the oldest are dropped when a new one arrives.
const Keep = 500

// MaxScene is the largest scene a step may hold. A scene is a few kilobytes (at most 16 overlays); the cap stops a step from
// becoming a way to park data.
const MaxScene = 64 << 10

// Kinds of step, as the browser names them. The server stores them as given (the list is checked), because the label is the
// browser's own wording and the kind only decides the icon and the filter.
var Kinds = map[string]bool{"opened": true, "channel": true, "region": true, "overlay": true, "link": true, "continued": true}

var (
	ErrNotFound = errors.New("step not found")
	ErrInvalid  = errors.New("invalid")
)

type Step struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"projectId"`
	ParentID  *int64    `json:"parentId"`
	DatasetID int64     `json:"datasetId"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	At        time.Time `json:"at"`
	// Scene is the stored scene/1 JSON; it is left out of the list (a list of 500 would carry megabytes) and sent by Get.
	Scene []byte `json:"-"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	for _, q := range []string{schema.ExplorationStepsTable, schema.ExplorationStepsIndex} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	if err := schema.Scope(db, "exploration_steps"); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

// clean keeps a label to one line of at most max characters.
func clean(s string, max int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// Add records a step. parent must be a step of the same project (or nil); an unknown or foreign parent is refused, so a
// branch can never point into another project's history. The oldest steps beyond Keep are removed in the same transaction.
func (s *Store) Add(projectID int64, parent *int64, datasetID int64, kind, label string, scene []byte) (Step, error) {
	label = clean(label, 120)
	switch {
	case !Kinds[kind]:
		return Step{}, errors.New("invalid: unknown kind of step")
	case label == "":
		return Step{}, errors.New("invalid: a step needs a label")
	case len(scene) == 0 || len(scene) > MaxScene:
		return Step{}, errors.New("invalid: the scene is empty or too large")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Step{}, err
	}
	defer tx.Rollback()
	if parent != nil {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM exploration_steps WHERE id=? AND project_id=?`, *parent, projectID).Scan(&n); err != nil {
			return Step{}, err
		}
		if n == 0 {
			return Step{}, errors.New("invalid: the step it continues from is not in this project's history")
		}
	}
	now := time.Now()
	res, err := tx.Exec(`INSERT INTO exploration_steps(project_id,parent_id,dataset_id,kind,label,scene,at) VALUES(?,?,?,?,?,?,?)`,
		projectID, parent, datasetID, kind, label, string(scene), now.Unix())
	if err != nil {
		return Step{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`DELETE FROM exploration_steps WHERE project_id=? AND id NOT IN (SELECT id FROM exploration_steps WHERE project_id=? ORDER BY id DESC LIMIT ?)`,
		projectID, projectID, Keep); err != nil {
		return Step{}, err
	}
	if err := tx.Commit(); err != nil {
		return Step{}, err
	}
	return Step{ID: id, ProjectID: projectID, ParentID: parent, DatasetID: datasetID, Kind: kind, Label: label, At: time.Unix(now.Unix(), 0).UTC(), Scene: scene}, nil
}

// List returns a project's steps, oldest first, without their scenes.
func (s *Store) List(projectID int64) ([]Step, error) {
	rows, err := s.db.Query(`SELECT id,project_id,parent_id,dataset_id,kind,label,at FROM exploration_steps WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		var parent sql.NullInt64
		var at int64
		if err := rows.Scan(&st.ID, &st.ProjectID, &parent, &st.DatasetID, &st.Kind, &st.Label, &at); err != nil {
			return nil, err
		}
		if parent.Valid {
			p := parent.Int64
			st.ParentID = &p
		}
		st.At = time.Unix(at, 0).UTC()
		out = append(out, st)
	}
	return out, rows.Err()
}

// Get returns one step with its scene.
func (s *Store) Get(id int64) (Step, error) {
	var st Step
	var parent sql.NullInt64
	var at int64
	var scene string
	err := s.db.QueryRow(`SELECT id,project_id,parent_id,dataset_id,kind,label,scene,at FROM exploration_steps WHERE id=?`, id).
		Scan(&st.ID, &st.ProjectID, &parent, &st.DatasetID, &st.Kind, &st.Label, &scene, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Step{}, ErrNotFound
	}
	if err != nil {
		return Step{}, err
	}
	if parent.Valid {
		p := parent.Int64
		st.ParentID = &p
	}
	st.Scene, st.At = []byte(scene), time.Unix(at, 0).UTC()
	return st, nil
}
