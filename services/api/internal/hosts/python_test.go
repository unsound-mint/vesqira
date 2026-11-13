package hosts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPyVersionAtLeastMin(t *testing.T) {
	for v, want := range map[string]bool{"3.11.0": true, "3.13.5": true, "3.12.0rc1": true, "3.14.0a1": true, "4.0.1": true,
		"3.10.14": false, "3.9.18": false, "2.7.18": false, "3": false, "": false, "x.y.z": false} {
		if got := pyVersionAtLeastMin(v); got != want {
			t.Errorf("%q: got %v want %v", v, got, want)
		}
	}
}

func TestParseProbe(t *testing.T) {
	line := func(f ...string) string { return strings.Join(append([]string{"VQPY"}, f...), "\t") }
	out := strings.Join([]string{
		"banner text from a login profile",
		line("/shared/Anaconda3/bin/python3", "3.13.5", "7.0.1", "2.1.3", ""),
		line("/home/u/vgt/.venv/bin/python", "3.9.18", "6.0", "1.26", ""),
		line("/home/u/a/bin/python", "3.12.1", "", "2.0", "astropy: ModuleNotFoundError: No module named 'astropy'"),
		line("/home/u/b/bin/python", "", "", "", "did not run"),
		line("relative/python", "3.12.1", "7", "2", ""),
		line("/home/u/$(id)/python", "3.12.1", "7", "2", ""),
		line("/home/u/c/bin/python", "3.12.1", "7", "2"), // too few fields
		"VQPY\t/home/u/d/bin/python\t3.12.1\t7\t2\t\textra",
	}, "\n")
	got := ParseProbe([]byte(out))
	if len(got) != 4 {
		t.Fatalf("want 4 candidates, got %+v", got)
	}
	if !got[0].Suitable || got[0].Python != "3.13.5" || got[0].Astropy != "7.0.1" || got[0].Numpy != "2.1.3" {
		t.Errorf("anaconda: %+v", got[0])
	}
	if got[1].Suitable || !strings.Contains(got[1].Problem, "older than 3.11") {
		t.Errorf("old venv should be unsuitable with a reason: %+v", got[1])
	}
	if got[2].Suitable || !strings.Contains(got[2].Problem, "astropy") {
		t.Errorf("missing astropy: %+v", got[2])
	}
	if got[3].Suitable || got[3].Problem == "" {
		t.Errorf("did not run: %+v", got[3])
	}
	var many strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintln(&many, line(fmt.Sprintf("/p/%d/python", i), "3.12.0", "7", "2", ""))
	}
	if n := len(ParseProbe([]byte(many.String()))); n != MaxCandidates {
		t.Errorf("candidates are bounded: %d", n)
	}
}

func TestParseDiagnosisClasses(t *testing.T) {
	line := func(f ...string) string {
		return strings.Join(append([]string{"VQPY", "/x/python"}, f...), "\t") + "\n"
	}
	cases := []struct {
		name, out, class, py, missing string
	}{
		{"not found", "VQ\tnopython\n", ClassPythonMissing, "", ""},
		{"did not run", line("", "", "", "did not run"), ClassPythonFailed, "", ""},
		{"too old", line("3.9.5", "6.0", "1.26", ""), ClassPythonOld, "3.9.5", ""},
		{"no astropy", line("3.12.1", "", "2.0", "astropy: ModuleNotFoundError"), ClassPythonImport, "3.12.1", "astropy"},
		{"no numpy", line("3.12.1", "", "", "numpy: x; astropy: y"), ClassPythonImport, "3.12.1", "numpy"},
		{"all fine", line("3.12.1", "7.0", "2.1", ""), ClassProtocol, "3.12.1", ""},
		{"deploy folder", "VQ\tdeploy\n" + line("3.12.1", "7.0", "2.1", ""), ClassDeploy, "3.12.1", ""},
		{"garbage", "hello\n", ClassProtocol, "", ""},
	}
	for _, c := range cases {
		d := parseDiagnosis([]byte(c.out))
		if d.Class != c.class || d.Python != c.py || d.Missing != c.missing {
			t.Errorf("%s: got %+v", c.name, d)
		}
		if strings.Contains(d.Message, "/x/python") {
			t.Errorf("%s: the message names a path: %q", c.name, d.Message)
		}
	}
	if d := parseDiagnosis([]byte(line("3.9.5", "6.0", "1.26", ""))); !strings.Contains(d.Message, "3.11") || !strings.Contains(d.Message, "3.9.5") {
		t.Errorf("an old Python is named with the minimum: %q", d.Message)
	}
}

