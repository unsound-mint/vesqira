package server

import (
	"container/list"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
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
	step    int // preview reduction (0 or 1 = full resolution)
	x, y    int
	q       string // canonical region description for spectra
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

// authorize re-checks a stored dataset against the current source roots and its registered size and mtime.
// A path does not prove the bytes are the ones whose header was catalogued, so a changed file is refused.
func (s *Server) authorize(ctx context.Context, d catalog.Dataset) (real string, k cacheKey, status int, msg string) {
	if d.HostID != 0 {
		return s.authorizeRemote(ctx, d)
	}
	real, err := s.roots().Resolve(d.Path)
	if err != nil {
		return "", k, 403, err.Error()
	}
	info, err := statFile(real)
	if err != nil {
		return "", k, 404, "file not found"
	}
	if info.Size() != d.SizeBytes || info.ModTime().UnixNano() != d.ModifiedAt.UnixNano() {
		return "", k, 409, "the file changed since it was registered; register it again"
	}
	return real, cacheKey{id: d.ID, size: info.Size(), mtime: info.ModTime().UnixNano()}, 0, ""
}

// source resolves a dataset id to an authorized, unchanged file.
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
	real, k, status, msg := s.authorize(r.Context(), d)
	if status != 0 {
		fail(w, status, msg)
		return
	}
	return d, real, k, true
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
	svc := s.planesFor(k.id)
	if svc == nil {
		fail(w, 503, "the scientific worker is not configured")
		return worker.Reply{}, false
	}
	rep, err := svc.Do(r.Context(), req)
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

// headerCards serves the file's own header text, one card per line, for reading in the app or saving as a text file.
// Nothing in it is interpreted; the viewer's readouts are derived elsewhere and labelled as declared or inferred.
func (s *Server) headerCards(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	k.op, k.channel = "cards", -1
	rep, ok := s.ask(w, r, k, map[string]any{"op": "cards", "path": real})
	if !ok {
		return
	}
	if r.URL.Query().Get("format") != "text" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(rep.Head)
		return
	}
	var h struct {
		Cards     []string `json:"cards"`
		Truncated bool     `json:"truncated"`
	}
	if json.Unmarshal(rep.Head, &h) != nil {
		fail(w, 502, "the worker returned an unreadable header")
		return
	}
	name := strings.Map(func(r rune) rune {
		if r < 128 && (r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return r
		}
		return '_'
	}, d.Name)
	body := strings.Join(h.Cards, "\n") + "\n"
	if h.Truncated {
		body += "# truncated: only the first cards are listed\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.header.txt"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(body))
}

// writeFrame sends a binary frame: uint32 little-endian header length, the JSON header, then the payload
// (little-endian numbers, layout stated in the header). The JSON is padded with spaces so the payload starts
// 8-byte aligned and the browser can view it in place.
func writeFrame(w http.ResponseWriter, rep worker.Reply) {
	head := rep.Head
	if pad := (8 - (4+len(head))%8) % 8; pad > 0 {
		head = append(append(make([]byte, 0, len(head)+pad), head...), "       "[:pad]...)
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

// plane answers with a frame whose payload is float32 pixels, row-major, row 0 = FITS y=1.
// The browser never receives more than one plane.
func (s *Server) plane(w http.ResponseWriter, r *http.Request) {
	_, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	ch, ok := index(w, r, "channel")
	if !ok {
		return
	}
	d := catalog.Dataset{}
	if got, err := s.Store.Get(k.id); err == nil {
		d = got
	}
	k.op, k.channel = "plane", ch
	req := map[string]any{"op": "plane", "path": real, "channel": ch}
	if q := r.URL.Query().Get("step"); q != "" { // a reduced display preview; absent means full resolution
		step, err := strconv.Atoi(q)
		if err != nil || step < 1 || step > 64 {
			fail(w, 400, "step must be an integer from 1 to 64")
			return
		}
		if step > 1 && d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation" {
			// block means of angles are wrong (1 and 179 degrees would average to 90); an orientation map is always sent whole
			fail(w, 400, "a reduced preview is not available for an orientation map")
			return
		}
		if step > 1 {
			k.step, req["step"] = step, step
		}
	}
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	writeFrame(w, rep)
}

// integer reads a required integer query parameter that may be negative (regions can overhang the image).
func integer(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < -(1<<20) || v > 1<<20 {
		fail(w, 400, name+" must be an integer")
		return 0, false
	}
	return v, true
}

// spectrum answers with the mean spectrum of a rectangle or circle (0-based pixel coordinates).
func (s *Server) spectrum(w http.ResponseWriter, r *http.Request) {
	d, real, k, ok := s.source(w, r)
	if !ok {
		return
	}
	if d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation" {
		// Enforced here, not only in the interface: the arithmetic mean of axial angles is not a valid statistic.
		fail(w, 409, "this dataset is declared as an axial orientation map; averaging its angles arithmetically is not valid. Use the orientation statistics")
		return
	}
	shape := r.URL.Query().Get("shape")
	var names []string
	switch shape {
	case "rect":
		names = []string{"x0", "y0", "x1", "y1"}
	case "circle":
		names = []string{"cx", "cy", "r"}
	default:
		fail(w, 400, "shape must be rect or circle")
		return
	}
	req := map[string]any{"op": "spectrum", "path": real, "shape": shape}
	q := shape
	for _, n := range names {
		v, ok := integer(w, r, n)
		if !ok {
			return
		}
		req[n] = v
		q += "," + strconv.Itoa(v)
	}
	k.op, k.q = "spectrum", q
	rep, ok := s.ask(w, r, k, req)
	if !ok {
		return
	}
	writeFrame(w, rep)
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
