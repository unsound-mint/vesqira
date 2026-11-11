package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

// The integrated terminal (ADR 0029, step 2; --enable-terminal). A terminal runs any command as the user, so the route is
// the most dangerous one the app has and is guarded in layers, all here on the server:
//
//  1. mounted only on the loopback owner listener and only with the flag (server.go); the review listener is another package;
//  2. localOnly (loopback Host, no cross-site Sec-Fetch-Site) for the POST and for the upgrade request;
//  3. the upgrade must carry an Origin equal to the page's own, which a foreign page cannot forge from a browser;
//  4. a per-session random token from POST /api/terminal/sessions, offered as a WebSocket subprotocol (never in the URL,
//     which ends up in logs and history), single use, valid for a short window;
//  5. one session per tab, a cap on sessions, an idle limit, a lifetime limit, and bounds on every frame.
//
// Wire format of the socket: binary frames are the terminal's bytes (input from the page, output from the shell); text
// frames are JSON control messages: {"type":"resize","cols":n,"rows":n} from the page, {"type":"exit",...} from the server.
// The audit log records that a session opened and closed (where, how long, why), never input or output.

const (
	termProtocol    = "vesqira.terminal.v1" // the subprotocol the server selects
	termTokenPrefix = "vesqira-token."      // the offered subprotocol that carries the token
	termMaxInput    = 64 << 10              // largest frame accepted from the page
	termOutChunk    = 32 << 10              // largest output frame sent
	termMaxControl  = 256                   // largest text (control) frame
	termMaxCols     = 500
	termMaxRows     = 200
	termMaxPending  = 32
)

var termTabRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// TerminalLimits bound a session. Zero values take the defaults.
type TerminalLimits struct {
	Connect     time.Duration // how long a token stays valid (default 60 s)
	Idle        time.Duration // no input from the page for this long ends the session (default 30 min)
	Lifetime    time.Duration // longest a session runs (default 8 h)
	MaxSessions int           // running sessions at once (default 8)
}

func (l TerminalLimits) get() TerminalLimits {
	if l.Connect <= 0 {
		l.Connect = 60 * time.Second
	}
	if l.Idle <= 0 {
		l.Idle = 30 * time.Minute
	}
	if l.Lifetime <= 0 {
		l.Lifetime = 8 * time.Hour
	}
	if l.MaxSessions <= 0 {
		l.MaxSessions = 8
	}
	return l
}

type termPending struct {
	hostID  int64
	tab     string
	expires time.Time
}

type termLive struct {
	tab  string
	stop func(reason string)
}

type termRegistry struct {
	mu      sync.Mutex
	pending map[[32]byte]termPending // by SHA-256 of the token, so the map never holds a usable token
	live    map[*termLive]struct{}
}

type terminalOpen struct {
	HostID int64  `json:"hostId"` // 0 or absent: a shell on this computer
	Tab    string `json:"tab"`    // chosen by the page, one per browser tab; a new session for the same tab ends the old one
}

// TerminalSession is the answer to POST /api/terminal/sessions.
type TerminalSession struct {
	Token           string `json:"token"`
	Protocol        string `json:"protocol"`
	ExpiresIn       int    `json:"expiresInSeconds"`
	IdleSeconds     int    `json:"idleSeconds"`
	LifetimeSeconds int    `json:"lifetimeSeconds"`
}

func (s *Server) openTerminalSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var in terminalOpen
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || !termTabRE.MatchString(in.Tab) || in.HostID < 0 {
		fail(w, 400, "bad request: a tab id of 8 to 64 letters, digits, _ or - is required")
		return
	}
	if in.HostID != 0 {
		if _, _, err := s.terminalHost(in.HostID); err != nil {
			fail(w, 404, "host not found")
			return
		}
	}
	lim := s.TerminalLimits.get()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		fail(w, 500, "internal error")
		return
	}
	token := hex.EncodeToString(raw[:])
	now := time.Now()
	t := &s.terms
	t.mu.Lock()
	if t.pending == nil {
		t.pending, t.live = map[[32]byte]termPending{}, map[*termLive]struct{}{}
	}
	var replaced []*termLive
	for k, p := range t.pending { // expired tokens, and any earlier token of this tab, are dead
		if now.After(p.expires) || p.tab == in.Tab {
			delete(t.pending, k)
		}
	}
	if len(t.pending) >= termMaxPending {
		t.mu.Unlock()
		fail(w, 429, "too many sessions are waiting to connect")
		return
	}
	for l := range t.live {
		if l.tab == in.Tab {
			replaced = append(replaced, l)
		}
	}
	t.pending[sha256.Sum256([]byte(token))] = termPending{hostID: in.HostID, tab: in.Tab, expires: now.Add(lim.Connect)}
	t.mu.Unlock()
	for _, l := range replaced {
		l.stop("replaced by a newer session in the same tab")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, TerminalSession{Token: token, Protocol: termProtocol, ExpiresIn: int(lim.Connect / time.Second),
		IdleSeconds: int(lim.Idle / time.Second), LifetimeSeconds: int(lim.Lifetime / time.Second)})
}

