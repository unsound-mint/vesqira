package boards

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Reviewer anchors, comments and token lookup (moved out of boards.go unchanged to keep that file under its size cap).

// Anchor places a comment on the published picture, in fractions of its width and height (0,0 is the top left). It
// refers to the picture the reviewer sees, not to a sky position: the preview is a rendering, not the data.
type Anchor struct {
	Kind string  `json:"kind"` // point | box
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	X1   float64 `json:"x1,omitempty"` // box only: the opposite corner
	Y1   float64 `json:"y1,omitempty"`
}

type Comment struct {
	ID             int64      `json:"id"`
	FigureRevision string     `json:"figureRevision"`
	ParentID       int64      `json:"parentId"` // 0 for a thread's first comment
	Author         string     `json:"author"`
	FromOwner      bool       `json:"fromOwner"`
	Body           string     `json:"body"`
	Anchor         *Anchor    `json:"anchor,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"` // set on a thread's first comment when the author closes it
}

// CommentInput is what a comment is made from. A reply names its parent and inherits the figure.
type CommentInput struct {
	FigureRevision string
	ParentID       int64
	Author         string
	Body           string
	Anchor         *Anchor
}

func (a *Anchor) valid() bool {
	ok := func(v float64) bool { return v >= 0 && v <= 1 } // false for NaN too
	switch a.Kind {
	case "point":
		return ok(a.X) && ok(a.Y) && a.X1 == 0 && a.Y1 == 0
	case "box":
		return ok(a.X) && ok(a.Y) && ok(a.X1) && ok(a.Y1) && a.X < a.X1 && a.Y < a.Y1
	}
	return false
}

// lookup resolves a token to a live snapshot. Unknown, revoked and expired tokens are indistinguishable (ErrNotFound).
func (s *Store) lookup(token string) (id int64, fr Frozen, sup *Superseded, err error) {
	if len(token) < 20 || len(token) > 100 {
		return 0, fr, nil, ErrNotFound
	}
	h := hashToken(token)
	var frozen, stored, reason string
	var exp, rv, by, at int64
	err = s.db.QueryRow(`SELECT id,expires_at,revoked,token_hash,frozen,superseded_by,supersede_reason,supersede_at FROM snapshots WHERE token_hash=?`, h).Scan(&id, &exp, &rv, &stored, &frozen, &by, &reason, &at)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || rv != 0 || time.Now().Unix() >= exp {
		return 0, fr, nil, ErrNotFound
	}
	if err := json.Unmarshal([]byte(frozen), &fr); err != nil {
		return 0, fr, nil, err
	}
	if by != 0 {
		sup = &Superseded{Reason: reason, At: time.Unix(at, 0).UTC()}
	}
	return id, fr, sup, nil
}

func (s *Store) Review(token string) (Review, error) {
	id, fr, sup, err := s.lookup(token)
	if err != nil {
		return Review{}, err
	}
	cs, err := s.Comments(id)
	prev := s.reviewRound(token, id, &fr)
	return Review{Snapshot: fr, Comments: cs, Outcome: s.outcome(id), Superseded: sup, Previous: prev}, err
}

