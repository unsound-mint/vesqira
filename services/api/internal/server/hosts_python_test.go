package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

func (e *vpnEnv) call(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	r, b := postJSON(t, e.ts.URL+path, body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

// A failed check says which part failed: ssh is 502 (unreachable), a Python problem is 422 with a code, and the message never
// carries the destination.
func TestHostCheckClassifiesFailures(t *testing.T) {
	e := newVPN(t, false)
	h := e.addHost(t, nil, nil)
	cases := []struct {
		d      hosts.Diagnosis
		status int
	}{
		{hosts.Diagnosis{Class: hosts.ClassSSH, Message: "ssh timed out reaching the host (is the VPN up?)."}, 502},
		{hosts.Diagnosis{Class: hosts.ClassPythonMissing, Message: "The Python command was not found on the host."}, 422},
		{hosts.Diagnosis{Class: hosts.ClassPythonOld, Python: "3.9.18", Message: "The host's Python is 3.9.18; the worker needs 3.11 or newer."}, 422},
		{hosts.Diagnosis{Class: hosts.ClassPythonImport, Python: "3.12.1", Missing: "astropy", Message: "Python 3.12.1 on the host cannot import astropy."}, 422},
		{hosts.Diagnosis{Class: hosts.ClassProtocol, Python: "3.12.1", Message: "the worker did not answer correctly"}, 502},
	}
	for _, c := range cases {
		d := c.d
		e.srv.diagHook = func(context.Context, hosts.Host) hosts.Diagnosis { return d }
		code, m := e.call(t, "/api/hosts/"+itoa(int(h.ID))+"/check", map[string]any{})
		if code != c.status || m["code"] != d.Class || m["error"] != d.Message {
			t.Errorf("%s: %d %v", d.Class, code, m)
		}
		if d.Python != "" && m["pythonVersion"] != d.Python || d.Missing != "" && m["missingModule"] != d.Missing {
			t.Errorf("%s: details missing: %v", d.Class, m)
		}
		if strings.Contains(m["error"].(string), "cluster") {
			t.Errorf("the message names the destination: %v", m["error"])
		}
	}
	// ssh worked for every class but the first: the host counts as reachable
	if state, _ := e.srv.reach.get(h.ID); state != "yes" {
		t.Errorf("reachable after a Python problem: %q", state)
	}
	e.srv.diagHook = func(context.Context, hosts.Host) hosts.Diagnosis {
		return hosts.Diagnosis{Class: hosts.ClassSSH, Message: "x"}
	}
	e.call(t, "/api/hosts/"+itoa(int(h.ID))+"/check", map[string]any{})
	if state, _ := e.srv.reach.get(h.ID); state != "no" {
		t.Errorf("reachable after an ssh failure: %q", state)
	}
}

// Changing the Python command validates it, stores it, drops the cached worker so the next start uses it, audits it, and checks again.
func TestSetHostPython(t *testing.T) {
	e := newVPN(t, false)
	h := e.addHost(t, nil, nil)
	path := "/api/hosts/" + itoa(int(h.ID)) + "/python"
	for _, bad := range []string{"", "python3; id", "$(id)", "-c", "a b", strings.Repeat("a", 201), "../py\nthon"} {
		if code, _ := e.call(t, path, map[string]any{"python": bad}); code != 422 {
			t.Errorf("%q should be refused with 422, got %d", bad, code)
		}
	}
	if code, _ := e.call(t, "/api/hosts/999/python", map[string]any{"python": "python3"}); code != 404 {
		t.Errorf("unknown host: %d", code)
	}
	got, _ := e.srv.HostDB.Get(h.ID)
	if got.Python != "python3" {
		t.Fatalf("a refused command changed the stored one: %q", got.Python)
	}
	e.up.Store(true)
	code, m := e.call(t, path, map[string]any{"python": " /shared/opt/Anaconda3/bin/python3 "})
	if code != 200 || m["astropy"] != "6" {
		t.Fatalf("set python: %d %v", code, m)
	}
	got, _ = e.srv.HostDB.Get(h.ID)
	if got.Python != "/shared/opt/Anaconda3/bin/python3" {
		t.Errorf("stored: %q", got.Python)
	}
	if !strings.Contains(e.waitAudit(t, "host python changed"), "host python changed") {
		t.Error("the change is audited")
	}
}

// The worker of a host is built once with the Python command in its start line; saving a new command must replace it.
func TestSetHostPythonRestartsWorkerWithNewCommand(t *testing.T) {
	e := newVPN(t, false)
	log := filepath.Join(e.dir, "ssh.log")
	// a stand-in ssh that records the remote command and answers nothing
	ssh := e.script(t, "ssh", `for a; do last="$a"; done; printf '%s\n' "$last" >> `+log+`; exit 0`)
	mgr := hosts.NewManager(ssh, map[string][]byte{"plane_worker.py": []byte("x")})
	mgr.ControlDir = t.TempDir()
	e.srv.Remote = mgr
	e.srv.pingHook = nil
	h := e.addHost(t, nil, nil)
	base := "/api/hosts/" + itoa(int(h.ID))
	e.call(t, base+"/check", map[string]any{})
	e.call(t, base+"/python", map[string]any{"python": "/opt/py311/bin/python"})
	b, _ := os.ReadFile(log)
	text := string(b)
	i, j := strings.Index(text, "exec python3 -P"), strings.Index(text, "exec /opt/py311/bin/python -P")
	if i < 0 || j < 0 || j < i {
		t.Errorf("the first start uses python3 and the one after the change uses the new command:\n%s", text)
	}
}

func TestFindHostPython(t *testing.T) {
	e := newVPN(t, false)
	h := e.addHost(t, nil, nil)
	path := "/api/hosts/" + itoa(int(h.ID)) + "/python/find"
	e.srv.findHook = func(context.Context, hosts.Host) ([]hosts.PythonCandidate, error) {
		return []hosts.PythonCandidate{
			{Path: "/shared/Anaconda3/bin/python3", Python: "3.13.5", Astropy: "7.0", Numpy: "2.1", Suitable: true},
			{Path: "/home/u/vgt/.venv/bin/python", Python: "3.9.18", Astropy: "6.0", Numpy: "1.26", Problem: "Python 3.9.18 is older than 3.11, which the worker needs"},
		}, nil
	}
	code, m := e.call(t, path, map[string]any{})
	cands, _ := m["candidates"].([]any)
	if code != 200 || len(cands) != 2 || m["minPython"] != "3.11" {
		t.Fatalf("find: %d %v", code, m)
	}
	if c := cands[1].(map[string]any); c["suitable"] != false || c["problem"] == "" {
		t.Errorf("an unsuitable candidate carries its reason: %v", c)
	}
	e.srv.findHook = func(context.Context, hosts.Host) ([]hosts.PythonCandidate, error) {
		return nil, context.DeadlineExceeded
	}
	if code, m := e.call(t, path, map[string]any{}); code != 502 || m["code"] != "ssh" {
		t.Errorf("unreachable: %d %v", code, m)
	}
}
