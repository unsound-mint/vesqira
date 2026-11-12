package reports

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/publish"
)

// The owner's side of publishing: which revisions went out, a local copy of the comments pulled from the publish
// server (so the owner and the agent can read them offline), replies the agent drafted for the owner to send, and the
// publish server's address. The publisher key is never stored here; it lives in a 0600 file beside the catalog.

const MaxReply = 4000

func openPublishing(db *sql.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS report_publications(
			report_id INTEGER NOT NULL, number INTEGER NOT NULL, revision_id TEXT NOT NULL, working_revision INTEGER NOT NULL,
			published_at INTEGER NOT NULL, space TEXT NOT NULL, doc TEXT NOT NULL, PRIMARY KEY(report_id, number))`,
		`CREATE TABLE IF NOT EXISTS publish_comments(
			id INTEGER PRIMARY KEY, space TEXT NOT NULL, report TEXT NOT NULL, revision TEXT NOT NULL, block TEXT NOT NULL,
			anchor TEXT NOT NULL DEFAULT '', parent_id INTEGER NOT NULL, author TEXT NOT NULL, from_owner INTEGER NOT NULL,
			body TEXT NOT NULL, created_at INTEGER NOT NULL, resolved_at INTEGER NOT NULL DEFAULT 0, quote TEXT NOT NULL DEFAULT '')`, // quote: schema 3
		`CREATE INDEX IF NOT EXISTS publish_comments_report ON publish_comments(report)`,
		`CREATE TABLE IF NOT EXISTS reply_drafts(
			id INTEGER PRIMARY KEY AUTOINCREMENT, comment_id INTEGER NOT NULL, body TEXT NOT NULL, author TEXT NOT NULL,
			created_at INTEGER NOT NULL, sent_at INTEGER NOT NULL DEFAULT 0, discarded INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS publish_settings(k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	for _, t := range []string{"publish_comments", "reply_drafts"} {
		if err := schema.Scope(db, t); err != nil {
			return err
		}
	}
	return nil
}

// ---------- settings ----------

func (s *Store) Setting(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM publish_settings WHERE k=?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(k, v string) error {
	_, err := s.db.Exec(`INSERT INTO publish_settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// ---------- publications ----------

type Publication struct {
	Number          int       `json:"number"`
	RevisionID      string    `json:"revisionId"`
	WorkingRevision int       `json:"workingRevision"`
	PublishedAt     time.Time `json:"publishedAt"`
	Space           string    `json:"space"`
	// Doc is the published revision exactly as readers have it (publish.Revision as JSON). Lists leave it out.
	Doc json.RawMessage `json:"doc,omitempty"`
}

func (s *Store) Publications(reportID int64) ([]Publication, error) {
	rows, err := s.db.Query(`SELECT number, revision_id, working_revision, published_at, space FROM report_publications WHERE report_id=? ORDER BY number DESC`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Publication{}
	for rows.Next() {
		var p Publication
		var at int64
		if err := rows.Scan(&p.Number, &p.RevisionID, &p.WorkingRevision, &at, &p.Space); err != nil {
			return nil, err
		}
		p.PublishedAt = time.UnixMilli(at).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastPublication is the newest publication of a report with its document; ErrNotFound when never published.
func (s *Store) LastPublication(reportID int64) (Publication, error) {
	var p Publication
	var at int64
	var doc string
	err := s.db.QueryRow(`SELECT number, revision_id, working_revision, published_at, space, doc FROM report_publications WHERE report_id=? ORDER BY number DESC LIMIT 1`, reportID).Scan(&p.Number, &p.RevisionID, &p.WorkingRevision, &at, &p.Space, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.PublishedAt, p.Doc = time.UnixMilli(at).UTC(), json.RawMessage(doc)
	return p, err
}

// NextNumber is the published number the next publication of this report gets.
func (s *Store) NextNumber(reportID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM report_publications WHERE report_id=?`, reportID).Scan(&n)
	return n, err
}

func (s *Store) RecordPublication(reportID int64, p Publication) error {
	_, err := s.db.Exec(`INSERT INTO report_publications(report_id,number,revision_id,working_revision,published_at,space,doc) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(report_id,number) DO NOTHING`, reportID, p.Number, p.RevisionID, p.WorkingRevision, p.PublishedAt.UnixMilli(), p.Space, string(p.Doc))
	return err
}

// PublishedKeys lists the keys of reports published at least once, for the space's report list.
func (s *Store) PublishedKeys(space string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT r.key FROM reports r JOIN report_publications p ON p.report_id=r.id WHERE p.space=? ORDER BY r.title`, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ---------- comments mirror ----------

// MirrorComments stores comments pulled from the publish server. A comment already held is updated (its resolved
// state may have changed).
func (s *Store) MirrorComments(cs []publish.Comment) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range cs {
		anchor := ""
		if c.Anchor != nil {
			b, _ := json.Marshal(c.Anchor)
			anchor = string(b)
		}
		var res int64
		if c.ResolvedAt != nil {
			res = c.ResolvedAt.UnixMilli()
		}
		if _, err := tx.Exec(`INSERT INTO publish_comments(id,space,report,revision,block,anchor,parent_id,author,from_owner,body,created_at,resolved_at,quote)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET resolved_at=excluded.resolved_at`,
			c.ID, c.Space, c.Report, c.Revision, c.Block, anchor, c.ParentID, c.Author, c.FromOwner, c.Body, c.CreatedAt.UnixMilli(), res, c.Quote); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CommentCursor is the highest comment id held, for the next pull.
func (s *Store) CommentCursor() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM publish_comments`).Scan(&n)
	return n, err
}

// ForgetComments removes mirrored comments the publish server deleted (a first comment takes its replies with it),
// with the reply drafts waiting on them.
func (s *Store) ForgetComments(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM reply_drafts WHERE comment_id=? AND sent_at=0`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM publish_comments WHERE id=? OR parent_id=?`, id, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Withdrawn forgets that a report was ever published: its publications, its mirrored comments and the replies
// drafted to them. The publish server has deleted the same; the draft itself is untouched, and publishing it again
// starts at revision 1.
func (s *Store) Withdrawn(reportID int64) error {
	r, err := s.Get(reportID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM reply_drafts WHERE sent_at=0 AND comment_id IN (SELECT id FROM publish_comments WHERE report=?)`, r.Key},
		{`DELETE FROM publish_comments WHERE report=?`, r.Key},
		{`DELETE FROM report_publications WHERE report_id=?`, reportID},
		{`DELETE FROM publish_settings WHERE k=?`, fmt.Sprintf("pending_publication_%d", reportID)},
	} {
		if _, err := tx.Exec(q.sql, q.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetResolved(commentID int64, at *time.Time) error {
	var v int64
	if at != nil {
		v = at.UnixMilli()
	}
	_, err := s.db.Exec(`UPDATE publish_comments SET resolved_at=? WHERE id=?`, v, commentID)
	return err
}

const commentCols = `id,space,report,revision,block,anchor,parent_id,author,from_owner,body,created_at,resolved_at,quote`

func scanComments(rows *sql.Rows) ([]publish.Comment, error) {
	defer rows.Close()
	out := []publish.Comment{}
	for rows.Next() {
		var c publish.Comment
		var anchor string
		var at, res int64
		if err := rows.Scan(&c.ID, &c.Space, &c.Report, &c.Revision, &c.Block, &anchor, &c.ParentID, &c.Author, &c.FromOwner, &c.Body, &at, &res, &c.Quote); err != nil {
			return nil, err
		}
		if anchor != "" {
			var a publish.Anchor
			if json.Unmarshal([]byte(anchor), &a) == nil {
				c.Anchor = &a
			}
		}
		c.CreatedAt = time.UnixMilli(at).UTC()
		if res > 0 {
			t := time.UnixMilli(res).UTC()
			c.ResolvedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Comments lists the mirrored comments of one report (by key), oldest first. An empty key lists all.
func (s *Store) Comments(reportKey string) ([]publish.Comment, error) {
	var rows *sql.Rows
	var err error
	if reportKey == "" {
		rows, err = s.db.Query(`SELECT ` + commentCols + ` FROM publish_comments ORDER BY id`)
	} else {
		rows, err = s.db.Query(`SELECT `+commentCols+` FROM publish_comments WHERE report=? ORDER BY id`, reportKey)
	}
	if err != nil {
		return nil, err
	}
	return scanComments(rows)
}

// Comment returns one mirrored comment.
func (s *Store) Comment(id int64) (publish.Comment, error) {
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM publish_comments WHERE id=?`, id)
	if err != nil {
		return publish.Comment{}, err
	}
	cs, err := scanComments(rows)
	if err != nil {
		return publish.Comment{}, err
	}
	if len(cs) == 0 {
		return publish.Comment{}, ErrNotFound
	}
	return cs[0], nil
}

// ---------- reply drafts ----------

type ReplyDraft struct {
	ID        int64      `json:"id"`
	CommentID int64      `json:"commentId"` // the thread's first comment
	Body      string     `json:"body"`
	Author    string     `json:"author"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
}

// DraftReply keeps a reply to a thread for the owner to read, edit and send. Nothing is sent from here.
func (s *Store) DraftReply(commentID int64, body, author string) (ReplyDraft, error) {
	if err := checkAuthor(author); err != nil {
		return ReplyDraft{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > MaxReply {
		return ReplyDraft{}, fmt.Errorf("%w: a reply needs 1 to %d characters", ErrInvalid, MaxReply)
	}
	c, err := s.Comment(commentID)
	if err != nil {
		return ReplyDraft{}, err
	}
	if c.ParentID != 0 {
		commentID = c.ParentID // a reply always goes to the thread
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO reply_drafts(comment_id,body,author,created_at) VALUES(?,?,?,?)`, commentID, body, author, now.UnixMilli())
	if err != nil {
		return ReplyDraft{}, err
	}
	id, _ := res.LastInsertId()
	return ReplyDraft{ID: id, CommentID: commentID, Body: body, Author: author, CreatedAt: now}, nil
}

// Drafts lists unsent, undiscarded reply drafts.
func (s *Store) Drafts() ([]ReplyDraft, error) {
	rows, err := s.db.Query(`SELECT id, comment_id, body, author, created_at FROM reply_drafts WHERE sent_at=0 AND discarded=0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReplyDraft{}
	for rows.Next() {
		var d ReplyDraft
		var at int64
		if err := rows.Scan(&d.ID, &d.CommentID, &d.Body, &d.Author, &at); err != nil {
			return nil, err
		}
		d.CreatedAt = time.UnixMilli(at).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Draft(id int64) (ReplyDraft, error) {
	var d ReplyDraft
	var at, sent int64
	var discarded bool
	err := s.db.QueryRow(`SELECT id, comment_id, body, author, created_at, sent_at, discarded FROM reply_drafts WHERE id=?`, id).Scan(&d.ID, &d.CommentID, &d.Body, &d.Author, &at, &sent, &discarded)
	if errors.Is(err, sql.ErrNoRows) || discarded {
		return d, ErrNotFound
	}
	d.CreatedAt = time.UnixMilli(at).UTC()
	if sent > 0 {
		t := time.UnixMilli(sent).UTC()
		d.SentAt = &t
	}
	return d, err
}

func (s *Store) MarkDraftSent(id int64) error {
	_, err := s.db.Exec(`UPDATE reply_drafts SET sent_at=? WHERE id=? AND sent_at=0`, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) DiscardDraft(id int64) error {
	res, err := s.db.Exec(`UPDATE reply_drafts SET discarded=1 WHERE id=? AND sent_at=0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
