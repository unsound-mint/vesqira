package server

import (
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The group server (--multi-user, ADR 0050 slice 2): sign-in, sessions, CSRF, tokens and the routes it does not mount.

const (
	groupOrigin = "https://lab.example.edu"
	groupHost   = "lab.example.edu"
	adminPass   = "a long passphrase here"
)

type groupClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *groupClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *groupClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type group struct {
	t     *testing.T
	s     *Server
	h     http.Handler
	acc   *accounts.Store
	audit *audit.Store
	clock *groupClock
	cat   *catalog.Store
}

func newGroup(t *testing.T) *group {
	t.Helper()
	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	acc, err := accounts.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	clock := &groupClock{t: time.Unix(1_800_000_000, 0)}
	acc.Now, acc.Params = clock.now, accounts.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}
	for _, sc := range authz.Scopes() {
		acc.KnownScopes = append(acc.KnownScopes, string(sc))
	}
	acc.SetKey(make([]byte, 32))
	au, err := audit.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au.Default = audit.System
	web := filepath.Join(dir, "web")
	_ = os.MkdirAll(web, 0o755)
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>app"), 0o644)
	s := &Server{Store: cat, Audit: au, WebDist: web, MultiUser: MultiUser{PublicOrigin: groupOrigin, Accounts: acc, Limiter: &accounts.Limiter{Now: clock.now}}}
	return &group{t: t, s: s, h: s.Handler(), acc: acc, audit: au, clock: clock, cat: cat}
}

type req struct {
	method, path, body string
	cookie             string            // session id; "" sends none
	hdr                map[string]string // overrides: a value of "-" removes the header
}

// do sends a request the way the app's own page does (same origin, JSON), unless hdr says otherwise.
func (g *group) do(r req) *httptest.ResponseRecorder {
	g.t.Helper()
	if r.method == "" {
		r.method = "GET"
	}
	q := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	q.Host = groupHost
	q.RemoteAddr = "203.0.113.5:4000"
	q.Header.Set("Sec-Fetch-Site", "same-origin")
	if r.method != "GET" && r.method != "HEAD" {
		q.Header.Set("Origin", groupOrigin)
		q.Header.Set("Content-Type", "application/json")
	}
	if r.cookie != "" {
		q.AddCookie(&http.Cookie{Name: SessionCookie, Value: r.cookie})
	}
	for k, v := range r.hdr {
		if k == "Host" {
			q.Host = v
			continue
		}
		if v == "-" {
			q.Header.Del(k)
		} else {
			q.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, q)
	return w
}

// sessionFrom returns the session id the response set, failing the test if a cookie was set without the required attributes.
func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	checkCookies(t, w)
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge >= 0 {
			return c.Value
		}
	}
	return ""
}

