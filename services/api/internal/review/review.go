// Package review is the only surface a supervisor can reach. It is built from a *boards.Store and nothing else:
// it has no catalog, no source roots, no worker and no filesystem access beyond the store's preview files, so a
// reviewer token cannot become access to raw data, paths or credentials. Every failure for a bad token is the same
// 404, and failures are rate limited per client address.
package review

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
)

const (
	maxFailures = 30
	maxTracked  = 10000
	window      = time.Minute
)

type Handler struct {
	Store *boards.Store
	// WebDist, if set, is a built web app served at the root so a reviewer's browser can open the review page.
	WebDist string
	// ClientIPHeader, if set (for example "X-Forwarded-For"), names the header a trusted reverse proxy fills with the
	// client address; without it every client behind the proxy shares one address and one failure allowance.
	ClientIPHeader string

	mu       sync.Mutex
	failures map[string][]time.Time
}

// clientKey is the address failures are counted against; IPv6 clients are grouped by /64 so rotating addresses inside
// one allocation does not reset the allowance.
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

// limited reports whether this client has used up its failed-token allowance, recording a failure when asked.
func (h *Handler) limited(r *http.Request, fail bool) bool {
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
	if fail {
		kept = append(kept, now)
	}
	if len(kept) == 0 {
		delete(h.failures, k)
	} else {
		if len(h.failures) >= maxTracked { // bound memory: forget everyone rather than grow without limit
			h.failures = map[string][]time.Time{}
		}
		h.failures[k] = kept
	}
	return len(kept) >= maxFailures
}

func secure(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
}

func notFound(w http.ResponseWriter) {
	secure(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}` + "\n"))
}

func (h *Handler) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /review-api/{token}", h.snapshot)
	mux.HandleFunc("GET /review-api/{token}/preview/{sha}", h.preview)
	mux.HandleFunc("POST /review-api/{token}/comments", h.comment)
	if h.WebDist != "" {
		mux.Handle("GET /", h.static())
	}
	return mux
}

// static serves the built web app. It never serves anything under /api, which does not exist here.
func (h *Handler) static() http.Handler {
	fs := http.FileServer(regularFilesOnly{http.Dir(h.WebDist)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") || strings.HasPrefix(r.URL.Path, "/review-api") {
			notFound(w)
			return
		}
		secure(w)
		w.Header().Set("Cache-Control", "no-cache")
		if st, err := os.Stat(filepath.Join(h.WebDist, filepath.Clean("/"+r.URL.Path))); (err != nil || !st.Mode().IsRegular()) && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(h.WebDist, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func (h *Handler) bad(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, boards.ErrNotFound) {
		// Only failed lookups are limited: a valid link, and the app itself, keep working for everyone behind the
		// same address; guessing a 256-bit token is hopeless either way.
		if h.limited(r, true) {
			secure(w)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return true
		}
		notFound(w)
		return true
	}
	secure(w)
	http.Error(w, "internal error", http.StatusInternalServerError)
	return true
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) {
	rv, err := h.Store.Review(r.PathValue("token"))
	if h.bad(w, r, err) {
		return
	}
	secure(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rv)
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	b, err := h.Store.ReviewPreview(r.PathValue("token"), r.PathValue("sha"))
	if h.bad(w, r, err) {
		return
	}
	secure(w)
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

func (h *Handler) comment(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		secure(w)
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in struct {
		FigureRevision string `json:"figureRevision"`
		Author         string `json:"author"`
		Body           string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		secure(w)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	c, err := h.Store.AddComment(r.PathValue("token"), in.FigureRevision, in.Author, in.Body)
	if errors.Is(err, boards.ErrInvalid) {
		secure(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "the comment needs text and a figure of this snapshot"})
		return
	}
	if h.bad(w, r, err) {
		return
	}
	secure(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(c)
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
		return noListing{f}, nil // index.html is still found inside it; the entries are never listed
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

type noListing struct{ http.File }

func (noListing) Readdir(int) ([]fs.FileInfo, error) { return nil, nil }
