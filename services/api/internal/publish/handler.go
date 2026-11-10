package publish

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxFailures = 30
	maxTracked  = 10000
	window      = time.Minute
)

// Handler serves the reader surface (/read-api, a token in the path) and the publisher surface (/publish-api, a bearer key).
type Handler struct {
	Store *Store
	// WebDist, if set, is a built web app served at the root so a reader's browser can open /read/{token}.
	WebDist string
	// ClientIPHeader, if set (for example "X-Forwarded-For"), names the header a trusted reverse proxy fills with the
	// client address; without it every client behind the proxy shares one address and one failure allowance.
	ClientIPHeader string

	mu       sync.Mutex
	failures map[string][]time.Time
}

// clientKey is the address failures are counted against; IPv6 clients are grouped by /64.
func (h *Handler) clientKey(r *http.Request) string {
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	if h.ClientIPHeader != "" {
		if v := r.Header.Get(h.ClientIPHeader); v != "" {
			parts := strings.Split(v, ",")
			addr = strings.TrimSpace(parts[len(parts)-1]) // the entry the trusted proxy appended
		}
	}
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Is6() && !ip.Is4In6() {
		if pfx, err := ip.Prefix(64); err == nil {
			return pfx.String()
		}
	}
	return addr
}

// failed records a failed token or key and reports whether this client has used up its allowance.
func (h *Handler) failed(r *http.Request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failures == nil {
		h.failures = map[string][]time.Time{}
	}
	k, now := h.clientKey(r), time.Now()
	kept := h.failures[k][:0]
	for _, t := range h.failures[k] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	if len(h.failures) >= maxTracked { // bound memory: forget everyone rather than grow without limit
		h.failures = map[string][]time.Time{}
	}
	h.failures[k] = kept
	return len(kept) >= maxFailures
}

func secure(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *Handler) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /read-api/{token}", h.readSpace)
	mux.HandleFunc("GET /read-api/{token}/reports/{key}", h.readReport)
	mux.HandleFunc("GET /read-api/{token}/assets/{sha}", h.readAsset)
	mux.HandleFunc("POST /read-api/{token}/comments", h.readComment)
	mux.HandleFunc("DELETE /read-api/{token}/comments/{id}", h.readDeleteComment)

	mux.HandleFunc("GET /publish-api/ping", h.pub(h.ping))
	mux.HandleFunc("PUT /publish-api/assets/{sha}", h.pub(h.putAsset))
	mux.HandleFunc("POST /publish-api/revisions", h.pub(h.pushRevision))
	mux.HandleFunc("PUT /publish-api/spaces/{slug}", h.pub(h.putSpace))
	mux.HandleFunc("GET /publish-api/readers", h.pub(h.listReaders))
	mux.HandleFunc("POST /publish-api/spaces/{slug}/readers", h.pub(h.createReader))
	mux.HandleFunc("POST /publish-api/readers/{id}/revoke", h.pub(h.revokeReader))
	mux.HandleFunc("GET /publish-api/comments", h.pub(h.feed))
	mux.HandleFunc("POST /publish-api/comments", h.pub(h.ownerComment))
	mux.HandleFunc("POST /publish-api/comments/{id}/resolve", h.pub(h.resolve))
	mux.HandleFunc("DELETE /publish-api/comments/{id}", h.pub(h.deleteComment))
	mux.HandleFunc("GET /publish-api/comments/deleted", h.pub(h.deletedFeed))
	mux.HandleFunc("DELETE /publish-api/reports/{key}", h.pub(h.withdraw))
	if h.WebDist != "" {
		mux.Handle("GET /", h.static())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		mux.ServeHTTP(w, r)
	})
}

var apiPrefixes = []string{"/api", "/review-api", "/read-api", "/publish-api"}

