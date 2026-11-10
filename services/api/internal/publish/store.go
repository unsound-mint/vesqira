package publish

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
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

const (
	MaxAssetBytes         = 8 << 20
	MaxDocBytes           = 4 << 20
	maxAssetPixels        = 4096
	maxBlocks             = 2000
	maxTextBytes          = 64 << 10
	maxJSONObject         = 64 << 10
	maxCommentRunes       = 4000
	maxCommentsSpace      = 2000
	maxCommentsPerLinkDay = 100
	maxReportsSpace       = 1000
	feedLimit             = 500
)

var (
	reKey   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	reSha   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reSlug  = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	reAsset = regexp.MustCompile(`asset:([0-9a-f]{64})`)
	reLink  = regexp.MustCompile(`\[\[report:([0-9a-f]{16})\|`)
)

// Store is the publish server's whole state: one SQLite database and a directory of uploaded assets.
type Store struct {
	db  *sql.DB
	dir string
}

// Open creates the tables if needed. dataDir holds the assets; it is created with mode 0700.
func Open(db *sql.DB, dataDir string) (*Store, error) {
	db.SetMaxOpenConns(1)
	if err := os.MkdirAll(filepath.Join(dataDir, "assets"), 0o700); err != nil {
		return nil, err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS publishers(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS spaces(slug TEXT PRIMARY KEY, title TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS space_reports(space TEXT NOT NULL, report TEXT NOT NULL, position INTEGER NOT NULL, PRIMARY KEY(space, report))`,
		`CREATE TABLE IF NOT EXISTS revisions(id TEXT PRIMARY KEY, report TEXT NOT NULL, number INTEGER NOT NULL, title TEXT NOT NULL, published_at INTEGER NOT NULL, doc TEXT NOT NULL, UNIQUE(report, number))`,
		`CREATE TABLE IF NOT EXISTS assets(sha TEXT PRIMARY KEY, mime TEXT NOT NULL, size INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS readers(id INTEGER PRIMARY KEY AUTOINCREMENT, space TEXT NOT NULL, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, expires_at INTEGER, revoked INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS comments(id INTEGER PRIMARY KEY AUTOINCREMENT, space TEXT NOT NULL, report TEXT NOT NULL, revision TEXT NOT NULL, block TEXT NOT NULL, anchor TEXT NOT NULL DEFAULT '', parent_id INTEGER NOT NULL DEFAULT 0, author TEXT NOT NULL, from_owner INTEGER NOT NULL DEFAULT 0, body TEXT NOT NULL, created_at INTEGER NOT NULL, resolved_at INTEGER, reader_id INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS comments_space ON comments(space)`,
		`CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		// which assets a revision shows, so a reader's picture request is one indexed lookup, not a scan of documents
		`CREATE TABLE IF NOT EXISTS revision_assets(revision TEXT NOT NULL, sha TEXT NOT NULL, PRIMARY KEY(sha, revision))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	st := &Store{db: db, dir: dataDir}
	if err := st.upgrade(); err != nil {
		return nil, err
	}
	return st, nil
}

func hashToken(tok string) string { h := sha256.Sum256([]byte(tok)); return hex.EncodeToString(h[:]) }

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// ---- publishers ----

// AddPublisher makes a publisher credential and returns the key, which is not stored (only its hash is).
func (s *Store) AddPublisher(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return "", invalid("name must be 1 to 80 characters")
	}
	key, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO publishers(name,key_hash,created_at) VALUES(?,?,?)`, name, hashToken(key), time.Now().Unix())
	return key, err
}

// RevokePublisher revokes every publisher credential with this name; ErrNotFound if there is none.
func (s *Store) RevokePublisher(name string) error {
	res, err := s.db.Exec(`UPDATE publishers SET revoked=1 WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Publisher reports whether key is a live publisher key, and records the contact when it is.
func (s *Store) Publisher(key string) bool {
	if len(key) < 20 || len(key) > 100 {
		return false
	}
	h := hashToken(key)
	var stored string
	var rv int
	if err := s.db.QueryRow(`SELECT key_hash,revoked FROM publishers WHERE key_hash=?`, h).Scan(&stored, &rv); err != nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || rv != 0 {
		return false
	}
	_, _ = s.db.Exec(`INSERT INTO meta(key,value) VALUES('last_publisher_contact',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339))
	return true
}

func (s *Store) lastContact() *time.Time {
	var v string
	if s.db.QueryRow(`SELECT value FROM meta WHERE key='last_publisher_contact'`).Scan(&v) != nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}

// ---- assets ----

var assetMime = map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}

// PutAsset stores an uploaded picture under its sha256. The bytes must hash to sha and fully decode as PNG, JPEG or GIF.
func (s *Store) PutAsset(sha string, body []byte) error {
	if !reSha.MatchString(sha) {
		return invalid("asset name must be a lowercase sha256")
	}
	if len(body) == 0 || len(body) > MaxAssetBytes {
		return invalid("asset must be 1 byte to 8 MiB")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != sha {
		return invalid("asset bytes do not match their sha256")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || assetMime[format] == "" {
		return invalid("asset must be a PNG, JPEG or GIF picture")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxAssetPixels || cfg.Height > maxAssetPixels {
		return invalid("asset must be at most %d by %d pixels", maxAssetPixels, maxAssetPixels)
	}
	if _, _, err := image.Decode(bytes.NewReader(body)); err != nil { // refuses truncated files
		return invalid("asset is not a complete picture")
	}
	dir := filepath.Join(s.dir, "assets")
	tmp, err := os.CreateTemp(dir, ".upload-*") // 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, sha)); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO assets(sha,mime,size) VALUES(?,?,?)`, sha, assetMime[format], len(body))
	return err
}

func (s *Store) readAsset(sha string) (string, []byte, error) {
	var mime string
	if !reSha.MatchString(sha) || s.db.QueryRow(`SELECT mime FROM assets WHERE sha=?`, sha).Scan(&mime) != nil {
		return "", nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "assets", sha))
	if err != nil {
		return "", nil, ErrNotFound
	}
	return mime, b, nil
}

func (s *Store) assetSize(sha string) int64 {
	var n int64
	if s.db.QueryRow(`SELECT size FROM assets WHERE sha=?`, sha).Scan(&n) != nil {
		return 0
	}
	return n
}

// Asset returns an asset for a reader, only if a revision of a report in the reader's space refers to it.
func (s *Store) Asset(token, sha string) (mime string, data []byte, err error) {
	rd, err := s.lookup(token)
	if err != nil {
		return "", nil, err
	}
	if !reSha.MatchString(sha) {
		return "", nil, ErrNotFound
	}
	var one int
	err = s.db.QueryRow(`SELECT 1 FROM revision_assets ra JOIN revisions r ON r.id=ra.revision
		JOIN space_reports sr ON sr.report=r.report AND sr.space=? WHERE ra.sha=? LIMIT 1`, rd.space, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return s.readAsset(sha)
}

func assetsOf(r Revision) map[string]bool {
	out := map[string]bool{}
	for _, b := range r.Blocks {
		if b.Figure != nil {
			out[b.Figure.Asset] = true
		}
		for _, m := range reAsset.FindAllStringSubmatch(b.Text, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// ---- revisions ----

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) >= 2 && len(t) <= maxJSONObject && t[0] == '{' && json.Valid(t)
}

func (s *Store) validateRevision(r *Revision) error {
	if !reKey.MatchString(r.Report) {
		return invalid("report key must be 16 lowercase hex characters")
	}
	if r.Number < 1 {
		return invalid("revision number must be at least 1")
	}
	if n := utf8.RuneCountInString(r.Title); n < 1 || n > 200 {
		return invalid("title must be 1 to 200 characters")
	}
	if utf8.RuneCountInString(r.Authors) > 500 {
		return invalid("authors is too long")
	}
	if len(r.Blocks) > maxBlocks {
		return invalid("at most %d blocks", maxBlocks)
	}
	seen := map[string]bool{}
	for i, b := range r.Blocks {
		if b.ID == "" || len(b.ID) > 64 {
			return invalid("block %d: id must be 1 to 64 characters", i)
		}
		if seen[b.ID] {
			return invalid("block %d: duplicate id", i)
		}
		seen[b.ID] = true
		switch b.Kind {
		case BlockText:
			if len(b.Text) > maxTextBytes {
				return invalid("block %d: text is longer than 64 KiB", i)
			}
			if b.Figure != nil || len(b.Run) != 0 {
				return invalid("block %d: a text block carries only text", i)
			}
			for _, m := range reAsset.FindAllStringSubmatch(b.Text, -1) {
				if s.assetSize(m[1]) <= 0 {
					return invalid("block %d: referenced asset %s is not uploaded", i, m[1])
				}
			}
		case BlockFigure:
			if b.Figure == nil || b.Text != "" || len(b.Run) != 0 {
				return invalid("block %d: a figure block carries only a figure", i)
			}
			if !reSha.MatchString(b.Figure.Asset) || s.assetSize(b.Figure.Asset) <= 0 {
				return invalid("block %d: the figure's asset is not uploaded", i)
			}
			if !isObject(b.Figure.Provenance) {
				return invalid("block %d: provenance must be a JSON object up to 64 KiB", i)
			}
		case BlockRun:
			if !isObject(b.Run) || b.Text != "" || b.Figure != nil {
				return invalid("block %d: a run block carries only a JSON object up to 64 KiB", i)
			}
		default:
			return invalid("block %d: unknown kind", i)
		}
	}
	return nil
}

// PushRevision validates and stores a revision, computing its ID from the content. Pushing the same revision again
// returns it unchanged; a different revision with a number already used for the report is ErrConflict.
func (s *Store) PushRevision(r Revision) (Revision, error) {
	if r.Blocks == nil {
		r.Blocks = []Block{}
	}
	if err := s.validateRevision(&r); err != nil {
		return Revision{}, err
	}
	if r.PublishedAt.IsZero() {
		r.PublishedAt = time.Now().UTC()
	}
	id, err := ComputeID(r)
	if err != nil {
		return Revision{}, err
	}
	r.ID = id
	doc, err := json.Marshal(r)
	if err != nil {
		return Revision{}, err
	}
	if len(doc) > MaxDocBytes {
		return Revision{}, invalid("revision is larger than 4 MiB")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback()
	var have, stored string
	err = tx.QueryRow(`SELECT id,doc FROM revisions WHERE report=? AND number=?`, r.Report, r.Number).Scan(&have, &stored)
	switch {
	case err == nil && have == id:
		var out Revision
		if err := json.Unmarshal([]byte(stored), &out); err != nil {
			return Revision{}, err
		}
		return out, nil
	case err == nil:
		return Revision{}, fmt.Errorf("%w: revision %d of this report already exists with different content", ErrConflict, r.Number)
	case !errors.Is(err, sql.ErrNoRows):
		return Revision{}, err
	}
	if _, err := tx.Exec(`INSERT INTO revisions(id,report,number,title,published_at,doc) VALUES(?,?,?,?,?,?)`, id, r.Report, r.Number, r.Title, r.PublishedAt.Unix(), string(doc)); err != nil {
		return Revision{}, err
	}
	for sha := range assetsOf(r) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO revision_assets(revision,sha) VALUES(?,?)`, id, sha); err != nil {
			return Revision{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Revision{}, err
	}
	return r, nil
}

// ---- spaces and readers ----

// PutSpace creates or replaces a space's title and report list.
func (s *Store) PutSpace(sp Space) error {
	if !reSlug.MatchString(sp.Slug) {
		return invalid("slug must be 1 to 40 characters of a-z, 0-9 and -")
	}
	if n := utf8.RuneCountInString(sp.Title); n < 1 || n > 200 {
		return invalid("title must be 1 to 200 characters")
	}
	if len(sp.Reports) > maxReportsSpace {
		return invalid("at most %d reports", maxReportsSpace)
	}
	for _, k := range sp.Reports {
		if !reKey.MatchString(k) {
			return invalid("report key must be 16 lowercase hex characters")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO spaces(slug,title,updated_at) VALUES(?,?,?) ON CONFLICT(slug) DO UPDATE SET title=excluded.title, updated_at=excluded.updated_at`, sp.Slug, sp.Title, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM space_reports WHERE space=?`, sp.Slug); err != nil {
		return err
	}
	for i, k := range sp.Reports {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO space_reports(space,report,position) VALUES(?,?,?)`, sp.Slug, k, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreateReader makes a named link to a space and returns it with its token, which is not stored.
func (s *Store) CreateReader(space, name string, expires *time.Time) (Reader, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return Reader{}, invalid("name must be 1 to 80 characters")
	}
	if expires != nil && !expires.After(time.Now()) {
		return Reader{}, invalid("expiry must be in the future")
	}
	var one int
	if s.db.QueryRow(`SELECT 1 FROM spaces WHERE slug=?`, space).Scan(&one) != nil {
		return Reader{}, ErrNotFound
	}
	tok, err := newToken()
	if err != nil {
		return Reader{}, err
	}
	now := time.Now()
	var exp any
	if expires != nil {
		exp = expires.Unix()
	}
	res, err := s.db.Exec(`INSERT INTO readers(space,name,token_hash,created_at,expires_at) VALUES(?,?,?,?,?)`, space, name, hashToken(tok), now.Unix(), exp)
	if err != nil {
		return Reader{}, err
	}
	id, _ := res.LastInsertId()
	rd := Reader{ID: id, Space: space, Name: name, CreatedAt: time.Unix(now.Unix(), 0).UTC(), Token: tok}
	if expires != nil {
		t := time.Unix(expires.Unix(), 0).UTC()
		rd.ExpiresAt = &t
	}
	return rd, nil
}

// Readers lists every reader link, newest first. Tokens are never included.
func (s *Store) Readers() ([]Reader, error) {
	rows, err := s.db.Query(`SELECT id,space,name,created_at,expires_at,revoked FROM readers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reader{}
	for rows.Next() {
		var rd Reader
		var created int64
		var exp sql.NullInt64
		var rv int
		if err := rows.Scan(&rd.ID, &rd.Space, &rd.Name, &created, &exp, &rv); err != nil {
			return nil, err
		}
		rd.CreatedAt = time.Unix(created, 0).UTC()
		if exp.Valid {
			t := time.Unix(exp.Int64, 0).UTC()
			rd.ExpiresAt = &t
		}
		rd.Revoked = rv != 0
		out = append(out, rd)
	}
	return out, rows.Err()
}

func (s *Store) RevokeReader(id int64) error {
	res, err := s.db.Exec(`UPDATE readers SET revoked=1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type readerLink struct {
	id    int64
	name  string
	space string
}

// lookup resolves a token. Unknown, revoked and expired tokens are indistinguishable (ErrNotFound).
func (s *Store) lookup(token string) (readerLink, error) {
	if len(token) < 20 || len(token) > 100 {
		return readerLink{}, ErrNotFound
	}
	h := hashToken(token)
	var rd readerLink
	var stored string
	var exp sql.NullInt64
	var rv int
	err := s.db.QueryRow(`SELECT id,name,space,token_hash,expires_at,revoked FROM readers WHERE token_hash=?`, h).Scan(&rd.id, &rd.name, &rd.space, &stored, &exp, &rv)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || rv != 0 || (exp.Valid && time.Now().Unix() >= exp.Int64) {
		return readerLink{}, ErrNotFound
	}
	return rd, nil
}

// ---- reader view ----

type ReportSummary struct {
	Key         string    `json:"key"`
	Latest      int       `json:"latest"`
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"publishedAt"`
}

type SpaceView struct {
	Reader   string          `json:"reader"`
	Title    string          `json:"title"`
	Reports  []ReportSummary `json:"reports"`
	LastSync *time.Time      `json:"lastSync,omitempty"`
	Comments int             `json:"comments"`
}

// Space is what a reader sees on opening their link.
func (s *Store) Space(token string) (SpaceView, error) {
	rd, err := s.lookup(token)
	if err != nil {
		return SpaceView{}, err
	}
	v := SpaceView{Reader: rd.name, Reports: []ReportSummary{}, LastSync: s.lastContact()}
	if err := s.db.QueryRow(`SELECT title FROM spaces WHERE slug=?`, rd.space).Scan(&v.Title); err != nil {
		return SpaceView{}, ErrNotFound
	}
	rows, err := s.db.Query(`SELECT r.report,r.number,r.title,r.published_at FROM space_reports sr JOIN revisions r ON r.report=sr.report
		WHERE sr.space=? AND r.number=(SELECT MAX(number) FROM revisions WHERE report=r.report) ORDER BY sr.position`, rd.space)
	if err != nil {
		return SpaceView{}, err
	}
	for rows.Next() {
		var rs ReportSummary
		var at int64
		if err := rows.Scan(&rs.Key, &rs.Latest, &rs.Title, &at); err != nil {
			rows.Close()
			return SpaceView{}, err
		}
		rs.PublishedAt = time.Unix(at, 0).UTC()
		v.Reports = append(v.Reports, rs)
	}
	rows.Close()
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE space=?`, rd.space).Scan(&v.Comments); err != nil {
		return SpaceView{}, err
	}
	return v, nil
}

// ReaderBlock is a block as a reader receives it; for text blocks Shared lists the [[report:key|...]] targets that
// are in the reader's space, so the client can show the rest as "not shared with you".
type ReaderBlock struct {
	Block
	Shared []string `json:"shared,omitempty"`
}

type ReaderRevision struct {
	ID          string        `json:"id"`
	Report      string        `json:"report"`
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	PublishedAt time.Time     `json:"publishedAt"`
	Authors     string        `json:"authors"`
	Blocks      []ReaderBlock `json:"blocks"`
}

type RevisionRef struct {
	Number      int       `json:"number"`
	PublishedAt time.Time `json:"publishedAt"`
}

type ReportView struct {
	Revision  ReaderRevision `json:"revision"`
	Revisions []RevisionRef  `json:"revisions"`
	Comments  []Comment      `json:"comments"`
}

func (s *Store) inSpace(space, report string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM space_reports WHERE space=? AND report=?`, space, report).Scan(&one) == nil
}

// Report returns a revision (the latest when number is 0), the report's revision list and its comments in this space.
func (s *Store) Report(token, key string, number int) (ReportView, error) {
	rd, err := s.lookup(token)
	if err != nil {
		return ReportView{}, err
	}
	if !reKey.MatchString(key) || number < 0 || !s.inSpace(rd.space, key) {
		return ReportView{}, ErrNotFound
	}
	var doc string
	q, args := `SELECT doc FROM revisions WHERE report=? AND number=?`, []any{key, number}
	if number == 0 {
		q, args = `SELECT doc FROM revisions WHERE report=? ORDER BY number DESC LIMIT 1`, []any{key}
	}
	if s.db.QueryRow(q, args...).Scan(&doc) != nil {
		return ReportView{}, ErrNotFound
	}
	var rev Revision
	if err := json.Unmarshal([]byte(doc), &rev); err != nil {
		return ReportView{}, err
	}
	shared := map[string]bool{}
	rows, err := s.db.Query(`SELECT report FROM space_reports WHERE space=?`, rd.space)
	if err != nil {
		return ReportView{}, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return ReportView{}, err
		}
		shared[k] = true
	}
	rows.Close()
	v := ReportView{Revision: ReaderRevision{ID: rev.ID, Report: rev.Report, Number: rev.Number, Title: rev.Title, PublishedAt: rev.PublishedAt, Authors: rev.Authors, Blocks: []ReaderBlock{}}}
	for _, b := range rev.Blocks {
		rb := ReaderBlock{Block: b}
		if b.Kind == BlockText {
			seen := map[string]bool{}
			for _, m := range reLink.FindAllStringSubmatch(b.Text, -1) {
				if shared[m[1]] && !seen[m[1]] {
					seen[m[1]] = true
					rb.Shared = append(rb.Shared, m[1])
				}
			}
		}
		v.Revision.Blocks = append(v.Revision.Blocks, rb)
	}
	v.Revisions = []RevisionRef{}
	rr, err := s.db.Query(`SELECT number,published_at FROM revisions WHERE report=? ORDER BY number`, key)
	if err != nil {
		return ReportView{}, err
	}
	for rr.Next() {
		var ref RevisionRef
		var at int64
		if err := rr.Scan(&ref.Number, &at); err != nil {
			rr.Close()
			return ReportView{}, err
		}
		ref.PublishedAt = time.Unix(at, 0).UTC()
		v.Revisions = append(v.Revisions, ref)
	}
	rr.Close()
	v.Comments, err = s.queryComments(`WHERE space=? AND report=? ORDER BY id`, rd.space, key)
	if err != nil {
		return ReportView{}, err
	}
	for i := range v.Comments {
		v.Comments[i].Space = ""
	}
	return v, nil
}

// ---- comments ----

// CommentInput is a new comment. Author is used only for owner comments; a reader's name comes from the link.
type CommentInput struct {
	Report   string
	Revision string // the revision ID
	Block    string
	ParentID int64
	Body     string
	Anchor   *Anchor
	Author   string
}

func validAnchor(a *Anchor) bool {
	ok := func(v float64) bool { return v >= 0 && v <= 1 } // false for NaN too
	switch a.Kind {
	case "point":
		return ok(a.X) && ok(a.Y) && a.X1 == 0 && a.Y1 == 0
	case "box":
		return ok(a.X) && ok(a.Y) && ok(a.X1) && ok(a.Y1) && a.X < a.X1 && a.Y < a.Y1
	}
	return false
}

// AddComment records a reader's comment, attributed to the link's name.
func (s *Store) AddComment(token string, in CommentInput) (Comment, error) {
	rd, err := s.lookup(token)
	if err != nil {
		return Comment{}, err
	}
	return s.addComment(rd.space, rd.name, rd.id, in)
}

// AddOwnerComment records a comment by the owner in a space.
func (s *Store) AddOwnerComment(space string, in CommentInput) (Comment, error) {
	author := strings.TrimSpace(in.Author)
	if n := utf8.RuneCountInString(author); n < 1 || n > 80 {
		return Comment{}, invalid("author must be 1 to 80 characters")
	}
	var one int
	if s.db.QueryRow(`SELECT 1 FROM spaces WHERE slug=?`, space).Scan(&one) != nil {
		return Comment{}, invalid("no such space")
	}
	return s.addComment(space, author, 0, in)
}

// addComment records a comment; readerID is the link it came through, 0 for the owner. A link may post at most
// maxCommentsPerLinkDay comments a day, and only readers' comments count towards the space's limit, so one leaked
// link can neither flood a space nor stop the owner from answering.
func (s *Store) addComment(space, author string, readerID int64, in CommentInput) (Comment, error) {
	owner := readerID == 0
	body := strings.TrimSpace(in.Body)
	if n := utf8.RuneCountInString(body); n < 1 || n > maxCommentRunes {
		return Comment{}, invalid("a comment is 1 to %d characters", maxCommentRunes)
	}
	var count int
	if !owner {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE space=? AND from_owner=0`, space).Scan(&count); err != nil {
			return Comment{}, err
		}
		if count >= maxCommentsSpace {
			return Comment{}, invalid("comment limit reached")
		}
		var today int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE reader_id=? AND created_at>?`, readerID, time.Now().Add(-24*time.Hour).Unix()).Scan(&today); err != nil {
			return Comment{}, err
		}
		if today >= maxCommentsPerLinkDay {
			return Comment{}, invalid("this link has reached its limit of %d comments a day", maxCommentsPerLinkDay)
		}
	}
	c := Comment{Space: space, Report: in.Report, Revision: in.Revision, Block: in.Block, ParentID: in.ParentID, Author: author, FromOwner: owner, Body: body}
	if in.ParentID != 0 {
		if in.Anchor != nil {
			return Comment{}, invalid("a reply carries no place on the picture")
		}
		err := s.db.QueryRow(`SELECT report,revision,block FROM comments WHERE id=? AND space=? AND parent_id=0`, in.ParentID, space).Scan(&c.Report, &c.Revision, &c.Block)
		if err != nil {
			return Comment{}, invalid("no such thread in this space")
		}
	} else {
		if !reKey.MatchString(in.Report) || !s.inSpace(space, in.Report) {
			return Comment{}, invalid("no such report in this space")
		}
		var doc string
		if s.db.QueryRow(`SELECT doc FROM revisions WHERE id=? AND report=?`, in.Revision, in.Report).Scan(&doc) != nil {
			return Comment{}, invalid("no such revision of this report")
		}
		var rev Revision
		if err := json.Unmarshal([]byte(doc), &rev); err != nil {
			return Comment{}, err
		}
		kind := ""
		for _, b := range rev.Blocks {
			if b.ID == in.Block {
				kind = b.Kind
			}
		}
		if kind == "" {
			return Comment{}, invalid("no such block in this revision")
		}
		if in.Anchor != nil {
			if kind != BlockFigure || !validAnchor(in.Anchor) {
				return Comment{}, invalid("the marked place must be inside a figure")
			}
			a := *in.Anchor
			c.Anchor = &a
		}
	}
	anchor := ""
	if c.Anchor != nil {
		b, _ := json.Marshal(c.Anchor)
		anchor = string(b)
	}
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO comments(space,report,revision,block,anchor,parent_id,author,from_owner,body,created_at,reader_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		space, c.Report, c.Revision, c.Block, anchor, c.ParentID, author, boolInt(owner), body, now.Unix(), readerID)
	if err != nil {
		return Comment{}, err
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt = time.Unix(now.Unix(), 0).UTC()
	if !owner {
		c.Space = ""
	}
	return c, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ResolveComment resolves or reopens a thread (a first comment). Only the publisher API calls it.
func (s *Store) ResolveComment(id int64, resolved bool) (Comment, error) {
	var pid int64
	if err := s.db.QueryRow(`SELECT parent_id FROM comments WHERE id=?`, id).Scan(&pid); err != nil {
		return Comment{}, ErrNotFound
	}
	if pid != 0 {
		return Comment{}, invalid("only a thread's first comment is resolved")
	}
	var at any
	if resolved {
		at = time.Now().Unix()
	}
	if _, err := s.db.Exec(`UPDATE comments SET resolved_at=? WHERE id=?`, at, id); err != nil {
		return Comment{}, err
	}
	cs, err := s.queryComments(`WHERE id=?`, id)
	if err != nil || len(cs) != 1 {
		return Comment{}, err
	}
	return cs[0], nil
}

// CommentsAfter is the owner's feed: up to 500 comments with an id above after, oldest first, with their space.
func (s *Store) CommentsAfter(after int64) ([]Comment, error) {
	return s.queryComments(fmt.Sprintf(`WHERE id>? ORDER BY id LIMIT %d`, feedLimit), after)
}

func (s *Store) queryComments(where string, args ...any) ([]Comment, error) {
	rows, err := s.db.Query(`SELECT id,space,report,revision,block,anchor,parent_id,author,from_owner,body,created_at,resolved_at FROM comments `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var anchor string
		var owner int
		var created int64
		var res sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Space, &c.Report, &c.Revision, &c.Block, &anchor, &c.ParentID, &c.Author, &owner, &c.Body, &created, &res); err != nil {
			return nil, err
		}
		if anchor != "" {
			var a Anchor
			if json.Unmarshal([]byte(anchor), &a) == nil && validAnchor(&a) {
				c.Anchor = &a
			}
		}
		c.FromOwner = owner != 0
		c.CreatedAt = time.Unix(created, 0).UTC()
		if res.Valid {
			t := time.Unix(res.Int64, 0).UTC()
			c.ResolvedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// upgrade brings a database written by an earlier build up to date: the reader_id column on comments and the
// revision_assets index (filled from the stored revisions once).
func (s *Store) upgrade() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('comments') WHERE name='reader_id'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE comments ADD COLUMN reader_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var indexed, revs int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM revision_assets), (SELECT COUNT(*) FROM revisions)`).Scan(&indexed, &revs); err != nil {
		return err
	}
	if indexed > 0 || revs == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT id, doc FROM revisions`)
	if err != nil {
		return err
	}
	type pair struct{ rev, sha string }
	var todo []pair
	for rows.Next() {
		var id, doc string
		if err := rows.Scan(&id, &doc); err != nil {
			rows.Close()
			return err
		}
		var r Revision
		if json.Unmarshal([]byte(doc), &r) == nil {
			for sha := range assetsOf(r) {
				todo = append(todo, pair{id, sha})
			}
		}
	}
	rows.Close()
	for _, p := range todo {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO revision_assets(revision,sha) VALUES(?,?)`, p.rev, p.sha); err != nil {
			return err
		}
	}
	return nil
}
