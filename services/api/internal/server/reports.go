package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/imgmeta"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Reports (ADR 0036). The owner and an agent (through `vesqira mcp`, which calls these routes on loopback) write
// reports; only the owner's own requests publish, send replies or resolve threads. The agent's MCP tools do not
// include those routes, and every write records its author.

func (s *Server) reportRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/reports", authz.ReportRead, s.listReports)
	s.route(mux, "POST /api/reports", authz.ReportWrite, s.createReport)
	s.route(mux, "GET /api/reports/graph", authz.ReportRead, s.reportGraph)
	s.route(mux, "GET /api/reports/{id}", authz.ReportRead, s.getReport)
	s.route(mux, "GET /api/reports/{id}/changes", authz.ReportRead, s.reportChanges)
	s.route(mux, "POST /api/reports/{id}", authz.ReportWrite, s.updateReport)
	s.route(mux, "POST /api/reports/{id}/seen", authz.ReportRead, s.reportSeen) // the reader's own state, not an edit
	s.route(mux, "POST /api/reports/{id}/delete", authz.LifecycleDelete, s.deleteReport)
	s.route(mux, "POST /api/reports/{id}/move", authz.ReportWrite, s.moveReport)
	s.route(mux, "POST /api/reports/{id}/withdraw", authz.PublishManage, s.withdrawReport)
	s.route(mux, "GET /api/reports/{id}/history", authz.ReportRead, s.reportHistory)
	s.route(mux, "GET /api/reports/{id}/history/{rev}", authz.ReportRead, s.reportRevision)
	s.route(mux, "GET /api/reports/{id}/preflight", authz.PublishManage, s.reportPreflight)
	s.route(mux, "POST /api/reports/{id}/publish", authz.PublishManage, s.publishReport)
	s.route(mux, "POST /api/attachments", authz.ReportWrite, s.addAttachment)
	s.route(mux, "GET /api/attachments/{sha}", authz.ReportRead, s.getAttachment)
	s.route(mux, "GET /api/publish", authz.PublishManage, s.publishStatus)
	s.route(mux, "POST /api/publish/settings", authz.PublishManage, s.publishSettings)
	s.route(mux, "POST /api/publish/sync", authz.PublishManage, s.publishSyncNow)
	s.route(mux, "GET /api/publish/readers", authz.PublishManage, s.listReaders)
	s.route(mux, "POST /api/publish/readers", authz.PublishManage, s.createReader)
	s.route(mux, "POST /api/publish/readers/{id}/revoke", authz.PublishManage, s.revokeReader)
	s.route(mux, "GET /api/report-comments", authz.ReportRead, s.reportComments)
	s.route(mux, "POST /api/report-comments/{id}/reply", authz.PublishManage, s.replyReportComment)
	s.route(mux, "POST /api/report-comments/{id}/resolve", authz.PublishManage, s.resolveReportComment)
	s.route(mux, "POST /api/report-comments/{id}/draft", authz.ReportWrite, s.draftReportReply)
	s.route(mux, "POST /api/report-comments/{id}/delete", authz.PublishManage, s.deleteReportComment)
	s.route(mux, "POST /api/reply-drafts/{id}/send", authz.PublishManage, s.sendReplyDraft)
	s.route(mux, "POST /api/reply-drafts/{id}/discard", authz.ReportWrite, s.discardReplyDraft)
}

func (s *Server) reportErr(w http.ResponseWriter, err error) {
	var re *remoteError
	switch {
	case errors.Is(err, reports.ErrNotFound):
		fail(w, 404, "not found")
	case errors.Is(err, reports.ErrInvalid):
		fail(w, 422, err.Error())
	case errors.Is(err, reports.ErrConflict):
		fail(w, 409, err.Error())
	case errors.Is(err, errNotConfigured):
		fail(w, 409, err.Error())
	case errors.As(err, &re):
		fail(w, 502, re.Error())
	case strings.HasPrefix(err.Error(), "the publish server") || strings.HasPrefix(err.Error(), "the publisher key"):
		fail(w, 502, err.Error())
	default:
		log.Printf("reports: %v", err)
		fail(w, 500, "internal error")
	}
}

