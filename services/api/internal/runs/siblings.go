package runs

import (
	"database/sql"
	"errors"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// RoleOutput is one output of a run found by its role: the run it belongs to, and the datasets that run lists with the run's
// reliability role (the run's own, never another run's).
type RoleOutput struct {
	RunID     int64
	RunName   string
	Dir       string // host-qualified for a remote run, as stored
	Profile   string
	DatasetID int64
	// Reliability are the datasets of the same run with the role reliability, in dataset id order.
	Reliability []int64
}

// OutputOf is the run that lists the dataset as an output, and the output's role (the latest run when several list it). found is
// false for a dataset no run in the workspace lists.
func (s *Store) OutputOf(ws workspace.ID, datasetID int64) (r RoleOutput, role string, found bool, err error) {
	err = s.db.QueryRow(`SELECT r.id,r.name,r.dir,r.profile,o.role FROM run_outputs o JOIN runs r ON r.id=o.run_id
		WHERE o.dataset_id=? AND r.workspace_id=? ORDER BY o.run_id DESC LIMIT 1`, datasetID, ws).Scan(&r.RunID, &r.RunName, &r.Dir, &r.Profile, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return r, "", false, nil
	}
	if err != nil {
		return r, "", false, err
	}
	r.DatasetID = datasetID
	r.Reliability, err = s.outputsWithRole(r.RunID, RoleReliability)
	return r, role, true, err
}

func (s *Store) outputsWithRole(runID int64, role string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT dataset_id FROM run_outputs WHERE run_id=? AND role=? ORDER BY dataset_id`, runID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// WithRole lists, for every run of the workspace read with the profile (name@version) that is not archived, its outputs with the
// role and the run's reliability outputs. Order is by run id.
func (s *Store) WithRole(ws workspace.ID, profile, role string) ([]RoleOutput, error) {
	rows, err := s.db.Query(`SELECT r.id,r.name,r.dir,r.profile,o.dataset_id FROM runs r JOIN run_outputs o ON o.run_id=r.id
		WHERE r.workspace_id=? AND r.profile=? AND o.role=? AND NOT `+schema.ArchivedExpr("run", "r.id")+` ORDER BY r.id,o.dataset_id`, ws, profile, role)
	if err != nil {
		return nil, err
	}
	var out []RoleOutput
	for rows.Next() {
		var o RoleOutput
		if err := rows.Scan(&o.RunID, &o.RunName, &o.Dir, &o.Profile, &o.DatasetID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rel := map[int64][]int64{}
	rr, err := s.db.Query(`SELECT o.run_id,o.dataset_id FROM run_outputs o JOIN runs r ON r.id=o.run_id WHERE r.workspace_id=? AND r.profile=? AND o.role=? ORDER BY o.run_id,o.dataset_id`, ws, profile, RoleReliability)
	if err != nil {
		return nil, err
	}
	defer rr.Close()
	for rr.Next() {
		var run, ds int64
		if err := rr.Scan(&run, &ds); err != nil {
			return nil, err
		}
		rel[run] = append(rel[run], ds)
	}
	for i := range out {
		out[i].Reliability = rel[out[i].RunID]
	}
	return out, rr.Err()
}
