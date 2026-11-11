package projects

import (
	"encoding/json"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
)

// Rules returns the project's import rules in priority order (the first matching rule wins).
func (s *Store) Rules(id int64) ([]discover.Rule, error) {
	rows, err := s.db.Query(`SELECT rule FROM import_rules WHERE project_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []discover.Rule{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r discover.Rule
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RulesSaved says whether the researcher has ever saved a rule list for the project, even an empty one.
func (s *Store) RulesSaved(id int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM import_lists WHERE project_id=?`, id).Scan(&n)
	return n > 0, err
}

// SetRules replaces the project's rule list, in order, in one transaction. The caller has validated the rules.
func (s *Store) SetRules(id int64, rules []discover.Rule) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM import_rules WHERE project_id=?`, id); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO import_lists(project_id,saved_at) VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET saved_at=excluded.saved_at`, id, now); err != nil {
		return err
	}
	for i, r := range rules {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO import_rules(project_id,position,name,rule,saved_at) VALUES(?,?,?,?,?)`, id, i, r.Name, string(b), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Applied is what happened to one file when a rule was applied to it.
type Applied struct {
	Path    string `json:"path"`
	Rule    string `json:"rule"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtimeNs"`
	// Outcome is registered, declared (already registered; only declarations applied), duplicate (offered, not registered),
	// skipped (a reason, nothing changed) or failed.
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// SaveApplied records what applying the rules did, one row per file (a later application of the same file replaces it).
func (s *Store) SaveApplied(id int64, rows []Applied) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, a := range rows {
		if _, err := tx.Exec(`INSERT INTO import_applied(project_id,path,rule,size_bytes,mtime_ns,outcome,detail,applied_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(project_id,path) DO UPDATE SET rule=excluded.rule,size_bytes=excluded.size_bytes,mtime_ns=excluded.mtime_ns,
				outcome=excluded.outcome,detail=excluded.detail,applied_at=excluded.applied_at`, id, a.Path, a.Rule, a.Size, a.MtimeNS, a.Outcome, a.Detail, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppliedFiles returns the application record of every file of the project, by relative path.
func (s *Store) AppliedFiles(id int64) (map[string]Applied, error) {
	rows, err := s.db.Query(`SELECT path,rule,size_bytes,mtime_ns,outcome,detail FROM import_applied WHERE project_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Applied{}
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Path, &a.Rule, &a.Size, &a.MtimeNS, &a.Outcome, &a.Detail); err != nil {
			return nil, err
		}
		out[a.Path] = a
	}
	return out, rows.Err()
}