// checkCookies fails for any Set-Cookie that is not the session cookie with every flag: the __Host- prefix, Secure, HttpOnly,
// SameSite=Lax, Path=/ and no Domain.
func checkCookies(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, raw := range w.Result().Header.Values("Set-Cookie") {
		low := strings.ToLower(raw)
		ok := strings.HasPrefix(raw, SessionCookie+"=") && strings.Contains(low, "; secure") && strings.Contains(low, "; httponly") &&
			strings.Contains(low, "; samesite=lax") && strings.Contains(low, "; path=/") && !strings.Contains(low, "domain=")
		if !ok {
			t.Errorf("a cookie without the required attributes was set: %s", raw)
		}
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return v
}

// setupAdmin creates the first admin through the setup link and enrols TOTP over HTTP; it returns the full session id and
// the TOTP secret.
func (g *group) setupAdmin(username string) (string, []byte) {
	g.t.Helper()
	tok, _, err := g.acc.CreateSetupToken()
	if err != nil {
		g.t.Fatal(err)
	}
	w := g.do(req{method: "POST", path: "/api/auth/setup", body: `{"token":"` + tok + `","name":"Ada","username":"` + username + `","password":"` + adminPass + `"}`})
	if w.Code != 201 {
		g.t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	pending := sessionFrom(g.t, w)
	w = g.do(req{method: "POST", path: "/api/auth/enrolment", body: `{}`, cookie: pending})
	en := decode[enrolmentBody](g.t, w)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	if err != nil || !strings.HasPrefix(en.URI, "otpauth://totp/") {
		g.t.Fatalf("enrolment: %+v %v", en, err)
	}
	w = g.do(req{method: "POST", path: "/api/auth/enrolment/confirm", body: `{"code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: pending})
	if w.Code != 200 {
		g.t.Fatalf("confirm: %d %s", w.Code, w.Body)
	}
	full := sessionFrom(g.t, w)
	if full == "" || full == pending {
		g.t.Fatal("the session id did not change at the second factor")
	}
	return full, secret
}

// signIn signs in with the password and a TOTP code at the clock's current step.
func (g *group) signIn(username string, secret []byte) string {
	g.t.Helper()
	w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"` + username + `","password":"` + adminPass + `"}`})
	if w.Code != 200 {
		g.t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	pending := sessionFrom(g.t, w)
	w = g.do(req{method: "POST", path: "/api/auth/second-factor", body: `{"code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: pending})
	if w.Code != 200 {
		g.t.Fatalf("second factor: %d %s", w.Code, w.Body)
	}
	return sessionFrom(g.t, w)
}

func (g *group) auditActions(actor string) []string {
	var out []string
	// account events (sign-in, reset) belong to the server, workspace 0; what an account does on data belongs to its workspace
	for _, ws := range []workspace.ID{0, workspace.Local} {
		es, err := g.audit.List(ws, "", 0, 500)
		if err != nil {
			g.t.Fatal(err)
		}
		for _, e := range es {
			if e.Actor == actor {
				out = append(out, e.Action)
			}
		}
	}
	return out
}

func TestGroupSignInRotatesTheSessionAndNeverAdoptsABrowsersId(t *testing.T) {
	g := newGroup(t)
	if st := decode[authStateBody](t, g.do(req{path: "/api/auth/state"})); !st.SetupNeeded || st.Stage != "none" {
		t.Fatalf("fresh server: %+v", st)
	}
	if w := g.do(req{path: "/api/capabilities"}); w.Code != 401 || !strings.Contains(w.Body.String(), "signin_required") {
		t.Fatalf("no session: %d %s", w.Code, w.Body)
	}
	_, secret := g.setupAdmin("ada")
	g.clock.add(30 * time.Second)

	// an id planted in the browser (session fixation) is never the one that signs in
	w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`, cookie: "planted-by-someone-else"})
	pending := sessionFrom(t, w)
	if w.Code != 200 || pending == "" || pending == "planted-by-someone-else" {
		t.Fatalf("sign-in: %d %q", w.Code, pending)
	}
	// a pending session is no actor
	if w := g.do(req{path: "/api/capabilities", cookie: pending}); w.Code != 401 {
		t.Fatalf("a pending session reached a route: %d", w.Code)
	}
	w = g.do(req{method: "POST", path: "/api/auth/second-factor", body: `{"code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: pending})
	full := sessionFrom(t, w)
	if w.Code != 200 || full == "" || full == pending {
		t.Fatalf("second factor: %d, id changed: %v", w.Code, full != pending)
	}
	if w := g.do(req{path: "/api/capabilities", cookie: pending}); w.Code != 401 {
		t.Fatal("the pre-sign-in id still works")
	}
	w = g.do(req{path: "/api/capabilities", cookie: full})
	caps := decode[Capabilities](t, w)
	if w.Code != 200 || !caps.Features.Accounts || caps.Features.Hosts || caps.Features.Editor {
		t.Fatalf("capabilities: %d %+v", w.Code, caps.Features)
	}
	st := decode[authStateBody](t, g.do(req{path: "/api/auth/state", cookie: full}))
	if st.Stage != "full" || !st.Recent || st.Account == nil || st.Account.Username != "ada" || !st.Account.Admin || st.SetupNeeded {
		t.Fatalf("state: %+v", st)
	}
	// signing out clears the cookie (with the same attributes) and ends the session
	w = g.do(req{method: "POST", path: "/api/auth/sign-out", body: `{}`, cookie: full})
	checkCookies(t, w)
	if w.Code != 204 || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("sign-out: %d %s", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := g.do(req{path: "/api/capabilities", cookie: full}); w.Code != 401 {
		t.Fatal("a signed-out session still works")
	}
}

// An unknown username and a wrong password answer with the same status and the same bytes, after the same argon2 work.
func TestGroupSignInDoesNotSayWhetherTheNameExists(t *testing.T) {
	g := newGroup(t)
	g.setupAdmin("ada")
	before := g.acc.Verifications()
	unknown := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"nobody","password":"` + adminPass + `"}`})
	wrong := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"not the passphrase"}`})
	if unknown.Code != 401 || wrong.Code != 401 || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown %d %q, wrong %d %q", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
	if len(unknown.Result().Cookies())+len(wrong.Result().Cookies()) != 0 {
		t.Fatal("a failed sign-in set a cookie")
	}
	if n := g.acc.Verifications() - before; n != 2 {
		t.Fatalf("%d argon2 verifications for two attempts", n)
	}
	// the backoff answers the same for both names too
	for i := 0; i < 4; i++ {
		g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"nobody","password":"x"}`})
		g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"x"}`})
	}
	u := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"nobody","password":"x"}`})
	k := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`})
	if u.Code != 429 || k.Code != 429 || u.Body.String() != k.Body.String() || u.Header().Get("Retry-After") != k.Header().Get("Retry-After") {
		t.Fatalf("backoff differs: %d %q / %d %q", u.Code, u.Body, k.Code, k.Body)
	}
}

// After five wrong passwords even the right one waits; the wait passes and the right password works again.
func TestGroupLockoutBacksOff(t *testing.T) {
	g := newGroup(t)
	_, secret := g.setupAdmin("ada")
	for i := 0; i < 5; i++ {
		if w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"guess number ` + itoa(i) + `"}`}); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`})
	if w.Code != 429 || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("right password during the backoff: %d %s", w.Code, w.Header().Get("Retry-After"))
	}
	g.clock.add(31 * time.Second)
	if g.signIn("ada", secret) == "" {
		t.Fatal("no session after the backoff")
	}
	if got := g.auditActions(g.mustAccount("ada").ID); !hasAction(got, "sign-in failed") || !hasAction(got, "signed in") {
		t.Fatalf("audit: %v", got)
	}
	if got := g.auditActions("anonymous"); !hasAction(got, "sign-in refused: too many attempts") {
		t.Fatalf("anonymous audit: %v", got)
	}
}

func (g *group) mustAccount(username string) accounts.Account {
	a, err := g.acc.ByUsername(username)
	if err != nil {
		g.t.Fatal(err)
	}
	return a
}

func hasAction(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A TOTP code signs in once; a recovery code signs in once.
func TestGroupSecondFactorCodesAreSingleUse(t *testing.T) {
	g := newGroup(t)
	full, secret := g.setupAdmin("ada")
	g.clock.add(30 * time.Second)
	code := accounts.CodeAt(secret, g.clock.now())
	second := func(body string) int {
		w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`})
		return g.do(req{method: "POST", path: "/api/auth/second-factor", body: body, cookie: sessionFrom(t, w)}).Code
	}
	if c := second(`{"code":"` + code + `"}`); c != 200 {
		t.Fatalf("first use: %d", c)
	}
	if c := second(`{"code":"` + code + `"}`); c != 401 {
		t.Fatalf("replayed TOTP code: %d", c)
	}
	codes := decode[struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}](t, g.do(req{method: "POST", path: "/api/account/recovery-codes", body: `{}`, cookie: full})).RecoveryCodes
	if len(codes) != 10 {
		t.Fatalf("%d recovery codes", len(codes))
	}
	if c := second(`{"recoveryCode":"` + codes[3] + `"}`); c != 200 {
		t.Fatalf("recovery code: %d", c)
	}
	if c := second(`{"recoveryCode":"` + codes[3] + `"}`); c != 401 {
		t.Fatalf("reused recovery code: %d", c)
	}
}