func authorOf(a string) string {
	if a == "" {
		return reports.AuthorOwner
	}
	return a
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	list, err := s.Reports.List(wsOf(r), actorID(r.Context()))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Author string `json:"author"`
		Parent int64  `json:"parent"` // the page to create it under; 0 or absent for the top level
	}
	if !body(w, r, reports.MaxBody+(16<<10), &in, true) {
		return
	}
	rep, err := s.Reports.CreateUnder(wsOf(r), actorID(r.Context()), in.Title, in.Body, authorOf(in.Author), in.Parent)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if rep.LastAuthor == reports.AuthorAgent {
		s.audit(r.Context(), "report drafted by the agent", "report", rep.ID, fmt.Sprintf("%q created", rep.Title))
	}
	writeJSON(w, 201, rep)
}

// reportView is a report with what surrounds it: links in, publications, the readers' comments and drafted replies.
type reportView struct {
	reports.Report
	Backlinks    []reports.Summary     `json:"backlinks"`
	Publications []reports.Publication `json:"publications"`
	Comments     []publish.Comment     `json:"comments"`
	Drafts       []reports.ReplyDraft  `json:"drafts"`
	Unresolved   []string              `json:"unresolvedLinks"` // [[titles]] that name no report
	BlockIDs     []string              `json:"blockIds"`        // per block of the body: its published id (see frozen.SourceIDs)
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	rep, err := s.Reports.Get(wsOf(r), actorID(r.Context()), id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	v := reportView{Report: rep}
	if v.Backlinks, err = s.Reports.Backlinks(wsOf(r), id); err == nil {
		if v.Publications, err = s.Reports.Publications(id); err == nil {
			if v.Comments, err = s.Reports.Comments(rep.Key); err == nil {
				v.Drafts, err = s.draftsFor(v.Comments)
			}
		}
	}
	if err != nil {
		s.reportErr(w, err)
		return
	}
	for _, t := range reports.ReportTitles(rep.Body) {
		if _, err := s.Reports.ByTitle(wsOf(r), actorID(r.Context()), t); errors.Is(err, reports.ErrNotFound) {
			v.Unresolved = append(v.Unresolved, t)
		}
	}
	space, _ := s.spaceOf(r)
	if f, err := s.freeze(wsOf(r), rep, space, false); err == nil {
		v.BlockIDs = f.SourceIDs
	}
	if v.BlockIDs == nil {
		v.BlockIDs = []string{}
	}
	writeJSON(w, 200, v)
}

func (s *Server) draftsFor(cs []publish.Comment) ([]reports.ReplyDraft, error) {
	all, err := s.Reports.Drafts()
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, c := range cs {
		ids[c.ID] = true
	}
	out := []reports.ReplyDraft{}
	for _, d := range all {
		if ids[d.CommentID] {
			out = append(out, d)
		}
	}
	return out, nil
}

// reportChanges compares the report now with an earlier revision (by default the one the owner last opened), block
// by block, so agent edits can be kept or undone one at a time.
func (s *Server) reportChanges(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	rep, err := s.Reports.Get(wsOf(r), actorID(r.Context()), id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	since := rep.SeenRevision
	if v := r.URL.Query().Get("since"); v != "" {
		if since, err = strconv.Atoi(v); err != nil || since < 0 {
			fail(w, 400, "since must be a revision number")
			return
		}
	}
	before := ""
	if since > 0 {
		h, err := s.Reports.Revision(wsOf(r), id, since)
		if err != nil {
			s.reportErr(w, err)
			return
		}
		before = h.Body
	}
	hist, err := s.Reports.History(wsOf(r), id, false)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	authors := map[string]bool{}
	for _, h := range hist {
		if h.Revision > since {
			authors[h.Author] = true
		}
	}
	by := []string{}
	for a := range authors {
		by = append(by, a)
	}
	writeJSON(w, 200, map[string]any{"since": since, "revision": rep.Revision, "by": by, "changes": reports.Changes(before, rep.Body)})
}

// reportGraph is every report, the figures and runs they reference, and the links between them.
func (s *Server) reportGraph(w http.ResponseWriter, r *http.Request) {
	list, err := s.Reports.List(wsOf(r), actorID(r.Context()))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	edges, err := s.Reports.Edges(wsOf(r))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	type node struct {
		ID        string `json:"id"` // report:<id> | figure:<revision> | run:<id>
		Kind      string `json:"kind"`
		Label     string `json:"label"`
		Published bool   `json:"published,omitempty"`
		Missing   bool   `json:"missing,omitempty"` // a reference to something that no longer exists
	}
	type edge struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"` // link | embeds
	}
	nodes := []node{}
	seen := map[string]bool{}
	for _, rp := range list {
		id := "report:" + strconv.FormatInt(rp.ID, 10)
		seen[id] = true
		nodes = append(nodes, node{ID: id, Kind: "report", Label: rp.Title, Published: rp.Published > 0})
	}
	out := []edge{}
	for _, e := range edges {
		from := "report:" + strconv.FormatInt(e.From, 10)
		var to string
		switch e.Kind {
		case "report":
			if e.To == 0 || e.To == e.From {
				continue // a link to nothing is shown in the report as unresolved, not in the graph
			}
			to = "report:" + strconv.FormatInt(e.To, 10)
			out = append(out, edge{from, to, "link"})
			continue
		case "figure":
			to = "figure:" + e.Target
			if !seen[to] {
				n := node{ID: to, Kind: "figure", Label: "Missing figure", Missing: true}
				if f, err := s.Boards.FigureByRevision(wsOf(r), e.Target); err == nil {
					n.Label, n.Missing = f.Title, false
				}
				nodes, seen[to] = append(nodes, n), true
			}
		case "run":
			to = "run:" + e.Target
			if !seen[to] {
				n := node{ID: to, Kind: "run", Label: "Missing run", Missing: true}
				if rid, err := strconv.ParseInt(e.Target, 10, 64); err == nil && s.Runs != nil {
					if run, err := s.Runs.Get(wsOf(r), rid); err == nil {
						n.Label, n.Missing = run.Name, false
					}
				}
				nodes, seen[to] = append(nodes, n), true
			}
		}
		out = append(out, edge{from, to, "embeds"})
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes, "edges": out})
}

