package review

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Parameter sweeps (their scripts, approval and grid) are the owner's. A reviewer's handler has no sweep route, and the app
// it serves cannot reach one by falling through to the static files.
func TestReviewListenerHasNoSweepRoutes(t *testing.T) {
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644)
	srv := httptest.NewServer((&Handler{WebDist: dist}).Handler())
	defer srv.Close()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/sweeps"}, {"POST", "/api/sweeps"}, {"POST", "/api/sweeps/preview"}, {"GET", "/api/sweeps/1"}, {"GET", "/api/sweeps/1/review"},
		{"POST", "/api/sweeps/1/submit"}, {"POST", "/api/sweeps/1/cancel"}, {"GET", "/api/sweeps/1/grid"}, {"POST", "/api/sweeps/1/board"},
		{"GET", "/review-api/sweeps"},
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