func (s *Server) terminalHost(id int64) (hosts.Host, *hosts.Manager, error) {
	if s.HostDB == nil || s.Remote == nil {
		return hosts.Host{}, nil, errors.New("hosts are not configured")
	}
	h, err := s.HostDB.Get(id)
	return h, s.Remote, err
}

// sameOrigin is true when the Origin header names the page that is calling: the same scheme-and-host as Host.
// A missing Origin is refused: every browser sends one on a WebSocket handshake, so only a non-browser client omits it.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host && u.Path == "" && u.User == nil
}

// takeToken finds the token among the offered subprotocols and consumes it: a token works once, valid or not.
func (s *Server) takeToken(r *http.Request) (termPending, bool) {
	var tok string
	proto := false
	for _, p := range websocket.Subprotocols(r) {
		switch {
		case p == termProtocol:
			proto = true
		case strings.HasPrefix(p, termTokenPrefix) && tok == "":
			tok = strings.TrimPrefix(p, termTokenPrefix)
		default:
			return termPending{}, false
		}
	}
	if !proto || len(tok) != 64 {
		return termPending{}, false
	}
	key := sha256.Sum256([]byte(tok))
	t := &s.terms
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pending[key]
	delete(t.pending, key)
	if !ok || time.Now().After(p.expires) {
		return termPending{}, false
	}
	return p, true
}

func (s *Server) terminalSocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		fail(w, 426, "a WebSocket upgrade is required")
		return
	}
	if !sameOrigin(r) {
		fail(w, 403, "cross-origin request refused")
		return
	}
	if r.URL.RawQuery != "" { // a token (or anything else) in the URL would end up in logs and history
		fail(w, 400, "no query parameters are accepted; the session token travels in the WebSocket subprotocol")
		return
	}
	p, ok := s.takeToken(r)
	if !ok {
		fail(w, 401, "missing, wrong, used or expired session token")
		return
	}
	lim := s.TerminalLimits.get()
	cmd, where, err := s.terminalCommand(p.hostID)
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	t := &s.terms
	t.mu.Lock()
	full := len(t.live) >= lim.MaxSessions
	t.mu.Unlock()
	if full {
		fail(w, 429, "too many terminal sessions are open")
		return
	}
	up := websocket.Upgrader{Subprotocols: []string{termProtocol}, ReadBufferSize: 4 << 10, WriteBufferSize: 4 << 10,
		CheckOrigin: func(*http.Request) bool { return true }} // checked above
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has answered
	}
	s.runTerminal(conn, cmd, where, p, lim)
}

// terminalCommand builds the command: the user's login shell here, or interactive ssh to a stored host.
func (s *Server) terminalCommand(hostID int64) (*exec.Cmd, string, error) {
	var cmd *exec.Cmd
	where := "this computer"
	if hostID != 0 {
		h, m, err := s.terminalHost(hostID)
		if err != nil {
			return nil, "", errors.New("host not found")
		}
		bin, args, env, err := m.TerminalCommand(h)
		if err != nil {
			return nil, "", errors.New("host cannot be used for a terminal")
		}
		cmd = exec.Command(bin, args...)
		cmd.Env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
		where = "host " + h.Name
	} else {
		argv := s.TerminalShell
		if len(argv) == 0 {
			sh := os.Getenv("SHELL")
			if st, err := os.Stat(sh); err != nil || !strings.HasPrefix(sh, "/") || st.IsDir() || st.Mode()&0o111 == 0 {
				sh = "/bin/sh"
			}
			argv = []string{sh, "-l"}
		}
		cmd = exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
	}
	return cmd, where, nil
}

type termControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) runTerminal(conn *websocket.Conn, cmd *exec.Cmd, where string, p termPending, lim TerminalLimits) {
	started := time.Now()
	var wmu sync.Mutex // gorilla allows one writer at a time
	send := func(typ int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(typ, data)
	}
	exitMsg := func(reason string, code *int) {
		b, _ := json.Marshal(map[string]any{"type": "exit", "reason": reason, "code": code})
		_ = send(websocket.TextMessage, b)
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		log.Printf("terminal: start: %v", err)
		exitMsg("could not start the shell", nil)
		_ = conn.Close()
		return
	}
	s.Audit.Record("terminal session opened", "terminal", p.hostID, where)

	stop := make(chan struct{})
	var reason string
	var once sync.Once
	finish := func(r string) { once.Do(func() { reason = r; close(stop) }) }
	live := &termLive{tab: p.tab, stop: finish}
	s.terms.mu.Lock()
	if s.terms.live == nil {
		s.terms.pending, s.terms.live = map[[32]byte]termPending{}, map[*termLive]struct{}{}
	}
	s.terms.live[live] = struct{}{}
	s.terms.mu.Unlock()

	exited := make(chan struct{}) // the process has been waited for
	var exitCode atomic.Int64
	exitCode.Store(-2)
	go func() {
		_ = cmd.Wait()
		exitCode.Store(int64(cmd.ProcessState.ExitCode()))
		close(exited)
	}()

	ptyDone := make(chan struct{}) // output pump finished: the shell closed its side
	go func() {
		defer close(ptyDone)
		buf := make([]byte, termOutChunk)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if send(websocket.BinaryMessage, buf[:n]) != nil {
					finish("client closed")
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { // the shell ended by itself: let the last output through, then say so
		select {
		case <-exited:
		case <-ptyDone:
			<-exited
		}
		select {
		case <-ptyDone:
		case <-time.After(300 * time.Millisecond): // a background process may still hold the terminal open
		}
		finish("shell exited")
	}()

	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())
	const pongWait = 60 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })
	conn.SetReadLimit(termMaxInput)
	go func() { // page to shell
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				if errors.Is(err, websocket.ErrReadLimit) {
					finish("input frame too large")
				} else {
					finish("client closed")
				}
				return
			}
			switch typ {
			case websocket.BinaryMessage:
				lastInput.Store(time.Now().UnixNano())
				if _, err := ptmx.Write(data); err != nil {
					return
				}
			case websocket.TextMessage:
				var c termControl
				if len(data) > termMaxControl || json.Unmarshal(data, &c) != nil || c.Type != "resize" ||
					c.Cols < 1 || c.Cols > termMaxCols || c.Rows < 1 || c.Rows > termMaxRows {
					finish("invalid control message")
					return
				}
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(c.Rows), Cols: uint16(c.Cols)})
			}
		}
	}()

	go func() { // limits and keep-alive
		tick := min(lim.Idle, lim.Lifetime) / 10
		tick = min(max(tick, 10*time.Millisecond), 5*time.Second)
		tk := time.NewTicker(tick)
		defer tk.Stop()
		lastPing := time.Now()
		for {
			select {
			case <-stop:
				return
			case now := <-tk.C:
				switch {
				case now.Sub(started) >= lim.Lifetime:
					finish("maximum session length reached")
					return
				case now.Sub(time.Unix(0, lastInput.Load())) >= lim.Idle:
					finish("idle timeout")
					return
				case now.Sub(lastPing) >= 20*time.Second:
					lastPing = now
					wmu.Lock()
					_ = conn.WriteControl(websocket.PingMessage, nil, now.Add(5*time.Second))
					wmu.Unlock()
				}
			}
		}
	}()

	<-stop
	terminate(cmd, ptmx, exited)
	<-ptyDone
	var code *int
	if c := int(exitCode.Load()); reason == "shell exited" && c >= 0 {
		code = &c
	}
	exitMsg(reason, code)
	wmu.Lock()
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	wmu.Unlock()
	_ = conn.Close()

	s.terms.mu.Lock()
	delete(s.terms.live, live)
	s.terms.mu.Unlock()
	s.Audit.Record("terminal session closed", "terminal", p.hostID, fmt.Sprintf("%s, %s, %s", where, time.Since(started).Round(time.Second), reason))
}

// terminate ends the shell and everything it started in its own session: hang up (as closing a terminal window does),
// give it a moment, then kill the process group. It returns when the process has been waited for.
func terminate(cmd *exec.Cmd, ptmx io.Closer, exited <-chan struct{}) {
	pgid := cmd.Process.Pid // pty.Start makes the shell a session leader, so its process group id is its pid
	select {
	case <-exited: // already waited for: the pid may be reused, so signal nothing
		_ = ptmx.Close()
		return
	default:
	}
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	_ = ptmx.Close()
	select {
	case <-exited:
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-exited
}
