package server

import (
	"log"
	"net/http"
	"strconv"
)

// The owner's notifications. They are served only on the owner's loopback listener: the reviewer surface has its own
// handler (internal/review) with its own routes, so a reviewer cannot read them, and the owner's routes are refused for any
// non-loopback Host by localOnly. The web app polls the list (about every 30 s) rather than holding a stream open: events
// are rare, the watcher's own interval is a minute or more, and a poll needs no connection slot, no shutdown hook and works
// the same in a browser tab and in the desktop window.

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ns, unread, err := s.Notes.List(wsOf(r), actorID(r.Context()), limit)
	if err != nil {
		log.Printf("notifications: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": ns, "unread": unread})
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	if len(in.IDs) > 500 {
		fail(w, 422, "at most 500 notifications at a time")
		return
	}
	if err := s.Notes.MarkRead(wsOf(r), actorID(r.Context()), in.IDs, in.All); err != nil {
		log.Printf("notifications: %v", err)
		fail(w, 500, "internal error")
		return
	}
	ns, unread, err := s.Notes.List(wsOf(r), actorID(r.Context()), 0)
	if err != nil {
		log.Printf("notifications: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": ns, "unread": unread})
}
