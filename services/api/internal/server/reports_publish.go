package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Publishing reports (ADR 0036): the preview of what goes out, publishing, the publish server settings and reader
// links, and the readers' comments pulled back and answered.

func (s *Server) spaceOf(r *http.Request) (slug, title string) {
	slug, _ = s.Reports.Setting("space")
	title, _ = s.Reports.Setting("space_title")
	if slug == "" {
		slug = "supervisor"
	}
	if title == "" {
		title = "Reports"
	}
	return slug, title
}

func (s *Server) reportPreflight(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	rep, err := s.Reports.Get(id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	space, _ := s.spaceOf(r)
	f, err := s.freeze(rep, space, true)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if c, err := s.publisher(); err != nil {
		f.Errors = append(f.Errors, err.Error())
	} else if rs, err := c.readers(); err != nil {
		f.Warnings = append(f.Warnings, err.Error())
	} else {
		for _, rd := range rs {
			if rd.Space == space && !rd.Revoked && (rd.ExpiresAt == nil || rd.ExpiresAt.After(time.Now())) {
				f.Readers = append(f.Readers, rd.Name)
			}
		}
		if len(f.Readers) == 0 {
			f.Warnings = append(f.Warnings, "Nobody has a reader link to this space yet: make one after publishing.")
		}
	}
	writeJSON(w, 200, map[string]any{"revision": f.Revision, "warnings": f.Warnings, "errors": f.Errors, "changes": f.Changes, "space": f.Space, "readers": f.Readers, "workingRevision": rep.Revision})
}

// publishReport freezes the report as it is at the revision the owner previewed, uploads its pictures, pushes the
// revision, records it and adds the report to the space. Pushes are idempotent, so retrying after a timeout is safe.
func (s *Server) publishReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Revision int `json:"revision"` // the working revision the owner saw in the preflight
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	rep, err := s.Reports.Get(id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if rep.Revision != in.Revision {
		fail(w, 409, fmt.Sprintf("the report changed since you previewed it (revision %d, now %d, last saved by %s); preview it again", in.Revision, rep.Revision, rep.LastAuthor))
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	space, title := s.spaceOf(r)
	f, err := s.freeze(rep, space, true)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if len(f.Errors) > 0 {
		fail(w, 422, "cannot publish: "+strings.Join(f.Errors, " "))
		return
	}
	if last, err := s.Reports.LastPublication(rep.ID); err == nil && sameContent(last.Doc, f.Revision) {
		fail(w, 409, fmt.Sprintf("nothing changed since published revision %d, so readers already have this", last.Number))
		return
	}
	for sha, b := range f.Assets {
		if err := c.putAsset(sha, b); err != nil {
			s.reportErr(w, err)
			return
		}
	}
	// The frozen revision is kept until the server confirms it, and a retry of the same working revision resends
	// exactly those bytes: a push whose answer was lost then lands on the same revision id instead of a conflict.
	pendingKey := fmt.Sprintf("pending_publication_%d", rep.ID)
	toPush := f.Revision
	if raw, _ := s.Reports.Setting(pendingKey); raw != "" {
		var p struct {
			Working  int              `json:"working"`
			Revision publish.Revision `json:"revision"`
		}
		if json.Unmarshal([]byte(raw), &p) == nil && p.Working == rep.Revision && p.Revision.Number == f.Revision.Number {
			toPush = p.Revision
		}
	}
	if raw, err := json.Marshal(map[string]any{"working": rep.Revision, "revision": toPush}); err == nil {
		if err := s.Reports.SetSetting(pendingKey, string(raw)); err != nil {
			s.reportErr(w, err)
			return
		}
	}
	stored, err := c.pushRevision(toPush)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	doc, _ := json.Marshal(stored)
	if err := s.Reports.RecordPublication(rep.ID, reports.Publication{Number: stored.Number, RevisionID: stored.ID, WorkingRevision: rep.Revision, PublishedAt: stored.PublishedAt, Space: space, Doc: doc}); err != nil {
		s.reportErr(w, err)
		return
	}
	_ = s.Reports.SetSetting(pendingKey, "")
	keys, err := s.Reports.PublishedKeys(space)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if err := c.putSpace(publish.Space{Slug: space, Title: title, Reports: keys}); err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit("report published", "report", rep.ID, fmt.Sprintf("%q as published revision %d to space %s", rep.Title, stored.Number, space))
	writeJSON(w, 200, map[string]any{"number": stored.Number, "revisionId": stored.ID, "space": space})
}

var (
	identifyingKey   = regexp.MustCompile(`(?i)(host|node|cluster|account|partition|queue|user|login|email|mail|owner|machine|server)`)
	identifyingValue = regexp.MustCompile(`@|^[a-z0-9-]+(\.[a-z0-9-]+){2,}$`)
)

// identifyingSettings names run settings readers would see that look like a machine, account or address (the run
// import drops paths, but not these). It warns; the owner decides.
func identifyingSettings(run string, params []runs.Param) string {
	var hits []string
	seen := map[string]bool{}
	for _, p := range params {
		if (identifyingKey.MatchString(p.Key) || identifyingValue.MatchString(strings.ToLower(p.Value))) && !seen[p.Key] {
			seen[p.Key] = true
			hits = append(hits, p.Key+"="+clipRunes(p.Value, 40))
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return fmt.Sprintf("Run %s shows readers settings that may name a machine, account or person: %s.", run, strings.Join(hits, ", "))
}

// sameContent is true when a new revision would show readers exactly what the last one does (title, attribution and
// every block), ignoring its number and time.
func sameContent(lastDoc json.RawMessage, next publish.Revision) bool {
	var last publish.Revision
	if json.Unmarshal(lastDoc, &last) != nil || last.Title != next.Title || last.Authors != next.Authors || len(last.Blocks) != len(next.Blocks) {
		return false
	}
	for i := range last.Blocks {
		a, _ := json.Marshal(last.Blocks[i])
		b, _ := json.Marshal(next.Blocks[i])
		if string(a) != string(b) {
			return false
		}
	}
	return true
}

func (s *Server) audit(action, kind string, id int64, detail string) {
	if s.Audit != nil {
		s.Audit.Record(action, kind, id, detail)
	}
}

// ---------- publish server settings, readers, sync ----------

type publishState struct {
	URL        string    `json:"url"`
	KeySet     bool      `json:"keySet"`
	Space      string    `json:"space"`
	SpaceTitle string    `json:"spaceTitle"`
	OwnerName  string    `json:"ownerName"`
	AgentName  string    `json:"agentName"`
	LastSync   time.Time `json:"lastSync,omitzero"`
	LastError  string    `json:"lastError,omitempty"`
}

func (s *Server) publishStatus(w http.ResponseWriter, r *http.Request) {
	st := publishState{}
	st.URL, _ = s.Reports.Setting("publish_url")
	st.Space, st.SpaceTitle = s.spaceOf(r)
	st.OwnerName, _ = s.Reports.Setting("owner_name")
	st.AgentName, _ = s.Reports.Setting("agent_name")
	if s.PublishKeyFile != "" {
		_, err := readKey(s.PublishKeyFile)
		st.KeySet = err == nil
	}
	s.syncMu.Lock()
	st.LastSync, st.LastError = s.lastSync, s.lastSyncErr
	s.syncMu.Unlock()
	writeJSON(w, 200, st)
}

var slugRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

func (s *Server) publishSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL        *string `json:"url"`
		Key        *string `json:"key"` // written to the key file, never stored in the catalog or returned
		Space      *string `json:"space"`
		SpaceTitle *string `json:"spaceTitle"`
		OwnerName  *string `json:"ownerName"`
		AgentName  *string `json:"agentName"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	set := map[string]string{}
	if in.URL != nil {
		u, err := checkPublishURL(*in.URL)
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		set["publish_url"] = u
		// The key belongs to one server: a new address never inherits it, or saving an address would send the
		// existing key to whoever runs that address.
		if cur, _ := s.Reports.Setting("publish_url"); cur != "" && cur != u && in.Key == nil && s.PublishKeyFile != "" {
			if err := os.Remove(s.PublishKeyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.reportErr(w, err)
				return
			}
		}
	}
	if in.Space != nil {
		if !slugRe.MatchString(*in.Space) {
			fail(w, 422, "a space name uses 1 to 40 lower-case letters, digits and hyphens")
			return
		}
		set["space"] = *in.Space
	}
	for k, p := range map[string]*string{"space_title": in.SpaceTitle, "owner_name": in.OwnerName, "agent_name": in.AgentName} {
		if p != nil {
			v := strings.Join(strings.Fields(*p), " ")
			if utf8.RuneCountInString(v) > 80 {
				fail(w, 422, "names have at most 80 characters")
				return
			}
			set[k] = v
		}
	}
	if in.Key != nil {
		k := strings.TrimSpace(*in.Key)
		if len(k) < 32 || len(k) > 128 || strings.ContainsAny(k, " \t\r\n") {
			fail(w, 422, "that does not look like a publisher key (print one with: vesqira publish-server add-publisher)")
			return
		}
		if s.PublishKeyFile == "" {
			fail(w, 409, "this server was started without a data directory for the key")
			return
		}
		if err := writeKey(s.PublishKeyFile, k); err != nil {
			s.reportErr(w, err)
			return
		}
	}
	for k, v := range set {
		if err := s.Reports.SetSetting(k, v); err != nil {
			s.reportErr(w, err)
			return
		}
	}
	if in.URL != nil || in.Key != nil {
		s.audit("publish server set", "publish", 0, "address "+set["publish_url"])
		if c, err := s.publisher(); err == nil {
			if err := c.ping(); err != nil {
				fail(w, 502, "saved, but the publish server did not accept the key: "+err.Error())
				return
			}
		}
	}
	s.publishStatus(w, r)
}

func (s *Server) listReaders(w http.ResponseWriter, r *http.Request) {
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	rs, err := c.readers()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if rs == nil {
		rs = []publish.Reader{}
	}
	writeJSON(w, 200, rs)
}

func (s *Server) createReader(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !body(w, r, 2<<10, &in, true) {
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	space, title := s.spaceOf(r)
	keys, err := s.Reports.PublishedKeys(space)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if err := c.putSpace(publish.Space{Slug: space, Title: title, Reports: nonNilStrings(keys)}); err != nil { // the space must exist first
		s.reportErr(w, err)
		return
	}
	rd, err := c.createReader(space, in.Name, in.ExpiresAt)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	base, _ := s.Reports.Setting("publish_url")
	s.audit("reader link created", "publish", rd.ID, fmt.Sprintf("for %q on space %s", rd.Name, space))
	writeJSON(w, 201, map[string]any{"reader": rd, "link": base + "/read/" + rd.Token})
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) revokeReader(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if err := c.revokeReader(id); err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit("reader link revoked", "publish", id, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// SyncComments pulls new comments from the publish server into the local mirror. It is called on start, every
// minute while the app runs, and on demand; each call is cheap when nothing is new.
func (s *Server) SyncComments() error {
	if s.Reports == nil {
		return nil
	}
	c, err := s.publisher()
	if errors.Is(err, errNotConfigured) {
		return nil
	}
	if err == nil {
		for range 20 { // at most 10 000 comments per call
			var after int64
			if after, err = s.Reports.CommentCursor(); err != nil {
				break
			}
			var cs []publish.Comment
			if cs, err = c.commentsAfter(after); err != nil || len(cs) == 0 {
				break
			}
			if err = s.Reports.MirrorComments(cs); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = s.syncDeletions(c)
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err != nil {
		s.lastSyncErr = err.Error()
		return err
	}
	s.lastSync, s.lastSyncErr = time.Now().UTC(), ""
	return nil
}

// syncDeletions follows comments deleted on the publish server (by a reader deleting their own, or by the owner from
// another copy of the app) so the local copy does not keep them.
func (s *Server) syncDeletions(c *publishClient) error {
	for range 20 {
		cur, _ := s.Reports.Setting("comment_deletion_cursor")
		after, _ := strconv.ParseInt(cur, 10, 64)
		ds, err := c.deletionsAfter(after)
		if err != nil || len(ds) == 0 {
			return err
		}
		ids := make([]int64, 0, len(ds))
		for _, d := range ds {
			ids = append(ids, d.ID)
			after = max(after, d.Seq)
		}
		if err := s.Reports.ForgetComments(ids); err != nil {
			return err
		}
		if err := s.Reports.SetSetting("comment_deletion_cursor", strconv.FormatInt(after, 10)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) publishSyncNow(w http.ResponseWriter, r *http.Request) {
	if err := s.SyncComments(); err != nil {
		s.reportErr(w, err)
		return
	}
	s.publishStatus(w, r)
}

func (s *Server) reportComments(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Reports.Comments(r.URL.Query().Get("report"))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	ds, err := s.Reports.Drafts()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"comments": cs, "drafts": ds})
}

func (s *Server) sendReply(thread publish.Comment, text string) (publish.Comment, error) {
	c, err := s.publisher()
	if err != nil {
		return publish.Comment{}, err
	}
	owner, _ := s.Reports.Setting("owner_name")
	if owner == "" {
		owner = "Author"
	}
	if thread.ParentID != 0 {
		if thread, err = s.Reports.Comment(thread.ParentID); err != nil {
			return publish.Comment{}, err
		}
	}
	out, err := c.ownerComment(thread.Space, publish.Comment{Report: thread.Report, Revision: thread.Revision, Block: thread.Block, ParentID: thread.ID, Body: text, Author: owner})
	if err != nil {
		return out, err
	}
	out.Space = thread.Space
	return out, s.Reports.MirrorComments([]publish.Comment{out})
}

func (s *Server) replyReportComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	thread, err := s.Reports.Comment(id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	out, err := s.sendReply(thread, in.Body)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit("report reply sent", "comment", out.ID, "")
	writeJSON(w, 201, out)
}

func (s *Server) resolveReportComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	out, err := c.resolve(id, in.Resolved)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if err := s.Reports.SetResolved(id, out.ResolvedAt); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// deleteReportComment deletes a reader's or the owner's comment on the publish server (a thread's first comment takes
// its replies with it) and from the local copy.
func (s *Server) deleteReportComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	ids, err := c.deleteComment(id)
	if err != nil && !isNotFound(err) {
		s.reportErr(w, err)
		return
	}
	if err := s.Reports.ForgetComments(append(ids, id)); err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit("report comment deleted", "comment", id, "")
	writeJSON(w, 200, map[string]any{"deleted": append(ids, id)})
}

func (s *Server) draftReportReply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Body   string `json:"body"`
		Author string `json:"author"`
	}
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	d, err := s.Reports.DraftReply(id, in.Body, authorOf(in.Author))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 201, d)
}

// sendReplyDraft sends a drafted reply, possibly edited by the owner first. Only the owner's app calls it.
func (s *Server) sendReplyDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Body *string `json:"body"`
	}
	if !body(w, r, 32<<10, &in, true) {
		return
	}
	d, err := s.Reports.Draft(id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if d.SentAt != nil {
		fail(w, 409, "this reply was sent already")
		return
	}
	text := d.Body
	if in.Body != nil {
		text = *in.Body
	}
	thread, err := s.Reports.Comment(d.CommentID)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	out, err := s.sendReply(thread, text)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if err := s.Reports.MarkDraftSent(id); err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit("report reply sent", "comment", out.ID, "from a drafted reply")
	writeJSON(w, 201, out)
}

func (s *Server) discardReplyDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := s.Reports.DiscardDraft(id); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// syncState is embedded in Server through fields declared in server.go.
type syncState struct {
	syncMu      sync.Mutex
	lastSync    time.Time
	lastSyncErr string
	publishMu   sync.Mutex // one publication at a time, so numbers are assigned in order
}