func (s *Server) updateReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Base   int     `json:"base"`
		Author string  `json:"author"`
	}
	if !body(w, r, reports.MaxBody+(16<<10), &in, true) {
		return
	}
	rep, err := s.Reports.Update(wsOf(r), actorID(r.Context()), id, reports.Update{Title: in.Title, Body: in.Body, Base: in.Base, Author: authorOf(in.Author)})
	if errors.Is(err, reports.ErrConflict) && rep.ID != 0 {
		writeJSON(w, 409, map[string]any{"error": err.Error(), "current": rep})
		return
	}
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if in.Author == reports.AuthorAgent {
		s.audit(r.Context(), "report drafted by the agent", "report", rep.ID, fmt.Sprintf("%q saved as revision %d", rep.Title, rep.Revision))
	}
	writeJSON(w, 200, rep)
}

func (s *Server) reportSeen(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if err := s.Reports.MarkSeen(wsOf(r), actorID(r.Context()), id, in.Revision); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := s.Reports.Delete(wsOf(r), id); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) moveReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Parent int64 `json:"parent"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if err := s.Reports.Move(wsOf(r), id, in.Parent); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// withdrawReport takes a report off the publish server: every published revision, its pictures and the readers'
// comments are deleted there, and the local record of them goes too. The draft stays.
func (s *Server) withdrawReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	rep, err := s.Reports.Get(wsOf(r), actorID(r.Context()), id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if pubs, err := s.Reports.Publications(id); err != nil || len(pubs) == 0 {
		s.reportErr(w, fmt.Errorf("%w: this report is not published", reports.ErrInvalid))
		return
	}
	c, err := s.publisher()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if err := c.withdraw(rep.Key); err != nil && !isNotFound(err) {
		s.reportErr(w, err)
		return
	}
	if err := s.Reports.Withdrawn(wsOf(r), id); err != nil {
		s.reportErr(w, err)
		return
	}
	s.audit(r.Context(), "report withdrawn from readers", "report", id, fmt.Sprintf("%q", rep.Title))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) reportHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	h, err := s.Reports.History(wsOf(r), id, false)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) reportRevision(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	rev, ok := pathInt(w, r, "rev")
	if !ok {
		return
	}
	h, err := s.Reports.Revision(wsOf(r), id, int(rev))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) addAttachment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Data string `json:"data"` // base64
	}
	if !body(w, r, reports.MaxAttachment*4/3+(16<<10), &in, true) {
		return
	}
	b, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		fail(w, 400, "data must be base64")
		return
	}
	sha, mime, err := s.Reports.AddAttachment(wsOf(r), actorID(r.Context()), b)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"sha": sha, "mime": mime, "markdown": "![](attachment:" + sha + ")"})
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	b, mime, err := s.Reports.Attachment(wsOf(r), r.PathValue("sha"))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(b)
}

// ---------- freezing ----------

// frozen is a revision ready to push, with the bytes of every asset it needs and what the owner should know first.
type frozen struct {
	Revision publish.Revision `json:"revision"`
	Assets   map[string][]byte
	Warnings []string    `json:"warnings"`
	Errors   []string    `json:"errors"` // anything here blocks publishing
	Changes  []blockDiff `json:"changes"`
	Space    string      `json:"space"`
	Readers  []string    `json:"readers"` // names of live reader links on the space, when the server could be asked
	// SourceIDs is, for each block of the working body (reports.Blocks), the id it would have when published, or ""
	// when it cannot be published. Comments name published block ids, so this places them on the draft.
	SourceIDs []string `json:"-"`
}

type blockDiff struct {
	Block  string `json:"block"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // new | unchanged | removed
	Text   string `json:"text,omitempty"`
}

