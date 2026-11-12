package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type termEnv struct {
	ts     *httptest.Server
	srv    *Server
	au     *audit.Store
	hostID int64
}

// newTerminal starts the owner API with the terminal on (or off). The shell is a plain sh with a fixed prompt.
func newTerminal(t *testing.T, enabled bool, mod func(*Server)) *termEnv {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	au, err := audit.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Roots: rs, Store: st, Worker: &fakeWorker{}, Audit: au, TerminalEnabled: enabled,
		TerminalShell: []string{"/bin/sh", "-c", "PS1='VQ$ ' exec /bin/sh -i"}}
	e := &termEnv{srv: srv, au: au}
	if mod != nil {
		mod(srv)
	}
	if srv.HostDB == nil {
		hs, err := hosts.Open(st.DB())
		if err != nil {
			t.Fatal(err)
		}
		srv.HostDB = hs
	}
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

func (e *termEnv) origin() string { return e.ts.URL }
func (e *termEnv) wsURL() string {
	return "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/api/terminal"
}

// session asks for a session token the way the page does.
func (e *termEnv) session(t *testing.T, body map[string]any, hdr map[string]string) (int, TerminalSession) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/terminal/sessions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s TerminalSession
	_ = json.NewDecoder(resp.Body).Decode(&s)
	return resp.StatusCode, s
}

func (e *termEnv) token(t *testing.T, tab string, hostID int64) string {
	t.Helper()
	code, s := e.session(t, map[string]any{"tab": tab, "hostId": hostID}, nil)
	if code != 200 || len(s.Token) != 64 {
		t.Fatalf("session: %d %+v", code, s)
	}
	return s.Token
}

// dial opens the socket; protos are the offered subprotocols and hdr the extra request headers.
func (e *termEnv) dial(url string, protos []string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	if hdr == nil {
		hdr = http.Header{}
	}
	if hdr.Get("Origin") == "" {
		hdr.Set("Origin", e.origin())
	}
	d := websocket.Dialer{Subprotocols: protos, HandshakeTimeout: 5 * time.Second}
	return d.Dial(url, hdr)
}

func (e *termEnv) connect(t *testing.T, tok string) *websocket.Conn {
	t.Helper()
	c, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + tok}, nil)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	if c.Subprotocol() != termProtocol {
		t.Errorf("selected subprotocol %q: the token must never be echoed", c.Subprotocol())
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// readUntil collects output until it contains want, or fails; a control message is returned as exit.
func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	var got strings.Builder
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		typ, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, got.String(), err)
		}
		if typ == websocket.TextMessage {
			t.Fatalf("waiting for %q, got control %s after %q", want, data, got.String())
		}
		got.Write(data)
		if strings.Contains(got.String(), want) {
			return got.String()
		}
	}
}

// readExit reads until the exit control message and returns it.
func readExit(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		typ, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("no exit message: %v", err)
		}
		if typ == websocket.TextMessage {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil || m["type"] != "exit" {
				t.Fatalf("control %s", data)
			}
			return m
		}
	}
}

func status(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func TestTerminalIsOffByDefault(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, false, nil)
	if code, _ := e.session(t, map[string]any{"tab": "tab-aaaaaaaa"}, nil); code != 404 {
		t.Errorf("POST sessions with the switch off: %d", code)
	}
	_, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + strings.Repeat("a", 64)}, nil)
	if err == nil || status(resp) != 404 {
		t.Errorf("GET /api/terminal with the switch off: %v %d", err, status(resp))
	}
	var caps Capabilities
	r, _ := http.Get(e.ts.URL + "/api/capabilities")
	_ = json.NewDecoder(r.Body).Decode(&caps)
	r.Body.Close()
	if caps.Features.Terminal {
		t.Error("capabilities report a terminal that is not mounted")
	}
	on := newTerminal(t, true, nil)
	r, _ = http.Get(on.ts.URL + "/api/capabilities")
	_ = json.NewDecoder(r.Body).Decode(&caps)
	r.Body.Close()
	if !caps.Features.Terminal {
		t.Error("capabilities do not report the terminal that is mounted")
	}
}

func TestTerminalRunsAShellAndEchoesInput(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "VQ$ ")
	if err := c.WriteMessage(websocket.BinaryMessage, []byte("echo $((6*7))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "42\r\n")
	// resize reaches the terminal
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":132,"rows":43}`)); err != nil {
		t.Fatal(err)
	}
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("stty size\n"))
	readUntil(t, c, "43 132")
	// exiting the shell is reported with its code
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("exit 3\n"))
	if m := readExitAfterOutput(t, c); m["reason"] != "shell exited" || m["code"] != float64(3) {
		t.Errorf("exit message: %v", m)
	}
}

