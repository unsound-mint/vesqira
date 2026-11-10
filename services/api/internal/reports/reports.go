// Package reports stores the owner's reports (ADR 0036): Markdown documents written by the owner or drafted by an
// agent, with an edit history that says who wrote each save, the links between reports, figures and runs, and the
// image attachments a report shows. Reports are mutable working state; what a reader sees is a published revision
// built from them elsewhere and never changed afterwards.
package reports

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for attachment validation
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTitle        = 200
	MaxBody         = 256 << 10
	MaxAttachment   = 8 << 20
	MaxAttachSide   = 4096
	AuthorOwner     = "owner"
	AuthorAgent     = "agent"
	historyPerFetch = 200
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

type Store struct {
	db  *sql.DB
	dir string // attachments live in dir/attachments
}

func Open(db *sql.DB, dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "attachments"), 0o700); err != nil {
		return nil, err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS reports(
			id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, title TEXT NOT NULL UNIQUE COLLATE NOCASE,
			body TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			seen_revision INTEGER NOT NULL DEFAULT 0, parent_id INTEGER NOT NULL DEFAULT 0)`, // parent_id: schema 3
		`CREATE TABLE IF NOT EXISTS report_history(
			report_id INTEGER NOT NULL, revision INTEGER NOT NULL, author TEXT NOT NULL, at INTEGER NOT NULL,
			title TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(report_id, revision))`,
		`CREATE TABLE IF NOT EXISTS report_links(
			report_id INTEGER NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL, PRIMARY KEY(report_id, kind, target))`,
		`CREATE INDEX IF NOT EXISTS report_links_target ON report_links(kind, target)`,
		`CREATE TABLE IF NOT EXISTS attachments(sha TEXT PRIMARY KEY, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	if err := openPublishing(db); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dataDir}, nil
}

// Report is the working copy. Revision counts saves; a client that saves names the revision it started from.
type Report struct {
	ID           int64     `json:"id"`
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Revision     int       `json:"revision"`
	SeenRevision int       `json:"seenRevision"` // the last revision the owner opened; agent saves after it are "new"
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	LastAuthor   string    `json:"lastAuthor"`
	Parent       int64     `json:"parent"` // the page this one sits under, 0 at the top
}

// Summary is a list row: no body.
type Summary struct {
	ID         int64     `json:"id"`
	Key        string    `json:"key"`
	Title      string    `json:"title"`
	Parent     int64     `json:"parent"`
	Date       string    `json:"date,omitempty"`    // front matter date, YYYY-MM-DD
	Summary    string    `json:"summary,omitempty"` // front matter summary
	Revision   int       `json:"revision"`
	UpdatedAt  time.Time `json:"updatedAt"`
	LastAuthor string    `json:"lastAuthor"`
	// AgentUnseen is true when the agent saved after the owner last opened the report.
	AgentUnseen bool `json:"agentUnseen"`
	// Published is the latest published number, 0 when never published; PublishedFrom is the working revision it was
	// built from, so a client can say "changed since published".
	Published     int `json:"published"`
	PublishedFrom int `json:"publishedFrom"`
}