func TestSSHProblemNamesNoDestination(t *testing.T) {
	for in, want := range map[string]string{
		"ssh: connect to host secret-login port 22: Connection timed out": "timed out",
		"user@secret-login: Permission denied (publickey).":               "refused",
		"Host key verification failed.":                                   "key",
		"ssh: Could not resolve hostname secret-login: Name or service":   "resolve",
		"ssh: connect to host secret-login port 22: Connection refused":   "refused",
		"something else about secret-login":                               "could not connect",
	} {
		got := SSHProblem(in, false)
		if !strings.Contains(got, want) || strings.Contains(got, "secret-login") {
			t.Errorf("%q -> %q", in, got)
		}
	}
	if !strings.Contains(SSHProblem("", true), "timed out") {
		t.Error("a time-out is reported as one")
	}
}

// probeEnv is a scratch "remote home" reached through a fake ssh that runs the remote command here with $HOME redirected.
type probeEnv struct {
	m    *Manager
	home string
	down string
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	down := filepath.Join(t.TempDir(), "DOWN")
	script := fmt.Sprintf(`#!/bin/sh
if [ -e %q ]; then echo "ssh: connect to host secret-login port 22: Connection timed out" >&2; exit 255; fi
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
HOME=%q
export HOME
exec sh -c "$*"
`, down, home)
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(p, nil)
	m.ControlDir = t.TempDir()
	return &probeEnv{m: m, home: home, down: down}
}

func (e *probeEnv) host(python string) Host {
	return Host{Name: "h", Dest: "login1", Python: python, Roots: []string{"/data"}}
}

