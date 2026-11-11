package review

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
)

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// withRequestID gives every reviewer response a correlation id (X-Request-Id, the caller's when it is a plain token), so a
// reviewer who reports a failure can quote it ("Reference: ..."), and logs failures with it. The log line holds the route
// pattern, never the path: the path carries the review token.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		sw := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status >= 400 {
			slog.Info("review request", "id", id, "method", r.Method, "route", r.Pattern, "status", sw.status)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}
