// Package runs records pipeline executions (a Run is one execution: its parameters, code version, status and
// output files) and links them to registered datasets. A VGT adapter reads the pipeline's run_metadata.toml.
//
// Nothing a run exposes contains a path, host or user name: parameters whose key or value looks like a location are
// dropped when the run is parsed, and outputs are referenced by dataset id and file name only.
package runs

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

const (
	MaxMetadataBytes = 256 << 10
	maxParams        = 200
	maxValue         = 200
)

var ErrNotFound = errors.New("not found")

type Param struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // config | run_parameters
}

type Output struct {
	Role      string `json:"role"` // psi | reliability | support | fit_rbar2
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
}

type Run struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name"`
	Kind           string            `json:"kind"`
	ImportedAt     time.Time         `json:"importedAt"`
	GeneratedAt    string            `json:"generatedAt"`
	GitCommit      string            `json:"gitCommit"`
	Dirty          bool              `json:"dirty"`
	Reproducible   bool              `json:"reproducibleFromCommit"`
	ElapsedSeconds *float64          `json:"elapsedSeconds"`
	Params         []Param           `json:"params"`
	Diagnostics    map[string]string `json:"diagnostics"`
	InputName      string            `json:"inputName"`
	InputSizeBytes int64             `json:"inputSizeBytes"`
	Outputs        []Output          `json:"outputs"`
	Metrics        []Metric          `json:"metrics"`
	// Review is the researcher's own assessment, kept apart from whether the computation finished.
	Review Review `json:"review"`
	// Archived runs are hidden from the lists but still resolve by id (ADR 0041).
	Archived bool `json:"archived"`
	// Environment is what the job observed about where it ran (ADR 0054), empty for a run no Vesqira job produced.
	Environment []EnvFact `json:"environment"`
	// Controls are the smoothing, noise model and control construction stated for the run; null when none was stated.
	Controls *Controls `json:"controls"`
}

// Review separates three different things: the computation finishing (the run exists), scientific QC, and interpretation.
type Review struct {
	QC             string `json:"qc"`             // not_assessed | passed | failed | inconclusive
	Interpretation string `json:"interpretation"` // not_interpreted | supports | challenges | inconclusive
	Note           string `json:"note"`
}

var qcValues = map[string]bool{"not_assessed": true, "passed": true, "failed": true, "inconclusive": true}
var interpValues = map[string]bool{"not_interpreted": true, "supports": true, "challenges": true, "inconclusive": true}

// Summary is what leaves the machine with a figure: no paths, ids or locations.
type Summary struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Role         string   `json:"role"`
	GitCommit    string   `json:"gitCommit"`
	Dirty        bool     `json:"dirty"`
	Reproducible bool     `json:"reproducibleFromCommit"`
	GeneratedAt  string   `json:"generatedAt"`
	Params       []Param  `json:"params"`
	Metrics      []Metric `json:"metrics"`
	Review       Review   `json:"review"`
	// Controls travel with the run to a reviewer: what was smoothed, the noise model and how a control was built, each with who
	// said so. Absent in summaries frozen before they existed.
	Controls *Controls `json:"controls,omitempty"`
}

// ---- parsing ----

type vgtMeta struct {
	GeneratedAt string         `toml:"generated_at_utc"`
	GitCommit   string         `toml:"gitcommit"`
	Config      map[string]any `toml:"config"`
	Diag        map[string]any `toml:"diagnostics"`
	Input       struct {
		Path      string `toml:"path"`
		SizeBytes int64  `toml:"size_bytes"`
		Stem      string `toml:"stem"`
	} `toml:"input"`
	Outputs map[string]any `toml:"outputs"`
	Perf    struct {
		Elapsed *float64 `toml:"elapsed_seconds"`
	} `toml:"performance"`
	RunParams map[string]any `toml:"run_parameters"`
	Metrics   map[string]any `toml:"metrics"`
}

// Parsed is a run read from metadata, before it is linked to datasets.
type Parsed struct {
	Run
	// OutputFiles maps role to a file name that must sit next to the metadata file.
	OutputFiles map[string]string
}

var outputKeys = map[string]string{"psi_path": "psi", "reliability_path": "reliability", "support_path": "support", "fit_rbar2_path": "fit_rbar2"}
var locationKey = regexp.MustCompile(`(?i)(^|_)(file|path|dir|directory|home|root|host|user)(_|$)`)

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case int64, float64, bool:
		return fmt.Sprint(x), true
	case time.Time:
		return x.UTC().Format(time.RFC3339), true
	}
	return "", false
}