var shaOnly = regexp.MustCompile(`^[0-9a-f]{64}$`)

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// freeze builds the published revision of a report. It reads only reviewer-safe facts: figure previews and their
// provenance (ADR 0011), run summaries (ADR 0014) and report titles and keys. It never reads a path.
func (s *Server) freeze(ws workspace.ID, rep reports.Report, space string, withAssets bool) (frozen, error) {
	f := frozen{Assets: map[string][]byte{}, Warnings: []string{}, Errors: []string{}, Space: space}
	number, err := s.Reports.NextNumber(rep.ID)
	if err != nil {
		return f, err
	}
	shared := map[string]bool{rep.Key: true}
	if keys, err := s.Reports.PublishedKeys(ws, space); err == nil {
		for _, k := range keys {
			shared[k] = true
		}
	}
	owner, _ := s.Reports.Setting("owner_name")
	if owner == "" {
		owner = "the author"
	}
	agent, _ := s.Reports.Setting("agent_name")
	if agent == "" {
		agent = "the agent"
	}
	hist, err := s.Reports.History(ws, rep.ID, false)
	if err != nil {
		return f, err
	}
	byAgent, byOwner := false, false
	for _, h := range hist {
		byAgent = byAgent || h.Author == reports.AuthorAgent
		byOwner = byOwner || h.Author == reports.AuthorOwner
	}
	authors := "By " + owner
	switch {
	case byAgent && byOwner:
		authors = "Drafted by " + agent + ", edited by " + owner
	case byAgent:
		authors = "Drafted by " + agent + ", published by " + owner
	}
	rev := publish.Revision{Report: rep.Key, Number: number, Title: rep.Title, PublishedAt: time.Now().UTC().Truncate(time.Second), Authors: authors,
		Date: reports.ReportDate(rep.Body), Summary: reports.ReportSummary(rep.Body)} // front matter: only these two keys leave the laptop
	unshared := map[string]bool{}
	missing := map[string]bool{}
	figTitle := func(revision string) string {
		if fig, err := s.Boards.FigureByRevision(ws, revision); err == nil {
			return fig.Title
		}
		return ""
	}
	runName := func(id int64) string {
		if s.Runs != nil {
			if run, err := s.Runs.Get(ws, id); err == nil {
				return run.Name
			}
		}
		return ""
	}
	// A picture leaves the laptop without its metadata (EXIF, XMP, comments, text chunks, trailing data: internal/imgmeta),
	// and is published under the hash of what is actually sent, which the publish server checks. The attachment on this
	// computer is not changed. A picture that cannot be cleaned is not published. This covers attachments and figure
	// previews alike.
	cleaned := map[string]cleanedPicture{} // original sha -> what is published, for this revision
	var stripped, kept []string
	note := func(c cleanedPicture) {
		for _, r := range c.removed {
			if !slices.Contains(stripped, r) {
				stripped = append(stripped, r)
			}
		}
		for _, k := range c.kept {
			if !slices.Contains(kept, k) {
				kept = append(kept, k)
			}
		}
	}
	addAttachment := func(sha string) string {
		if !shaOnly.MatchString(sha) {
			return "#"
		}
		c, done := cleaned[sha]
		if !done {
			var err error
			c, err = s.publishablePicture(sha, func() ([]byte, error) { b, _, err := s.Reports.Attachment(ws, sha); return b, err }, withAssets, &f)
			switch {
			case errors.Is(err, errCannotClean):
				f.Errors = append(f.Errors, "An image ("+sha[:12]+"…) could not be cleaned of its metadata, so it is not published; replace it with a fresh export.")
			case err != nil:
				f.Errors = append(f.Errors, "An image ("+sha[:12]+"…) is missing from the attachments; remove it or upload it again.")
			default:
				note(c)
			}
			cleaned[sha] = c
		}
		if c.pub == "" {
			return "#"
		}
		return "asset:" + c.pub
	}
	seen := map[string]int{}
	add := func(b publish.Block, content string) {
		id := publish.ContentID(b.Kind, content)
		if n := seen[id]; n > 0 { // the same paragraph twice: keep ids unique and stable in order
			seen[id] = n + 1
			id = publish.ContentID(b.Kind, content+"\x00"+strconv.Itoa(n))
		} else {
			seen[id] = 1
		}
		b.ID = id
		rev.Blocks = append(rev.Blocks, b)
	}
	for _, b := range reports.Blocks(rep.Body) {
		before := len(rev.Blocks)
		switch b.Kind {
		case "code":
			add(publish.Block{Kind: publish.BlockText, Text: b.Text}, b.Text)
		case "text":
			t := reports.RewriteWikiLinks(b.Text, func(title, label string) string {
				shown := label
				if shown == "" {
					shown = title
				}
				target, err := s.Reports.ByTitle(ws, "", title) // only the title and id are used: no account's seen revision is needed
				if err != nil {
					missing[title] = true
					return shown
				}
				if !shared[target.Key] {
					unshared[target.Title] = true
				}
				return "[[report:" + target.Key + "|" + strings.NewReplacer("]", "", "|", "").Replace(shown) + "]]"
			})
			t = reports.RewriteInline(t, func(revision string) string {
				if title := figTitle(revision); title != "" {
					return "*Figure: " + title + "*"
				}
				f.Errors = append(f.Errors, "A figure mentioned in the text no longer exists.")
				return "*a figure*"
			}, func(id int64) string {
				if name := runName(id); name != "" {
					return "`" + name + "`"
				}
				f.Errors = append(f.Errors, fmt.Sprintf("Run %d mentioned in the text no longer exists.", id))
				return "a run"
			})
			t = reports.RewriteAttachments(t, addAttachment)
			add(publish.Block{Kind: publish.BlockText, Text: t}, t)
		case "figure":
			fig, err := s.Boards.FigureByRevision(ws, b.Figure)
			if err != nil {
				f.Errors = append(f.Errors, "A figure embedded in the report ("+b.Figure[:12]+"…) no longer exists; remove the line or embed another figure.")
				break
			}
			pic, err := s.publishablePicture(fig.PreviewSHA, func() ([]byte, error) { return s.Boards.Preview(fig.PreviewSHA) }, withAssets, &f)
			if errors.Is(err, errCannotClean) {
				f.Errors = append(f.Errors, fmt.Sprintf("The picture of %q could not be cleaned of its metadata, so it is not published.", fig.Title))
				break
			} else if err != nil {
				f.Errors = append(f.Errors, fmt.Sprintf("The picture of %q cannot be read.", fig.Title))
				break
			}
			note(pic)
			add(publish.Block{Kind: publish.BlockFigure, Figure: &publish.Figure{Asset: pic.pub, Width: fig.Width, Height: fig.Height, Title: fig.Title, Caption: fig.Caption, Revision: fig.Revision, Provenance: fig.Provenance}}, b.Figure)
			if fig.Caption == "" {
				f.Warnings = append(f.Warnings, fmt.Sprintf("Figure %q has no caption.", fig.Title))
			}
			var p struct {
				Run *struct {
					Dirty  bool `json:"dirty"`
					Review struct {
						QC string `json:"qc"`
					} `json:"review"`
				} `json:"run"`
			}
			_ = json.Unmarshal(fig.Provenance, &p)
			if p.Run != nil && p.Run.Dirty {
				f.Warnings = append(f.Warnings, fmt.Sprintf("Figure %q comes from a run made from uncommitted code.", fig.Title))
			}
			var pr struct {
				Run *struct {
					Name   string       `json:"name"`
					Params []runs.Param `json:"params"`
				} `json:"run"`
			}
			if json.Unmarshal(fig.Provenance, &pr) == nil && pr.Run != nil {
				if w := identifyingSettings(pr.Run.Name, pr.Run.Params); w != "" {
					f.Warnings = append(f.Warnings, w)
				}
			}
		case "run":
			if s.Runs == nil {
				f.Errors = append(f.Errors, "Runs are not available on this server.")
				break
			}
			sum, err := s.Runs.SummaryOf(ws, b.Run, "")
			if err != nil {
				f.Errors = append(f.Errors, fmt.Sprintf("Run %d referenced in the report no longer exists.", b.Run))
				break
			}
			js, _ := json.Marshal(sum)
			add(publish.Block{Kind: publish.BlockRun, Run: js}, string(js))
			if w := identifyingSettings(sum.Name, sum.Params); w != "" {
				f.Warnings = append(f.Warnings, w)
			}
			if sum.Review.QC == "" || sum.Review.QC == "not_assessed" {
				f.Warnings = append(f.Warnings, fmt.Sprintf("Run %s has not been assessed; readers will see \"not assessed\".", sum.Name))
			}
		}
		if len(rev.Blocks) > before {
			f.SourceIDs = append(f.SourceIDs, rev.Blocks[len(rev.Blocks)-1].ID)
		} else {
			f.SourceIDs = append(f.SourceIDs, "") // not publishable as it is (the error says why)
		}
	}
	for t := range missing {
		f.Warnings = append(f.Warnings, fmt.Sprintf("[[%s]] names no report; readers see it as plain text.", t))
	}
	for t := range unshared {
		f.Warnings = append(f.Warnings, fmt.Sprintf("Links to %q, which is not shared with this space: readers see the link text but cannot open it.", t))
	}
	if len(stripped) > 0 {
		f.Warnings = append(f.Warnings, "Pictures are published without their metadata ("+strings.Join(stripped, ", ")+"). The attachments on this computer are unchanged.")
	}
	if len(kept) > 0 {
		f.Warnings = append(f.Warnings, "A "+strings.Join(kept, " and a ")+" is kept in some pictures because it is needed to draw their colours; it can hold the name of the display or camera it came from.")
	}
	if len(rev.Blocks) == 0 {
		f.Errors = append(f.Errors, "The report is empty.")
	}
	f.Revision = rev
	// What changed since the last publication, by block identity.
	prev := map[string]bool{}
	if last, err := s.Reports.LastPublication(rep.ID); err == nil {
		var old publish.Revision
		if json.Unmarshal(last.Doc, &old) == nil {
			for _, b := range old.Blocks {
				prev[b.ID] = true
			}
			now := map[string]bool{}
			for _, b := range rev.Blocks {
				now[b.ID] = true
			}
			for _, b := range old.Blocks {
				if !now[b.ID] {
					f.Changes = append(f.Changes, blockDiff{Block: b.ID, Kind: b.Kind, Status: "removed", Text: clipRunes(b.Text, 200)})
				}
			}
		}
	}
	for _, b := range rev.Blocks {
		st := "new"
		if prev[b.ID] {
			st = "unchanged"
		}
		f.Changes = append(f.Changes, blockDiff{Block: b.ID, Kind: b.Kind, Status: st, Text: clipRunes(b.Text, 200)})
	}
	return f, nil
}