// readExitAfterOutput skips remaining output frames.
func readExitAfterOutput(t *testing.T, c *websocket.Conn) map[string]any { return readExit(t, c) }

func TestTerminalTokenRules(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, func(s *Server) { s.TerminalLimits.Connect = 300 * time.Millisecond })
	good := e.token(t, "tab-aaaaaaaa", 0)
	cases := map[string]struct {
		protos []string
		url    string
	}{
		"no token":         {protos: []string{termProtocol}},
		"no protocol":      {protos: []string{termTokenPrefix + good}},
		"wrong token":      {protos: []string{termProtocol, termTokenPrefix + strings.Repeat("0", 64)}},
		"short token":      {protos: []string{termProtocol, termTokenPrefix + "abc"}},
		"two tokens":       {protos: []string{termProtocol, termTokenPrefix + good, termTokenPrefix + good}},
		"foreign protocol": {protos: []string{termProtocol, termTokenPrefix + good, "other"}},
		"token in URL":     {protos: []string{termProtocol}, url: "?token=" + good},
		"token in URL too": {protos: []string{termProtocol, termTokenPrefix + good}, url: "?token=" + good},
	}
	for name, c := range cases {
		_, resp, err := e.dial(e.wsURL()+c.url, c.protos, nil)
		if err == nil || (status(resp) != 401 && status(resp) != 400) {
			t.Errorf("%s: %v %d, want refusal", name, err, status(resp))
		}
	}
	// the refusals above must not have used up the good token
	c := e.connect(t, good)
	readUntil(t, c, "VQ$ ")
	// reused
	_, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + good}, nil)
	if err == nil || status(resp) != 401 {
		t.Errorf("reused token: %v %d", err, status(resp))
	}
	// expired
	old := e.token(t, "tab-bbbbbbbb", 0)
	time.Sleep(400 * time.Millisecond)
	_, resp, err = e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + old}, nil)
	if err == nil || status(resp) != 401 {
		t.Errorf("expired token: %v %d", err, status(resp))
	}
}

func TestTerminalRefusesForeignOriginHostAndSite(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	proto := func(tok string) []string { return []string{termProtocol, termTokenPrefix + tok} }
	for name, h := range map[string]http.Header{
		"cross-site origin": {"Origin": {"https://evil.example"}},
		"other port":        {"Origin": {"http://127.0.0.1:1"}},
		"origin with path":  {"Origin": {e.origin() + "/x"}},
		"null origin":       {"Origin": {"null"}},
		"cross-site fetch":  {"Sec-Fetch-Site": {"cross-site"}},
		"same-site fetch":   {"Sec-Fetch-Site": {"same-site"}},
		"foreign host":      {"Host": {"evil.example"}},
	} {
		tok := e.token(t, "tab-aaaaaaaa", 0)
		_, resp, err := e.dial(e.wsURL(), proto(tok), h)
		if err == nil || status(resp) != 403 {
			t.Errorf("%s: %v %d, want 403", name, err, status(resp))
		}
	}
	// a handshake with no Origin at all is not a browser: refused
	tok := e.token(t, "tab-aaaaaaaa", 0)
	d := websocket.Dialer{Subprotocols: proto(tok)}
	req := http.Header{}
	_, resp, err := d.Dial(e.wsURL(), req)
	if err == nil || status(resp) != 403 {
		t.Errorf("no origin: %v %d", err, status(resp))
	}
	// the same refusals apply to asking for a session
	for name, h := range map[string]map[string]string{
		"cross-site origin": {"Origin": "https://evil.example"},
		"cross-site fetch":  {"Sec-Fetch-Site": "cross-site"},
		"foreign host":      {"Host": "evil.example"},
	} {
		if code, _ := e.session(t, map[string]any{"tab": "tab-aaaaaaaa"}, h); code != 403 {
			t.Errorf("session, %s: %d", name, code)
		}
	}
	// a bad body and a non-JSON content type are refused
	if code, _ := e.session(t, map[string]any{"tab": "x"}, nil); code != 400 {
		t.Errorf("short tab id: %d", code)
	}
	if code, _ := e.session(t, map[string]any{"tab": "tab-aaaaaaaa", "extra": 1}, nil); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	if code, _ := e.session(t, map[string]any{"tab": "tab-aaaaaaaa", "hostId": 99}, nil); code != 404 {
		t.Errorf("unknown host: %d", code)
	}
	req2, _ := http.NewRequest("POST", e.ts.URL+"/api/terminal/sessions", strings.NewReader(`tab=tab-aaaaaaaa`))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2, _ := http.DefaultClient.Do(req2)
	if r2.StatusCode != 415 {
		t.Errorf("form body: %d", r2.StatusCode)
	}
	r2.Body.Close()
	// and the socket route is not a plain GET
	r3, _ := http.Get(e.ts.URL + "/api/terminal")
	if r3.StatusCode != 426 {
		t.Errorf("plain GET: %d", r3.StatusCode)
	}
	r3.Body.Close()
}