type HistoryEntry struct {
	Revision int       `json:"revision"`
	Author   string    `json:"author"`
	At       time.Time `json:"at"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
}

func cleanTitle(t string) (string, error) {
	t = strings.Join(strings.Fields(t), " ")
	if t == "" || utf8.RuneCountInString(t) > MaxTitle || strings.ContainsAny(t, "[]|#") {
		return "", fmt.Errorf("%w: a title needs 1 to %d characters and no [ ] | or #, which links use", ErrInvalid, MaxTitle)
	}
	return t, nil
}

func checkBody(b string) error {
	if len(b) > MaxBody || !utf8.ValidString(b) || strings.ContainsRune(b, 0) {
		return fmt.Errorf("%w: a report body is UTF-8 text of at most %d KiB", ErrInvalid, MaxBody>>10)
	}
	return nil
}

func checkAuthor(a string) error {
	if a != AuthorOwner && a != AuthorAgent {
		return fmt.Errorf("%w: author must be owner or agent", ErrInvalid)
	}
	return nil
}

func newKey() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

func (s *Store) Create(title, body, author string) (Report, error) {
	return s.CreateUnder(title, body, author, 0)
}

// CreateUnder creates a report as a child page of parent (0 for the top level).
func (s *Store) CreateUnder(title, body, author string, parent int64) (Report, error) {
	t, err := cleanTitle(title)
	if err != nil {
		return Report{}, err
	}
	if err := checkBody(body); err != nil {
		return Report{}, err
	}
	if err := checkAuthor(author); err != nil {
		return Report{}, err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	if parent != 0 {
		var one int
		if tx.QueryRow(`SELECT 1 FROM reports WHERE id=?`, parent).Scan(&one) != nil {
			return Report{}, fmt.Errorf("%w: the parent page does not exist", ErrInvalid)
		}
	}
	seen := 0
	if author == AuthorOwner {
		seen = 1
	}
	res, err := tx.Exec(`INSERT INTO reports(key,title,body,revision,created_at,updated_at,seen_revision,parent_id) VALUES(?,?,?,1,?,?,?,?)`, newKey(), t, body, now.UnixMilli(), now.UnixMilli(), seen, parent)
	if isUnique(err) {
		return Report{}, fmt.Errorf("%w: a report called %q exists already", ErrConflict, t)
	}
	if err != nil {
		return Report{}, err
	}
	id, _ := res.LastInsertId()
	if err := saveRevision(tx, id, 1, author, now, t, body); err != nil {
		return Report{}, err
	}
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return s.Get(id)
}

func saveRevision(tx *sql.Tx, id int64, rev int, author string, at time.Time, title, body string) error {
	if _, err := tx.Exec(`INSERT INTO report_history(report_id,revision,author,at,title,body) VALUES(?,?,?,?,?,?)`, id, rev, author, at.UnixMilli(), title, body); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM report_links WHERE report_id=?`, id); err != nil {
		return err
	}
	for _, l := range Links(body) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO report_links(report_id,kind,target) VALUES(?,?,?)`, id, l.Kind, l.Target); err != nil {
			return err
		}
	}
	return nil
}

func scanReport(row interface{ Scan(...any) error }) (Report, error) {
	var r Report
	var c, u int64
	err := row.Scan(&r.ID, &r.Key, &r.Title, &r.Body, &r.Revision, &r.SeenRevision, &c, &u, &r.LastAuthor, &r.Parent)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, ErrNotFound
	}
	r.CreatedAt, r.UpdatedAt = time.UnixMilli(c).UTC(), time.UnixMilli(u).UTC()
	return r, err
}

const reportCols = `r.id,r.key,r.title,r.body,r.revision,r.seen_revision,r.created_at,r.updated_at,
	(SELECT author FROM report_history h WHERE h.report_id=r.id AND h.revision=r.revision), r.parent_id`

func (s *Store) Get(id int64) (Report, error) {
	return scanReport(s.db.QueryRow(`SELECT `+reportCols+` FROM reports r WHERE r.id=?`, id))
}

// ByTitle finds a report by title, ignoring case, as a [[link]] does.
func (s *Store) ByTitle(title string) (Report, error) {
	return scanReport(s.db.QueryRow(`SELECT `+reportCols+` FROM reports r WHERE r.title=?`, strings.Join(strings.Fields(title), " ")))
}

func (s *Store) List() ([]Summary, error) {
	rows, err := s.db.Query(`SELECT r.id,r.key,r.title,r.revision,r.updated_at,r.parent_id,substr(r.body,1,8192),
		(SELECT author FROM report_history h WHERE h.report_id=r.id AND h.revision=r.revision),
		EXISTS(SELECT 1 FROM report_history h WHERE h.report_id=r.id AND h.revision>r.seen_revision AND h.author='agent'),
		COALESCE((SELECT MAX(number) FROM report_publications p WHERE p.report_id=r.id),0),
		COALESCE((SELECT working_revision FROM report_publications p WHERE p.report_id=r.id ORDER BY number DESC LIMIT 1),0)
		FROM reports r ORDER BY r.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var x Summary
		var u int64
		var head string
		if err := rows.Scan(&x.ID, &x.Key, &x.Title, &x.Revision, &u, &x.Parent, &head, &x.LastAuthor, &x.AgentUnseen, &x.Published, &x.PublishedFrom); err != nil {
			return nil, err
		}
		x.Date, x.Summary = ReportDate(head), ReportSummary(head)
		x.UpdatedAt = time.UnixMilli(u).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// Update is a save. Title and Body are optional (nil keeps the current value). Base must be the revision the caller
