// Package boards stores figures (pinned evidence), research boards (mutable drafts), frozen review snapshots,
// review capability tokens and reviewer comments. It never stores or returns a source path: everything a reviewer can
// ever see is built here from an explicit whitelist of fields.
package boards

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

const (
	MaxPNGBytes   = 8 << 20
	MaxPNGSide    = 4096
	MaxText       = 2000
	MaxComment    = 4000
	MaxCommentsPS = 500 // comments per snapshot
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

type Store struct {
	db  *sql.DB
	dir string // content-addressed preview files live in dir/figures
}

func Open(db *sql.DB, dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "figures"), 0o700); err != nil {
		return nil, err
	}
	if err := schema.EnsureArchive(db); err != nil {
		return nil, err
	}
	for _, q := range []string{
		schema.FiguresTable,
		`CREATE TABLE IF NOT EXISTS boards(
			id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS board_items(
			board_id INTEGER NOT NULL, position INTEGER NOT NULL, figure_id INTEGER, job_id INTEGER, note TEXT NOT NULL DEFAULT '',
			layout_x INTEGER, layout_y INTEGER, layout_w INTEGER, layout_h INTEGER,
			PRIMARY KEY(board_id, position), CHECK((figure_id IS NULL) <> (job_id IS NULL)))`,
		`CREATE TABLE IF NOT EXISTS snapshots(
			id INTEGER PRIMARY KEY AUTOINCREMENT, board_id INTEGER NOT NULL, label TEXT NOT NULL, created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, token_hash TEXT NOT NULL UNIQUE, frozen TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS comments(
			id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, figure_revision TEXT NOT NULL, author TEXT NOT NULL,
			body TEXT NOT NULL, created_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	for _, c := range [][2]string{{"superseded_by", "INTEGER NOT NULL DEFAULT 0"}, {"supersede_reason", "TEXT NOT NULL DEFAULT ''"}, {"supersede_at", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := schema.AddColumn(db, "snapshots", c[0], c[1]); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS snapshot_outcomes(id INTEGER PRIMARY KEY AUTOINCREMENT, snapshot_id INTEGER NOT NULL, at INTEGER NOT NULL, outcome TEXT NOT NULL, note TEXT NOT NULL, author TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	// a panel may carry its place on the board's grid (ADR 0060); older boards have none and the client derives one
	for _, c := range []string{"layout_x", "layout_y", "layout_w", "layout_h"} {
		if err := schema.AddColumn(db, "board_items", c, "INTEGER"); err != nil {
			return nil, err
		}
	}
	// A catalog older than schema 27 has figures with dataset_id NOT NULL and no kind: schema.Guard rebuilds it before this runs
	// (ADR 0065), and scene and identity come with FiguresTable, so there is nothing to add here.
	// comments grew threads, anchors, owner replies and a resolved mark; older catalogs get the columns
	for _, c := range [][2]string{{"parent_id", "INTEGER NOT NULL DEFAULT 0"}, {"anchor", "TEXT NOT NULL DEFAULT ''"}, {"from_owner", "INTEGER NOT NULL DEFAULT 0"}, {"resolved_at", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := schema.AddColumn(db, "comments", c[0], c[1]); err != nil {
			return nil, err
		}
	}
	return &Store{db: db, dir: dataDir}, nil
}

// clip keeps at most n characters (not bytes), so a multibyte character is never cut in half.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---------- figures ----------

// Figure is pinned scientific evidence: the picture and the facts it was made from do not change. Title and caption
// are working text and may be edited.
type Figure struct {
	ID int64 `json:"id"`
	// DatasetID is the dataset the figure was made from; null for a cross-run plot (Kind "crossrun", ADR 0065), which is made
	// from runs and has no dataset.
	DatasetID  *int64          `json:"datasetId"`
	Kind       string          `json:"kind"` // dataset | crossrun
	Title      string          `json:"title"`
	Caption    string          `json:"caption"`
	CreatedAt  time.Time       `json:"createdAt"`
	Revision   string          `json:"revision"`
	PreviewSHA string          `json:"previewSha"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Provenance json.RawMessage `json:"provenance"`
	// Scene is the scene/1 description the picture was drawn from (ADR 0042), absent for figures saved before scenes. It
	// names dataset ids, so it is owner-side only: snapshots and published reports copy provenance, never this.
	Scene json.RawMessage `json:"scene,omitempty"`
	// Identity is what was known about the bytes of each dataset the figure was drawn from when it was saved (ADR 0053):
	// size, modification time and the fingerprints, each labelled with what it is. Owner-side only, like Scene: snapshots
	// and published reports copy provenance, never this. Absent for figures saved before identities.
	Identity json.RawMessage `json:"identity,omitempty"`
	// Archived figures are hidden from the lists but still resolve by id, on boards, in reports and in reviews (ADR 0041).
	Archived bool `json:"archived"`
}

