package server

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// PlaneService is the persistent scientific worker as the API sees it.
type PlaneService interface {
	Do(ctx context.Context, req map[string]any) (worker.Reply, error)
}

// cacheKey includes the file signature, so a file that changed on disk can never serve stale pixels.
type cacheKey struct {
	id      int64
	size    int64
	mtime   int64
	op      string
	channel int
	x, y    int
}

// PlaneCache is a byte-bounded LRU of worker replies. Misses cost a worker read; hits cost nothing.
type PlaneCache struct {
	mu    sync.Mutex
	max   int
	used  int
	order *list.List
	items map[cacheKey]*list.Element
}

type cacheItem struct {
	key   cacheKey
	reply worker.Reply
}

func NewPlaneCache(maxBytes int) *PlaneCache {
	return &PlaneCache{max: maxBytes, order: list.New(), items: map[cacheKey]*list.Element{}}
}

func size(r worker.Reply) int { return len(r.Head) + len(r.Payload) + 64 }

func (c *PlaneCache) get(k cacheKey) (worker.Reply, bool) {
	if c == nil {
		return worker.Reply{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*cacheItem).reply, true
	}
	return worker.Reply{}, false
}

func (c *PlaneCache) put(k cacheKey, r worker.Reply) {
	if c == nil || size(r) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.used -= size(e.Value.(*cacheItem).reply)
		c.order.Remove(e)
	}
	c.items[k] = c.order.PushFront(&cacheItem{k, r})
	c.used += size(r)
	for c.used > c.max {
		last := c.order.Back()
		it := last.Value.(*cacheItem)
		c.used -= size(it.reply)
		delete(c.items, it.key)
		c.order.Remove(last)
	}
}

// source resolves a dataset id to an authorized, unchanged file. Every request re-authorizes the stored
// path against the current roots and compares size and mtime with the registration, because a path does
// not prove the bytes are the ones whose header was catalogued.
func (s *Server) source(w http.ResponseWriter, r *http.Request) (d catalog.Dataset, real string, k cacheKey, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	d, err = s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return
	}
	if err != nil {
		log.Printf("source: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if real, err = s.Roots.Resolve(d.Path); err != nil {
		fail(w, 403, err.Error())
		return
	}
	info, err := statFile(real)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	if info.Size() != d.SizeBytes || info.ModTime().UnixNano() != d.ModifiedAt.UnixNano() {
		fail(w, 409, "the file changed since it was registered; register it again")
		return
	}
	return d, real, cacheKey{id: d.ID, size: info.Size(), mtime: info.ModTime().UnixNano()}, true
}

// index reads a required non-negative integer query parameter.
func index(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < 0 || v > 1<<30 {
		fail(w, 400, name+" must be a non-negative integer")
		return 0, false
	}
	return v, true
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request, k cacheKey, req map[string]any) (worker.Reply, bool) {
	if rep, hit := s.Cache.get(k); hit {
		return rep, true
	}
	if s.Planes == nil {
		fail(w, 503, "the scientific worker is not configured")
		return worker.Reply{}, false
	}
	rep, err := s.Planes.Do(r.Context(), req)
	var re *worker.RequestError
	switch {
	case err == nil:
		s.Cache.put(k, rep)
		return rep, true
	case errors.As(err, &re):
		code := 422
		if re.Code == "bad_request" {
			code = 400
		}
		fail(w, code, re.Message)
	case errors.Is(err, context.Canceled):
		// client went away (for example a scrub moved on); nothing to answer
	case errors.Is(err, worker.ErrTimeout):
		fail(w, 504, "the scientific worker timed out")
	default:
		log.Printf("worker: %v", err)
		fail(w, 502, "the scientific worker is unavailable")
	}
	return worker.Reply{}, false
}

func (s *Server) view(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	k.op, k.channel = "view", -1
	rep, ok := s.ask(w, r, k, map[string]any{"op": "view", "path": real})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}

// plane answers with a binary frame: uint32 little-endian header length, the JSON header, then the pixels
// (float32 little-endian, row-major, row 0 is FITS y=1). The browser never receives more than one plane.
func (s *Server) plane(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	ch, ok := index(w, r, "channel")
	if !ok {
		return
	}
	k.op, k.channel = "plane", ch
	rep, ok := s.ask(w, r, k, map[string]any{"op": "plane", "path": real, "channel": ch})
	if !ok {
		return
	}
	// Pad the JSON with spaces so the pixels start 4-byte aligned and the browser can view them in place.
	head := rep.Head
	if pad := (4 - (4+len(head))%4) % 4; pad > 0 {
		head = append(append(make([]byte, 0, len(head)+pad), head...), "   "[:pad]...)
	}
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(head)))
	w.Header().Set("Content-Type", "application/vnd.vesqira.plane")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(n[:])
	_, _ = w.Write(head)
	_, _ = w.Write(rep.Payload)
}

func (s *Server) coord(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	var v [3]int
	for i, n := range []string{"x", "y", "channel"} {
		if v[i], ok = index(w, r, n); !ok {
			return
		}
	}
	k.op, k.x, k.y, k.channel = "coord", v[0], v[1], v[2]
	rep, ok := s.ask(w, r, k, map[string]any{"op": "coord", "path": real, "x": v[0], "y": v[1], "channel": v[2]})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rep.Head)
}
