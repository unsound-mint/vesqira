package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type ctxKey int

const requestIDKey ctxKey = 1

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID is the correlation id of the request being served, or "" outside one. A support report that quotes it
// (the X-Request-Id response header) can be matched to the one log line, without any request content being stored.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// The id also travels to the scientific worker (worker.WithRequestID) so its log lines for this request can be matched.

// requestLogger writes one JSON line per request to stderr. Level is info for failures and slow requests and debug for the
// rest, so by default the log is quiet; VESQIRA_LOG=debug shows everything.
// apiLogTail keeps the last log lines for the support bundle (support.go).
var apiLogTail = worker.NewTail(300)

var requestLogger = slog.New(slog.NewJSONHandler(apiLogTail.Writer(os.Stderr), &slog.HandlerOptions{Level: logLevel()}))

func logLevel() slog.Level {
	if strings.EqualFold(os.Getenv("VESQIRA_LOG"), "debug") {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Hijack lets the terminal WebSocket (terminal.go) take over the connection; without it the upgrade would fail.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not support hijacking")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests gives every API request a correlation id (the caller's X-Request-Id when it is a plain token, otherwise a new
// one), returns it in the response, and logs method, path, status, size and duration. The query string is never logged: it can
// carry file names and regions. Static files are not logged.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		id := r.Header.Get("X-Request-Id")
		if !validRequestID.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(worker.WithRequestID(context.WithValue(r.Context(), requestIDKey, id), id)))
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		d := time.Since(start)
		level := slog.LevelDebug
		if sw.status >= 400 || d > 500*time.Millisecond {
			level = slog.LevelInfo
		}
		requestLogger.Log(r.Context(), level, "request", "id", id, "method", r.Method, "path", r.URL.Path, "status", sw.status, "bytes", sw.bytes, "ms", d.Milliseconds())
	})
}
