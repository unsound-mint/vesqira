package review

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestEveryReviewerResponseCarriesARequestID(t *testing.T) {
	ts, _, tok, _, _ := setup(t)
	plain := regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)
	for _, path := range []string{"/review-api/" + tok, "/review-api/not-a-token", "/review-api/" + tok + "/preview/zz", "/"} {
		resp, _ := get(t, ts.URL+path)
		if id := resp.Header.Get("X-Request-Id"); !plain.MatchString(id) {
			t.Errorf("%s: id %q", path, id)
		}
	}
	// a caller's plain id is kept, anything else is replaced
	for in, kept := range map[string]bool{"caller-id-12345": true, "short": false, "bad id with spaces": false, strings.Repeat("a", 65): false} {
		req, _ := http.NewRequest("GET", ts.URL+"/review-api/not-a-token", nil)
		req.Header.Set("X-Request-Id", in)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Request-Id"); (got == in) != kept || !plain.MatchString(got) {
			t.Errorf("sent %q, got %q", in, got)
		}
	}
}
