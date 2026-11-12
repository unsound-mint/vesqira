package review

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func setup(t *testing.T) (*httptest.Server, *boards.Store, string, boards.Figure, string) {
	t.Helper()
	dir := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	st, err := boards.Open(cat.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4)))
	f, err := st.CreateFigure(workspace.Local, "local-owner", boards.NewFigure{DatasetID: 1, Title: "Peak", Caption: "cap", Provenance: json.RawMessage(`{"channel":3}`)}, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	bd, _ := st.CreateBoard(workspace.Local, "local-owner", "B")
	st.AddItem(workspace.Local, bd.ID, f.ID)
	_, tok, err := st.Publish(workspace.Local, "local-owner", bd.ID, "v1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(dir, "dist")
	os.MkdirAll(web, 0o755)
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("root:x:0:0"), 0o600)
	ts := httptest.NewServer((&Handler{Store: st, WebDist: web}).Handler())
	t.Cleanup(ts.Close)
	return ts, st, tok, f, dir
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func TestReviewerSeesTheSnapshotAndItsPreviewsOnly(t *testing.T) {
	ts, _, tok, f, dir := setup(t)
	r, body := get(t, ts.URL+"/review-api/"+tok)
	if r.StatusCode != 200 || !strings.Contains(body, `"title":"Peak"`) || r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, body)
	}
	for _, leak := range []string{dir, "datasetId", "\"path\"", "token_hash"} {
		if strings.Contains(body, leak) {
			t.Errorf("review JSON contains %q: %s", leak, body)
		}
	}
	pr, pb := get(t, ts.URL+"/review-api/"+tok+"/preview/"+f.PreviewSHA)
	if pr.StatusCode != 200 || pr.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(pb, "\x89PNG") {
		t.Fatalf("preview: %d", pr.StatusCode)
	}
	if pr, _ := get(t, ts.URL+"/review-api/"+tok+"/preview/"+strings.Repeat("a", 64)); pr.StatusCode != 404 {
		t.Errorf("a preview outside the snapshot: %d", pr.StatusCode)
	}
}

func TestNothingOfTheOwnerAPIIsReachable(t *testing.T) {
	ts, _, tok, _, dir := setup(t)
	for _, p := range []string{"/api/datasets", "/api/roots", "/api/figures", "/api/boards", "/api/datasets/1/plane?channel=0", "/review-api", "/api/", "/api/datasets/1/spectrum"} {
		r, body := get(t, ts.URL+p)
		if r.StatusCode == 200 && !strings.HasPrefix(body, "<html>") {
			t.Errorf("%s reachable: %d %s", p, r.StatusCode, body)
		}
		if r.StatusCode == 200 {
			t.Errorf("%s answered 200", p)
		}
	}
	// path traversal out of the web root
	for _, p := range []string{"/..%2fsecret.txt", "/../secret.txt", "/%2e%2e/secret.txt", "/..%5csecret.txt"} {
		if _, body := get(t, ts.URL+p); strings.Contains(body, "root:x") {
			t.Errorf("%s escaped the web root", p)
		}
	}
	_ = tok
	_ = dir
	// the app is served for any other path so a reviewer's browser can open a link
	if r, body := get(t, ts.URL+"/anything"); r.StatusCode != 200 || !strings.Contains(body, "app") {
		t.Errorf("fallback: %d %s", r.StatusCode, body)
	}
}