// errCannotClean is a picture that exists but cannot be published safely: imgmeta refused it or could not prove the cleaned
// file draws the same picture.
var errCannotClean = errors.New("the picture cannot be cleaned of its metadata")

// cleanedPicture is what is published for one picture: the hash of the cleaned bytes, and what was removed and kept.
type cleanedPicture struct {
	pub           string
	removed, kept []string
}

// publishablePicture cleans one picture (an attachment or a figure preview, found by its sha) and, when the bytes are
// needed because the report is being published, adds them to f.Assets under the hash of what will be sent. Cleaning is
// deterministic and a sha names fixed bytes, so the result is remembered: opening a report, which builds the same
// revision to compare with, must not decode every image again. A cache hit never needs the bytes.
func (s *Server) publishablePicture(sha string, load func() ([]byte, error), withBytes bool, f *frozen) (cleanedPicture, error) {
	if v, ok := s.pictures.Load(sha); ok && !withBytes {
		return v.(cleanedPicture), nil
	}
	b, err := load()
	if err != nil {
		return cleanedPicture{}, err
	}
	clean, removed, kept, err := imgmeta.CleanWithNotes(b)
	if err != nil {
		return cleanedPicture{}, errCannotClean
	}
	sum := sha256.Sum256(clean)
	c := cleanedPicture{pub: hex.EncodeToString(sum[:]), removed: removed, kept: kept}
	s.pictures.Store(sha, c)
	if withBytes {
		f.Assets[c.pub] = clean
	}
	return c, nil
}