// looksLikeLocation reports values that name a place on some machine. They are never kept.
func looksLikeLocation(s string) bool {
	return strings.Contains(s, "/") || strings.Contains(s, `\`) || strings.HasPrefix(s, "~")
}

func params(m map[string]any, source string) []Param {
	var out []Param
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, ok := scalar(m[k])
		if !ok || locationKey.MatchString(k) || looksLikeLocation(s) || len(k) > 80 {
			continue
		}
		if len(s) > maxValue {
			s = s[:maxValue]
		}
		out = append(out, Param{Key: k, Value: s, Source: source})
	}
	return out
}

// Settings keeps the scalar entries of any decoded run record that do not name a place on a machine: the rule run
// import applies, shared so that discovery shows the same settings an import would keep.
func Settings(m map[string]any, source string) []Param { return params(m, source) }

// ParseVGT reads the VGT pipeline's run_metadata.toml. Output paths in the file are relative to the machine the run
// happened on, so only their file names are kept and the caller looks for them next to the metadata.
func ParseVGT(data []byte, dirName string) (Parsed, error) {
	if len(data) > MaxMetadataBytes {
		return Parsed{}, errors.New("run metadata is too large")
	}
	var m vgtMeta
	if _, err := toml.Decode(string(data), &m); err != nil {
		return Parsed{}, errors.New("run metadata is not valid TOML")
	}
	if m.Outputs == nil && m.GitCommit == "" && m.Config == nil {
		return Parsed{}, errors.New("this does not look like a VGT run_metadata.toml")
	}
	p := Parsed{OutputFiles: map[string]string{}}
	p.Kind = "vgt"
	p.Name = dirName
	p.GeneratedAt = m.GeneratedAt
	p.GitCommit = m.GitCommit
	p.Dirty = strings.HasSuffix(m.GitCommit, "-dirty")
	p.Reproducible = m.GitCommit != "" && !p.Dirty // a dirty tree is not described by its commit hash
	p.ElapsedSeconds = m.Perf.Elapsed
	p.InputName = filepath.Base(m.Input.Path)
	if m.Input.Path == "" {
		p.InputName = ""
	}
	p.InputSizeBytes = m.Input.SizeBytes
	p.Params = mergeParams(params(m.Config, "config"), params(m.RunParams, "run_parameters"))
	if len(p.Params) > maxParams {
		p.Params = p.Params[:maxParams]
	}
	p.Metrics = parseMetrics(m.Metrics)
	p.Diagnostics = map[string]string{}
	for k, v := range m.Diag {
		if s, ok := scalar(v); ok && len(k) <= 60 {
			p.Diagnostics[k] = s
		}
	}
	for k, role := range outputKeys {
		if s, ok := m.Outputs[k].(string); ok && s != "" {
			if b := filepath.Base(filepath.FromSlash(s)); b != "." && b != string(filepath.Separator) {
				p.OutputFiles[role] = b
			}
		}
	}
	return p, nil
}

// ---- store ----

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS runs(
			id INTEGER PRIMARY KEY AUTOINCREMENT, dir TEXT NOT NULL UNIQUE, name TEXT NOT NULL, kind TEXT NOT NULL,
			imported_at INTEGER NOT NULL, generated_at TEXT NOT NULL, git_commit TEXT NOT NULL, elapsed REAL,
			input_name TEXT NOT NULL, input_size INTEGER NOT NULL, params TEXT NOT NULL, diagnostics TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS run_review(run_id INTEGER PRIMARY KEY, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS run_review_history(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, at INTEGER NOT NULL, qc TEXT NOT NULL, interpretation TEXT NOT NULL, note TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS run_outputs(
			run_id INTEGER NOT NULL, role TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, role))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	if err := schema.AddColumn(db, "runs", "metrics", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return nil, err
	}
	for _, c := range []string{"environment", "controls"} {
		if err := schema.AddColumn(db, "runs", c, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return nil, err
		}
	}
	if err := schema.EnsureArchive(db); err != nil {
		return nil, err
	}
	st := &Store{db}
	if err := st.openROHSA(); err != nil {
		return nil, err
	}
	return st, nil
}

func nonNil(m []Metric) []Metric {
	if m == nil {
		return []Metric{}
	}
	return m
}

// IDByDir is the id of the run imported from dir (the canonical folder, host-qualified for a remote one), or 0.
func (s *Store) IDByDir(dir string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM runs WHERE dir=?`, dir).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Save records a run (or refreshes it when the same directory is imported again) with its linked outputs.
func (s *Store) Save(dir string, p Parsed, outs []Output) (Run, error) {
	pj, _ := jsonMarshal(p.Params)
	dj, _ := jsonMarshal(p.Diagnostics)
	mj, _ := jsonMarshal(nonNil(p.Metrics))
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO runs(dir,name,kind,imported_at,generated_at,git_commit,elapsed,input_name,input_size,params,diagnostics,metrics)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dir) DO UPDATE SET name=excluded.name,imported_at=excluded.imported_at,generated_at=excluded.generated_at,
			git_commit=excluded.git_commit,elapsed=excluded.elapsed,input_name=excluded.input_name,input_size=excluded.input_size,
			params=excluded.params,diagnostics=excluded.diagnostics,metrics=excluded.metrics`,
		dir, p.Name, p.Kind, time.Now().Unix(), p.GeneratedAt, p.GitCommit, p.ElapsedSeconds, p.InputName, p.InputSizeBytes, pj, dj, mj)
	if err != nil {
		return Run{}, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM runs WHERE dir=?`, dir).Scan(&id); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(`DELETE FROM run_outputs WHERE run_id=?`, id); err != nil {
		return Run{}, err
	}
	for _, o := range outs {
		if _, err := tx.Exec(`INSERT INTO run_outputs(run_id,role,dataset_id,name) VALUES(?,?,?,?)`, id, o.Role, o.DatasetID, o.Name); err != nil {
			return Run{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return s.Get(id)
}

var runCols = `id,name,kind,imported_at,generated_at,git_commit,elapsed,input_name,input_size,params,diagnostics,metrics,environment,controls,` + schema.ArchivedExpr("run", "runs.id")

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var at int64
	var el sql.NullFloat64
	var pj, dj, mj, ej, cj string
	if err := row.Scan(&r.ID, &r.Name, &r.Kind, &at, &r.GeneratedAt, &r.GitCommit, &el, &r.InputName, &r.InputSizeBytes, &pj, &dj, &mj, &ej, &cj, &r.Archived); err != nil {
		return Run{}, err
	}
	r.ImportedAt = time.Unix(at, 0).UTC()
	if el.Valid {
		r.ElapsedSeconds = &el.Float64
	}
	r.Dirty = strings.HasSuffix(r.GitCommit, "-dirty")
	r.Reproducible = r.GitCommit != "" && !r.Dirty
	_ = jsonUnmarshal(pj, &r.Params)
	_ = jsonUnmarshal(dj, &r.Diagnostics)
	_ = jsonUnmarshal(mj, &r.Metrics)
	r.Metrics = nonNil(r.Metrics)
	_ = jsonUnmarshal(ej, &r.Environment)
	if r.Environment == nil {
		r.Environment = []EnvFact{}
	}
	var c Controls
	if jsonUnmarshal(cj, &c) == nil && !c.Empty() {
		r.Controls = &c
	}
	if r.Params == nil {
		r.Params = []Param{}
	}
	return r, nil
}

func (s *Store) outputs(id int64) ([]Output, error) {
	rows, err := s.db.Query(`SELECT role,dataset_id,name FROM run_outputs WHERE run_id=? ORDER BY role`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Output{}
	for rows.Next() {
		var o Output
		if err := rows.Scan(&o.Role, &o.DatasetID, &o.Name); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) Get(id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	if r.Outputs, err = s.outputs(id); err != nil {
		return r, err
	}
	r.Review = s.review(id)
	return r, nil
}

func (s *Store) review(id int64) Review {
	rv := Review{QC: "not_assessed", Interpretation: "not_interpreted"}
	var qc, in, note string
	if s.db.QueryRow(`SELECT qc,interpretation,note FROM run_review WHERE run_id=?`, id).Scan(&qc, &in, &note) == nil {
		rv = Review{QC: qc, Interpretation: in, Note: note}
	}
	return rv
}

// SetReview records the researcher's assessment. Completion of the computation is not an assessment of the science.
func (s *Store) SetReview(id int64, rv Review) error {
	if !qcValues[rv.QC] || !interpValues[rv.Interpretation] {
		return errors.New("unknown assessment value")
	}
	if len(rv.Note) > 500 {
		rv.Note = rv.Note[:500]
	}
	if _, err := s.Get(id); err != nil {
		return err
	}
	// The current assessment and its history change together: nothing is ever overwritten without a trace.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO run_review(run_id,qc,interpretation,note) VALUES(?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET qc=excluded.qc,interpretation=excluded.interpretation,note=excluded.note`, id, rv.QC, rv.Interpretation, rv.Note); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO run_review_history(run_id,at,qc,interpretation,note) VALUES(?,?,?,?,?)`, id, time.Now().Unix(), rv.QC, rv.Interpretation, rv.Note); err != nil {
		return err
	}
	return tx.Commit()
}

// ReviewRevision is one past or present state of a run's assessment.
type ReviewRevision struct {
	At             time.Time `json:"at"`
	QC             string    `json:"qc"`
	Interpretation string    `json:"interpretation"`
	Note           string    `json:"note"`
}

// History lists the assessments of a run, newest first. A run never assessed has none.
func (s *Store) History(id int64) ([]ReviewRevision, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT at,qc,interpretation,note FROM run_review_history WHERE run_id=? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewRevision{}
	for rows.Next() {
		var rv ReviewRevision
		var at int64
		if err := rows.Scan(&at, &rv.QC, &rv.Interpretation, &rv.Note); err != nil {
			return nil, err
		}
		rv.At = time.Unix(at, 0).UTC()
		out = append(out, rv)
	}
	return out, rows.Err()
}

func (s *Store) List() ([]Run, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs ORDER BY imported_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var rs []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	out := []Run{}
	for _, r := range rs {
		if r.Outputs, err = s.outputs(r.ID); err != nil {
			return nil, err
		}
		r.Review = s.review(r.ID)
		out = append(out, r)
	}
	return out, nil
}

// OutputRoles maps every dataset that is some run's output to its role in that run (the latest run when several list it).
func (s *Store) OutputRoles() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT dataset_id,role FROM run_outputs ORDER BY run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out[id] = role
	}
	return out, rows.Err()
}

