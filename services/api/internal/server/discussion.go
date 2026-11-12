package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/discussion"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Comments by members on a figure or a report of their workspace (ADR 0050 section 3). The route has already put the actor in
// the target's workspace (or answered 404); the author is the account, never a typed name. In local mode the author is the
// local owner, and the routes work the same.

type memberComment struct {
	discussion.Comment
	AuthorName string `json:"authorName"`
	Mine       bool   `json:"mine"`
}

func (s *Server) discussionRoutes(mux *http.ServeMux) {
	if s.Discussion == nil {
		return
	}
	s.route(mux, "GET /api/figures/{id}/discussion", authz.FigureRead, s.listDiscussion("figure"))
	s.route(mux, "POST /api/figures/{id}/discussion", authz.CommentWrite, s.addDiscussion("figure"))
	s.route(mux, "GET /api/reports/{id}/discussion", authz.ReportRead, s.listDiscussion("report"))
	s.route(mux, "POST /api/reports/{id}/discussion", authz.CommentWrite, s.addDiscussion("report"))
	s.route(mux, "POST /api/discussion/{id}/delete", authz.CommentWrite, s.deleteDiscussion)
}

// authorName is the display name of an account id: the local owner in local mode, the account's name on the group server, or
// "former member" for an account that no longer exists.
func (s *Server) authorName(id string) string {
	if id == authz.LocalOwnerID {
		return authz.LocalOwner().Name
	}
	if s.MultiUser.Accounts != nil {
		if a, err := s.MultiUser.Accounts.Get(id); err == nil {
			return a.Name
		}
	}
	return "former member"
}

// discussionTarget checks that the figure or report of the path exists in the actor's workspace (the route already resolved
// it on the group server; in local mode this is the only check).
func (s *Server) discussionTarget(w http.ResponseWriter, r *http.Request, kind string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "not found")
		return 0, false
	}
	if !s.targetIn(wsOf(r), kind, id) {
		fail(w, 404, "not found")
		return 0, false
	}
	return id, true
}

func (s *Server) targetIn(ws workspace.ID, kind string, id int64) bool {
	var n int
	q := `SELECT COUNT(*) FROM figures WHERE id=? AND workspace_id=?`
	if kind == "report" {
		q = `SELECT COUNT(*) FROM reports WHERE id=? AND workspace_id=?`
	}
	return s.Store.DB().QueryRow(q, id, ws).Scan(&n) == nil && n > 0
}

func (s *Server) listDiscussion(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.discussionTarget(w, r, kind)
		if !ok {
			return
		}
		cs, err := s.Discussion.List(wsOf(r), kind, id)
		if err != nil {
			log.Printf("discussion: %v", err)
			fail(w, 500, "internal error")
			return
		}
		me := actorID(r.Context())
		out := []memberComment{}
		for _, c := range cs {
			out = append(out, memberComment{Comment: c, AuthorName: s.authorName(c.Author), Mine: c.Author == me})
		}
		writeJSON(w, 200, map[string]any{"comments": out})
	}
}

func (s *Server) addDiscussion(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Body string }
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, "invalid request body")
			return
		}
		id, ok := s.discussionTarget(w, r, kind)
		if !ok {
			return
		}
		me := actorID(r.Context())
		c, err := s.Discussion.Add(wsOf(r), kind, id, me, in.Body)
		if errors.Is(err, discussion.ErrBadBody) {
			fail(w, 422, err.Error())
			return
		} else if err != nil {
			log.Printf("discussion: %v", err)
			fail(w, 500, "internal error")
			return
		}
		writeJSON(w, 201, memberComment{Comment: c, AuthorName: s.authorName(me), Mine: true})
	}
}

func (s *Server) deleteDiscussion(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	c, err := s.Discussion.Get(wsOf(r), id)
	if errors.Is(err, discussion.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		log.Printf("discussion: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if a, _ := authz.From(r.Context()); !a.Owns(c.Author) {
		fail(w, 403, "only its author, a Maintainer or an Owner can delete a comment")
		return
	}
	if err := s.Discussion.Delete(wsOf(r), id); err != nil {
		fail(w, 404, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
