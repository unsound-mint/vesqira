package publish

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type hworld struct {
	world
	h   http.Handler
	key string
}

func newH(t *testing.T, web string) hworld {
	w := newWorld(t)
	key, err := w.s.AddPublisher("laptop")
	if err != nil {
		t.Fatal(err)
	}
	return hworld{w, (&Handler{Store: w.s, WebDist: web}).Handler(), key}
}

func (x hworld) do(method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if body != "" && !strings.HasPrefix(path, "/publish-api/assets") {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, req)
	return rec
}

func TestPublisherRoutesNeedALiveKey(t *testing.T) {
	x := newH(t, "")
	routes := [][2]string{
		{"GET", "/publish-api/ping"}, {"PUT", "/publish-api/assets/" + strings.Repeat("a", 64)}, {"POST", "/publish-api/revisions"},
		{"PUT", "/publish-api/spaces/sup"}, {"GET", "/publish-api/readers"}, {"POST", "/publish-api/spaces/sup/readers"},
		{"POST", "/publish-api/readers/1/revoke"}, {"GET", "/publish-api/comments"}, {"POST", "/publish-api/comments"},
		{"POST", "/publish-api/comments/1/resolve"},
	}
	revoked, _ := x.s.AddPublisher("old")
	_ = x.s.RevokePublisher("old")
	var first string
	for _, rt := range routes {
		for _, auth := range []string{"", revoked} {
			rec := x.do(rt[0], rt[1], auth, "{}")
			if rec.Code != 401 {
				t.Fatalf("%v with %q: %d", rt, auth, rec.Code)
			}
			if first == "" {
				first = rec.Body.String()
			}
			if rec.Body.String() != first {
				t.Fatalf("401 bodies differ: %q vs %q", rec.Body.String(), first)
			}
		}
	}
	if rec := x.do("GET", "/publish-api/ping", x.key, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body)
	}
}

func TestReaderBadTokensIdenticalAndLimited(t *testing.T) {
	x := newH(t, "")
	rd, _ := x.s.CreateReader("sup", "Gone", nil)
	_ = x.s.RevokeReader(rd.ID)
	soon := time.Now().Add(1100 * time.Millisecond)
	ex, _ := x.s.CreateReader("sup", "Short", &soon)
	time.Sleep(1200 * time.Millisecond)
	var bodies []string
	for _, tok := range []string{strings.Repeat("z", 43), rd.Token, ex.Token} {
		for _, p := range []string{"", "/reports/" + keyA, "/assets/" + x.asset} {
			rec := x.do("GET", "/read-api/"+tok+p, "", "")
			if rec.Code != 404 {
				t.Fatalf("%s: %d", p, rec.Code)
			}
			bodies = append(bodies, rec.Body.String())
		}
		if rec := x.do("POST", "/read-api/"+tok+"/comments", "", `{"body":"x"}`); rec.Code != 404 {
			t.Fatalf("comment: %d", rec.Code)
		} else {
			bodies = append(bodies, rec.Body.String())
		}
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("bodies differ: %q vs %q", b, bodies[0])
		}
	}
	// 13 failures so far; the 30th counts as the limit.
	var last int
	for i := 0; i < 20; i++ {
		last = x.do("GET", "/read-api/"+strings.Repeat("q", 43), "", "").Code
	}
	if last != 429 {
		t.Fatalf("after many failures: %d", last)
	}
	// a valid link still works for the same client
	if rec := x.do("GET", "/read-api/"+x.token, "", ""); rec.Code != 200 {
		t.Fatalf("valid link: %d", rec.Code)
	}
}

func TestPublisherFailuresAreLimited(t *testing.T) {
	x := newH(t, "")
	var code int
	for i := 0; i < 30; i++ {
		code = x.do("GET", "/publish-api/ping", "bad-key-bad-key-bad-key", "").Code
	}
	if code != 429 {
		t.Fatalf("code = %d", code)
	}
}