// fakePython writes an interpreter that prints one canned probe line whatever it is asked.
func (e *probeEnv) fakePython(t *testing.T, rel, version, astropy, numpy, problem string) string {
	t.Helper()
	p := filepath.Join(e.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\t%%s\\t%%s\\t%%s\\n' %q %q %q %q\n", version, astropy, numpy, problem)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func byPath(cs []PythonCandidate) map[string]PythonCandidate {
	m := map[string]PythonCandidate{}
	for _, c := range cs {
		m[c.Path] = c
	}
	return m
}

func TestDiagnoseAgainstScratchHome(t *testing.T) {
	e := newProbeEnv(t)
	ctx := context.Background()
	good := e.fakePython(t, "ok/bin/python", "3.12.1", "7.0", "2.1", "")
	old := e.fakePython(t, "old/bin/python", "3.9.18", "6.0", "1.26", "")
	noAstro := e.fakePython(t, "noastro/bin/python", "3.12.1", "", "2.1", "astropy: ModuleNotFoundError: No module named 'astropy'")

	if d := e.m.Diagnose(ctx, e.host(good)); d.Class != ClassProtocol || d.Python != "3.12.1" {
		t.Errorf("good python: %+v", d)
	}
	if d := e.m.Diagnose(ctx, e.host(old)); d.Class != ClassPythonOld || d.Python != "3.9.18" {
		t.Errorf("old python: %+v", d)
	}
	if d := e.m.Diagnose(ctx, e.host(noAstro)); d.Class != ClassPythonImport || d.Missing != "astropy" {
		t.Errorf("no astropy: %+v", d)
	}
	if d := e.m.Diagnose(ctx, e.host("no-such-python-anywhere")); d.Class != ClassPythonMissing {
		t.Errorf("missing python: %+v", d)
	}
	// a ~/ command resolves against the host's home, like the worker start does
	if d := e.m.Diagnose(ctx, e.host("~/ok/bin/python")); d.Class != ClassProtocol || d.Python != "3.12.1" {
		t.Errorf("tilde python: %+v", d)
	}
	// the home folder cannot hold the deployed worker
	_ = os.RemoveAll(filepath.Join(e.home, ".cache"))
	if err := os.WriteFile(filepath.Join(e.home, ".cache"), []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := e.m.Diagnose(ctx, e.host(good)); d.Class != ClassDeploy {
		t.Errorf("unwritable cache: %+v", d)
	}
	// ssh itself fails
	if err := os.WriteFile(e.down, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d := e.m.Diagnose(ctx, e.host(good))
	if d.Class != ClassSSH || !strings.Contains(d.Message, "timed out") || strings.Contains(d.Message, "secret-login") || strings.Contains(d.Message, "login1") {
		t.Errorf("ssh down: %+v", d)
	}
}

func TestFindPythonInScratchHome(t *testing.T) {
	e := newProbeEnv(t)
	ctx := context.Background()
	venv := e.fakePython(t, ".venv-a/bin/python", "3.12.1", "7.0", "2.1", "")
	proj := e.fakePython(t, "vgt/.venv-irgetas-py/bin/python", "3.9.18", "6.0", "1.26", "")
	plain := e.fakePython(t, "work/venv/bin/python", "3.12.1", "", "2.1", "astropy: ModuleNotFoundError: No module named 'astropy'")
	conda := e.fakePython(t, "miniconda3/envs/sci/bin/python", "3.11.4", "6.1", "1.26", "")
	dot := e.fakePython(t, ".conda/envs/x/bin/python", "3.13.0", "7.0", "2.1", "")
	// an environment module system: only the login shell defines `module`, and a loaded module puts its python3 on PATH
	mod := e.fakePython(t, "mods/Anaconda3/2025.06-1/bin/python3", "3.13.5", "7.0", "2.1", "")
	e.fakePython(t, "mods/GCC/12/bin/python3", "3.13.5", "7.0", "2.1", "")
	profile := `module() {
  if [ "$1" = "-t" ]; then printf '/opt/modulefiles:\nAnaconda3/2025.06-1\nPython/3.9.5 (D)\nGCC/12\nbad;name\n'
  elif [ "$1" = "load" ]; then PATH="$HOME/mods/$2/bin:$PATH"; fi
}
`
	if err := os.WriteFile(filepath.Join(e.home, ".bash_profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	cs, err := e.m.FindPython(ctx, e.host("python3"))
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(cs)
	for p, suitable := range map[string]bool{venv: true, proj: false, plain: false, conda: true, dot: true, mod: true} {
		c, ok := got[p]
		if !ok {
			t.Errorf("not found: %s (got %+v)", p, cs)
			continue
		}
		if c.Suitable != suitable {
			t.Errorf("%s: suitable=%v, want %v (%+v)", p, c.Suitable, suitable, c)
		}
		if !suitable && c.Problem == "" {
			t.Errorf("%s: unsuitable without a reason", p)
		}
	}
	if c := got[proj]; !strings.Contains(c.Problem, "3.11") {
		t.Errorf("the 3.9 venv is refused for its version: %+v", c)
	}
	if _, ok := got[filepath.Join(e.home, "mods/GCC/12/bin/python3")]; ok {
		t.Error("only Anaconda, Miniconda, Miniforge and Python modules are loaded")
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if seen[c.Path] {
			t.Errorf("duplicate candidate %s", c.Path)
		}
		seen[c.Path] = true
		if !pythonRe.MatchString(c.Path) || !strings.HasPrefix(c.Path, "/") {
			t.Errorf("candidate path would not pass the validator: %q", c.Path)
		}
	}
}

func TestFindPythonBoundsCandidates(t *testing.T) {
	e := newProbeEnv(t)
	for i := 0; i < 30; i++ {
		e.fakePython(t, fmt.Sprintf("p%02d/venv/bin/python", i), "3.12.0", "7.0", "2.1", "")
	}
	cs, err := e.m.FindPython(context.Background(), e.host("python3"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) > MaxCandidates {
		t.Errorf("%d candidates tested, the bound is %d", len(cs), MaxCandidates)
	}
}

func TestFindPythonSSHDown(t *testing.T) {
	e := newProbeEnv(t)
	if err := os.WriteFile(e.down, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.FindPython(context.Background(), e.host("python3"))
	if err == nil || strings.Contains(err.Error(), "secret-login") {
		t.Errorf("want a plain ssh error, got %v", err)
	}
}
