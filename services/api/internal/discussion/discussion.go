// Package discussion keeps the comments signed-in members write on a figure or a report of their workspace (ADR 0050 section 3:
// "comment on figures and reports inside the workspace", replacing self-declared names for members). The author is the account
// that wrote it, never a typed name. Outsiders keep the token-based comments of review links (internal/review), which this
// package does not touch. The table is created by the catalog (schema.WorkspaceTables).
package discussion

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// MaxBody is the longest comment, in characters.
const MaxBody = 4000

var (
	ErrNotFound = errors.New("comment not found")
	ErrBadBody  = errors.New("a comment is 1 to 4000 characters")
	ErrBadKind  = errors.New("comments are on a figure or a report")
)

// Comment is one comment. Author is the account id (local-owner in local mode); the server adds the display name.
type Comment struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Target  int64     `json:"targetId"`
	Author  string    `json:"authorId"`
	Body    string    `json:"body"`
	Created time.Time `json:"createdAt"`
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) *Store { return &Store{db} }

func kindOK(k string) bool { return k == "figure" || k == "report" }

// List returns the comments on one figure or report of ws, oldest first. The caller has already found the target in ws.
func (s *Store) List(ws workspace.ID, kind string, target int64) ([]Comment, error) {
	if !kindOK(kind) {
		return nil, ErrBadKind
	}
	rows, err := s.db.Query(`SELECT id, kind, target_id, created_by, body, created_at FROM member_comments
		WHERE workspace_id=? AND kind=? AND target_id=? ORDER BY id`, ws, kind, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var at int64
		if err := rows.Scan(&c.ID, &c.Kind, &c.Target, &c.Author, &c.Body, &at); err != nil {
			return nil, err
		}
		c.Created = time.Unix(at, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Add records a comment by an account on a target of ws. The caller has already found the target in ws.
func (s *Store) Add(ws workspace.ID, kind string, target int64, by, body string) (Comment, error) {
	if !kindOK(kind) {
		return Comment{}, ErrBadKind
	}
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > MaxBody || !utf8.ValidString(body) {
		return Comment{}, ErrBadBody
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO member_comments(workspace_id, created_by, kind, target_id, body, created_at) VALUES(?,?,?,?,?,?)`, ws, by, kind, target, body, now)
	if err != nil {
		return Comment{}, err
	}
	id, _ := res.LastInsertId()
	return Comment{ID: id, Kind: kind, Target: target, Author: by, Body: body, Created: time.Unix(now, 0).UTC()}, nil
}

// Get returns one comment of ws; another workspace's id is ErrNotFound.
func (s *Store) Get(ws workspace.ID, id int64) (Comment, error) {
	var c Comment
	var at int64
	err := s.db.QueryRow(`SELECT id, kind, target_id, created_by, body, created_at FROM member_comments WHERE id=? AND workspace_id=?`, id, ws).
		Scan(&c.ID, &c.Kind, &c.Target, &c.Author, &c.Body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, ErrNotFound
	}
	c.Created = time.Unix(at, 0).UTC()
	return c, err
}

// Delete removes a comment of ws. The caller decides who may (its author, or a Maintainer or Owner).
func (s *Store) Delete(ws workspace.ID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM member_comments WHERE id=? AND workspace_id=?`, id, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTarget removes every comment on a target of ws (the target was deleted).
func (s *Store) DeleteTarget(ws workspace.ID, kind string, target int64) error {
	_, err := s.db.Exec(`DELETE FROM member_comments WHERE workspace_id=? AND kind=? AND target_id=?`, ws, kind, target)
	return err
}