func TestReaderCannotCrossSpaces(t *testing.T) {
	x := newH(t, "")
	if rec := x.do("GET", "/read-api/"+x.token+"/reports/"+keyB, "", ""); rec.Code != 404 {
		t.Fatalf("report: %d", rec.Code)
	}
	if rec := x.do("GET", "/read-api/"+x.token+"/assets/"+x.other, "", ""); rec.Code != 404 {
		t.Fatalf("asset: %d", rec.Code)
	}
	rec := x.do("GET", "/read-api/"+x.token+"/assets/"+x.asset, "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec := x.do("GET", "/read-api/"+x.token+"/reports/"+keyA+"?rev=x", "", ""); rec.Code != 404 {
		t.Fatalf("bad rev: %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	x := newH(t, "")
	for _, p := range []string{"/read-api/" + x.token, "/read-api/bad", "/publish-api/ping", "/nowhere"} {
		h := x.do("GET", p, "", "").Header()
		for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
			if h.Get(k) == "" {
				t.Errorf("%s lacks %s", p, k)
			}
		}
		if h.Get("Cache-Control") != "no-store" {
			t.Errorf("%s cache = %q", p, h.Get("Cache-Control"))
		}
	}
}

func TestStaticFallback(t *testing.T) {
	dist := t.TempDir()
	_ = os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>app</html>"), 0o600)
	x := newH(t, dist)
	for _, p := range []string{"/", "/read/sometoken", "/deep/route"} {
		if rec := x.do("GET", p, "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"/publish-api/nothing", "/read-api", "/api/x", "/review-api/x"} {
		rec := x.do("GET", p, "", "")
		if rec.Code != 404 || strings.Contains(rec.Body.String(), "app") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
}

func TestPublisherWorkflowOverHTTP(t *testing.T) {
	x := newH(t, "")
	b := pngBytes(t, 6, 6, 9)
	req := httptest.NewRequest("PUT", "/publish-api/assets/"+shaOf(b), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+x.key)
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("asset: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("PUT", "/publish-api/assets/"+shaOf(b), x.key, "not the bytes"); rec.Code != 422 {
		t.Fatalf("mismatch: %d", rec.Code)
	}
	body := `{"report":"` + keyB + `","number":2,"title":"B2","publishedAt":"2026-01-02T03:04:05Z","authors":"x","blocks":[{"id":"a","kind":"text","text":"hi"}]}`
	if rec := x.do("POST", "/publish-api/revisions", x.key, body); rec.Code != 200 {
		t.Fatalf("revision: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("POST", "/publish-api/revisions", x.key, body); rec.Code != 200 {
		t.Fatalf("again: %d", rec.Code)
	}
	if rec := x.do("POST", "/publish-api/revisions", x.key, strings.Replace(body, `"hi"`, `"ho"`, 1)); rec.Code != 409 {
		t.Fatalf("conflict: %d", rec.Code)
	}
	if rec := x.do("POST", "/publish-api/revisions", x.key, strings.Replace(body, `"authors"`, `"extra":1,"authors"`, 1)); rec.Code != 400 {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := x.do("PUT", "/publish-api/spaces/sup", x.key, `{"title":"S","reports":["`+keyA+`","`+keyB+`"]}`); rec.Code != 200 {
		t.Fatalf("space: %d", rec.Code)
	}
	if rec := x.do("PUT", "/publish-api/spaces/Bad_Slug", x.key, `{"title":"S"}`); rec.Code != 422 {
		t.Fatalf("slug: %d", rec.Code)
	}
	rec = x.do("POST", "/publish-api/spaces/sup/readers", x.key, `{"name":"Second"}`)
	var rd Reader
	_ = json.Unmarshal(rec.Body.Bytes(), &rd)
	if rec.Code != 201 || rd.Token == "" {
		t.Fatalf("reader: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("GET", "/publish-api/readers", x.key, ""); strings.Contains(rec.Body.String(), rd.Token) {
		t.Fatal("token listed")
	}
	rec = x.do("POST", "/read-api/"+rd.Token+"/comments", "", `{"report":"`+keyA+`","revision":"`+x.revA.ID+`","block":"t1","body":"hello","author":"Mallory"}`)
	var c Comment
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != 201 || c.Author != "Second" {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("POST", "/read-api/"+rd.Token+"/comments", "", `{"report":"`+keyA+`","body":""}`); rec.Code != 422 {
		t.Fatalf("invalid comment: %d", rec.Code)
	}
	if rec := x.do("GET", "/publish-api/comments?after=0", x.key, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"space":"sup"`) {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("POST", "/publish-api/comments/"+itoa(int(c.ID))+"/resolve", x.key, `{"resolved":true}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "resolvedAt") {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body)
	}
	// readers cannot resolve
	if rec := x.do("POST", "/read-api/"+rd.Token+"/comments/"+itoa(int(c.ID))+"/resolve", "", `{"resolved":true}`); rec.Code == 200 {
		t.Fatal("reader resolved")
	}
	if rec := x.do("POST", "/publish-api/readers/"+itoa(int(rd.ID))+"/revoke", x.key, ""); rec.Code != 200 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := x.do("GET", "/read-api/"+rd.Token, "", ""); rec.Code != 404 {
		t.Fatalf("revoked link: %d", rec.Code)
	}
	if rec := x.do("POST", "/publish-api/comments", x.key, `{"space":"sup","report":"`+keyA+`","revision":"`+x.revA.ID+`","block":"t1","body":"owner","author":"Denis"}`); rec.Code != 201 {
		t.Fatalf("owner comment: %d %s", rec.Code, rec.Body)
	}
}

// Reader-facing responses are marshalled and checked for fields that would leak the owner's instance or credentials.
func TestReaderResponsesLeakNoInternals(t *testing.T) {
	w := newWorld(t)
	c, _ := w.s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "f1", Body: "x", Anchor: &Anchor{Kind: "point", X: .1, Y: .1}})
	sv, _ := w.s.Space(w.token)
	rv, _ := w.s.Report(w.token, keyA, 0)
	forbidden := []string{"path", "dir", "host", "token", "key_hash", "keyhash", "datasetid"}
	var walk func(t reflect.Type, seen map[reflect.Type]bool, trail string)
	walk = func(rt reflect.Type, seen map[reflect.Type]bool, trail string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			for _, bad := range forbidden {
				if strings.Contains(strings.ToLower(name), bad) {
					t.Errorf("%s.%s is a reader-facing field named like %q", trail, name, bad)
				}
			}
			walk(f.Type, seen, trail+"."+name)
		}
	}
	for _, v := range []any{sv, rv, c} {
		walk(reflect.TypeOf(v), map[reflect.Type]bool{}, reflect.TypeOf(v).Name())
		raw, _ := json.Marshal(v)
		var generic any
		_ = json.Unmarshal(raw, &generic)
		checkKeys(t, generic, forbidden)
		if strings.Contains(string(raw), w.token) {
			t.Errorf("%T response contains the token", v)
		}
	}
}

func checkKeys(t *testing.T, v any, forbidden []string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			for _, bad := range forbidden {
				if strings.Contains(strings.ToLower(k), bad) {
					t.Errorf("response has key %q", k)
				}
			}
			checkKeys(t, vv, forbidden)
		}
	case []any:
		for _, vv := range x {
			checkKeys(t, vv, forbidden)
		}
	}
}

func TestDeleteAndWithdrawRoutes(t *testing.T) {
	x := newH(t, "")
	for _, rt := range [][2]string{{"DELETE", "/publish-api/comments/1"}, {"GET", "/publish-api/comments/deleted"}, {"DELETE", "/publish-api/reports/" + keyA}} {
		if rec := x.do(rt[0], rt[1], "", ""); rec.Code != 401 {
			t.Fatalf("%v without key: %d", rt, rec.Code)
		}
	}
	body := `{"report":"` + keyA + `","revision":"` + x.revA.ID + `","block":"t1","body":"hi","quote":"  a   phrase "}`
	rec := x.do("POST", "/read-api/"+x.token+"/comments", "", body)
	var c Comment
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &c) != nil || c.Quote != "a phrase" {
		t.Fatalf("quoted comment: %d %s", rec.Code, rec.Body)
	}
	id := strconv.FormatInt(c.ID, 10)
	if rec := x.do("DELETE", "/read-api/"+x.token+"/comments/abc", "", ""); rec.Code != 404 {
		t.Errorf("bad id: %d", rec.Code)
	}
	if rec := x.do("DELETE", "/read-api/short/comments/"+id, "", ""); rec.Code != 404 {
		t.Errorf("bad token: %d", rec.Code)
	}
	if rec := x.do("DELETE", "/read-api/"+x.token+"/comments/"+id, "", ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("reader delete: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("DELETE", "/read-api/"+x.token+"/comments/"+id, "", ""); rec.Code != 404 {
		t.Errorf("again: %d", rec.Code)
	}
	rec = x.do("GET", "/publish-api/comments/deleted?after=0", x.key, "")
	var del struct{ Deletions []Deletion }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &del) != nil || len(del.Deletions) != 1 || del.Deletions[0].ID != c.ID {
		t.Fatalf("deleted feed: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("GET", "/publish-api/comments/deleted?after=-1", x.key, ""); rec.Code != 400 {
		t.Errorf("bad cursor: %d", rec.Code)
	}
	if rec := x.do("GET", "/publish-api/comments?after=0", x.key, ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("plain feed must still work: %d %s", rec.Code, rec.Body)
	}
	rec = x.do("POST", "/read-api/"+x.token+"/comments", "", `{"report":"`+keyA+`","revision":"`+x.revA.ID+`","block":"t1","body":"again"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	rec = x.do("DELETE", "/publish-api/comments/"+strconv.FormatInt(c.ID, 10), x.key, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":[`+strconv.FormatInt(c.ID, 10)+`]`) {
		t.Errorf("owner delete: %d %s", rec.Code, rec.Body)
	}
	if rec := x.do("DELETE", "/publish-api/comments/9999", x.key, ""); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	rec = x.do("POST", "/read-api/"+x.token+"/comments", "", `{"report":"`+keyA+`","revision":"`+x.revA.ID+`","block":"t1","body":"w"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	rec = x.do("DELETE", "/publish-api/reports/"+keyA, x.key, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deletedComments":[`+strconv.FormatInt(c.ID, 10)+`]`) {
		t.Fatalf("withdraw: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/reports/" + keyA, "/reports/" + keyA + "?rev=1", "/assets/" + x.asset} {
		if rec := x.do("GET", "/read-api/"+x.token+p, "", ""); rec.Code != 404 {
			t.Errorf("%s after withdraw: %d", p, rec.Code)
		}
	}
	if rec := x.do("DELETE", "/publish-api/reports/"+keyA, x.key, ""); rec.Code != 404 {
		t.Errorf("withdraw twice: %d", rec.Code)
	}
}