func TestTerminalSessionClosesOnIdleAndLifetime(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, func(s *Server) { s.TerminalLimits = TerminalLimits{Idle: 300 * time.Millisecond, Lifetime: time.Hour} })
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "VQ$ ")
	if m := readExit(t, c); m["reason"] != "idle timeout" {
		t.Errorf("idle: %v", m)
	}
	// typing keeps a session alive past the idle limit, but not past the lifetime
	e2 := newTerminal(t, true, func(s *Server) {
		s.TerminalLimits = TerminalLimits{Idle: 400 * time.Millisecond, Lifetime: time.Second}
	})
	c2 := e2.connect(t, e2.token(t, "tab-aaaaaaaa", 0))
	stopTyping := make(chan struct{})
	defer close(stopTyping)
	go func() {
		for {
			select {
			case <-stopTyping:
				return
			case <-time.After(100 * time.Millisecond):
				_ = c2.WriteMessage(websocket.BinaryMessage, []byte(" "))
			}
		}
	}()
	for {
		_ = c2.SetReadDeadline(time.Now().Add(10 * time.Second))
		typ, data, err := c2.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.TextMessage {
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["reason"] != "maximum session length reached" {
				t.Errorf("lifetime: %v", m)
			}
			break
		}
	}
}

func TestTerminalFrameAndResizeBounds(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	// an oversized input frame ends the session
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "VQ$ ")
	_ = c.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte("x"), termMaxInput+1))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second)) // the library answers an oversized frame with close code 1009
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
				t.Errorf("big frame: %v", err)
			}
			break
		}
	}
	// a frame at the limit is fine
	c = e.connect(t, e.token(t, "tab-bbbbbbbb", 0))
	readUntil(t, c, "VQ$ ")
	if err := c.WriteMessage(websocket.BinaryMessage, append(bytes.Repeat([]byte("x"), termMaxInput-1), '\n')); err != nil {
		t.Fatal(err)
	}
	// resize outside the bounds, unknown control messages and oversized control frames end the session
	for i, msg := range []string{
		`{"type":"resize","cols":0,"rows":10}`,
		`{"type":"resize","cols":10,"rows":0}`,
		`{"type":"resize","cols":` + strconv.Itoa(termMaxCols+1) + `,"rows":10}`,
		`{"type":"resize","cols":10,"rows":` + strconv.Itoa(termMaxRows+1) + `}`,
		`{"type":"resize","cols":-5,"rows":10}`,
		`{"type":"run","cmd":"id"}`,
		`not json`,
		`{"type":"resize","cols":10,"rows":10,"pad":"` + strings.Repeat("p", termMaxControl) + `"}`,
	} {
		c := e.connect(t, e.token(t, "tab-cccc"+strconv.Itoa(1000+i), 0))
		readUntil(t, c, "VQ$ ")
		_ = c.WriteMessage(websocket.TextMessage, []byte(msg))
		if m := readExit(t, c); m["reason"] != "invalid control message" {
			t.Errorf("%q: %v", msg[:min(len(msg), 40)], m)
		}
	}
	// the largest allowed size is accepted
	c = e.connect(t, e.token(t, "tab-dddddddd", 0))
	readUntil(t, c, "VQ$ ")
	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":`+strconv.Itoa(termMaxCols)+`,"rows":`+strconv.Itoa(termMaxRows)+`}`))
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("stty size\n"))
	readUntil(t, c, strconv.Itoa(termMaxRows)+" "+strconv.Itoa(termMaxCols))
}