// started from; a stale base is refused with ErrConflict and the current report, so no save overwrites another.
type Update struct {
	Title  *string
	Body   *string
	Base   int
	Author string
}

func (s *Store) Update(id int64, in Update) (Report, error) {
	if err := checkAuthor(in.Author); err != nil {
		return Report{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	cur, err := scanReport(tx.QueryRow(`SELECT `+reportCols+` FROM reports r WHERE r.id=?`, id))
	if err != nil {
		return Report{}, err
	}
	if in.Base != cur.Revision {
		return cur, fmt.Errorf("%w: the report changed since revision %d (now %d, last saved by %s); read it again and reapply your change", ErrConflict, in.Base, cur.Revision, cur.LastAuthor)
	}
	title, body := cur.Title, cur.Body
	if in.Title != nil {
		if title, err = cleanTitle(*in.Title); err != nil {
			return Report{}, err
		}
	}
	if in.Body != nil {
		if err := checkBody(*in.Body); err != nil {
			return Report{}, err
		}
		body = *in.Body
	}
	if title == cur.Title && body == cur.Body {
		return cur, nil
	}
	now, rev := time.Now().UTC(), cur.Revision+1
	seen := cur.SeenRevision
	if in.Author == AuthorOwner && seen == cur.Revision { // the owner saving what they were looking at has seen it
		seen = rev
	}
	_, err = tx.Exec(`UPDATE reports SET title=?, body=?, revision=?, updated_at=?, seen_revision=? WHERE id=?`, title, body, rev, now.UnixMilli(), seen, id)
	if isUnique(err) {
		return Report{}, fmt.Errorf("%w: a report called %q exists already", ErrConflict, title)
	}
	if err != nil {
		return Report{}, err
	}
	if err := saveRevision(tx, id, rev, in.Author, now, title, body); err != nil {
		return Report{}, err
	}
	if !strings.EqualFold(title, cur.Title) {
		if err := relink(tx, cur.Title, title, in.Author, now); err != nil {
			return Report{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return s.Get(id)
}

// relink rewrites [[Old]] and [[Old|label]] in every report that links the renamed one, as a new revision of each, so
// a rename never leaves links dangling. Each rewrite is recorded in that report's history.
func relink(tx *sql.Tx, oldTitle, newTitle, author string, now time.Time) error {
	rows, err := tx.Query(`SELECT r.id, r.title, r.body, r.revision FROM reports r JOIN report_links l ON l.report_id=r.id WHERE l.kind='report' AND l.target=?`, strings.ToLower(oldTitle))
	if err != nil {
		return err
	}
	type hit struct {
		id          int64
		title, body string
		rev         int
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.title, &h.body, &h.rev); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	re := regexp.MustCompile(`\[\[\s*(?i:` + regexp.QuoteMeta(oldTitle) + `)\s*(\|[^\]]*)?\]\]`)
	for _, h := range hits {
		nb := re.ReplaceAllString(h.body, "[["+newTitle+"$1]]")
		if nb == h.body {
			continue
		}
		if _, err := tx.Exec(`UPDATE reports SET body=?, revision=?, updated_at=? WHERE id=?`, nb, h.rev+1, now.UnixMilli(), h.id); err != nil {
			return err
		}
		if err := saveRevision(tx, h.id, h.rev+1, author, now, h.title, nb); err != nil {
			return err
		}
	}
	return nil
}

// Move puts a report under another page (0 for the top level). A page cannot move under itself or its own children.
func (s *Store) Move(id, parent int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	if tx.QueryRow(`SELECT 1 FROM reports WHERE id=?`, id).Scan(&one) != nil {
		return ErrNotFound
	}
	for p, steps := parent, 0; p != 0; steps++ {
		if p == id || steps > 10000 {
			return fmt.Errorf("%w: a page cannot go under itself or a page inside it", ErrInvalid)
		}
		if err := tx.QueryRow(`SELECT parent_id FROM reports WHERE id=?`, p).Scan(&p); err != nil {
			return fmt.Errorf("%w: the parent page does not exist", ErrInvalid)
		}
	}
	if _, err := tx.Exec(`UPDATE reports SET parent_id=? WHERE id=?`, parent, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSeen records that the owner opened the report at a revision, so agent saves up to it are no longer new.
func (s *Store) MarkSeen(id int64, revision int) error {
	res, err := s.db.Exec(`UPDATE reports SET seen_revision=MAX(seen_revision, MIN(?, revision)) WHERE id=?`, revision, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a report that was never published. A published report has readers who may have commented on it;
// it is not deleted from here.
func (s *Store) Delete(id int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM report_publications WHERE report_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: this report has been published, so readers may have commented on it; it cannot be deleted", ErrConflict)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent int64
	_ = tx.QueryRow(`SELECT parent_id FROM reports WHERE id=?`, id).Scan(&parent)
	res, err := tx.Exec(`DELETE FROM reports WHERE id=?`, id)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return ErrNotFound
	}
	// its child pages move up to where it was
	if _, err := tx.Exec(`UPDATE reports SET parent_id=? WHERE parent_id=?`, parent, id); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM report_history WHERE report_id=?`, `DELETE FROM report_links WHERE report_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// History lists saves, newest first, without bodies unless withBodies.
func (s *Store) History(id int64, withBodies bool) ([]HistoryEntry, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT revision, author, at, title, CASE WHEN ? THEN body ELSE '' END FROM report_history WHERE report_id=? ORDER BY revision DESC LIMIT ?`, withBodies, id, historyPerFetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		var at int64
		if err := rows.Scan(&h.Revision, &h.Author, &at, &h.Title, &h.Body); err != nil {
			return nil, err
		}
		h.At = time.UnixMilli(at).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// Revision returns one saved revision with its body.
func (s *Store) Revision(id int64, rev int) (HistoryEntry, error) {
	var h HistoryEntry
	var at int64
	err := s.db.QueryRow(`SELECT revision, author, at, title, body FROM report_history WHERE report_id=? AND revision=?`, id, rev).Scan(&h.Revision, &h.Author, &at, &h.Title, &h.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	h.At = time.UnixMilli(at).UTC()
	return h, err
}

// Backlinks lists the reports that link to this one by title.
func (s *Store) Backlinks(id int64) ([]Summary, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT r.id, r.key, r.title, r.revision, r.updated_at FROM reports r JOIN report_links l ON l.report_id=r.id
		WHERE l.kind='report' AND l.target=? AND r.id<>? ORDER BY r.title`, strings.ToLower(r.Title), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var x Summary
		var u int64
		if err := rows.Scan(&x.ID, &x.Key, &x.Title, &x.Revision, &u); err != nil {
			return nil, err
		}
		x.UpdatedAt = time.UnixMilli(u).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// UsingFigure and UsingRun list reports that embed a figure revision or reference a run.
func (s *Store) Using(kind, target string) ([]Summary, error) {
	rows, err := s.db.Query(`SELECT r.id, r.key, r.title, r.revision, r.updated_at FROM reports r JOIN report_links l ON l.report_id=r.id
		WHERE l.kind=? AND l.target=? ORDER BY r.title`, kind, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var x Summary
		var u int64
		if err := rows.Scan(&x.ID, &x.Key, &x.Title, &x.Revision, &u); err != nil {
			return nil, err
		}
		x.UpdatedAt = time.UnixMilli(u).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---------- attachments ----------

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AddAttachment stores an image a report shows. It must be a PNG, JPEG or GIF that decodes completely, within the
// same size limits as a figure preview. Identical bytes are stored once.
func (s *Store) AddAttachment(data []byte) (sha, mime string, err error) {
	if len(data) == 0 || len(data) > MaxAttachment {
		return "", "", fmt.Errorf("%w: an attachment is an image of at most %d MiB", ErrInvalid, MaxAttachment>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxAttachSide || cfg.Height > MaxAttachSide {
		return "", "", fmt.Errorf("%w: an attachment must be a PNG, JPEG or GIF within %dx%d", ErrInvalid, MaxAttachSide, MaxAttachSide)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", "", fmt.Errorf("%w: the image cannot be decoded completely", ErrInvalid)
	}
	mime = "image/" + format
	sum := sha256.Sum256(data)
	sha = hex.EncodeToString(sum[:])
	file := filepath.Join(s.dir, "attachments", sha)
	if _, err := os.Stat(file); err != nil {
		tmp, err := os.CreateTemp(filepath.Join(s.dir, "attachments"), ".part-*")
		if err != nil {
			return "", "", err
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", "", err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return "", "", err
		}
		if err := os.Rename(tmp.Name(), file); err != nil {
			os.Remove(tmp.Name())
			return "", "", err
		}
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO attachments(sha,mime,size,created_at) VALUES(?,?,?,?)`, sha, mime, len(data), time.Now().UnixMilli())
	return sha, mime, err
}

func (s *Store) Attachment(sha string) ([]byte, string, error) {
	if !shaRe.MatchString(sha) {
		return nil, "", ErrNotFound
	}
	var mime string
	if err := s.db.QueryRow(`SELECT mime FROM attachments WHERE sha=?`, sha).Scan(&mime); errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	} else if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "attachments", sha))
	if err != nil {
		return nil, "", err
	}
	return b, mime, nil
}

// Edge is one link from a report: to another report (by its id when the title resolves), a figure revision or a run.
type Edge struct {
	From   int64  `json:"from"`
	Kind   string `json:"kind"` // report | figure | run
	Target string `json:"target"`
	To     int64  `json:"to,omitempty"` // the linked report's id, 0 when the title names no report
}

// Edges lists every link between reports, figures and runs, for the graph.
func (s *Store) Edges() ([]Edge, error) {
	rows, err := s.db.Query(`SELECT l.report_id, l.kind, l.target, COALESCE(t.id, 0) FROM report_links l
		LEFT JOIN reports t ON l.kind='report' AND lower(t.title)=l.target
		WHERE l.kind IN ('report','figure','run') ORDER BY l.report_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Edge{}
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.Kind, &e.Target, &e.To); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Change is one step of the difference between two revisions, block by block: same, added, removed, or changed
// (a removed block directly followed by an added one, paired so a reviewer sees before and after together).
type Change struct {
	Status string `json:"status"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Changes compares two bodies by blocks with a longest-common-subsequence alignment.
func Changes(before, after string) []Change {
	a, b := blockTexts(before), blockTexts(after)
	n, m := len(a), len(b)
	if n*m > 4_000_000 { // bodies are at most 256 KiB, but keep the table bounded: report everything as changed
		return []Change{{Status: "changed", Before: before, After: after}}
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []Change
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, Change{Status: "same", Before: a[i], After: b[j]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, Change{Status: "removed", Before: a[i]})
			i++
		default:
			if k := len(out) - 1; k >= 0 && out[k].Status == "removed" {
				out[k] = Change{Status: "changed", Before: out[k].Before, After: b[j]}
			} else {
				out = append(out, Change{Status: "added", After: b[j]})
			}
			j++
		}
	}
	return out
}

// blockTexts is each block as the Markdown that reproduces it, so blocks can be compared and joined back.
func blockTexts(body string) []string {
	var out []string
	for _, b := range Blocks(body) {
		switch b.Kind {
		case "figure":
			out = append(out, "![[figure:"+b.Figure+"]]")
		case "run":
			out = append(out, fmt.Sprintf("[[run:%d]]", b.Run))
		default:
			out = append(out, b.Text)
		}
	}
	return out
}