// Figure kinds.
const (
	KindDatasetFigure = "dataset"
	KindCrossRun      = "crossrun"
)

// NewFigure carries what the caller knows. Provenance must already be the reviewer-safe document; Scene, when set, is a
// validated scene/1 document (internal/scene), or the cross-run/1 description of a cross-run plot (then DatasetID is nil).
type NewFigure struct {
	// DatasetID is the dataset the figure is made from; 0 means none, which is only valid for a cross-run plot (ADR 0065).
	DatasetID  int64
	Title      string
	Caption    string
	Provenance json.RawMessage
	Scene      json.RawMessage
	Identity   json.RawMessage // optional: the figure-identity/1 document (internal/server figureIdentity)
	// RedrawnFrom is the revision of the figure this one is a redrawing of (ADR 0042, section 5). It is part of the new
	// figure's revision, so a redrawing is a figure of its own even when its pixels equal the original's, and it never
	// finds (or replaces) the original. Empty for every other figure, which keeps the revision it always had.
	RedrawnFrom string
}

func (s *Store) CreateFigure(in NewFigure, pngBytes []byte) (Figure, error) {
	if len(pngBytes) == 0 || len(pngBytes) > MaxPNGBytes {
		return Figure{}, fmt.Errorf("%w: preview must be a PNG of at most %d bytes", ErrInvalid, MaxPNGBytes)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxPNGSide || cfg.Height > MaxPNGSide {
		return Figure{}, fmt.Errorf("%w: preview is not a PNG within %dx%d", ErrInvalid, MaxPNGSide, MaxPNGSide)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil { // full decode: a truncated image is refused
		return Figure{}, fmt.Errorf("%w: preview PNG cannot be decoded", ErrInvalid)
	}
	title, caption := clip(in.Title, 200), clip(in.Caption, MaxText)
	if title == "" {
		return Figure{}, fmt.Errorf("%w: a figure needs a title", ErrInvalid)
	}
	kind := KindDatasetFigure
	var dataset any = in.DatasetID
	if in.DatasetID == 0 {
		dataset = nil
		kind = KindCrossRun
		if len(in.Scene) == 0 {
			return Figure{}, fmt.Errorf("%w: a figure with no dataset needs the description of the runs it was drawn from", ErrInvalid)
		}
	}
	sum := sha256.Sum256(pngBytes)
	sha := hex.EncodeToString(sum[:])
	// The scene is part of the evidence. Without one the bytes hashed are what they always were, so a figure saved again
	// without a scene still finds its older twin.
	revBytes := append(append(append([]byte{}, in.Provenance...), in.Scene...), sum[:]...)
	if in.RedrawnFrom != "" {
		revBytes = append(revBytes, []byte("redrawn-from:"+in.RedrawnFrom)...)
	}
	rev := sha256.Sum256(revBytes)
	revision := hex.EncodeToString(rev[:])
	file := filepath.Join(s.dir, "figures", sha+".png")
	if _, err := os.Stat(file); err != nil {
		// write to a temporary file and rename, so a crash can never leave a truncated preview under its final name
		tmp, err := os.CreateTemp(filepath.Join(s.dir, "figures"), ".tmp-*")
		if err != nil {
			return Figure{}, err
		}
		_, werr := tmp.Write(pngBytes)
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), file)
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return Figure{}, werr
		}
	}
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO figures(dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(revision) DO NOTHING`, dataset, kind, title, caption, now.Unix(), revision, sha, cfg.Width, cfg.Height, string(in.Provenance), string(in.Scene), string(in.Identity))
	if err != nil {
		return Figure{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 { // the same evidence again returns the existing figure
		return s.FigureByRevision(revision)
	}
	id, _ := res.LastInsertId()
	return s.Figure(id)
}

var figCols = `id,dataset_id,kind,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity,` + schema.ArchivedExpr("figure", "figures.id")

func scanFigure(row interface{ Scan(...any) error }) (Figure, error) {
	var f Figure
	var at int64
	var prov, sc, ident string
	var ds sql.NullInt64
	if err := row.Scan(&f.ID, &ds, &f.Kind, &f.Title, &f.Caption, &at, &f.Revision, &f.PreviewSHA, &f.Width, &f.Height, &prov, &sc, &ident, &f.Archived); err != nil {
		return Figure{}, err
	}
	if ds.Valid {
		f.DatasetID = &ds.Int64
	}
	f.CreatedAt, f.Provenance = time.Unix(at, 0).UTC(), json.RawMessage(prov)
	if sc != "" {
		f.Scene = json.RawMessage(sc)
	}
	if ident != "" {
		f.Identity = json.RawMessage(ident)
	}
	return f, nil
}

func (s *Store) Figure(id int64) (Figure, error) {
	f, err := scanFigure(s.db.QueryRow(`SELECT `+figCols+` FROM figures WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// FigureByRevision finds a figure by its evidence revision (the identity a report embeds).
func (s *Store) FigureByRevision(rev string) (Figure, error) {
	f, err := scanFigure(s.db.QueryRow(`SELECT `+figCols+` FROM figures WHERE revision=?`, rev))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

func (s *Store) Figures() ([]Figure, error) {
	rows, err := s.db.Query(`SELECT ` + figCols + ` FROM figures ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Figure{}
	for rows.Next() {
		f, err := scanFigure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetText edits the working text of a figure. The evidence (picture, provenance, revision) is unchanged.
func (s *Store) SetText(id int64, title, caption string) error {
	title = clip(title, 200)
	if title == "" {
		return fmt.Errorf("%w: a figure needs a title", ErrInvalid)
	}
	res, err := s.db.Exec(`UPDATE figures SET title=?, caption=? WHERE id=?`, title, clip(caption, MaxText), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemovePreview deletes a figure preview file that nothing refers to any more (see lifecycle.Deleted).
func (s *Store) RemovePreview(sha string) error {
	if _, err := s.Preview(sha); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.dir, "figures", sha+".png"))
}

func (s *Store) Preview(sha string) ([]byte, error) {
	if len(sha) != 64 {
		return nil, ErrNotFound
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "figures", sha+".png"))
	if err != nil {
		return nil, ErrNotFound
	}
	return b, nil
}

// ---------- boards ----------

// Item is one panel of a board: a pinned figure, or a job that is still to produce one. A job item holds only the job's
// id; the server fills in the rest from the jobs store (boards does not know jobs).
type Item struct {
	Position int      `json:"position"`
	Kind     string   `json:"kind"` // figure | job
	Figure   *Figure  `json:"figure,omitempty"`
	Job      *JobInfo `json:"job,omitempty"`
	Note     string   `json:"note"`
	// Layout is the panel's place on the board's grid; nil until the researcher first arranges the board (ADR 0060).
	Layout *Rect `json:"layout,omitempty"`
}

// Grid limits (ADR 0060). The grid has GridCols columns of equal width; rows have a fixed height, so y and h are counted in rows.
const (
	GridCols = 12
	MaxRows  = 10000 // y of the top edge
	MaxSpan  = 200   // h
)

// Rect is a panel's place on the grid: column X (0-based) and row Y, spanning W columns and H rows.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (r Rect) valid() bool {
	return r.X >= 0 && r.W >= 1 && r.X+r.W <= GridCols && r.Y >= 0 && r.Y <= MaxRows && r.H >= 1 && r.H <= MaxSpan
}

func (r Rect) overlaps(o Rect) bool {
	return r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H
}

// JobInfo is what the owner's board shows about a followed job. Progress is present only when the job's log says how
// far it is; ETASeconds is an estimate at the rate so far and is nil when there is nothing to base it on.
type JobInfo struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	State       string       `json:"state"`
	SubmittedAt *time.Time   `json:"submittedAt"`
	FinishedAt  *time.Time   `json:"finishedAt"`
	RunID       *int64       `json:"runId"`
	Progress    *JobProgress `json:"progress,omitempty"`
	// Label and SweepID are set for a job that belongs to a parameter sweep ("N = 12, λ = 10"). They are the owner's:
	// a snapshot freezes only a job's name and state, so neither reaches a reviewer.
	Label   string `json:"label,omitempty"`
	SweepID *int64 `json:"sweepId,omitempty"`
}

type JobProgress struct {
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	ETASeconds *int64 `json:"etaSeconds"`
}

const (
	KindFigure = "figure"
	KindJob    = "job"
)

type Board struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Items     []Item    `json:"items"`
	// Archived boards are hidden from the lists but still open by id (ADR 0041).
	Archived bool `json:"archived"`
}

func (s *Store) CreateBoard(title string) (Board, error) {
	title = clip(title, 200)
	if title == "" {
		return Board{}, fmt.Errorf("%w: a board needs a title", ErrInvalid)
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO boards(title,created_at,updated_at) VALUES(?,?,?)`, title, now, now)
	if err != nil {
		return Board{}, err
	}
	id, _ := res.LastInsertId()
	return s.Board(id)
}

func (s *Store) Boards() ([]Board, error) {
	rows, err := s.db.Query(`SELECT id FROM boards ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Board{}
	for _, id := range ids {
		b, err := s.Board(id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Store) Board(id int64) (Board, error) {
	var b Board
	var c, u int64
	err := s.db.QueryRow(`SELECT id,title,created_at,updated_at,`+schema.ArchivedExpr("board", "boards.id")+` FROM boards WHERE id=?`, id).Scan(&b.ID, &b.Title, &c, &u, &b.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	b.CreatedAt, b.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
	rows, err := s.db.Query(`SELECT i.position, i.note, i.figure_id, i.job_id, i.layout_x, i.layout_y, i.layout_w, i.layout_h FROM board_items i WHERE i.board_id=? ORDER BY i.position`, id)
	if err != nil {
		return b, err
	}
	type row struct {
		pos  int
		note string
		fid  sql.NullInt64
		jid  sql.NullInt64
		rect *Rect
	}
	var rs []row
	for rows.Next() {
		var r row
		var x, y, w, h sql.NullInt64
		if err := rows.Scan(&r.pos, &r.note, &r.fid, &r.jid, &x, &y, &w, &h); err != nil {
			rows.Close()
			return b, err
		}
		if x.Valid && y.Valid && w.Valid && h.Valid {
			r.rect = &Rect{int(x.Int64), int(y.Int64), int(w.Int64), int(h.Int64)}
		}
		rs = append(rs, r)
	}
	rows.Close()
	b.Items = []Item{}
	for _, r := range rs {
		if r.jid.Valid {
			b.Items = append(b.Items, Item{Position: r.pos, Kind: KindJob, Job: &JobInfo{ID: r.jid.Int64}, Note: r.note, Layout: r.rect})
			continue
		}
		f, err := s.Figure(r.fid.Int64)
		if err != nil {
			return b, err
		}
		b.Items = append(b.Items, Item{Position: r.pos, Kind: KindFigure, Figure: &f, Note: r.note, Layout: r.rect})
	}
	return b, nil
}

func (s *Store) touch(id int64) {
	_, _ = s.db.Exec(`UPDATE boards SET updated_at=? WHERE id=?`, time.Now().Unix(), id)
}

func (s *Store) AddItem(boardID, figureID int64) error {
	if _, err := s.Board(boardID); err != nil {
		return err
	}
	if _, err := s.Figure(figureID); err != nil {
		return err
	}
	var next int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM board_items WHERE board_id=?`, boardID).Scan(&next)
	if _, err := s.db.Exec(`INSERT INTO board_items(board_id,position,figure_id) VALUES(?,?,?)`, boardID, next, figureID); err != nil {
		return err
	}
	s.touch(boardID)
	return nil
}

// AddJobItem puts a panel on the board that follows a job. The caller has checked that the job exists.
func (s *Store) AddJobItem(boardID, jobID int64) error {
	if _, err := s.Board(boardID); err != nil {
		return err
	}
	var next int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM board_items WHERE board_id=?`, boardID).Scan(&next)
	if _, err := s.db.Exec(`INSERT INTO board_items(board_id,position,job_id) VALUES(?,?,?)`, boardID, next, jobID); err != nil {
		return err
	}
	s.touch(boardID)
	return nil
}

// PinItem replaces the job panel at pos with a figure at the same position, keeping its note. A panel that is not a
// job panel is ErrConflict, so pinning twice (two open tabs) cannot swap an already pinned figure.
func (s *Store) PinItem(boardID int64, pos int, figureID int64) error {
	if _, err := s.Figure(figureID); err != nil {
		return fmt.Errorf("%w: no such figure", ErrInvalid)
	}
	res, err := s.db.Exec(`UPDATE board_items SET figure_id=?, job_id=NULL WHERE board_id=? AND position=? AND job_id IS NOT NULL`, figureID, boardID, pos)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if s.db.QueryRow(`SELECT 1 FROM board_items WHERE board_id=? AND position=?`, boardID, pos).Scan(&one) != nil {
			return ErrNotFound
		}
		return fmt.Errorf("%w: that panel does not follow a job", ErrConflict)
	}
	s.touch(boardID)
	return nil
}

// Expect is what a client last saw at a position, for an edit that must not land on something else (an undo, with the board
// possibly changed in another window since). Kind is "figure" or "job", ID the figure's or job's id; Note, when present, the
// note as the client last saw it. The position is the one the edit names.
type Expect struct {
	Kind string  `json:"kind"`
	ID   int64   `json:"id"`
	Note *string `json:"note,omitempty"`
}

// ConflictError is ErrConflict for a failed Expect: the item at the position is no longer the one the client saw (or is gone).
type ConflictError struct{}

func (ConflictError) Error() string {
	return "the board has changed: the panel is no longer what was expected there"
}
func (ConflictError) Is(t error) bool { return t == ErrConflict }

// editTx runs one edit of the item at pos in a single transaction. Its first statement is a write, so the transaction holds the
// write lock before it reads: what expect is checked against cannot change before the edit is applied, and a second writer waits
// (busy_timeout) instead of failing on a stale read. With expect nil an unknown position is ErrNotFound; with expect, the item
// being absent or different is ConflictError and nothing is changed.
func (s *Store) editTx(boardID int64, pos int, expect *Expect, edit func(tx *sql.Tx, n int) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE boards SET updated_at=? WHERE id=?`, time.Now().Unix(), boardID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM board_items WHERE board_id=?`, boardID).Scan(&n); err != nil {
		return err
	}
	var note string
	var fid, jid sql.NullInt64
	err = tx.QueryRow(`SELECT note, figure_id, job_id FROM board_items WHERE board_id=? AND position=?`, boardID, pos).Scan(&note, &fid, &jid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expect != nil {
			return ConflictError{}
		}
		return ErrNotFound
	case err != nil:
		return err
	}
	if expect != nil {
		kind, id := KindFigure, fid.Int64
		if jid.Valid {
			kind, id = KindJob, jid.Int64
		}
		if expect.Kind != kind || expect.ID != id || (expect.Note != nil && *expect.Note != note) {
			return ConflictError{}
		}
	}
	if err := edit(tx, n); err != nil {
		return err
	}
	return tx.Commit()
}

// reorderTx rewrites positions 0..n-1 in the given order of the current positions.
func reorderTx(tx *sql.Tx, boardID int64, order []int) error {
	if _, err := tx.Exec(`UPDATE board_items SET position = position + 100000 WHERE board_id=?`, boardID); err != nil {
		return err
	}
	for newPos, old := range order {
		if _, err := tx.Exec(`UPDATE board_items SET position=? WHERE board_id=? AND position=?`, newPos, boardID, old+100000); err != nil {
			return err
		}
	}
	return nil
}

// RemoveItem takes the item at pos off the board and closes the gap, in one transaction. expect may be nil.
func (s *Store) RemoveItem(boardID int64, pos int, expect *Expect) error {
	return s.editTx(boardID, pos, expect, func(tx *sql.Tx, n int) error {
		if _, err := tx.Exec(`DELETE FROM board_items WHERE board_id=? AND position=?`, boardID, pos); err != nil {
			return err
		}
		var order []int
		for i := 0; i < n; i++ {
			if i != pos {
				order = append(order, i)
			}
		}
		return reorderTx(tx, boardID, order)
	})
}

// MoveItem moves the item at pos by delta places (-1 up, +1 down), in one transaction. expect may be nil.
func (s *Store) MoveItem(boardID int64, pos, delta int, expect *Expect) error {
	return s.editTx(boardID, pos, expect, func(tx *sql.Tx, n int) error {
		to := pos + delta
		if to < 0 || to >= n {
			return ErrNotFound
		}
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		order[pos], order[to] = order[to], order[pos]
		return reorderTx(tx, boardID, order)
	})
}

// SetNote sets the note of the item at pos, in one transaction. expect may be nil.
func (s *Store) SetNote(boardID int64, pos int, note string, expect *Expect) error {
	return s.editTx(boardID, pos, expect, func(tx *sql.Tx, _ int) error {
		_, err := tx.Exec(`UPDATE board_items SET note=? WHERE board_id=? AND position=?`, clip(note, MaxText), boardID, pos)
		return err
	})
}

// SetLayout stores the grid places of every panel of the board, in position order, in one transaction (ADR 0060). A nil entry
// clears that panel's place (the client then derives one). Every non-nil place must be inside the grid and no two may overlap;
// compaction is the client's business, the server only refuses what cannot be shown. expect names the panel at each position
// and must cover the whole board: a different count or a different panel is ConflictError and nothing changes. from, when
// given, is the layout the client is replacing; a different stored layout is also ConflictError, so a second window cannot
// be overwritten by an undo that it did not see.
func (s *Store) SetLayout(boardID int64, rects []*Rect, expect []Expect, from []*Rect) error {
	for i, r := range rects {
		if r == nil {
			continue
		}
		if !r.valid() {
			return fmt.Errorf("%w: panel %d is outside the grid", ErrInvalid, i)
		}
		for j := 0; j < i; j++ {
			if rects[j] != nil && r.overlaps(*rects[j]) {
				return fmt.Errorf("%w: panels %d and %d overlap", ErrInvalid, j, i)
			}
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE boards SET updated_at=? WHERE id=?`, time.Now().Unix(), boardID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	rows, err := tx.Query(`SELECT position, figure_id, job_id, layout_x, layout_y, layout_w, layout_h FROM board_items WHERE board_id=? ORDER BY position`, boardID)
	if err != nil {
		return err
	}
	type cur struct {
		pos  int
		kind string
		id   int64
		rect *Rect
	}
	var items []cur
	for rows.Next() {
		var c cur
		var fid, jid, x, y, w, h sql.NullInt64
		if err := rows.Scan(&c.pos, &fid, &jid, &x, &y, &w, &h); err != nil {
			rows.Close()
			return err
		}
		c.kind, c.id = KindFigure, fid.Int64
		if jid.Valid {
			c.kind, c.id = KindJob, jid.Int64
		}
		if x.Valid && y.Valid && w.Valid && h.Valid {
			c.rect = &Rect{int(x.Int64), int(y.Int64), int(w.Int64), int(h.Int64)}
		}
		items = append(items, c)
	}
	rows.Close()
	if len(items) != len(rects) || len(expect) != len(items) || (from != nil && len(from) != len(items)) {
		return ConflictError{}
	}
	for i, c := range items {
		if expect[i].Kind != c.kind || expect[i].ID != c.id {
			return ConflictError{}
		}
		if from != nil && !sameRect(from[i], c.rect) {
			return ConflictError{}
		}
	}
	for i, c := range items {
		r := rects[i]
		var args []any
		if r == nil {
			args = []any{nil, nil, nil, nil}
		} else {
			args = []any{r.X, r.Y, r.W, r.H}
		}
		if _, err := tx.Exec(`UPDATE board_items SET layout_x=?, layout_y=?, layout_w=?, layout_h=? WHERE board_id=? AND position=?`, append(args, boardID, c.pos)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sameRect(a, b *Rect) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ---------- snapshots, tokens, review ----------

// FrozenItem is everything a reviewer may see about one figure. There is deliberately no path, dataset id or
// credential field: the type is the whitelist.
//
// A panel that follows a job is frozen as {kind:"job", title, state, at, note} and nothing else: no host, path, work
// directory, script, scheduler id, configuration or parameters (see MarshalJSON). Its state is as of At and never changes.
type FrozenItem struct {
	Kind       string          `json:"kind,omitempty"` // "" or "figure": a figure; "job": a run that had not finished
	State      string          `json:"state,omitempty"`
	At         *time.Time      `json:"at,omitempty"`
	Revision   string          `json:"revision"`
	Title      string          `json:"title"`
	Caption    string          `json:"caption"`
	Note       string          `json:"note"`
	PreviewSHA string          `json:"previewSha"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Provenance json.RawMessage `json:"provenance"`
	// Layout is the panel's place on the grid as saved when the board was published; absent when the board was never arranged
	// (and on snapshots made before layouts), in which case the reader derives it from the order.
	Layout *Rect `json:"layout,omitempty"`
}

func (f FrozenItem) MarshalJSON() ([]byte, error) {
	if f.Kind == KindJob {
		return json.Marshal(struct {
			Kind   string     `json:"kind"`
			Title  string     `json:"title"`
			State  string     `json:"state"`
			At     *time.Time `json:"at"`
			Note   string     `json:"note"`
			Layout *Rect      `json:"layout,omitempty"`
		}{f.Kind, f.Title, f.State, f.At, f.Note, f.Layout})
	}
	type plain FrozenItem
	return json.Marshal(plain(f))
}

// JobSnapshot is what the server tells the store about a followed job when a board is published or previewed. Only
// these two facts ever reach a snapshot.
type JobSnapshot struct{ Name, State string }

// freeze turns the board's items into what a reviewer may see.
func freeze(items []Item, jobs map[int64]JobSnapshot, at time.Time) []FrozenItem {
	out := []FrozenItem{}
	for _, it := range items {
		if it.Kind == KindJob {
			js, ok := jobs[it.Job.ID]
			if !ok {
				js = JobSnapshot{Name: "Run", State: "unknown"}
			}
			t := at.UTC()
			out = append(out, FrozenItem{Kind: KindJob, Title: clip(js.Name, 200), State: js.State, At: &t, Note: it.Note, Layout: it.Layout})
			continue
		}
		out = append(out, FrozenItem{Revision: it.Figure.Revision, Title: it.Figure.Title, Caption: it.Figure.Caption, Note: it.Note,
			PreviewSHA: it.Figure.PreviewSHA, Width: it.Figure.Width, Height: it.Figure.Height, Provenance: it.Figure.Provenance, Layout: it.Layout})
	}
	return out
}

type Frozen struct {
	BoardTitle string       `json:"boardTitle"`
	Label      string       `json:"label"`
	Question   string       `json:"question,omitempty"` // what the author asks the reviewer to look at
	CreatedAt  time.Time    `json:"createdAt"`
	ExpiresAt  time.Time    `json:"expiresAt"`
	Items      []FrozenItem `json:"items"`
}

type Snapshot struct {
	ID        int64     `json:"id"`
	BoardID   int64     `json:"boardId"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Revoked   bool      `json:"revoked"`
	Items     int       `json:"items"`
	Question  string    `json:"question,omitempty"`
	// Outcome is what the reviewer said about this version; empty means not yet reviewed. It is not a scientific
	// assessment of any run: that is the author's own, and kept apart.
	Outcome      string     `json:"outcome"`
	OutcomeNote  string     `json:"outcomeNote,omitempty"`
	OutcomeBy    string     `json:"outcomeBy,omitempty"`
	OutcomeAt    *time.Time `json:"outcomeAt,omitempty"`
	SupersededBy int64      `json:"supersededBy,omitempty"`
	// Comments counts the reviewers' comments (not the author's replies); LastActivity is the time of the latest
	// reviewer comment or outcome, so the author can tell what is new since they last looked.
	Comments     int        `json:"comments"`
	LastActivity *time.Time `json:"lastActivity,omitempty"`
}

func hashToken(tok string) string { h := sha256.Sum256([]byte(tok)); return hex.EncodeToString(h[:]) }

// Publish freezes the board as it is now and returns a review token. Only the token's hash is stored, so the token
// can be shown once and never recovered; revoke and publish again to share anew.
func (s *Store) Publish(boardID int64, label string, ttl time.Duration) (Snapshot, string, error) {
	return s.PublishWith(boardID, PublishOptions{Label: label, TTL: ttl})
}

// PublishOptions are what the author sets when asking for a review.
type PublishOptions struct {
	Label    string
	Question string // what to look at; shown to the reviewer
	TTL      time.Duration
	// Supersedes names an earlier snapshot of the same board that this one replaces; Reason is shown to anyone who
	// opens the old link.
	Supersedes int64
	Reason     string
	// Jobs describes the jobs that the board's job panels follow, by job id.
	Jobs map[int64]JobSnapshot
}

func (s *Store) PublishWith(boardID int64, o PublishOptions) (Snapshot, string, error) {
	label, ttl := o.Label, o.TTL
	b, err := s.Board(boardID)
	if err != nil {
		return Snapshot{}, "", err
	}
	if len(b.Items) == 0 {
		return Snapshot{}, "", fmt.Errorf("%w: the board has no figures", ErrInvalid)
	}
	if ttl <= 0 || ttl > 90*24*time.Hour {
		return Snapshot{}, "", fmt.Errorf("%w: expiry must be between a moment and 90 days", ErrInvalid)
	}
	now := time.Now()
	fr := Frozen{BoardTitle: b.Title, Label: clip(label, 200), Question: clip(o.Question, 1000), CreatedAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(), Items: []FrozenItem{}}
	fr.Items = freeze(b.Items, o.Jobs, now)
	raw, err := json.Marshal(fr)
	if err != nil {
		return Snapshot{}, "", err
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return Snapshot{}, "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(buf[:])
	res, err := s.db.Exec(`INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen) VALUES(?,?,?,?,?,?)`,
		boardID, fr.Label, now.Unix(), fr.ExpiresAt.Unix(), hashToken(tok), string(raw))
	if err != nil {
		return Snapshot{}, "", err
	}
	id, _ := res.LastInsertId()
	if o.Supersedes != 0 {
		reason := clip(o.Reason, 500)
		if reason == "" {
			reason = "The author has replaced this review with a newer version."
		}
		if _, err := s.db.Exec(`UPDATE snapshots SET superseded_by=?, supersede_reason=?, supersede_at=? WHERE id=? AND board_id=? AND superseded_by=0`, id, reason, now.Unix(), o.Supersedes, boardID); err != nil {
			return Snapshot{}, "", err
		}
	}
	return Snapshot{ID: id, BoardID: boardID, Label: fr.Label, CreatedAt: now.UTC(), ExpiresAt: fr.ExpiresAt, Items: len(fr.Items), Question: fr.Question}, tok, nil
}

func (s *Store) Snapshots(boardID int64) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id,board_id,label,created_at,expires_at,revoked,frozen,superseded_by FROM snapshots WHERE board_id=? ORDER BY id DESC`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var sn Snapshot
		var c, e, rv int64
		var frozen string
		if err := rows.Scan(&sn.ID, &sn.BoardID, &sn.Label, &c, &e, &rv, &frozen, &sn.SupersededBy); err != nil {
			return nil, err
		}
		var fr Frozen
		_ = json.Unmarshal([]byte(frozen), &fr)
		sn.CreatedAt, sn.ExpiresAt, sn.Revoked, sn.Items, sn.Question = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), rv != 0, len(fr.Items), fr.Question
		out = append(out, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // the catalog uses one connection: finish this query before asking another
	for i := range out {
		s.fillOutcome(&out[i])
	}
	return out, nil
}

func (s *Store) Revoke(snapshotID int64) error {
	res, err := s.db.Exec(`UPDATE snapshots SET revoked=1 WHERE id=?`, snapshotID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Review is a reviewer's view of one snapshot.
type Review struct {
	Snapshot Frozen    `json:"snapshot"`
	Comments []Comment `json:"comments"`
	// Outcome is the reviewer's latest word on this version (empty: not yet reviewed).
	Outcome Outcome `json:"outcome"`
	// Superseded is set when the author has replaced this version; the link keeps working so nothing disappears.
	Superseded *Superseded `json:"superseded,omitempty"`
}

type Outcome struct {
	Value string     `json:"value"`
	Note  string     `json:"note,omitempty"`
	By    string     `json:"by,omitempty"`
	At    *time.Time `json:"at,omitempty"`
}

type Superseded struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

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
func (s *Store) lookup(token string) (id int64, fr Frozen, err error) {
	if len(token) < 20 || len(token) > 100 {
		return 0, fr, ErrNotFound
	}
	h := hashToken(token)
	var frozen, stored string
	var exp, rv int64
	err = s.db.QueryRow(`SELECT id,expires_at,revoked,token_hash,frozen FROM snapshots WHERE token_hash=?`, h).Scan(&id, &exp, &rv, &stored, &frozen)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || rv != 0 || time.Now().Unix() >= exp {
		return 0, fr, ErrNotFound
	}
	if err := json.Unmarshal([]byte(frozen), &fr); err != nil {
		return 0, fr, err
	}
	return id, fr, nil
}

func (s *Store) Review(token string) (Review, error) {
	id, fr, err := s.lookup(token)
	if err != nil {
		return Review{}, err
	}
	cs, err := s.Comments(id)
	rv := Review{Snapshot: fr, Comments: cs, Outcome: s.outcome(id)}
	var by, at int64
	var reason string
	if s.db.QueryRow(`SELECT superseded_by,supersede_reason,supersede_at FROM snapshots WHERE id=?`, id).Scan(&by, &reason, &at) == nil && by != 0 {
		rv.Superseded = &Superseded{Reason: reason, At: time.Unix(at, 0).UTC()}
	}
	return rv, err
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
	_, fr, err := s.lookup(token)
	if err != nil {
		return nil, err
	}
	for _, it := range fr.Items {
		if it.PreviewSHA != "" && it.PreviewSHA == sha {
			return s.Preview(sha)
		}
	}
	return nil, ErrNotFound
}

// AddComment records a reviewer's comment, or reply, on a figure revision that is part of the snapshot.
func (s *Store) AddComment(token string, in CommentInput) (Comment, error) {
	id, fr, err := s.lookup(token)
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
func (s *Store) OwnerReply(snapshotID int64, in CommentInput) (Comment, error) {
	if in.ParentID == 0 {
		return Comment{}, fmt.Errorf("%w: the author replies inside a thread", ErrInvalid)
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
func (s *Store) SetResolved(commentID int64, resolved bool) error {
	var at int64
	if resolved {
		at = time.Now().Unix()
	}
	res, err := s.db.Exec(`UPDATE comments SET resolved_at=? WHERE id=? AND parent_id=0`, at, commentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OwnerComments lists the comments on a snapshot for its owner.
func (s *Store) OwnerComments(snapshotID int64) ([]Comment, error) { return s.Comments(snapshotID) }
