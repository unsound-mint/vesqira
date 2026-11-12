package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"encoding/json"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type vpnEnv struct {
	ts    *httptest.Server
	srv   *Server
	au    *audit.Store
	dir   string
	pings atomic.Int32
	up    atomic.Bool // what the stand-in host check answers
}

// newVPN starts the owner API with the VPN companion on (or off). The host check is a stand-in that counts its calls.
func newVPN(t *testing.T, enabled bool) *vpnEnv {
	t.Helper()
	e := &vpnEnv{dir: t.TempDir()}
	root := filepath.Join(e.dir, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(e.dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if e.au, err = audit.Open(st.DB()); err != nil {
		t.Fatal(err)
	}
	hs, err := hosts.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	mgr := hosts.NewManager("false", nil)
	mgr.ControlDir = t.TempDir()
	e.srv = &Server{Roots: rs, Store: st, Worker: &fakeWorker{}, Audit: e.au, HostDB: hs, Remote: mgr, VPNEnabled: enabled, VPNPoll: 200 * time.Millisecond, VPNLogDir: filepath.Join(e.dir, "vpnlogs")}
	e.srv.pingHook = func(context.Context, hosts.Host, map[string]any) (worker.Reply, error) {
		e.pings.Add(1)
		if e.up.Load() {
			return worker.Reply{Head: []byte(`{"python":"3","astropy":"6","numpy":"2","roots":null,"enforces_roots":true}`)}, nil
		}
		return worker.Reply{}, errors.New("ssh: connection timed out")
	}
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// script writes an executable sh script and returns its path.
func (e *vpnEnv) script(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *vpnEnv) addHost(t *testing.T, connect, disconnect []string) hosts.Host {
	t.Helper()
	in := map[string]any{"name": "Cluster", "dest": "cluster", "roots": []string{"/data"}}
	if connect != nil {
		in["vpnCommand"] = connect
	}
	if disconnect != nil {
		in["vpnDisconnectCommand"] = disconnect
	}
	var h hosts.Host
	if code := postJSONStatus(t, e.ts.URL+"/api/hosts", in, &h); code != 201 {
		t.Fatalf("add host: %d", code)
	}
	return h
}

func (e *vpnEnv) vpn(t *testing.T, h hosts.Host, body any) (int, map[string]any) {
	t.Helper()
	r, b := postJSON(t, e.ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/vpn", body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

// waitAudit waits for the audit log to contain want (the exit of a client is recorded when its process ends) and returns it.
func (e *vpnEnv) waitAudit(t *testing.T, want string) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if a := e.auditText(t); strings.Contains(a, want) {
			return a
		}
		time.Sleep(25 * time.Millisecond)
	}
	return e.auditText(t)
}

func (e *vpnEnv) auditText(t *testing.T) string {
	t.Helper()
	es, err := e.au.List("", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, x := range es {
		b.WriteString(x.Action + " | " + x.Detail + "\n")
	}
	return b.String()
}

// Every element reaches the program exactly as stored: a ";" or "$HOME" inside one is text, not shell syntax, and nothing
// is created by it.
func TestVPNRunsTheStoredArgvWithoutAShell(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	echo := e.script(t, "echo-argv", `for a in "$@"; do printf '[%s]\n' "$a"; done; echo SECRET-OUTPUT-TOKEN`)
	marker := filepath.Join(e.dir, "pwned")
	argv := []string{echo, "connect;touch " + marker, "$HOME", "`id`", "two words", "--flag=a&b|c>d"}
	h := e.addHost(t, argv, nil)
	if len(h.VPNCommand) != len(argv) || h.VPNCommand[1] != argv[1] {
		t.Fatalf("not stored verbatim: %q", h.VPNCommand)
	}
	e.up.Store(true)
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	want := "[connect;touch " + marker + "]\n[$HOME]\n[`id`]\n[two words]\n[--flag=a&b|c>d]\nSECRET-OUTPUT-TOKEN\n"
	if m["output"] != want {
		t.Fatalf("the program received other arguments than stored:\n got %q\nwant %q", m["output"], want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a shell interpreted the argument")
	}
	if m["message"] != "VPN command finished; the host is reachable" || m["reachable"] != true || m["exitCode"] != float64(0) {
		t.Fatalf("%v", m)
	}
	if got := e.pings.Load(); got != 1 {
		t.Fatalf("the reachability check ran %d times, want once", got)
	}
	a := e.auditText(t)
	if !strings.Contains(e.waitAudit(t, "host vpn client exit | Cluster: exit 0 after"), "host vpn client exit | Cluster: exit 0 after") ||
		!strings.Contains(a, "host vpn connect | Cluster: client started (pid") || strings.Contains(a, "SECRET-OUTPUT-TOKEN") || strings.Contains(a, "touch") {
		t.Fatalf("audit: %s", a)
	}
}

func TestVPNConnectThatLeavesTheHostUnreachableSaysSo(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	fail := e.script(t, "fail", `echo "auth failed" >&2; exit 3`)
	h := e.addHost(t, []string{fail}, []string{"true"})
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["exitCode"] != float64(3) || m["reachable"] != false ||
		m["message"] != "VPN client exited with code 3; the host is still not reachable" || m["output"] != "auth failed\n" {
		t.Fatalf("%d %v", code, m)
	}
	// the host list tells what was learned, so the page shows "not reachable"
	_, b := get(t, e.ts.URL+"/api/hosts")
	var l struct {
		Hosts []struct {
			Reachable string `json:"reachable"`
		} `json:"hosts"`
	}
	_ = json.Unmarshal(b, &l)
	if len(l.Hosts) != 1 || l.Hosts[0].Reachable != "no" {
		t.Fatalf("%s", b)
	}
	// disconnect runs the other command and checks again
	before := e.pings.Load()
	code, m = e.vpn(t, h, map[string]string{"action": "disconnect"})
	if code != 200 || m["exitCode"] != float64(0) || e.pings.Load() != before+1 {
		t.Fatalf("%d %v pings %d", code, m, e.pings.Load())
	}
}

// A successful connect makes the job watcher drop its backoff for that host and wake up at once.
func TestVPNConnectKicksTheJobWatcher(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	h := e.addHost(t, []string{"true"}, nil)
	e.up.Store(true)
	if code, _ := e.vpn(t, h, map[string]string{"action": "connect"}); code != 200 {
		t.Fatal(code)
	}
	select {
	case <-e.srv.reach.wakeCh():
	case <-time.After(time.Second):
		t.Fatal("the watcher was not woken")
	}
	if !e.srv.reach.takeKick(h.ID) || e.srv.reach.takeKick(h.ID) {
		t.Fatal("the kick must be taken exactly once")
	}
	// a failed connect does not
	e2 := newVPN(t, true)
	h2 := e2.addHost(t, []string{"false"}, nil)
	e2.up.Store(true)
	e2.vpn(t, h2, map[string]string{"action": "connect"})
	if e2.srv.reach.takeKick(h2.ID) {
		t.Fatal("a failed command kicked the watcher")
	}
}

func TestVPNPageCanOnlySayConnectOrDisconnect(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	marker := filepath.Join(e.dir, "ran")
	prog := e.script(t, "mark", `touch `+marker)
	h := e.addHost(t, []string{prog}, nil)
	for name, body := range map[string]any{
		"extra argv":     map[string]any{"action": "connect", "argv": []string{"/bin/sh", "-c", "id"}},
		"extra command":  map[string]any{"action": "connect", "vpnCommand": []string{"/bin/true"}},
		"extra args":     map[string]any{"action": "connect", "args": []string{"x"}},
		"no body fields": map[string]any{},
		"not a string":   map[string]any{"action": 1},
	} {
		if code, _ := e.vpn(t, h, body); code != 400 && code != 422 {
			t.Errorf("%s: %d, want a refusal", name, code)
		}
	}
	for _, a := range []string{"", "run", "Connect", "connect ", "status"} {
		if code, _ := e.vpn(t, h, map[string]string{"action": a}); code != 422 {
			t.Errorf("action %q: %d, want 422", a, code)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused request still ran the command")
	}
	// no disconnect command stored: refused, nothing run
	if code, m := e.vpn(t, h, map[string]string{"action": "disconnect"}); code != 409 {
		t.Fatalf("%d %v", code, m)
	}
	if e.pings.Load() != 0 {
		t.Fatal("a refused request checked the host")
	}
}

func TestVPNIsOffUnlessStartedWithTheFlagAndUnlessTheHostHasACommand(t *testing.T) {
	t.Parallel()
	off := newVPN(t, false)
	if r, _ := postJSON(t, off.ts.URL+"/api/hosts", map[string]any{"name": "x", "dest": "x", "roots": []string{"/d"}, "vpnCommand": []string{"true"}}); r.StatusCode != 403 {
		t.Fatalf("a VPN command was accepted with the feature off: %d", r.StatusCode)
	}
	h := off.addHost(t, nil, nil)
	for _, p := range []string{"/vpn", "/vpn-config"} {
		r, _ := postJSON(t, off.ts.URL+"/api/hosts/"+itoa(int(h.ID))+p, map[string]string{"action": "connect"})
		if r.StatusCode != 404 && r.StatusCode != 405 {
			t.Errorf("POST %s with the feature off: %d", p, r.StatusCode)
		}
	}
	_, b := get(t, off.ts.URL+"/api/capabilities")
	if !strings.Contains(string(b), `"vpn":false`) && !strings.Contains(string(b), `"vpn": false`) {
		t.Errorf("capabilities: %s", b)
	}
	on := newVPN(t, true)
	h2 := on.addHost(t, nil, nil)
	if code, _ := on.vpn(t, h2, map[string]string{"action": "connect"}); code != 409 {
		t.Fatalf("a host without a command ran something: %d", code)
	}
}

func TestVPNCommandsAreValidatedWhenStored(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	h := e.addHost(t, nil, nil)
	long := strings.Repeat("a", hosts.MaxVPNArgLen+1)
	many := make([]string, hosts.MaxVPNArgs+1)
	for i := range many {
		many[i] = "x"
	}
	for name, cmd := range map[string]any{
		"empty program":    []string{""},
		"oversized part":   []string{"gpclient", long},
		"too many parts":   many,
		"relative program": []string{"./gpclient"},
		"program in a dir": []string{"bin/gpclient"},
		"dash program":     []string{"-gpclient"},
		"unclean path":     []string{"/usr/bin/../bin/x"},
		"shell line":       []string{"gpclient connect; id"},
		"line break":       []string{"gpclient", "a\nb"},
		"NUL":              []string{"gpclient", "a\x00b"},
	} {
		r, b := postJSON(t, e.ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/vpn-config", map[string]any{"vpnCommand": cmd})
		if r.StatusCode != 422 {
			t.Errorf("%s: %d %s, want 422", name, r.StatusCode, b)
		}
		if r, b := postJSON(t, e.ts.URL+"/api/hosts", map[string]any{"name": "N-" + name, "dest": "ok", "roots": []string{"/d"}, "vpnDisconnectCommand": cmd}); r.StatusCode != 422 {
			t.Errorf("%s on create: %d %s, want 422", name, r.StatusCode, b)
		}
	}
	for name, body := range map[string]string{
		"not strings":   `{"vpnCommand":["gpclient",3]}`,
		"an object":     `{"vpnCommand":{"a":"b"}}`,
		"a string":      `{"vpnCommand":"gpclient connect x"}`,
		"unknown field": `{"vpnCommand":["gpclient"],"shell":true}`,
	} {
		if code, b := postRaw(t, e.ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/vpn-config", body); code != 400 {
			t.Errorf("%s: %d %s, want 400", name, code, b)
		}
	}
	// valid forms: bare name, absolute path, arguments with spaces and metacharacters
	for _, cmd := range [][]string{{"gpclient", "connect", "vpn.example.edu"}, {"/usr/bin/openconnect", "--user=me", "--protocol=gp", "vpn.example.edu"}, {"pkexec", "/usr/bin/gpclient", "connect;x y"}} {
		if code := postJSONStatus(t, e.ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/vpn-config", map[string]any{"vpnCommand": cmd}, nil); code != 200 {
			t.Errorf("%q refused: %d", cmd, code)
		}
	}
	// clearing
	var cleared hosts.Host
	if code := postJSONStatus(t, e.ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/vpn-config", map[string]any{}, &cleared); code != 200 || len(cleared.VPNCommand) != 0 {
		t.Fatalf("%d %+v", code, cleared)
	}
	if a := e.auditText(t); !strings.Contains(a, "host vpn commands | Cluster: connect cleared, disconnect cleared") || strings.Contains(a, "pkexec") {
		t.Fatalf("audit: %s", a)
	}
}

func postRaw(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func readPid(t *testing.T, file string) int {
	t.Helper()
	var b []byte
	var err error
	for i := 0; i < 200; i++ {
		if b, err = os.ReadFile(file); err == nil && strings.HasSuffix(string(b), "\n") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, c := range strings.TrimSpace(string(b)) {
		pid = pid*10 + int(c-'0')
	}
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil && !isZombie(pid) }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// foregroundClient is a stand-in for `gpclient connect`: it prints a line and stays in the foreground, with a helper child,
// until it is stopped. Its pid and its child's pid are written to files.
func (e *vpnEnv) foregroundClient(t *testing.T) (prog, clientPid, childPid string) {
	t.Helper()
	clientPid, childPid = filepath.Join(e.dir, "client.pid"), filepath.Join(e.dir, "child.pid")
	prog = e.script(t, "fg", `echo $$ > `+clientPid+`; sleep 300 & echo $! > `+childPid+`; echo "tunnel is up"; wait`)
	t.Cleanup(func() { // never leave a test client behind
		for _, f := range []string{clientPid, childPid} {
			if b, err := os.ReadFile(f); err == nil {
				pid := 0
				for _, c := range strings.TrimSpace(string(b)) {
					pid = pid*10 + int(c-'0')
				}
				if pid > 1 {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	return
}

// A client that stays in the foreground is not killed when the wait ends: it is the tunnel. The answer says so, the host list
// shows it running with its output, and disconnect without a disconnect command ends its whole process group.
func TestVPNForegroundClientSurvivesTheWaitAndDisconnectStopsIt(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	e.srv.VPNTimeout = 600 * time.Millisecond
	prog, clientPid, childPid := e.foregroundClient(t)
	h := e.addHost(t, []string{prog}, nil)
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["timedOut"] != true || m["reachable"] != false || !strings.HasPrefix(m["message"].(string), "VPN client is running but the host is still not reachable after ") ||
		!strings.Contains(m["output"].(string), "tunnel is up") {
		t.Fatalf("%d %v", code, m)
	}
	cp, chp := readPid(t, clientPid), readPid(t, childPid)
	if !alive(cp) || !alive(chp) {
		t.Fatal("the client was killed when the wait ended")
	}
	if pg, err := syscall.Getpgid(cp); err != nil || pg != cp { // own session and group
		t.Fatalf("the client is not in its own process group: %d %v", pg, err)
	}
	// the host list shows it
	_, b := get(t, e.ts.URL+"/api/hosts")
	var l struct {
		Hosts []struct {
			VPNClient *struct {
				Running bool   `json:"running"`
				Pid     int    `json:"pid"`
				Output  string `json:"output"`
			} `json:"vpnClient"`
		} `json:"hosts"`
	}
	_ = json.Unmarshal(b, &l)
	if len(l.Hosts) != 1 || l.Hosts[0].VPNClient == nil || !l.Hosts[0].VPNClient.Running || l.Hosts[0].VPNClient.Pid != cp || !strings.Contains(l.Hosts[0].VPNClient.Output, "tunnel is up") {
		t.Fatalf("%s", b)
	}
	// the log is private
	if fi, err := os.Stat(filepath.Join(e.dir, "vpnlogs", "host-"+itoa(int(h.ID))+".log")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log: %v %v", fi, err)
	}
	// a second connect is refused while it runs
	if code, m := e.vpn(t, h, map[string]string{"action": "connect"}); code != 409 || !strings.Contains(m["error"].(string), "already running") {
		t.Fatalf("%d %v", code, m)
	}
	// disconnect, with no disconnect command stored, ends the group
	code, m = e.vpn(t, h, map[string]string{"action": "disconnect"})
	if code != 200 || m["message"] != "VPN client stopped; the host is still not reachable" {
		t.Fatalf("%d %v", code, m)
	}
	waitDead(t, cp)
	waitDead(t, chp)
	a := e.waitAudit(t, "host vpn client exit")
	for _, want := range []string{"host vpn connect | Cluster: client started (pid", "host vpn disconnect | Cluster: client (pid", "stopped with SIGTERM", "host vpn client exit | Cluster: exit -1 after"} {
		if !strings.Contains(a, want) {
			t.Errorf("audit lacks %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "tunnel is up") {
		t.Fatalf("client output reached the audit log: %s", a)
	}
	// nothing left to stop
	if code, _ := e.vpn(t, h, map[string]string{"action": "disconnect"}); code != 409 {
		t.Fatalf("disconnect with nothing running: %d", code)
	}
}

// The host becoming reachable while the client keeps running is the success the page reports, with the seconds it took.
func TestVPNConnectReportsWhenTheHostBecomesReachable(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	e.srv.VPNTimeout = 10 * time.Second
	prog, clientPid, _ := e.foregroundClient(t)
	h := e.addHost(t, []string{prog}, nil)
	go func() {
		time.Sleep(500 * time.Millisecond)
		e.up.Store(true)
	}()
	start := time.Now()
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["reachable"] != true || m["timedOut"] != false || !strings.HasPrefix(m["message"].(string), "VPN client started; the host became reachable after ") || !strings.HasSuffix(m["message"].(string), " s") {
		t.Fatalf("%d %v", code, m)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if !alive(readPid(t, clientPid)) {
		t.Fatal("the client was stopped")
	}
	if !e.srv.reach.takeKick(h.ID) {
		t.Fatal("the watcher was not kicked")
	}
}

// With a disconnect command stored, that command is what disconnect runs; the client is not signalled by Vesqira.
func TestVPNDisconnectCommandIsUsedWhenConfigured(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	e.srv.VPNTimeout = 500 * time.Millisecond
	prog, clientPid, _ := e.foregroundClient(t)
	marker := filepath.Join(e.dir, "disconnected")
	dis := e.script(t, "dis", `echo "bye" ; touch `+marker)
	h := e.addHost(t, []string{prog}, []string{dis})
	if code, _ := e.vpn(t, h, map[string]string{"action": "connect"}); code != 200 {
		t.Fatal(code)
	}
	cp := readPid(t, clientPid)
	e.srv.VPNTimeout = 5 * time.Second
	code, m := e.vpn(t, h, map[string]string{"action": "disconnect"})
	if code != 200 || m["output"] != "bye\n" || m["exitCode"] != float64(0) {
		t.Fatalf("%d %v", code, m)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the disconnect command did not run")
	}
	if !alive(cp) {
		t.Fatal("Vesqira signalled the client although a disconnect command is configured")
	}
	if a := e.auditText(t); !strings.Contains(a, "host vpn disconnect | Cluster: command exit 0 after") || strings.Contains(a, "bye") {
		t.Fatalf("audit: %s", a)
	}
}

// A disconnect command that hangs is still bounded.
func TestVPNDisconnectCommandIsBounded(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	e.srv.VPNTimeout = 400 * time.Millisecond
	pidFile := filepath.Join(e.dir, "hang.pid")
	hang := e.script(t, "hang", `sleep 60 & echo $! > `+pidFile+`; wait`)
	h := e.addHost(t, []string{"true"}, []string{hang})
	code, m := e.vpn(t, h, map[string]string{"action": "disconnect"})
	if code != 200 || m["timedOut"] != true || !strings.Contains(m["message"].(string), "did not finish within") {
		t.Fatalf("%d %v", code, m)
	}
	waitDead(t, readPid(t, pidFile))
	if a := e.auditText(t); !strings.Contains(a, "Cluster: command timed out after") {
		t.Fatalf("audit: %s", a)
	}
}

func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] == 'Z'
}

func TestVPNOneRunAtATimePerHost(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	started, gate := filepath.Join(e.dir, "started"), filepath.Join(e.dir, "gate")
	slow := e.script(t, "slow", `touch `+started+`; while [ ! -e `+gate+` ]; do sleep 0.02; done; echo done`)
	h := e.addHost(t, []string{slow}, []string{"true"})
	other := e.addHostNamed(t, "Other", []string{"true"})
	type res struct {
		code int
		m    map[string]any
	}
	first := make(chan res, 1)
	go func() {
		c, m := e.vpn(t, h, map[string]string{"action": "connect"})
		first <- res{c, m}
	}()
	for i := 0; ; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("the command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, a := range []string{"connect", "disconnect"} { // the other direction is also refused while one runs
		if code, m := e.vpn(t, h, map[string]string{"action": a}); code != 409 || !strings.Contains(m["error"].(string), "already running") {
			t.Fatalf("%s while running: %d %v", a, code, m)
		}
	}
	if code, _ := e.vpn(t, other, map[string]string{"action": "connect"}); code != 200 { // another host is independent
		t.Fatalf("another host was blocked: %d", code)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := <-first
	if r.code != 200 || r.m["output"] != "done\n" {
		t.Fatalf("%d %v", r.code, r.m)
	}
	if code, _ := e.vpn(t, h, map[string]string{"action": "disconnect"}); code != 200 { // released
		t.Fatalf("the host stayed locked: %d", code)
	}
}

func (e *vpnEnv) addHostNamed(t *testing.T, name string, connect []string) hosts.Host {
	t.Helper()
	var h hosts.Host
	if code := postJSONStatus(t, e.ts.URL+"/api/hosts", map[string]any{"name": name, "dest": "other", "roots": []string{"/data"}, "vpnCommand": connect}, &h); code != 201 {
		t.Fatalf("add host: %d", code)
	}
	return h
}

func TestVPNProgramThatDoesNotStartIsReported(t *testing.T) {
	t.Parallel()
	e := newVPN(t, true)
	h := e.addHost(t, []string{"definitely-not-a-vpn-client-xyz", "connect"}, nil)
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["message"] != "VPN command could not be started; the host is still not reachable" || !strings.Contains(m["output"].(string), "definitely-not-a-vpn-client-xyz") {
		t.Fatalf("%d %v", code, m)
	}
	if a := e.auditText(t); !strings.Contains(a, "host vpn connect | Cluster: did not start") {
		t.Fatalf("audit: %s", a)
	}
}