// static serves the built web app; API prefixes are never served from it.
func (h *Handler) static() http.Handler {
	files := http.FileServer(regularFilesOnly{http.Dir(h.WebDist)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				notFound(w)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		if st, err := os.Stat(filepath.Join(h.WebDist, filepath.Clean("/"+r.URL.Path))); (err != nil || !st.Mode().IsRegular()) && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(h.WebDist, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

// ---- reader routes ----

// bad answers a store error. A failed lookup is the same 404 for every cause and counts against the client.
func (h *Handler) bad(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		if h.failed(r) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return true
		}
		notFound(w)
	default:
		h.storeError(w, err)
	}
	return true
}

// storeError maps the non-lookup errors; publisher routes use it directly.
func (h *Handler) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": strings.TrimPrefix(err.Error(), "invalid: ")})
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": strings.TrimPrefix(err.Error(), "conflict: ")})
	case errors.Is(err, ErrNotFound):
		notFound(w)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) readSpace(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.Space(r.PathValue("token"))
	if h.bad(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) readReport(w http.ResponseWriter, r *http.Request) {
	n := 0
	if s := r.URL.Query().Get("rev"); s != "" {
		var err error
		if n, err = strconv.Atoi(s); err != nil || n < 0 {
			n = -1
		}
	}
	v, err := h.Store.Report(r.PathValue("token"), r.PathValue("key"), n)
	if h.bad(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) readAsset(w http.ResponseWriter, r *http.Request) {
	mt, b, err := h.Store.Asset(r.PathValue("token"), r.PathValue("sha"))
	if h.bad(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", mt)
	_, _ = w.Write(b)
}

type commentBody struct {
	Space    string  `json:"space"`
	Report   string  `json:"report"`
	Revision string  `json:"revision"`
	Block    string  `json:"block"`
	ParentID int64   `json:"parentId"`
	Body     string  `json:"body"`
	Anchor   *Anchor `json:"anchor"`
	Quote    string  `json:"quote"`
	Author   string  `json:"author"`
}

func (b commentBody) input() CommentInput {
	return CommentInput{Report: b.Report, Revision: b.Revision, Block: b.Block, ParentID: b.ParentID, Body: b.Body, Anchor: b.Anchor, Quote: b.Quote, Author: b.Author}
}

// decode reads a JSON body of at most limit bytes; it reports false after answering the client.
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any, strict bool) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil || dec.More() {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *Handler) readComment(w http.ResponseWriter, r *http.Request) {
	var in commentBody
	if !decode(w, r, 16<<10, &in, false) {
		return
	}
	c, err := h.Store.AddComment(r.PathValue("token"), in.input()) // any client-sent author or space is ignored
	if h.bad(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) readDeleteComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		id = -1 // answered like any other unknown comment
	}
	if h.bad(w, r, h.Store.DeleteOwnComment(r.PathValue("token"), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- publisher routes ----

func (h *Handler) pub(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !h.Store.Publisher(strings.TrimSpace(key)) {
			if h.failed(r) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func (h *Handler) putAsset(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAssetBytes+1))
	if err != nil {
		http.Error(w, "asset too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := h.Store.PutAsset(r.PathValue("sha"), b); err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sha": r.PathValue("sha")})
}

func (h *Handler) pushRevision(w http.ResponseWriter, r *http.Request) {
	var rev Revision
	if !decode(w, r, MaxDocBytes+(64<<10), &rev, true) {
		return
	}
	out, err := h.Store.PushRevision(rev)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) putSpace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string   `json:"title"`
		Reports []string `json:"reports"`
	}
	if !decode(w, r, 256<<10, &in, true) {
		return
	}
	sp := Space{Slug: r.PathValue("slug"), Title: in.Title, Reports: in.Reports}
	if err := h.Store.PutSpace(sp); err != nil {
		h.storeError(w, err)
		return
	}
	if sp.Reports == nil {
		sp.Reports = []string{}
	}
	writeJSON(w, http.StatusOK, sp)
}

func (h *Handler) listReaders(w http.ResponseWriter, r *http.Request) {
	rs, err := h.Store.Readers()
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (h *Handler) createReader(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !decode(w, r, 8<<10, &in, true) {
		return
	}
	rd, err := h.Store.CreateReader(r.PathValue("slug"), in.Name, in.ExpiresAt)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rd)
}

func (h *Handler) revokeReader(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	if err := h.Store.RevokeReader(id); err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if s := r.URL.Query().Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			http.Error(w, "after must be a comment id", http.StatusBadRequest)
			return
		}
	}
	cs, err := h.Store.CommentsAfter(after)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (h *Handler) ownerComment(w http.ResponseWriter, r *http.Request) {
	var in commentBody
	if !decode(w, r, 16<<10, &in, false) {
		return
	}
	c, err := h.Store.AddOwnerComment(in.Space, in.input())
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if !decode(w, r, 1<<10, &in, true) {
		return
	}
	c, err := h.Store.ResolveComment(id, in.Resolved)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	ids, err := h.Store.DeleteComment(id)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]int64{"deleted": ids})
}

func (h *Handler) deletedFeed(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if s := r.URL.Query().Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			http.Error(w, "after must be a deletion sequence number", http.StatusBadRequest)
			return
		}
	}
	ds, err := h.Store.DeletedAfter(after)
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Deletion{"deletions": ds})
}

func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	ids, err := h.Store.WithdrawReport(r.PathValue("key"))
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]int64{"deletedComments": ids})
}

// regularFilesOnly stops http.FileServer listing directories and following anything but regular files.
type regularFilesOnly struct{ fs http.FileSystem }

func (r regularFilesOnly) Open(name string) (http.File, error) {
	f, err := r.fs.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, os.ErrNotExist
	}
	if st.IsDir() {
		return noListing{f}, nil
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

type noListing struct{ http.File }

func (noListing) Readdir(int) ([]fs.FileInfo, error) { return nil, nil }
