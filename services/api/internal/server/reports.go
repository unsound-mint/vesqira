package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// Reports (ADR 0030). The owner and an agent (through `vesqira mcp`, which calls these routes on loopback) write
// reports; only the owner's own requests publish, send replies or resolve threads. The agent's MCP tools do not
// include those routes, and every write records its author.

func (s *Server) reportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reports", s.listReports)
	mux.HandleFunc("POST /api/reports", s.createReport)
	mux.HandleFunc("GET /api/reports/graph", s.reportGraph)
	mux.HandleFunc("GET /api/reports/{id}", s.getReport)
	mux.HandleFunc("GET /api/reports/{id}/changes", s.reportChanges)
	mux.HandleFunc("POST /api/reports/{id}", s.updateReport)
	mux.HandleFunc("POST /api/reports/{id}/seen", s.reportSeen)
	mux.HandleFunc("POST /api/reports/{id}/delete", s.deleteReport)
	mux.HandleFunc("GET /api/reports/{id}/history", s.reportHistory)
	mux.HandleFunc("GET /api/reports/{id}/history/{rev}", s.reportRevision)
	mux.HandleFunc("GET /api/reports/{id}/preflight", s.reportPreflight)
	mux.HandleFunc("POST /api/reports/{id}/publish", s.publishReport)
	mux.HandleFunc("POST /api/attachments", s.addAttachment)
	mux.HandleFunc("GET /api/attachments/{sha}", s.getAttachment)
	mux.HandleFunc("GET /api/publish", s.publishStatus)
	mux.HandleFunc("POST /api/publish/settings", s.publishSettings)
	mux.HandleFunc("POST /api/publish/sync", s.publishSyncNow)
	mux.HandleFunc("GET /api/publish/readers", s.listReaders)
	mux.HandleFunc("POST /api/publish/readers", s.createReader)
	mux.HandleFunc("POST /api/publish/readers/{id}/revoke", s.revokeReader)
	mux.HandleFunc("GET /api/report-comments", s.reportComments)
	mux.HandleFunc("POST /api/report-comments/{id}/reply", s.replyReportComment)
	mux.HandleFunc("POST /api/report-comments/{id}/resolve", s.resolveReportComment)
	mux.HandleFunc("POST /api/report-comments/{id}/draft", s.draftReportReply)
	mux.HandleFunc("POST /api/reply-drafts/{id}/send", s.sendReplyDraft)
	mux.HandleFunc("POST /api/reply-drafts/{id}/discard", s.discardReplyDraft)
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
	list, err := s.Reports.List()
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
	}
	if !body(w, r, reports.MaxBody+(16<<10), &in, true) {
		return
	}
	rep, err := s.Reports.Create(in.Title, in.Body, authorOf(in.Author))
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if rep.LastAuthor == reports.AuthorAgent {
		s.audit("report drafted by the agent", "report", rep.ID, fmt.Sprintf("%q created", rep.Title))
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
	rep, err := s.Reports.Get(id)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	v := reportView{Report: rep}
	if v.Backlinks, err = s.Reports.Backlinks(id); err == nil {
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
		if _, err := s.Reports.ByTitle(t); errors.Is(err, reports.ErrNotFound) {
			v.Unresolved = append(v.Unresolved, t)
		}
	}
	space, _ := s.spaceOf(r)
	if f, err := s.freeze(rep, space, false); err == nil {
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
	rep, err := s.Reports.Get(id)
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
		h, err := s.Reports.Revision(id, since)
		if err != nil {
			s.reportErr(w, err)
			return
		}
		before = h.Body
	}
	hist, err := s.Reports.History(id, false)
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
	list, err := s.Reports.List()
	if err != nil {
		s.reportErr(w, err)
		return
	}
	edges, err := s.Reports.Edges()
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
				if f, err := s.Boards.FigureByRevision(e.Target); err == nil {
					n.Label, n.Missing = f.Title, false
				}
				nodes, seen[to] = append(nodes, n), true
			}
		case "run":
			to = "run:" + e.Target
			if !seen[to] {
				n := node{ID: to, Kind: "run", Label: "Missing run", Missing: true}
				if rid, err := strconv.ParseInt(e.Target, 10, 64); err == nil && s.Runs != nil {
					if run, err := s.Runs.Get(rid); err == nil {
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
	rep, err := s.Reports.Update(id, reports.Update{Title: in.Title, Body: in.Body, Base: in.Base, Author: authorOf(in.Author)})
	if errors.Is(err, reports.ErrConflict) && rep.ID != 0 {
		writeJSON(w, 409, map[string]any{"error": err.Error(), "current": rep})
		return
	}
	if err != nil {
		s.reportErr(w, err)
		return
	}
	if in.Author == reports.AuthorAgent {
		s.audit("report drafted by the agent", "report", rep.ID, fmt.Sprintf("%q saved as revision %d", rep.Title, rep.Revision))
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
	if err := s.Reports.MarkSeen(id, in.Revision); err != nil {
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
	if err := s.Reports.Delete(id); err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) reportHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	h, err := s.Reports.History(id, false)
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
	h, err := s.Reports.Revision(id, int(rev))
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
	sha, mime, err := s.Reports.AddAttachment(b)
	if err != nil {
		s.reportErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"sha": sha, "mime": mime, "markdown": "![](attachment:" + sha + ")"})
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	b, mime, err := s.Reports.Attachment(r.PathValue("sha"))
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
func (s *Server) freeze(rep reports.Report, space string, withAssets bool) (frozen, error) {
	f := frozen{Assets: map[string][]byte{}, Warnings: []string{}, Errors: []string{}, Space: space}
	number, err := s.Reports.NextNumber(rep.ID)
	if err != nil {
		return f, err
	}
	shared := map[string]bool{rep.Key: true}
	if keys, err := s.Reports.PublishedKeys(space); err == nil {
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
	hist, err := s.Reports.History(rep.ID, false)
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
	rev := publish.Revision{Report: rep.Key, Number: number, Title: rep.Title, PublishedAt: time.Now().UTC().Truncate(time.Second), Authors: authors}
	unshared := map[string]bool{}
	missing := map[string]bool{}
	figTitle := func(revision string) string {
		if fig, err := s.Boards.FigureByRevision(revision); err == nil {
			return fig.Title
		}
		return ""
	}
	runName := func(id int64) string {
		if s.Runs != nil {
			if run, err := s.Runs.Get(id); err == nil {
				return run.Name
			}
		}
		return ""
	}
	addAttachment := func(sha string) string {
		if !shaOnly.MatchString(sha) {
			return "#"
		}
		if _, ok := f.Assets[sha]; !ok {
			b, _, err := s.Reports.Attachment(sha)
			if err != nil {
				f.Errors = append(f.Errors, "An image ("+sha[:12]+"…) is missing from the attachments; remove it or upload it again.")
				return "#"
			}
			f.Assets[sha] = b
		}
		return "asset:" + sha
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
				target, err := s.Reports.ByTitle(title)
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
			fig, err := s.Boards.FigureByRevision(b.Figure)
			if err != nil {
				f.Errors = append(f.Errors, "A figure embedded in the report ("+b.Figure[:12]+"…) no longer exists; remove the line or embed another figure.")
				break
			}
			if withAssets {
				png, err := s.Boards.Preview(fig.PreviewSHA)
				if err != nil {
					f.Errors = append(f.Errors, fmt.Sprintf("The picture of %q cannot be read.", fig.Title))
					break
				}
				f.Assets[fig.PreviewSHA] = png
			}
			add(publish.Block{Kind: publish.BlockFigure, Figure: &publish.Figure{Asset: fig.PreviewSHA, Width: fig.Width, Height: fig.Height, Title: fig.Title, Caption: fig.Caption, Revision: fig.Revision, Provenance: fig.Provenance}}, b.Figure)
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
			sum, err := s.Runs.SummaryOf(b.Run, "")
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
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err != nil {
		s.lastSyncErr = err.Error()
		return err
	}
	s.lastSync, s.lastSyncErr = time.Now().UTC(), ""
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