func TestTerminalOneSessionPerTabAndPerToken(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	first := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, first, "VQ$ ")
	// a new session for the same tab replaces the old one; the old one is told why
	second := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	if m := readExit(t, first); !strings.Contains(m["reason"].(string), "replaced") {
		t.Errorf("first session: %v", m)
	}
	readUntil(t, second, "VQ$ ")
	// another tab is independent
	other := e.connect(t, e.token(t, "tab-bbbbbbbb", 0))
	readUntil(t, other, "VQ$ ")
	_ = second.WriteMessage(websocket.BinaryMessage, []byte("echo alive\n"))
	readUntil(t, second, "alive")
	// a second token for the same tab invalidates a token that was not used yet
	t1 := e.token(t, "tab-cccccccc", 0)
	_ = e.token(t, "tab-cccccccc", 0)
	_, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + t1}, nil)
	if err == nil || status(resp) != 401 {
		t.Errorf("superseded token: %v %d", err, status(resp))
	}
}

func TestTerminalCapsOpenSessions(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, func(s *Server) { s.TerminalLimits.MaxSessions = 2 })
	for _, tab := range []string{"tab-aaaaaaaa", "tab-bbbbbbbb"} {
		readUntil(t, e.connect(t, e.token(t, tab, 0)), "VQ$ ")
	}
	_, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + e.token(t, "tab-cccccccc", 0)}, nil)
	if err == nil || status(resp) != 429 {
		t.Errorf("third session: %v %d", err, status(resp))
	}
}

func TestTerminalAuditHasNoContent(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "VQ$ ")
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("echo SECRET-KEYSTROKES-12345\n"))
	readUntil(t, c, "SECRET-KEYSTROKES-12345\r\n")
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("exit\n"))
	readExit(t, c)
	var es []audit.Entry
	for i := 0; i < 100; i++ { // the close entry is written just after the exit message
		es, _ = e.au.List(workspace.Local, "terminal", 0, 10)
		if len(es) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(es) != 2 {
		t.Fatalf("audit entries: %+v", es)
	}
	closed, opened := es[0], es[1] // newest first
	if opened.Action != "terminal session opened" || opened.Detail != "this computer" {
		t.Errorf("open entry: %+v", opened)
	}
	if closed.Action != "terminal session closed" || !strings.Contains(closed.Detail, "shell exited") || !strings.HasPrefix(closed.Detail, "this computer, ") {
		t.Errorf("close entry (duration, reason): %+v", closed)
	}
	for _, x := range es {
		if strings.Contains(x.Detail, "SECRET") || strings.Contains(x.Detail, "echo") || strings.Contains(x.Detail, "VQ$") {
			t.Errorf("audit holds terminal content: %+v", x)
		}
	}
}

