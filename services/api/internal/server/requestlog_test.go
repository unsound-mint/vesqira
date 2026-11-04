package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := requestLogger
	requestLogger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { requestLogger = old })
	return &buf
}

func TestEveryAPIRequestGetsACorrelationIdAndAFailureIsLoggedWithoutItsQuery(t *testing.T) {
	buf := capture(t)
	var seen string
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		if r.URL.Path == "/api/boom" {
			http.Error(w, "x", 500)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	do := func(path, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if header != "" {
			req.Header.Set("X-Request-Id", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := do("/api/boom?path=/home/someone/secret.fits", "")
	id := w.Header().Get("X-Request-Id")
	if len(id) != 16 || seen != id {
		t.Fatalf("a request gets a generated id that handlers can read: %q %q", id, seen)
	}
	line := buf.String()
	if !strings.Contains(line, `"id":"`+id+`"`) || !strings.Contains(line, `"status":500`) || !strings.Contains(line, `"path":"/api/boom"`) {
		t.Errorf("log line: %s", line)
	}
	if strings.Contains(line, "secret") || strings.Contains(line, "someone") {
		t.Errorf("the query string must never be logged: %s", line)
	}
	if w2 := do("/api/x", "client-id-12345"); w2.Header().Get("X-Request-Id") != "client-id-12345" {
		t.Errorf("a plain caller id is kept: %q", w2.Header().Get("X-Request-Id"))
	}
	for _, bad := range []string{"short", "has space in it 1234", "x\x00yz-0123456", strings.Repeat("a", 65)} {
		if got := do("/api/x", bad).Header().Get("X-Request-Id"); got == bad || len(got) != 16 {
			t.Errorf("an unusable id %q must be replaced, got %q", bad, got)
		}
	}
	buf.Reset()
	if w3 := do("/index.html", ""); w3.Header().Get("X-Request-Id") != "" || buf.Len() != 0 {
		t.Error("static files are neither tagged nor logged")
	}
}
