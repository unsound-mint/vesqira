// Package projects stores the folders the researcher opened as projects, the entries the last scan of each found, and
// the answers given to its "needs your input" list. A project row is a reference (host and canonical path): the files
// stay where they are. The scan entries are a cache that a rescan replaces; the answers are the researcher's and stay.
//
// Tables are created here, idempotently, like the other stores (the import rule tables from the DDL shared with schema 18; see
// internal/schema).
package projects

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

var ErrNotFound = errors.New("project not found")

type Project struct {
	ID         int64           `json:"id"`
	HostID     int64           `json:"hostId"` // 0 is this computer
	Path       string          `json:"path"`   // canonical (symlinks resolved) on that machine
	Name       string          `json:"name"`
	OpenedAt   time.Time       `json:"openedAt"`
	LastScanAt *time.Time      `json:"lastScanAt"`
	Summary    json.RawMessage `json:"summary"`
}

// Row is one entry of the last scan, in the group it is browsed under: runs, data, configs or jobs. Parent is the
// folder it is listed in ("" for the project folder), Path its full relative path. A run's outputs are rows of the
// runs group whose parent is the run folder. Dir rows are made up from the paths so the tree can be paged by folder.
type Row struct {
	Group  string          `json:"group"`
	Parent string          `json:"parent"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"` // dir | run | output | data | config | job
	Path   string          `json:"path"`
	Format string          `json:"format"`
	Data   json.RawMessage `json:"data"`
}

type Answer struct {
	Need       string `json:"need"`
	Answer     string `json:"answer"`
	Value      string `json:"value"`
	DeclaredBy string `json:"declaredBy"`
	Date       string `json:"date"`
	Written    bool   `json:"writtenToProject"` // also in .vesqira/project.toml
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	for _, q := range []string{
		schema.ProjectsTable,
		`CREATE TABLE IF NOT EXISTS project_entries(
			project_id INTEGER NOT NULL, grp TEXT NOT NULL, parent TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL,
			path TEXT NOT NULL, format TEXT NOT NULL DEFAULT '', data TEXT NOT NULL, PRIMARY KEY(project_id, grp, parent, name))`,
		`CREATE INDEX IF NOT EXISTS project_entries_format ON project_entries(project_id, format)`,
		schema.ImportRulesTables,
		`CREATE TABLE IF NOT EXISTS project_answers(
			project_id INTEGER NOT NULL, need TEXT NOT NULL, answer TEXT NOT NULL, value TEXT NOT NULL, declared_by TEXT NOT NULL,
			date TEXT NOT NULL, written INTEGER NOT NULL, PRIMARY KEY(project_id, need))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	return &Store{db}, nil
}

const cols = `id,host_id,root,name,opened_at,last_scan_at,summary`

func scan(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var opened int64
	var last sql.NullInt64
	var sum string
	if err := row.Scan(&p.ID, &p.HostID, &p.Path, &p.Name, &opened, &last, &sum); err != nil {
		return Project{}, err
	}
	p.OpenedAt = time.Unix(opened, 0).UTC()
	if last.Valid {
		t := time.Unix(last.Int64, 0).UTC()
		p.LastScanAt = &t
	}
	p.Summary = json.RawMessage(sum)
	return p, nil
}

// Touch records that the folder was opened now, creating the project the first time.
func (s *Store) Touch(hostID int64, root, name string) (Project, error) {
	_, err := s.db.Exec(`INSERT INTO projects(host_id,root,name,opened_at) VALUES(?,?,?,?)
		ON CONFLICT(host_id,root) DO UPDATE SET opened_at=excluded.opened_at`, hostID, root, name, time.Now().Unix())
	if err != nil {
		return Project{}, err
	}
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM projects WHERE host_id=? AND root=?`, hostID, root))
}

func (s *Store) Get(id int64) (Project, error) {
	p, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM projects WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// List returns the projects, the most recently opened first.
func (s *Store) List(limit int) ([]Project, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM projects ORDER BY opened_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Roots are the canonical folders of the projects on this computer: each is a readable root while it is open.
func (s *Store) Roots(hostID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT root FROM projects WHERE host_id=? ORDER BY id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveScan replaces the project's entries and summary with a new scan's, in one transaction.
func (s *Store) SaveScan(id int64, at time.Time, summary json.RawMessage, rows []Row) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM project_entries WHERE project_id=?`, id); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT OR REPLACE INTO project_entries(project_id,grp,parent,name,kind,path,format,data) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(id, r.Group, r.Parent, r.Name, r.Kind, r.Path, r.Format, string(r.Data)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE projects SET last_scan_at=?, summary=? WHERE id=?`, at.Unix(), string(summary), id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSummary replaces the summary alone (a scan that is still registering files, or one that was cancelled).
func (s *Store) SetSummary(id int64, summary json.RawMessage) error {
	_, err := s.db.Exec(`UPDATE projects SET summary=? WHERE id=?`, string(summary), id)
	return err
}

func scanRow(row interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var d string
	if err := row.Scan(&r.Group, &r.Parent, &r.Name, &r.Kind, &r.Path, &r.Format, &d); err != nil {
		return Row{}, err
	}
	r.Data = json.RawMessage(d)
	return r, nil
}

const rowCols = `grp,parent,name,kind,path,format,data`

// Children pages the entries listed in one folder of one group, folders first, then by name.
func (s *Store) Children(id int64, group, parent string, limit, offset int) ([]Row, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM project_entries WHERE project_id=? AND grp=? AND parent=?`, id, group, parent).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM project_entries WHERE project_id=? AND grp=? AND parent=?
		ORDER BY (kind<>'dir'), name LIMIT ? OFFSET ?`, id, group, parent, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ByFormat lists the entries of one format (FITS for the registration and the units questions).
func (s *Store) ByFormat(id int64, format string) ([]Row, error) {
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM project_entries WHERE project_id=? AND format=? AND kind<>'dir' ORDER BY path`, id, format)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Files lists every entry that is a file or a run (not a folder), for comparing a new scan with the last one.
func (s *Store) Files(id int64) ([]Row, error) {
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM project_entries WHERE project_id=? AND kind<>'dir'`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunRows lists every run entry of the project (not its outputs).
func (s *Store) RunRows(id int64) ([]Row, error) {
	rows, err := s.db.Query(`SELECT `+rowCols+` FROM project_entries WHERE project_id=? AND kind='run' ORDER BY path`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAnswer records an answer; a later answer to the same need replaces the earlier one.
func (s *Store) SetAnswer(id int64, a Answer) error {
	w := 0
	if a.Written {
		w = 1
	}
	_, err := s.db.Exec(`INSERT INTO project_answers(project_id,need,answer,value,declared_by,date,written) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project_id,need) DO UPDATE SET answer=excluded.answer,value=excluded.value,declared_by=excluded.declared_by,date=excluded.date,written=excluded.written`,
		id, a.Need, a.Answer, a.Value, a.DeclaredBy, a.Date, w)
	return err
}

func (s *Store) Answers(id int64) (map[string]Answer, error) {
	rows, err := s.db.Query(`SELECT need,answer,value,declared_by,date,written FROM project_answers WHERE project_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Answer{}
	for rows.Next() {
		var a Answer
		var w int
		if err := rows.Scan(&a.Need, &a.Answer, &a.Value, &a.DeclaredBy, &a.Date, &w); err != nil {
			return nil, err
		}
		a.Written = w == 1
		out[a.Need] = a
	}
	return out, rows.Err()
}