func TestTerminalHostSessionUsesTheConfiguredSSH(t *testing.T) {
	dir := t.TempDir()
	argsFile, sockFile := filepath.Join(dir, "args"), filepath.Join(dir, "sock")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprintf '%s' \"$SSH_AUTH_SOCK\" > " + sockFile + "\necho fake-ssh-ready\nexec cat\n"
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "/run/agent-for-test.sock")
	mgr := hosts.NewManager(ssh, nil)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	var hostID int64
	e := newTerminal(t, true, func(s *Server) { s.Remote = mgr })
	h, err := e.srv.HostDB.Create(hosts.Host{Name: "cluster", Dest: "login.example", Roots: []string{"/data"}})
	if err != nil {
		t.Fatal(err)
	}
	hostID = h.ID
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", hostID))
	readUntil(t, c, "fake-ssh-ready")
	got, _ := os.ReadFile(argsFile)
	args := strings.Fields(string(got))
	if len(args) < 2 || args[0] != "-tt" || args[len(args)-1] != "login.example" || args[len(args)-2] != "--" {
		t.Errorf("ssh arguments: %q", args)
	}
	if !strings.Contains(" "+strings.Join(args, " ")+" ", " -e none ") {
		t.Errorf("ssh escape character not disabled: %q", args)
	}
	for _, a := range args {
		if a == "BatchMode=yes" {
			t.Error("a terminal must be able to prompt")
		}
	}
	if sock, _ := os.ReadFile(sockFile); string(sock) != "/run/agent-for-test.sock" {
		t.Errorf("agent socket not passed on: %q", sock)
	}
	// what is typed reaches the remote side
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("hello-remote\n"))
	readUntil(t, c, "hello-remote")
	_ = c.Close()
	for i := 0; i < 100; i++ {
		if es, _ := e.au.List(workspace.Local, "terminal", hostID, 10); len(es) == 2 {
			if !strings.Contains(es[1].Detail, "host cluster") {
				t.Errorf("audit: %+v", es)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the host session was not audited as opened and closed")
}

func TestTerminalHostIsChosenByIDOnly(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, nil)
	// a free-form string is not a host id; unknown ids are refused before any process starts
	b := `{"tab":"tab-aaaaaaaa","hostId":"-oProxyCommand=evil"}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/terminal/sessions", strings.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Errorf("string host: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if code, _ := e.session(t, map[string]any{"tab": "tab-aaaaaaaa", "hostId": -1}, nil); code != 400 {
		t.Errorf("negative host: %d", code)
	}
}

func TestTerminalClosedPageEndsTheShell(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	e := newTerminal(t, true, func(s *Server) {
		s.TerminalShell = []string{"/bin/sh", "-c", "echo $$ > " + pidFile + "; echo up; exec sleep 600"}
	})
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "up")
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("no pid")
	}
	_ = c.Close()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return
		}
		if data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); strings.Contains(string(data), ") Z ") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error("the shell kept running after the page closed the socket")
}

// An interactive shell gives each job its own process group and a job may ignore SIGHUP: closing the tab must still end it.
func TestTerminalEndsJobsThatIgnoreHUPInTheirOwnGroup(t *testing.T) {
	t.Parallel()
	e := newTerminal(t, true, func(s *Server) {
		s.TerminalShell = []string{"/bin/sh", "-c", "trap '' HUP; PS1='VQ$ ' exec /bin/sh -i"}
	})
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "VQ$ ")
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("sleep 1000 & echo JOB=$!=\n"))
	re := regexp.MustCompile(`JOB=([0-9]+)=`) // the typed line is echoed first, and has no digits
	var out string
	var m []string
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for m == nil {
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("no job pid in %q: %v", out, err)
		}
		out += string(data)
		m = re.FindStringSubmatch(out)
	}
	pid := 0
	if m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	if pid == 0 {
		t.Fatalf("no job pid in %q", out)
	}
	dead := func() bool {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		return err != nil || statIsZombie(b)
	}
	if dead() {
		t.Fatal("the job is not running before the tab closes")
	}
	_ = c.Close()
	for i := 0; i < 300; i++ {
		if dead() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Error("a job that ignores SIGHUP survived the end of its session")
}

// The shell exits by itself and leaves a HUP-ignoring background job behind: the session end still removes it.
func TestTerminalEndsLeftoverJobsAfterTheShellExited(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	e := newTerminal(t, true, func(s *Server) {
		s.TerminalShell = []string{"/bin/sh", "-c", "trap '' HUP; sleep 1000 & echo $! > " + pidFile + "; echo up; exit 0"}
	})
	c := e.connect(t, e.token(t, "tab-aaaaaaaa", 0))
	readUntil(t, c, "up")
	readExit(t, c)
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("no pid")
	}
	for i := 0; i < 300; i++ {
		d, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil || statIsZombie(d) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Error("a background job outlived the shell and its session")
}

func TestStatParsingSurvivesOddCommandNames(t *testing.T) {
	t.Parallel()
	line := []byte("123 (a) b (c) S 1 123 456 0 -1 4194560")
	if got := parseStatSession(line); got != 456 {
		t.Errorf("session %d", got)
	}
	if statIsZombie(line) || !statIsZombie([]byte("1 (x) Z 1 1 1")) {
		t.Error("zombie detection")
	}
	if parseStatSession([]byte("garbage")) != -1 {
		t.Error("garbage accepted")
	}
}

// Concurrent upgrades must not exceed the cap: the check and the registration are one critical section.
func TestTerminalCapHoldsUnderConcurrentUpgrades(t *testing.T) {
	t.Parallel()
	const cap, attempts = 3, 12
	e := newTerminal(t, true, func(s *Server) { s.TerminalLimits.MaxSessions = cap })
	tokens := make([]string, attempts)
	for i := range tokens {
		tokens[i] = e.token(t, fmt.Sprintf("tab-%08d", i), 0)
	}
	var ok, full atomic.Int32
	var wg sync.WaitGroup
	conns := make(chan *websocket.Conn, attempts)
	for _, tok := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, resp, err := e.dial(e.wsURL(), []string{termProtocol, termTokenPrefix + tok}, nil)
			switch {
			case err == nil:
				ok.Add(1)
				conns <- c
			case status(resp) == 429:
				full.Add(1)
			}
		}()
	}
	wg.Wait()
	close(conns)
	for c := range conns {
		defer c.Close()
	}
	if ok.Load() != cap || full.Load() != attempts-cap {
		t.Errorf("accepted %d, refused %d; want %d and %d", ok.Load(), full.Load(), cap, attempts-cap)
	}
}
