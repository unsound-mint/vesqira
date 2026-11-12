package boards

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Outcomes a reviewer can give a version. None of them says a run is scientifically valid: that is a separate
// assessment kept by the author on the run.
var outcomes = map[string]bool{"commented": true, "changes_requested": true, "reviewed": true}

func (s *Store) outcome(snapshotID int64) Outcome {
	var o Outcome
	var at int64
	if s.db.QueryRow(`SELECT outcome,note,author,at FROM snapshot_outcomes WHERE snapshot_id=? ORDER BY id DESC LIMIT 1`, snapshotID).Scan(&o.Value, &o.Note, &o.By, &at) == nil {
		t := time.Unix(at, 0).UTC()
		o.At = &t
	}
	return o
}

func (s *Store) fillOutcome(sn *Snapshot) {
	o := s.outcome(sn.ID)
	sn.Outcome, sn.OutcomeNote, sn.OutcomeBy, sn.OutcomeAt = o.Value, o.Note, o.By, o.At
	var n int
	var last sql.NullInt64
	if s.db.QueryRow(`SELECT COUNT(*), MAX(created_at) FROM comments WHERE snapshot_id=? AND from_owner=0`, sn.ID).Scan(&n, &last) == nil {
		sn.Comments = n
		if last.Valid {
			t := time.Unix(last.Int64, 0).UTC()
			sn.LastActivity = &t
		}
	}
	if o.At != nil && (sn.LastActivity == nil || o.At.After(*sn.LastActivity)) {
		sn.LastActivity = o.At
	}
}

// SetOutcome records the reviewer's word on this version. Each call is kept, the latest counts.
func (s *Store) SetOutcome(token, value, note, author string) (Outcome, error) {
	id, _, _, err := s.lookup(token)
	if err != nil {
		return Outcome{}, err
	}
	if !outcomes[value] {
		return Outcome{}, fmt.Errorf("%w: unknown outcome", ErrInvalid)
	}
	note, author = clip(note, 1000), clip(author, 80)
	if author == "" {
		author = "Reviewer"
	}
	now := time.Now()
	if _, err := s.db.Exec(`INSERT INTO snapshot_outcomes(snapshot_id,at,outcome,note,author) VALUES(?,?,?,?,?)`, id, now.Unix(), value, note, author); err != nil {
		return Outcome{}, err
	}
	t := now.UTC()
	return Outcome{Value: value, Note: note, By: author, At: &t}, nil
}

// Preflight describes exactly what publishing the board would expose, and what a careful author would want to know
// first. It creates nothing.
type Preflight struct {
	Frozen   Frozen   `json:"frozen"`
	Warnings []string `json:"warnings"`
}

func (s *Store) Preflight(ws workspace.ID, boardID int64, question string, jobs map[int64]JobSnapshot) (Preflight, error) {
	b, err := s.Board(ws, boardID)
	if err != nil {
		return Preflight{}, err
	}
	if len(b.Items) == 0 {
		return Preflight{}, fmt.Errorf("%w: the board has no figures", ErrInvalid)
	}
	fr := Frozen{BoardTitle: b.Title, Question: clip(question, 1000), CreatedAt: time.Now().UTC(), Items: []FrozenItem{}}
	var warns []string
	if fr.Question == "" {
		warns = append(warns, "No question for the reviewer: say what you want them to look at.")
	}
	fr.Items = freeze(b.Items, jobs, fr.CreatedAt)
	for _, it := range b.Items {
		if it.Kind == KindJob {
			js, ok := jobs[it.Job.ID]
			state := "in an unknown state"
			if ok {
				state = js.State
			}
			switch state {
			case "completed", "failed", "cancelled":
				warns = append(warns, fmt.Sprintf("Panel %d is a run that is %s but not pinned as a figure; the snapshot shows a placeholder and will not change. Pin its result, then publish a new revision.", it.Position+1, state))
			default:
				warns = append(warns, fmt.Sprintf("Panel %d follows a run that has not finished; the snapshot shows it as %s and will not change. Publish a new revision when it finishes.", it.Position+1, state))
			}
			continue
		}
		var p struct {
			Run *struct {
				Dirty  bool `json:"dirty"`
				Review *struct {
					QC string `json:"qc"`
				} `json:"review"`
			} `json:"run"`
			Interpretation *struct {
				DeclaredBy string `json:"declaredBy"`
			} `json:"interpretation"`
		}
		_ = json.Unmarshal(it.Figure.Provenance, &p)
		name := it.Figure.Title
		if it.Figure.Caption == "" {
			warns = append(warns, fmt.Sprintf("%q has no caption.", name))
		}
		if p.Run != nil && p.Run.Dirty {
			warns = append(warns, fmt.Sprintf("%q comes from a run made from uncommitted code, so it cannot be reproduced from a commit.", name))
		}
		if p.Run != nil && (p.Run.Review == nil || p.Run.Review.QC == "" || p.Run.Review.QC == "not_assessed") {
			warns = append(warns, fmt.Sprintf("The run behind %q has not been assessed: the reviewer will see \"not assessed\".", name))
		}
	}
	return Preflight{Frozen: fr, Warnings: warns}, nil
}