func TestGroupSessionsExpireIdleAndAbsolute(t *testing.T) {
	g := newGroup(t)
	_, secret := g.setupAdmin("ada")
	g.clock.add(30 * time.Second)
	sid := g.signIn("ada", secret)
	g.clock.add(accounts.IdleTimeout - time.Minute)
	if w := g.do(req{path: "/api/capabilities", cookie: sid}); w.Code != 200 {
		t.Fatalf("active session refused: %d", w.Code)
	}
	g.clock.add(accounts.IdleTimeout)
	if w := g.do(req{path: "/api/capabilities", cookie: sid}); w.Code != 401 {
		t.Fatalf("idle session: %d", w.Code)
	}
	g.clock.add(time.Minute)
	sid = g.signIn("ada", secret)
	for el := time.Duration(0); el+8*time.Hour < accounts.AbsoluteTimeout; el += 8 * time.Hour {
		g.clock.add(8 * time.Hour)
		if w := g.do(req{path: "/api/capabilities", cookie: sid}); w.Code != 200 {
			t.Fatalf("busy session ended after %v", el)
		}
	}
	g.clock.add(8 * time.Hour)
	if w := g.do(req{path: "/api/capabilities", cookie: sid}); w.Code != 401 {
		t.Fatalf("a session outlived seven days: %d", w.Code)
	}
}

func TestGroupCSRFRules(t *testing.T) {
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	write := func(hdr map[string]string, body string) int {
		return g.do(req{method: "POST", path: "/api/auth/sign-out-everywhere", body: body, cookie: full, hdr: hdr}).Code
	}
	for name, c := range map[string]struct {
		hdr  map[string]string
		body string
		want int
	}{
		"cross-origin POST":          {map[string]string{"Origin": "https://evil.example"}, `{}`, 403},
		"POST without Origin":        {map[string]string{"Origin": "-"}, `{}`, 403},
		"Origin with another scheme": {map[string]string{"Origin": "http://lab.example.edu"}, `{}`, 403},
		"cross-site Sec-Fetch-Site":  {map[string]string{"Sec-Fetch-Site": "cross-site"}, `{}`, 403},
		"same-site Sec-Fetch-Site":   {map[string]string{"Sec-Fetch-Site": "same-site"}, `{}`, 403},
		"form-encoded body":          {map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, `a=b`, 415},
		"wrong Host":                 {map[string]string{"Host": "evil.example"}, `{}`, 403},
		"loopback Host":              {map[string]string{"Host": "127.0.0.1:8787"}, `{}`, 403},
		"cookie and bearer":          {map[string]string{"Authorization": "Bearer vsq_x"}, `{}`, 400},
	} {
		if got := write(c.hdr, c.body); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if w := g.do(req{path: "/api/capabilities", cookie: full}); w.Code != 200 {
		t.Fatal("a refused write ended the session anyway")
	}
	// GET: a cross-site read of the API is refused, a wrong Host too; a navigation to the app shell from a link is not
	if w := g.do(req{path: "/api/capabilities", cookie: full, hdr: map[string]string{"Sec-Fetch-Site": "cross-site"}}); w.Code != 403 {
		t.Errorf("cross-site API GET: %d", w.Code)
	}
	if w := g.do(req{path: "/api/capabilities", cookie: full, hdr: map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}}); w.Code != 403 {
		t.Errorf("a cross-site navigation to /api passed: %d", w.Code)
	}
	if w := g.do(req{path: "/api/capabilities", cookie: full, hdr: map[string]string{"Host": "evil.example"}}); w.Code != 403 {
		t.Errorf("wrong Host GET: %d", w.Code)
	}
	if w := g.do(req{path: "/", hdr: map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}}); w.Code != 200 {
		t.Errorf("navigation to the app from a link: %d", w.Code)
	}
	if w := g.do(req{path: "/", hdr: map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"}}); w.Code != 403 {
		t.Errorf("a cross-site subresource load of the app: %d", w.Code)
	}
	// the right write passes
	if got := write(nil, `{}`); got != 200 {
		t.Fatalf("same-origin write: %d", got)
	}
}

