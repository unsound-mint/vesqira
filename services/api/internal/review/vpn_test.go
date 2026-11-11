package review

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The VPN companion (ADR 0029 step 3) is a route of the owner listener only: running a program as the owner is never
// answered on the review listener, whatever is asked, and a host's commands cannot be set there either.
func TestVPNRoutesAreNotOnTheReviewListener(t *testing.T) {
	ts, _, _, _, _ := setup(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/hosts/1/vpn", `{"action":"connect"}`},
		{"POST", "/api/hosts/1/vpn-config", `{"vpnCommand":["/bin/true"]}`},
		{"POST", "/api/hosts", `{"name":"x","dest":"x","roots":["/d"],"vpnCommand":["/bin/true"]}`},
		{"GET", "/api/hosts", ""},
	} {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 405 || strings.Contains(string(b), "exitCode") {
			t.Errorf("%s %s on the review listener: %d %s", c.method, c.path, resp.StatusCode, b)
		}
	}
}
