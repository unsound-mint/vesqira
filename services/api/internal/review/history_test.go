package review

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The exploration history holds the owner's dataset ids and what was looked at (ADR 0057). A reviewer's handler has no route to it.
func TestReviewListenerHasNoExplorationHistory(t *testing.T) {
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644)
	srv := httptest.NewServer((&Handler{WebDist: dist}).Handler())
	defer srv.Close()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/history"}, {"GET", "/api/history?project=0"}, {"GET", "/api/history/1"}, {"POST", "/api/history"}, {"GET", "/review-api/history"},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 405 {
			t.Errorf("%s %s: %d, want 404", c.method, c.path, resp.StatusCode)
		}
	}
}