// The editor (ADR 0029) is a route of the owner listener only. The reviewer handler is a different package that does not
// reach the server package at all (internal/arch), and answers nothing under /api, whatever the method.
func TestEditorRoutesAreNotOnTheReviewListener(t *testing.T) {
	ts, _, _, _, dir := setup(t)
	target := filepath.Join(dir, "secret.txt")
	before, _ := os.ReadFile(target)
	for _, method := range []string{"GET", "PUT", "POST", "DELETE"} {
		body := `{"path":` + `"` + target + `","hostId":1,"content":"owned","base":{"size":1,"mtime":"1","sha256":"a"}}`
		req, _ := http.NewRequest(method, ts.URL+"/api/files/text?hostId=1&path="+target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 || strings.Contains(string(b), "root:x") {
			t.Errorf("%s /api/files/text on the review listener: %d %s", method, resp.StatusCode, b)
		}
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(before, after) {
		t.Error("a request to the review listener changed a file")
	}
}

// The terminal (ADR 0029) is also a route of the owner listener only: neither the session request nor the WebSocket
// upgrade is answered on the review listener, whatever the headers.
func TestTerminalRoutesAreNotOnTheReviewListener(t *testing.T) {
	ts, _, _, _, _ := setup(t)
	for _, c := range []struct{ method, path string }{{"POST", "/api/terminal/sessions"}, {"GET", "/api/terminal"}, {"PUT", "/api/terminal"}} {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, strings.NewReader(`{"tab":"tab-aaaaaaaa"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set("Sec-WebSocket-Protocol", "vesqira.terminal.v1")
		req.Header.Set("Origin", ts.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 101 || resp.StatusCode == 200 && strings.Contains(string(b), "token") || resp.Header.Get("Sec-WebSocket-Accept") != "" {
			t.Errorf("%s %s on the review listener: %d %s", c.method, c.path, resp.StatusCode, b)
		}
	}
}

func TestEveryBadTokenIsTheSame404AndFailuresAreLimited(t *testing.T) {
	ts, _, tok, _, _ := setup(t)
	var first string
	for _, bad := range []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "short", tok + "x", strings.ToUpper(tok)} {
		r, body := get(t, ts.URL+"/review-api/"+bad)
		if r.StatusCode != 404 {
			t.Fatalf("%q: %d", bad, r.StatusCode)
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("bad tokens are distinguishable: %q vs %q", body, first)
		}
	}
	var last int
	for i := 0; i < 40; i++ {
		r, _ := get(t, ts.URL+"/review-api/"+strings.Repeat("B", 43))
		last = r.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("brute force not limited: last status %d", last)
	}
}

func TestComments(t *testing.T) {
	ts, st, tok, f, _ := setup(t)
	post := func(ct, body string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/review-api/"+tok+"/comments", strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	good := `{"figureRevision":"` + f.Revision + `","author":"Dr. Reviewer","body":"Please show the unit."}`
	if r := post("application/json", good); r.StatusCode != 201 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r := post("text/plain", good); r.StatusCode != 415 {
		t.Errorf("text/plain: %d", r.StatusCode)
	}
	if r := post("application/json", `{"figureRevision":"nope","body":"x"}`); r.StatusCode != 422 {
		t.Errorf("unknown figure: %d", r.StatusCode)
	}
	if r := post("application/json", `{"figureRevision":"`+f.Revision+`","body":"`+strings.Repeat("x", 20000)+`"}`); r.StatusCode != 400 {
		t.Errorf("oversized body: %d", r.StatusCode)
	}
	rv, err := st.Review(tok)
	if err != nil || len(rv.Comments) != 1 || rv.Comments[0].Author != "Dr. Reviewer" {
		t.Fatalf("%+v %v", rv, err)
	}
}

func TestLimitBlocksOnlyFailedLookupsAndNotTheAppOrAValidLink(t *testing.T) {
	ts, _, tok, _, _ := setup(t)
	for i := 0; i < 40; i++ {
		get(t, ts.URL+"/review-api/"+strings.Repeat("C", 43))
	}
	if r, _ := get(t, ts.URL+"/review-api/"+tok); r.StatusCode != 200 {
		t.Errorf("a valid link must keep working for a client that was limited: %d", r.StatusCode)
	}
	if r, _ := get(t, ts.URL+"/"); r.StatusCode != 200 {
		t.Errorf("the app must keep loading: %d", r.StatusCode)
	}
	if r, _ := get(t, ts.URL+"/review-api/"+strings.Repeat("D", 43)); r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("further guesses must be limited with security headers: %d %v", r.StatusCode, r.Header)
	}
}

func TestDirectoriesAreNotListedAndCharsetJSONIsAccepted(t *testing.T) {
	ts, _, tok, f, dir := setup(t)
	os.MkdirAll(filepath.Join(dir, "dist", "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "dist", "assets", "app.js"), []byte("x"), 0o644)
	if _, body := get(t, ts.URL+"/assets/"); strings.Contains(body, "app.js") {
		t.Errorf("directory listing exposed: %s", body)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/review-api/"+tok+"/comments", strings.NewReader(`{"figureRevision":"`+f.Revision+`","body":"hi"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 201 {
		t.Errorf("charset content type: %v %v", r, err)
	}
}

func TestClientsBehindAProxyAreToldApartWhenConfigured(t *testing.T) {
	h := &Handler{ClientIPHeader: "X-Forwarded-For"}
	mk := func(xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:5555"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	if h.clientKey(mk("evil, 203.0.113.9")) != "203.0.113.9" || h.clientKey(mk("")) != "10.0.0.1" {
		t.Errorf("client keys: %q %q", h.clientKey(mk("evil, 203.0.113.9")), h.clientKey(mk("")))
	}
	v6a, v6b := mk("2001:db8:1:2::1"), mk("2001:db8:1:2:ffff::9")
	if h.clientKey(v6a) != h.clientKey(v6b) {
		t.Error("addresses in one /64 must share an allowance")
	}
}
