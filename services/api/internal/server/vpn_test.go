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
	e.srv = &Server{Roots: rs, Store: st, Worker: &fakeWorker{}, Audit: e.au, HostDB: hs, Remote: mgr, VPNEnabled: enabled}
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
	if !strings.Contains(a, "host vpn connect | Cluster: exit 0 after") || strings.Contains(a, "SECRET-OUTPUT-TOKEN") || strings.Contains(a, "touch") {
		t.Fatalf("audit: %s", a)
	}
}

func TestVPNConnectThatLeavesTheHostUnreachableSaysSo(t *testing.T) {
	e := newVPN(t, true)
	fail := e.script(t, "fail", `echo "auth failed" >&2; exit 3`)
	h := e.addHost(t, []string{fail}, []string{"true"})
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["exitCode"] != float64(3) || m["reachable"] != false ||
		m["message"] != "VPN command failed (exit 3); the host is still not reachable" || m["output"] != "auth failed\n" {
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
	code, m = e.vpn(t, h, map[string]string{"action": "disconnect"})
	if code != 200 || m["exitCode"] != float64(0) || e.pings.Load() != 2 {
		t.Fatalf("%d %v pings %d", code, m, e.pings.Load())
	}
}

// A successful connect makes the job watcher drop its backoff for that host and wake up at once.
func TestVPNConnectKicksTheJobWatcher(t *testing.T) {
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

func TestVPNTimeoutKillsTheWholeProcessTree(t *testing.T) {
	e := newVPN(t, true)
	e.srv.VPNTimeout = 400 * time.Millisecond
	pidFile := filepath.Join(e.dir, "child.pid")
	hang := e.script(t, "hang", `sleep 60 & echo $! > `+pidFile+`; echo started; wait`)
	h := e.addHost(t, []string{hang}, nil)
	start := time.Now()
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["timedOut"] != true || m["reachable"] != false {
		t.Fatalf("%d %v", code, m)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the timeout took %s", d)
	}
	if !strings.Contains(m["message"].(string), "did not finish within") || !strings.Contains(m["output"].(string), "started") {
		t.Fatalf("%v", m)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, c := range strings.TrimSpace(string(b)) {
		pid = pid*10 + int(c-'0')
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && !isZombie(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the child %d outlived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a := e.auditText(t); !strings.Contains(a, "Cluster: timed out after") {
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
	e := newVPN(t, true)
	h := e.addHost(t, []string{"definitely-not-a-vpn-client-xyz", "connect"}, nil)
	code, m := e.vpn(t, h, map[string]string{"action": "connect"})
	if code != 200 || m["message"] != "VPN command could not be started; the host is still not reachable" || !strings.Contains(m["output"].(string), "definitely-not-a-vpn-client-xyz") {
		t.Fatalf("%d %v", code, m)
	}
	if a := e.auditText(t); !strings.Contains(a, "Cluster: did not start after") {
		t.Fatalf("audit: %s", a)
	}
}