func TestGroupTokensNeedARecentSignInAndStopWhenRevokedOrExpired(t *testing.T) {
	g := newGroup(t)
	full, secret := g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	g.clock.add(accounts.RecentWindow + time.Second)
	if w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"laptop","scopes":["read"]}`, cookie: full}); w.Code != 401 || !strings.Contains(w.Body.String(), "reauth_required") {
		t.Fatalf("token without a recent sign-in: %d %s", w.Code, w.Body)
	}
	w := g.do(req{method: "POST", path: "/api/auth/confirm", body: `{"password":"` + adminPass + `","code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: full})
	fresh := sessionFrom(t, w)
	if w.Code != 200 || fresh == "" || fresh == full {
		t.Fatalf("confirm: %d, id changed: %v", w.Code, fresh != full)
	}
	w = g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"laptop","scopes":["read","push"],"days":1}`, cookie: fresh})
	made := decode[struct {
		Token string         `json:"token"`
		Info  accounts.Token `json:"info"`
	}](t, w)
	if w.Code != 201 || !strings.HasPrefix(made.Token, accounts.TokenPrefix) {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	bearer := map[string]string{"Authorization": "Bearer " + made.Token}
	if w := g.do(req{path: "/api/capabilities", hdr: bearer}); w.Code != 200 {
		t.Fatalf("read with the token: %d %s", w.Code, w.Body)
	}
	// a token is exempt from the cookie CSRF checks (nothing ambient carries it), but not from its scope
	if w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"more","scopes":["write"]}`, hdr: map[string]string{"Authorization": "Bearer " + made.Token, "Origin": "-"}}); w.Code != 403 {
		t.Fatalf("a token minted a token: %d", w.Code)
	}
	list := decode[struct {
		Tokens []accounts.Token `json:"tokens"`
	}](t, g.do(req{path: "/api/account/tokens", cookie: fresh}))
	if len(list.Tokens) != 1 || strings.Contains(g.do(req{path: "/api/account/tokens", cookie: fresh}).Body.String(), made.Token) {
		t.Fatal("the list shows the token's value")
	}
	if w := g.do(req{method: "POST", path: "/api/account/tokens/" + made.Info.ID + "/revoke", body: `{}`, cookie: fresh}); w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := g.do(req{path: "/api/capabilities", hdr: bearer}); w.Code != 401 || !strings.Contains(w.Body.String(), "token_invalid") {
		t.Fatalf("revoked token: %d %s", w.Code, w.Body)
	}
	// expiry
	w = g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"day","scopes":["write"],"days":1}`, cookie: fresh})
	day := decode[struct {
		Token string `json:"token"`
	}](t, w).Token
	g.clock.add(24 * time.Hour)
	if w := g.do(req{path: "/api/capabilities", hdr: map[string]string{"Authorization": "Bearer " + day}}); w.Code != 401 {
		t.Fatalf("expired token: %d", w.Code)
	}
	// neither a token nor its hash is in the request log or the audit log
	es, _ := g.audit.List(workspace.Local, "", 0, 500)
	logs := strings.Join(apiLogTail.Lines(), "\n")
	for _, secret := range []string{made.Token, accounts.Hash(made.Token), day, accounts.Hash(day)} {
		if strings.Contains(logs, secret) {
			t.Fatal("a token or its hash is in the request log")
		}
		for _, e := range es {
			if strings.Contains(e.Detail, secret) || strings.Contains(e.Action, secret) {
				t.Fatalf("a token or its hash is in the audit log: %+v", e)
			}
		}
	}
	got := g.auditActions(ada.ID)
	for _, want := range []string{"admin account created from the setup link", "second factor enrolled", "signed in", "sign-in confirmed", "access token created", "access token revoked"} {
		if !hasAction(got, want) {
			t.Errorf("audit lacks %q: %v", want, got)
		}
	}
}

// ADR 0050 test 4: on the group server the local-only features, clusters, jobs, sweeps and host routes are not mounted. Each
// route a local server declares with one of those permissions answers 404 to a signed-in admin who passes every CSRF check.
func TestGroupServerDoesNotMountDisabledFeatures(t *testing.T) {
	_, local := fullServer(t)
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	s, declared := fullServerWith(t, func(s *Server) {
		s.MultiUser = g.s.MultiUser
		s.Audit = g.audit
	})
	s.onRoute = nil
	h := s.Handler()
	disabled := 0
	for pattern, perm := range local {
		if !notOnGroupServer[perm] && !pathsNotOnGroupServer[pattern] && !groupServerOff(pattern) {
			continue
		}
		disabled++
		if _, ok := declared[pattern]; ok {
			t.Errorf("%s (%s) is mounted on the group server", pattern, perm)
		}
		method, path, _ := strings.Cut(pattern, " ")
		for _, p := range []string{"{id}", "{lid}", "{pos}", "{sid}"} {
			path = strings.ReplaceAll(path, p, "1")
		}
		q := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		q.Host = groupHost
		q.Header.Set("Origin", groupOrigin)
		q.Header.Set("Content-Type", "application/json")
		q.AddCookie(&http.Cookie{Name: SessionCookie, Value: full})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != 404 {
			t.Errorf("%s answered %d on the group server, want 404", pattern, w.Code)
		}
	}
	for p := range pathsNotOnGroupServer {
		if _, ok := local[p]; !ok {
			t.Errorf("%s is listed as left out but no local server declares it", p)
		}
	}
	// slice 3 mounts them again, confined to the workspace's data roots (TestDataRootsConfinePathRoutes)
	for _, must := range []string{"GET /api/fs/complete", "POST /api/projects/open"} {
		if _, ok := declared[must]; !ok {
			t.Errorf("%s is not mounted on the group server", must)
		}
	}
	for _, must := range []string{"GET /api/files/text", "PUT /api/files/text", "POST /api/fs/choose", "GET /api/hosts/ssh-config", "GET /api/terminal",
		"POST /api/terminal/sessions", "GET /api/hosts", "POST /api/hosts", "GET /api/hosts/{id}/list", "GET /api/hosts/{id}/text", "POST /api/jobs",
		"GET /api/jobs", "POST /api/jobs/{id}/submit", "GET /api/sweeps", "POST /api/sweeps", "POST /api/transfers", "POST /api/analysis/job", "GET /api/offline",
		"GET /api/publish", "POST /api/reports/{id}/publish", "GET /api/report-comments", "POST /api/reply-drafts/{id}/send"} {
		if !notOnGroupServer[local[must]] && !groupServerOff(must) {
			t.Errorf("%s is not among the routes the group server leaves out", must)
		}
	}
	if disabled < 40 {
		t.Fatalf("only %d disabled routes: the fixture is not mounting every optional route", disabled)
	}
	// what is mounted there declares known permissions that the spec agrees with, the account routes included
	if _, ok := declared["POST /api/auth/sign-in"]; !ok || len(declared) < 150 {
		t.Fatalf("group server mounts %d routes", len(declared))
	}
	checkDeclarationsAgainstSpec(t, declared)
}

// An action through an ordinary route is recorded for the account that did it, never for the local owner.
func TestGroupAuditNamesTheAccountNotTheLocalOwner(t *testing.T) {
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	d, err := g.cat.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: "/data/cube.fits", Name: "cube.fits", Header: json.RawMessage(`{"naxis":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if w := g.do(req{method: "POST", path: "/api/datasets/" + itoa(int(d.ID)) + "/unit", body: `{"clear":true}`, cookie: full}); w.Code != 200 {
		t.Fatalf("unit: %d %s", w.Code, w.Body)
	}
	es, _ := g.audit.List(workspace.Local, "dataset", d.ID, 10)
	if len(es) != 1 || es[0].Actor != g.mustAccount("ada").ID || es[0].Via != "session" {
		t.Fatalf("entries: %+v", es)
	}
	all, _ := g.audit.List(workspace.Local, "", 0, 500)
	for _, e := range all {
		if e.Actor == authz.LocalOwnerID {
			t.Errorf("the group server recorded the local owner: %+v", e)
		}
	}
}