func (s *Store) Comments(snapshotID int64) ([]Comment, error) {
	rows, err := s.db.Query(`SELECT id,figure_revision,parent_id,author,from_owner,body,anchor,created_at,resolved_at FROM comments WHERE snapshot_id=? ORDER BY id`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var at, res, own int64
		var anchor string
		if err := rows.Scan(&c.ID, &c.FigureRevision, &c.ParentID, &c.Author, &own, &c.Body, &anchor, &at, &res); err != nil {
			return nil, err
		}
		c.FromOwner = own != 0
		c.CreatedAt = time.Unix(at, 0).UTC()
		if res != 0 {
			t := time.Unix(res, 0).UTC()
			c.ResolvedAt = &t
		}
		if anchor != "" {
			var a Anchor
			if json.Unmarshal([]byte(anchor), &a) == nil && a.valid() {
				c.Anchor = &a
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReviewPreview returns a preview only if it belongs to the token's snapshot: a valid token never opens other figures.
func (s *Store) ReviewPreview(token, sha string) ([]byte, error) {
	id, fr, _, err := s.lookup(token)
	if err != nil {
		return nil, err
	}
	for _, it := range fr.Items {
		if it.PreviewSHA != "" && it.PreviewSHA == sha {
			return s.Preview(sha)
		}
	}
	if s.previousPicture(token, id, fr, sha) { // a redrawn figure's earlier picture, while the earlier round's link is valid
		return s.Preview(sha)
	}
	return nil, ErrNotFound
}

// AddComment records a reviewer's comment, or reply, on a figure revision that is part of the snapshot.
func (s *Store) AddComment(token string, in CommentInput) (Comment, error) {
	id, fr, _, err := s.lookup(token)
	if err != nil {
		return Comment{}, err
	}
	found := false
	for _, it := range fr.Items {
		found = found || (it.Revision != "" && it.Revision == in.FigureRevision) // a job panel has no revision to comment on
	}
	if !found && in.ParentID == 0 {
		return Comment{}, fmt.Errorf("%w: comment needs text and a figure of this snapshot", ErrInvalid)
	}
	return s.addComment(id, in, false)
}

// OwnerReply records the author's reply inside a thread of one of their own snapshots.
func (s *Store) OwnerReply(ws workspace.ID, snapshotID int64, in CommentInput) (Comment, error) {
	if in.ParentID == 0 {
		return Comment{}, fmt.Errorf("%w: the author replies inside a thread", ErrInvalid)
	}
	if err := s.ownsSnapshot(ws, snapshotID); err != nil {
		return Comment{}, err
	}
	in.Author = "Author"
	return s.addComment(snapshotID, in, true)
}

func (s *Store) addComment(snapshotID int64, in CommentInput, owner bool) (Comment, error) {
	in.Body, in.Author = clip(in.Body, MaxComment), clip(in.Author, 80)
	if in.Body == "" {
		return Comment{}, fmt.Errorf("%w: comment needs text and a figure of this snapshot", ErrInvalid)
	}
	if in.Author == "" {
		in.Author = "Reviewer"
	}
	anchorJSON := ""
	if in.ParentID != 0 {
		if in.Anchor != nil {
			return Comment{}, fmt.Errorf("%w: only a thread's first comment is placed on the picture", ErrInvalid)
		}
		// a reply belongs to a first comment of the same snapshot and takes its figure
		err := s.db.QueryRow(`SELECT figure_revision FROM comments WHERE id=? AND snapshot_id=? AND parent_id=0`, in.ParentID, snapshotID).Scan(&in.FigureRevision)
		if err != nil {
			return Comment{}, fmt.Errorf("%w: no such thread in this snapshot", ErrInvalid)
		}
	} else if in.Anchor != nil {
		if !in.Anchor.valid() {
			return Comment{}, fmt.Errorf("%w: the marked place is outside the picture", ErrInvalid)
		}
		b, _ := json.Marshal(in.Anchor)
		anchorJSON = string(b)
	}
	now := time.Now()
	own := 0
	if owner {
		own = 1
	}
	// count and insert in one statement so concurrent comments cannot pass the limit
	res, err := s.db.Exec(`INSERT INTO comments(snapshot_id,figure_revision,author,body,created_at,parent_id,anchor,from_owner)
		SELECT ?,?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM comments WHERE snapshot_id=?) < ?`,
		snapshotID, in.FigureRevision, in.Author, in.Body, now.Unix(), in.ParentID, anchorJSON, own, snapshotID, MaxCommentsPS)
	if err != nil {
		return Comment{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Comment{}, fmt.Errorf("%w: comment limit reached", ErrInvalid)
	}
	cid, _ := res.LastInsertId()
	return Comment{ID: cid, FigureRevision: in.FigureRevision, ParentID: in.ParentID, Author: in.Author, FromOwner: owner, Body: in.Body, Anchor: in.Anchor, CreatedAt: now.UTC()}, nil
}

// SetResolved closes or reopens a thread. Only the author does this, through the owner API.
func (s *Store) SetResolved(ws workspace.ID, commentID int64, resolved bool) error {
	var at int64
	if resolved {
		at = time.Now().Unix()
	}
	res, err := s.db.Exec(`UPDATE comments SET resolved_at=? WHERE id=? AND parent_id=0 AND snapshot_id IN (SELECT id FROM snapshots WHERE workspace_id=?)`, at, commentID, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ownsSnapshot is ErrNotFound unless the snapshot belongs to the workspace.
func (s *Store) ownsSnapshot(ws workspace.ID, snapshotID int64) error {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM snapshots WHERE id=? AND workspace_id=?`, snapshotID, ws).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// OwnerComments lists the comments on a snapshot of the workspace for its owner.
func (s *Store) OwnerComments(ws workspace.ID, snapshotID int64) ([]Comment, error) {
	if err := s.ownsSnapshot(ws, snapshotID); err != nil {
		return nil, err
	}
	return s.Comments(snapshotID)
}
