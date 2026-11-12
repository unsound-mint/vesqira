// Package audit keeps an append-only record of the actions that change state or let someone see data: which host was
// added, which job was submitted, which board was published or revoked. It never stores secrets or file contents; the
// detail is a short, human-readable line.
package audit

import (
	"context"
	"database/sql"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
	"strings"
	"time"
)

type Entry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Kind   string    `json:"kind"`   // host | job | run | board | snapshot | dataset | file
	Target int64     `json:"target"` // the id of that object, 0 when none
	Detail string    `json:"detail"`
	// Who did it (ADR 0050 section 4): the account id, or "local-owner" in local mode; the account a background action ran on
	// behalf of, if any; and how the actor was authenticated ("local" today).
	Actor      string `json:"actor"`
	OnBehalfOf string `json:"on_behalf_of"`
	Via        string `json:"via"`
}

// Actor is who an entry is recorded for, and in which workspace (0: none, the entry belongs to the server). The package knows
// nothing of internal/authz; the server hands it plain values.
type Actor struct {
	ID, OnBehalfOf, Via string
	Workspace           workspace.ID
}

// LocalOwner is the actor of every entry in local mode. internal/server tests that it matches authz.LocalOwner.
var LocalOwner = Actor{ID: "local-owner", Via: "local", Workspace: workspace.Local}

// System is the actor of an entry made by background work with no account to name, on the group server (ADR 0050 section 4).
var System = Actor{ID: "system", Via: "system"}

type actorKey struct{}

// WithActor returns a context whose entries RecordCtx attributes to a. The server installs the request's actor here.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

type Store struct {
	db *sql.DB
	// Default is who Record writes. Local mode records LocalOwner for everything; a mode with several actors records with RecordAs.
	Default Actor
}

func Open(db *sql.DB) (*Store, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL, actor TEXT NOT NULL DEFAULT 'local-owner', on_behalf_of TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT 'local')`,
		`CREATE INDEX IF NOT EXISTS audit_target ON audit(kind, target)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	if err := schema.Scope(db, "audit"); err != nil {
		return nil, err
	}
	return &Store{db: db, Default: LocalOwner}, nil
}

// Record appends one entry of workspace ws for the store's default actor. A nil store records nothing, so callers need no checks.
func (s *Store) Record(ws workspace.ID, action, kind string, target int64, detail string) {
	if s == nil {
		return
	}
	s.RecordAs(ws, s.Default, action, kind, target, detail)
}

// RecordCtx appends one entry for the actor the context carries (WithActor), in that actor's workspace, or for the store's
// default actor when it carries none.
func (s *Store) RecordCtx(ctx context.Context, action, kind string, target int64, detail string) {
	if s == nil {
		return
	}
	a, ok := ctx.Value(actorKey{}).(Actor)
	if !ok {
		a = s.Default
	}
	s.RecordAs(a.Workspace, a, action, kind, target, detail)
}

// RecordAs appends one entry of workspace ws for the given actor.
func (s *Store) RecordAs(ws workspace.ID, a Actor, action, kind string, target int64, detail string) {
	if s == nil {
		return
	}
	detail = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, detail)
	if len(detail) > 300 {
		detail = detail[:300]
	}
	_, _ = s.db.Exec(`INSERT INTO audit(at,action,kind,target,detail,actor,on_behalf_of,via,workspace_id) VALUES(?,?,?,?,?,?,?,?,?)`, time.Now().Unix(), action, kind, target, detail, a.ID, a.OnBehalfOf, a.Via, ws)
}

// List returns the workspace's newest entries first; kind and target narrow it when set.
func (s *Store) List(ws workspace.ID, kind string, target int64, limit int) ([]Entry, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,at,action,kind,target,detail,actor,on_behalf_of,via FROM audit WHERE workspace_id=?`
	args := []any{ws}
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
		if target != 0 {
			q += ` AND target=?`
			args = append(args, target)
		}
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Action, &e.Kind, &e.Target, &e.Detail, &e.Actor, &e.OnBehalfOf, &e.Via); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
