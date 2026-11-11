package review

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The owner's notifications (job ends, imports) and transfers live on the owner's listener only. A reviewer's handler has no such route,
// and the app it serves cannot reach one by falling through to the static files.
func TestReviewListenerHasNoNotifications(t *testing.T) {
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644)
	srv := httptest.NewServer((&Handler{WebDist: dist}).Handler())
	defer srv.Close()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/notifications"}, {"POST", "/api/notifications/read"}, {"GET", "/api/jobs"}, {"GET", "/review-api/notifications"},
		{"GET", "/api/transfers"}, {"POST", "/api/transfers"}, {"POST", "/api/transfers/1/cancel"}, {"POST", "/api/transfers/1/retry"}, {"POST", "/api/transfers/1/resolve"}, {"POST", "/api/transfers/1/remove"}, {"POST", "/api/transfers/1/register"},
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