func TestGroupAdminResetsASecondFactor(t *testing.T) {
	g := newGroup(t)
	full, secret := g.setupAdmin("ada")
	tok, _, _ := g.acc.CreateSetupToken()
	w := g.do(req{method: "POST", path: "/api/auth/setup", body: `{"token":"` + tok + `","name":"Bob","username":"bob","password":"` + adminPass + `"}`})
	bobPending := sessionFrom(t, w)
	bob := g.mustAccount("bob")
	bobToken, _, err := g.acc.CreateToken(bob.ID, "bob's laptop", []string{"read"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w := g.do(req{method: "POST", path: "/api/accounts/" + bob.ID + "/reset-second-factor", body: `{}`, cookie: full}); w.Code != 204 {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{path: "/api/auth/state", cookie: bobPending}); decode[authStateBody](t, w).Stage != "none" {
		t.Fatal("the reset left the account's session alive")
	}
	if w := g.do(req{path: "/api/capabilities", hdr: map[string]string{"Authorization": "Bearer " + bobToken}}); w.Code != 401 {
		t.Fatalf("the reset left the account's token working: %d", w.Code)
	}
	got := g.auditActions(g.mustAccount("ada").ID)
	for _, want := range []string{"second factor reset", "sessions ended by the reset", "access tokens revoked by the reset"} {
		if !hasAction(got, want) {
			t.Errorf("audit lacks %q: %v", want, got)
		}
	}
	g.clock.add(accounts.RecentWindow)
	if w := g.do(req{path: "/api/accounts", cookie: full}); w.Code != 401 {
		t.Fatalf("admin list without a recent sign-in: %d", w.Code)
	}
	_ = secret
}

// The group server has no hosts (main passes no host store or ssh manager). A dataset that names a host (one could only arrive
// with a restored local catalog) is refused in a controlled way by the reads still mounted, never by a nil dereference.
func TestGroupServerReadsOfAHostDatasetFailCleanly(t *testing.T) {
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	ep := &echoPlanes{}
	g.s.Planes, g.s.Worker = ep, &fakeWorker{}
	g.h = g.s.Handler()
	d, err := g.cat.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: "/remote/cube.fits", Name: "cube.fits", HostID: 7, Header: json.RawMessage(`{"naxis":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	id := itoa(int(d.ID))
	defer func() {
		if len(ep.calls) != 0 {
			t.Errorf("a host dataset reached the local worker: %v", ep.calls)
		}
	}()
	for _, p := range []string{"/api/datasets/" + id, "/api/datasets/" + id + "/view", "/api/datasets/" + id + "/plane?index=0", "/api/datasets/" + id + "/header-cards",
		"/api/datasets/" + id + "/coord?x=1&y=1", "/api/datasets/" + id + "/spectrum?x=1&y=1", "/api/datasets/" + id + "/locations",
		"/api/datasets/availability", "/api/preview?path=/remote/cube.fits"} {
		w := g.do(req{path: p, cookie: full})
		if w.Code == 0 || (w.Code >= 500 && w.Code != 502 && w.Code != 503) {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
}

// Two password-only sign-ins of an account without a second factor: once one enrols, the other cannot enrol a secret of its
// own and be let in on the password alone; it owes a code from the enrolled secret.
func TestGroupPendingEnrolmentCannotReplaceAnEnrolledSecret(t *testing.T) {
	g := newGroup(t)
	tok, _, _ := g.acc.CreateSetupToken()
	w := g.do(req{method: "POST", path: "/api/auth/setup", body: `{"token":"` + tok + `","name":"Ada","username":"ada","password":"` + adminPass + `"}`})
	first := sessionFrom(t, w)
	w = g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`})
	second := sessionFrom(t, w)
	if decode[authStateBody](t, w).Stage != "enrol" {
		t.Fatal("the second sign-in is not at enrolment")
	}
	// the second session starts an enrolment of its own before the first one finishes
	other := decode[enrolmentBody](t, g.do(req{method: "POST", path: "/api/auth/enrolment", body: `{}`, cookie: second}))
	en := decode[enrolmentBody](t, g.do(req{method: "POST", path: "/api/auth/enrolment", body: `{}`, cookie: first}))
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	if w := g.do(req{method: "POST", path: "/api/auth/enrolment/confirm", body: `{"code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: first}); w.Code != 200 {
		t.Fatalf("first enrolment: %d %s", w.Code, w.Body)
	}
	otherSecret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(other.Secret)
	g.clock.add(30 * time.Second)
	if w := g.do(req{method: "POST", path: "/api/auth/enrolment/confirm", body: `{"code":"` + accounts.CodeAt(otherSecret, g.clock.now()) + `"}`, cookie: second}); w.Code != 409 {
		t.Fatalf("a pending session replaced the enrolled secret: %d %s", w.Code, w.Body)
	}
	if w := g.do(req{method: "POST", path: "/api/auth/enrolment", body: `{}`, cookie: second}); w.Code == 200 { // 401: it is no longer at enrolment
		t.Fatalf("a pending session started a new enrolment over an enrolled one: %d", w.Code)
	}
	// that session now owes a code from the enrolled secret, and the enrolled secret still works
	if st := decode[authStateBody](t, g.do(req{path: "/api/auth/state", cookie: second})); st.Stage != "second_factor" {
		t.Fatalf("stage %q", st.Stage)
	}
	if w := g.do(req{method: "POST", path: "/api/auth/second-factor", body: `{"code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: second}); w.Code != 200 {
		t.Fatalf("the enrolled secret no longer works: %d %s", w.Code, w.Body)
	}
}

// Behind a proxy on a private address with no client-address header, failures for many names never stop everyone (the
// per-address limit is off), while the per-name backoff still holds. With the header, the per-address limit counts clients.
func TestGroupProxyCannotTurnFailuresIntoAGlobalLockout(t *testing.T) {
	g := newGroup(t)
	_, secret := g.setupAdmin("ada")
	viaProxy := func(name, pass string, hdr map[string]string) *httptest.ResponseRecorder {
		q := httptest.NewRequest("POST", "/api/auth/sign-in", strings.NewReader(`{"username":"`+name+`","password":"`+pass+`"}`))
		q.Host, q.RemoteAddr = groupHost, "10.0.0.2:5000"
		q.Header.Set("Origin", groupOrigin)
		q.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			q.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		g.h.ServeHTTP(w, q)
		return w
	}
	for i := 0; i < 40; i++ {
		if w := viaProxy("guess"+itoa(i), "wrong password", nil); w.Code != 401 {
			t.Fatalf("failure %d behind the proxy: %d", i, w.Code)
		}
	}
	if w := viaProxy("ada", adminPass, nil); w.Code != 200 {
		t.Fatalf("40 failures through the proxy locked the admin out: %d %s", w.Code, w.Body)
	}
	for i := 0; i < 5; i++ {
		viaProxy("bob", "wrong password", nil)
	}
	if w := viaProxy("bob", "wrong password", nil); w.Code != 429 {
		t.Fatalf("the per-name backoff is off behind the proxy: %d", w.Code)
	}
	// with the header set, a client spraying names through the proxy is stopped
	g.s.MultiUser.ClientIPHeader = "X-Forwarded-For"
	g.h = g.s.Handler()
	spray := map[string]string{"X-Forwarded-For": "198.51.100.9"}
	for i := 0; i < 30; i++ {
		viaProxy("spray"+itoa(i), "wrong password", spray)
	}
	if w := viaProxy("fresh", "wrong password", spray); w.Code != 429 {
		t.Fatalf("the per-address limit does not count clients named by the header: %d", w.Code)
	}
	if w := viaProxy("ada", adminPass, map[string]string{"X-Forwarded-For": "198.51.100.10"}); w.Code != 200 {
		t.Fatalf("another client was stopped too: %d", w.Code)
	}
	_ = secret
}

// A parallel burst of guesses is limited like a sequence: an attempt is reserved before the password or the code is checked,
// so at most the free attempts of the backoff reach the verifier, however many requests arrive at once.
func TestGroupParallelGuessesAreLimitedLikeSequentialOnes(t *testing.T) {
	g := newGroup(t)
	_, secret := g.setupAdmin("ada")
	burst := func(n int, send func(i int) int) map[int]int {
		var mu sync.Mutex
		codes := map[int]int{}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				c := send(i)
				mu.Lock()
				codes[c]++
				mu.Unlock()
			}(i)
		}
		close(start)
		wg.Wait()
		return codes
	}
	before := g.acc.Verifications()
	codes := burst(100, func(i int) int {
		return g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"guess ` + itoa(i) + ` of many"}`}).Code
	})
	if n := g.acc.Verifications() - before; n > 5 {
		t.Fatalf("100 parallel wrong passwords: %d reached argon2 (answers %v)", n, codes)
	}
	if codes[200] != 0 {
		t.Fatalf("a wrong password signed in: %v", codes)
	}
	// TOTP: a pending session guessing codes in parallel
	g.clock.add(time.Hour)
	g.s.MultiUser.Limiter = &accounts.Limiter{Now: g.clock.now}
	g.h = g.s.Handler()
	w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"` + adminPass + `"}`})
	pending := sessionFrom(t, w)
	right := accounts.CodeAt(secret, g.clock.now())
	beforeTOTP := g.acc.TOTPChecks()
	codes = burst(100, func(i int) int {
		code := fmt.Sprintf("%06d", i)
		if code == right {
			code = "999999"
		}
		return g.do(req{method: "POST", path: "/api/auth/second-factor", body: `{"code":"` + code + `"}`, cookie: pending}).Code
	})
	if n := g.acc.TOTPChecks() - beforeTOTP; n > 5 {
		t.Fatalf("100 parallel TOTP guesses: %d were checked (answers %v)", n, codes)
	}
}