// ForDataset returns the reviewer-safe summary of the run that produced a dataset, or nil.
func (s *Store) ForDataset(datasetID int64) (*Summary, error) {
	var id int64
	var role string
	err := s.db.QueryRow(`SELECT run_id,role FROM run_outputs WHERE dataset_id=? ORDER BY run_id DESC LIMIT 1`, datasetID).Scan(&id, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.SummaryOf(id, role)
}

// SummaryOf is the reviewer-safe account of a run (no paths, directories or hosts), as a figure's provenance and a
// published report show it. role is the output's role when the summary is about one output, else "".
func (s *Store) SummaryOf(id int64, role string) (*Summary, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	commit := r.GitCommit
	if i := strings.Index(commit, "-dirty"); i > 12 {
		commit = commit[:12] + commit[i:]
	} else if len(commit) > 12 && i < 0 {
		commit = commit[:12]
	}
	return &Summary{Name: r.Name, Kind: r.Kind, Role: role, GitCommit: commit, Dirty: r.Dirty, Reproducible: r.Reproducible, GeneratedAt: r.GeneratedAt, Params: r.Params, Metrics: r.Metrics, Review: r.Review, Controls: r.Controls}, nil
}

// mergeParams keeps one row for a setting recorded identically in both sections, and both rows when they disagree.
func mergeParams(a, b []Param) []Param {
	seen := map[string]string{}
	for _, p := range a {
		seen[p.Key] = p.Value
	}
	out := a
	for _, p := range b {
		if v, ok := seen[p.Key]; ok && v == p.Value {
			continue
		}
		out = append(out, p)
	}
	return out
}

// SetEnvironment keeps what a job observed about where it ran. It replaces an earlier record: the latest import of the run's
// folder is the one that read the file.
func (s *Store) SetEnvironment(id int64, facts []EnvFact) error {
	b, err := jsonMarshal(facts)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE runs SET environment=? WHERE id=?`, b, id)
	return affected(res, err)
}

// SetControls replaces the controls stated for a run; nil or an empty set clears them. Blocks must already be validated and
// stamped (Controls.Declare).
func (s *Store) SetControls(id int64, c *Controls) error {
	j := ""
	if !c.Empty() {
		b, err := jsonMarshal(c)
		if err != nil {
			return err
		}
		j = b
	}
	res, err := s.db.Exec(`UPDATE runs SET controls=? WHERE id=?`, j, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
