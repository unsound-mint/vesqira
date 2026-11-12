package runs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Saved views of the runs table (ADR 0062): a name for a filter query, the columns the researcher exposed, the sort and the
// plot's axes. They are mutable working state, not evidence: saving one under an existing name replaces it. A view holds
// words and keys only, never run ids or values, so it keeps working as runs come and go, and it cannot carry data off the machine.

const (
	// MaxViews bounds what the table can hold; a researcher keeps a handful.
	MaxViews    = 100
	maxName     = 80
	maxQuery    = 2000
	maxColumns  = 200
	maxColumnID = 120
)

var (
	ErrViewNotFound = errors.New("view not found")
	ErrViewInvalid  = errors.New("invalid")
)

// ViewSort is the column a view sorts by; Dir is 1 (ascending) or -1.
type ViewSort struct {
	Column string `json:"column"`
	Dir    int    `json:"dir"`
}

// ViewPlot is the cross-run plot of a view. Y is a result key; X is a parameter or result key (XKind says which); By is a
// parameter key to colour by. Empty means not chosen.
type ViewPlot struct {
	Y     string `json:"y"`
	X     string `json:"x"`
	XKind string `json:"xKind"`
	By    string `json:"by"`
}

// ViewSpec is everything a saved view remembers.
type ViewSpec struct {
	Query   string    `json:"query"`
	Columns []string  `json:"columns"`
	Sort    *ViewSort `json:"sort"`
	Plot    *ViewPlot `json:"plot"`
}

type View struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Spec      ViewSpec  `json:"spec"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func invalid(msg string) error { return errors.New("invalid: " + msg) }

// oneLine reports whether s is a single line of printable text.
func oneLine(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 32 || r == 127 })
}

// CleanView checks a view and returns it with its name trimmed. Columns are ids the browser forms ("status", "p:block_size",
// "m:am"); only their shape is checked, since a column for a parameter that no run has yet is still a fair thing to keep.
func CleanView(name string, sp ViewSpec) (string, ViewSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName || !oneLine(name) {
		return "", sp, invalid("a view needs a name of one line, at most 80 characters")
	}
	if len(sp.Query) > maxQuery || !oneLine(sp.Query) {
		return "", sp, invalid("the query must be one line of at most 2000 characters")
	}
	if len(sp.Columns) > maxColumns {
		return "", sp, invalid("too many columns")
	}
	seen := map[string]bool{}
	cols := make([]string, 0, len(sp.Columns))
	for _, c := range sp.Columns {
		if c == "" || len(c) > maxColumnID || !oneLine(c) {
			return "", sp, invalid("a column id must be one line of at most 120 characters")
		}
		if !seen[c] {
			seen[c] = true
			cols = append(cols, c)
		}
	}
	sp.Columns = cols
	if sp.Sort != nil && (sp.Sort.Column == "" || len(sp.Sort.Column) > maxColumnID || !oneLine(sp.Sort.Column) || (sp.Sort.Dir != 1 && sp.Sort.Dir != -1)) {
		return "", sp, invalid("the sort needs a column and a direction of 1 or -1")
	}
	if p := sp.Plot; p != nil {
		for _, k := range []string{p.Y, p.X, p.By} {
			if len(k) > maxColumnID || !oneLine(k) {
				return "", sp, invalid("a plot key must be one line of at most 120 characters")
			}
		}
		if p.XKind != "" && p.XKind != "param" && p.XKind != "metric" {
			return "", sp, invalid("the plot's x is a param or a metric")
		}
	}
	return name, sp, nil
}

// SaveView stores a view under its name, replacing a view of that name (compared without regard to case).
func (s *Store) SaveView(ws workspace.ID, by, name string, sp ViewSpec) (View, error) {
	name, sp, err := CleanView(name, sp)
	if err != nil {
		return View{}, err
	}
	raw, err := json.Marshal(sp)
	if err != nil {
		return View{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return View{}, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM run_views WHERE workspace_id=? AND name=? COLLATE NOCASE`, ws, name).Scan(&id)
	now := time.Now().Unix()
	switch {
	case err == nil:
		_, err = tx.Exec(`UPDATE run_views SET name=?, spec=?, updated_at=? WHERE id=? AND workspace_id=?`, name, string(raw), now, id, ws)
	case errors.Is(err, sql.ErrNoRows):
		var n int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM run_views WHERE workspace_id=?`, ws).Scan(&n); err != nil {
			return View{}, err
		}
		if n >= MaxViews {
			return View{}, invalid("at most 100 views are kept; delete one first")
		}
		var res sql.Result
		if res, err = tx.Exec(`INSERT INTO run_views(name,spec,updated_at,workspace_id,created_by) VALUES(?,?,?,?,?)`, name, string(raw), now, ws, by); err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return View{}, err
	}
	if err := tx.Commit(); err != nil {
		return View{}, err
	}
	return View{ID: id, Name: name, Spec: sp, UpdatedAt: time.Unix(now, 0).UTC()}, nil
}

// Views lists the saved views by name.
func (s *Store) Views(ws workspace.ID) ([]View, error) {
	rows, err := s.db.Query(`SELECT id,name,spec,updated_at FROM run_views WHERE workspace_id=? ORDER BY name COLLATE NOCASE`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		var v View
		var raw string
		var at int64
		if err := rows.Scan(&v.ID, &v.Name, &raw, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &v.Spec); err != nil {
			continue // a row this version cannot read is skipped, not fatal for the list
		}
		if v.Spec.Columns == nil {
			v.Spec.Columns = []string{}
		}
		v.UpdatedAt = time.Unix(at, 0).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteView removes a view; the runs it filtered are untouched.
func (s *Store) DeleteView(ws workspace.ID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM run_views WHERE id=? AND workspace_id=?`, id, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrViewNotFound
	}
	return nil
}