// Strangers typing the admin's name at the sign-in page do not block the signed-in admin's re-confirmation: it counts
// against its own key, tied to the account, and wrong confirmations back off only that.
func TestGroupConfirmIsNotBlockedByAnonymousFailures(t *testing.T) {
	g := newGroup(t)
	full, secret := g.setupAdmin("ada")
	g.clock.add(30 * time.Second) // a fresh TOTP step for the confirmation
	for i := 0; i < 5; i++ {
		g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"stranger guess"}`})
	}
	if w := g.do(req{method: "POST", path: "/api/auth/sign-in", body: `{"username":"ada","password":"stranger guess"}`}); w.Code != 429 {
		t.Fatalf("the name is not backing off: %d", w.Code)
	}
	w := g.do(req{method: "POST", path: "/api/auth/confirm", body: `{"password":"` + adminPass + `","code":"` + accounts.CodeAt(secret, g.clock.now()) + `"}`, cookie: full})
	if w.Code != 200 {
		t.Fatalf("the admin's confirmation was blocked by strangers: %d %s", w.Code, w.Body)
	}
	fresh := sessionFrom(t, w)
	for i := 0; i < 5; i++ {
		g.do(req{method: "POST", path: "/api/auth/confirm", body: `{"password":"wrong","code":"000000"}`, cookie: fresh})
	}
	if w := g.do(req{method: "POST", path: "/api/auth/confirm", body: `{"password":"wrong","code":"000000"}`, cookie: fresh}); w.Code != 429 {
		t.Fatalf("wrong confirmations do not back off: %d", w.Code)
	}
}

// The client-address header is honoured only from a peer that may set it (loopback, private, or a --trusted-proxy range):
// a client on a public address cannot escape the per-address limit by writing its own X-Forwarded-For.
func TestGroupClientHeaderIsTrustedOnlyFromProxies(t *testing.T) {
	g := newGroup(t)
	g.setupAdmin("ada")
	g.s.MultiUser.ClientIPHeader = "X-Forwarded-For"
	g.h = g.s.Handler()
	from := func(peer, xff, name string) int {
		q := httptest.NewRequest("POST", "/api/auth/sign-in", strings.NewReader(`{"username":"`+name+`","password":"wrong password"}`))
		q.Host, q.RemoteAddr = groupHost, peer
		q.Header.Set("Origin", groupOrigin)
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		g.h.ServeHTTP(w, q)
		return w.Code
	}
	for i := 0; i < 30; i++ {
		from("203.0.113.7:4000", "198.51.100."+itoa(i), "n"+itoa(i))
	}
	if c := from("203.0.113.7:4000", "198.51.100.200", "fresh"); c != 429 {
		t.Fatalf("a public client forging the header escaped the per-address limit: %d", c)
	}
	// a configured proxy range is trusted
	g.s.MultiUser.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	g.s.MultiUser.Limiter = &accounts.Limiter{Now: g.clock.now}
	g.h = g.s.Handler()
	for i := 0; i < 30; i++ {
		from("203.0.113.9:4000", "198.51.100."+itoa(i), "m"+itoa(i))
	}
	if c := from("203.0.113.9:4000", "198.51.100.201", "fresh"); c != 401 {
		t.Fatalf("clients behind a trusted proxy were counted as one: %d", c)
	}
}

// Signing out everywhere ends the sessions and keeps the tokens, and says so.
func TestGroupSignOutEverywhereKeepsTokensAndSaysSo(t *testing.T) {
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	raw, _, err := g.acc.CreateToken(g.mustAccount("ada").ID, "cli", []string{"read"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := g.do(req{method: "POST", path: "/api/auth/sign-out-everywhere", body: `{}`, cookie: full})
	body := decode[struct {
		SessionsEnded int    `json:"sessionsEnded"`
		TokensKept    int    `json:"tokensKept"`
		Note          string `json:"note"`
	}](t, w)
	if w.Code != 200 || body.SessionsEnded != 1 || body.TokensKept != 1 || body.Note != "access tokens stay valid; revoke them under Tokens" {
		t.Fatalf("%d %+v", w.Code, body)
	}
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/auth/sign-out-everywhere", "POST", 200, "application/json"), "sign-out-everywhere", w.Body.Bytes())
	if w := g.do(req{path: "/api/capabilities", cookie: full}); w.Code != 401 {
		t.Fatal("a session survived")
	}
	if w := g.do(req{path: "/api/capabilities", hdr: map[string]string{"Authorization": "Bearer " + raw}}); w.Code != 200 {
		t.Fatalf("the token stopped: %d", w.Code)
	}
}

// The account routes answer what packages/contracts says they do.
func TestGroupAccountRepliesMatchTheSpec(t *testing.T) {
	g := newGroup(t)
	doc := loadSpec(t)
	full, _ := g.setupAdmin("ada")
	check := func(method, path, specPath string, status int, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		conforms(t, responseSchemaAt(t, doc, specPath, method, status, "application/json"), method+" "+path, w.Body.Bytes())
	}
	check("GET", "/api/auth/state", "/api/auth/state", 200, g.do(req{path: "/api/auth/state", cookie: full}))
	check("GET", "/api/auth/state", "/api/auth/state", 200, g.do(req{path: "/api/auth/state"}))
	check("GET", "/api/capabilities", "/api/capabilities", 200, g.do(req{path: "/api/capabilities", cookie: full}))
	w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"laptop","scopes":["write","relay"],"days":90}`, cookie: full})
	check("POST", "/api/account/tokens", "/api/account/tokens", 201, w)
	id := decode[struct {
		Info accounts.Token `json:"info"`
	}](t, w).Info.ID
	check("GET", "/api/account/tokens", "/api/account/tokens", 200, g.do(req{path: "/api/account/tokens", cookie: full}))
	check("POST", "/api/account/tokens/"+id+"/revoke", "/api/account/tokens/{id}/revoke", 200, g.do(req{method: "POST", path: "/api/account/tokens/" + id + "/revoke", body: `{}`, cookie: full}))
	check("GET", "/api/accounts", "/api/accounts", 200, g.do(req{path: "/api/accounts", cookie: full}))
	check("POST", "/api/account/second-factor", "/api/account/second-factor", 200, g.do(req{method: "POST", path: "/api/account/second-factor", body: `{}`, cookie: full}))
	check("POST", "/api/account/recovery-codes", "/api/account/recovery-codes", 200, g.do(req{method: "POST", path: "/api/account/recovery-codes", body: `{}`, cookie: full}))
}

// Local mode is unchanged: no account route, no accounts feature in the capabilities, and the local owner acts.
func TestLocalModeHasNoAccountRoutes(t *testing.T) {
	s := &Server{}
	h := s.Handler()
	for _, p := range []string{"/api/auth/state", "/api/account/tokens", "/api/accounts"} {
		r := httptest.NewRequest("GET", p, nil)
		r.Host = "127.0.0.1:8787"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("local mode answers %s with %d", p, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/capabilities", nil)
	r.Host = "127.0.0.1:8787"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "accounts") || len(w.Result().Cookies()) != 0 {
		t.Fatalf("local capabilities changed: %d %s", w.Code, w.Body)
	}
}
