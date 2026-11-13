package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MinPythonMajor and MinPythonMinor are the oldest Python the worker runs on. The start command uses `python -P`
// (do not put the script folder on sys.path: the worker adds its own deployed folder explicitly), and -P exists from 3.11.
// A 3.9 or 3.10 interpreter exits with "Unknown option: -P" before the worker starts. -P stays: it keeps a planted module in
// the current folder from shadowing a worker import.
const (
	MinPythonMajor = 3
	MinPythonMinor = 11
)

// Failure classes of a host check. Only the first is about the network; the others mean ssh worked.
const (
	ClassSSH           = "ssh"            // ssh could not connect or authenticate
	ClassDeploy        = "deploy"         // ssh works but the worker cannot be copied to ~/.cache/vesqira
	ClassPythonMissing = "python_missing" // the Python command was not found
	ClassPythonFailed  = "python_failed"  // found, but it did not run
	ClassPythonOld     = "python_old"     // older than the minimum above
	ClassPythonImport  = "python_import"  // numpy or astropy cannot be imported
	ClassProtocol      = "protocol"       // Python and its modules are fine, but the worker did not answer correctly
)

// Diagnosis is what a fixed probe found when the worker could not be used. Message never names the destination or a path.
type Diagnosis struct {
	Class   string `json:"class"`
	Message string `json:"message"`
	Python  string `json:"pythonVersion,omitempty"`
	Missing string `json:"missingModule,omitempty"`
}

// PythonCandidate is one interpreter found on a host. Path is absolute and passes the Python command validator.
type PythonCandidate struct {
	Path     string `json:"path"`
	Python   string `json:"python,omitempty"`
	Astropy  string `json:"astropy,omitempty"`
	Numpy    string `json:"numpy,omitempty"`
	Suitable bool   `json:"suitable"`
	Problem  string `json:"problem,omitempty"` // why it cannot be used
}

// MaxCandidates and the time bounds of the search keep it light on a shared login node (the numbers are also in findScript).
const (
	MaxCandidates = 20
	findTimeout   = 40 * time.Second
)

// probeFunc tests one interpreter and prints one line: VQPY, path, Python version, astropy version, numpy version, error
// (tab separated). The Python snippet has no single quotes; it is a fixed text.
const probeFunc = `vq_test() {
  vq_p=$1
  command -v timeout >/dev/null 2>&1 && vq_t="timeout 8" || vq_t=""
  vq_o=$($vq_t "$vq_p" -c "import sys
r=[sys.version.split()[0],\"\",\"\",\"\"]
def f(e):
    return (type(e).__name__+\": \"+str(e)).replace(\"\\t\",\" \").replace(\"\\n\",\" \")[:120]
try:
    import numpy
    r[2]=numpy.__version__
except Exception as e:
    r[3]=\"numpy: \"+f(e)
try:
    import astropy
    r[1]=astropy.__version__
except Exception as e:
    r[3]=(r[3]+\"; \" if r[3] else \"\")+\"astropy: \"+f(e)
print(\"\\t\".join(r))" 2>/dev/null </dev/null | tail -n 1)
  if [ -n "$vq_o" ]; then printf 'VQPY\t%s\t%s\n' "$vq_p" "$vq_o"; else printf 'VQPY\t%s\t\t\t\tdid not run\n' "$vq_p"; fi
}
`

// diagnoseScript resolves the configured Python the way the worker start does (the same sh, the same PATH) and tests it.
// $1 is the Python command.
const diagnoseScript = probeFunc + `vq_py=$1
case "$vq_py" in "~/"*) vq_py="$HOME/${vq_py#"~/"}";; esac
mkdir -p "$HOME/.cache/vesqira" 2>/dev/null && [ -w "$HOME/.cache/vesqira" ] || printf 'VQ\tdeploy\n'
vq_r=$(command -v -- "$vq_py" 2>/dev/null)
if [ -z "$vq_r" ] || [ ! -x "$vq_r" ]; then printf 'VQ\tnopython\n'; exit 0; fi
vq_test "$vq_r"
`

// findScript lists interpreters likely to have astropy and tests each; output is streamed, so a time-out keeps what was found.
// Order: what a login shell finds, then environment modules (Anaconda, Miniconda, Miniforge, then Python), then virtual
// environments and conda installs under $HOME. Each path is tried once; at most 20 are tested and no new one is started
// after 30 seconds. A module name only ever reaches `module load` as an argument, after a name-character filter.
const findScript = probeFunc + `vq_start=$(date +%s)
vq_n=0
vq_seen=" "
vq_try() {
  [ "$vq_n" -lt 20 ] || return 0
  [ $(( $(date +%s) - vq_start )) -lt 30 ] || return 0
  case "$1" in /*) ;; *) return 0;; esac
  [ -x "$1" ] || return 0
  case "$vq_seen" in *" $1 "*) return 0;; esac
  vq_seen="$vq_seen$1 "
  vq_n=$((vq_n+1))
  vq_test "$1"
}
vq_mod() {
  [ "$vq_k" -lt 6 ] || return 0
  vq_k=$((vq_k+1))
  vq_x=$($vq_login 'module load "$1" >/dev/null 2>&1; command -v python3' sh "$1" 2>/dev/null </dev/null | grep '^/' | tail -n 1)
  [ -n "$vq_x" ] && vq_try "$vq_x"
  return 0
}
if command -v bash >/dev/null 2>&1; then vq_login="bash -lc"; else vq_login="sh -lc"; fi
for vq_x in $($vq_login 'command -v python3; command -v python' 2>/dev/null </dev/null | grep '^/'); do vq_try "$vq_x"; done
vq_mods=$($vq_login 'command -v module >/dev/null 2>&1 && module -t avail 2>&1' 2>/dev/null </dev/null | sed 's/([^)]*)$//' | grep -E '^[A-Za-z0-9._+/-]+$' | sort -r)
vq_k=0
for vq_m in $vq_mods; do case "$vq_m" in Anaconda*|Miniconda*|Miniforge*) vq_mod "$vq_m";; esac; done
for vq_m in $vq_mods; do case "$vq_m" in Python*) vq_mod "$vq_m";; esac; done
for vq_x in "$HOME"/.venv*/bin/python "$HOME"/*/.venv*/bin/python "$HOME"/*/venv/bin/python \
  "$HOME"/miniconda3/bin/python "$HOME"/anaconda3/bin/python "$HOME"/miniforge3/bin/python "$HOME"/mambaforge/bin/python \
  "$HOME"/miniconda3/envs/*/bin/python "$HOME"/anaconda3/envs/*/bin/python "$HOME"/miniforge3/envs/*/bin/python "$HOME"/mambaforge/envs/*/bin/python \
  "$HOME"/.conda/envs/*/bin/python; do
  vq_try "$vq_x"
done
`

// pyVersionAtLeastMin reports whether "3.11.4" (or "3.12.0rc1") is at least the worker's minimum.
func pyVersionAtLeastMin(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(strings.TrimRight(strings.SplitN(parts[1], "+", 2)[0], "abcrdev"))
	if e1 != nil || e2 != nil {
		return false
	}
	return major > MinPythonMajor || major == MinPythonMajor && minor >= MinPythonMinor
}

func cleanField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = strings.ToValidUTF8(s[:160], "")
	}
	return s
}

// ParseProbe reads the VQPY lines a probe printed. Lines that are malformed or whose path would not pass the Python command
// validator are dropped, and at most MaxCandidates are kept: the output comes from another machine.
func ParseProbe(out []byte) []PythonCandidate {
	cands := []PythonCandidate{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 6 || f[0] != "VQPY" {
			continue
		}
		c := PythonCandidate{Path: f[1], Python: cleanField(f[2]), Astropy: cleanField(f[3]), Numpy: cleanField(f[4])}
		if !strings.HasPrefix(c.Path, "/") || !pythonRe.MatchString(c.Path) {
			continue
		}
		problem := cleanField(f[5])
		switch {
		case c.Python == "":
			c.Problem = "it did not run"
		case !pyVersionAtLeastMin(c.Python):
			c.Problem = fmt.Sprintf("Python %s is older than %d.%d, which the worker needs", c.Python, MinPythonMajor, MinPythonMinor)
		case problem != "":
			c.Problem = problem
		case c.Astropy == "" || c.Numpy == "":
			c.Problem = "numpy or astropy is missing"
		default:
			c.Suitable = true
		}
		cands = append(cands, c)
		if len(cands) >= MaxCandidates {
			break
		}
	}
	return cands
}

const deployMsg = "ssh works, but the worker cannot be copied to ~/.cache/vesqira on the host (is the home folder writable and not full?)."

// parseDiagnosis turns the probe of the configured Python into a class and a message that never names the destination or a path.
func parseDiagnosis(out []byte) Diagnosis {
	var deployBlocked bool
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimRight(line, "\r") {
		case "VQ\tdeploy":
			deployBlocked = true
		case "VQ\tnopython":
			return Diagnosis{Class: ClassPythonMissing, Message: "The Python command was not found on the host. It is looked up the way the worker is started, without your login profile, so a module's Python needs its full path."}
		}
	}
	cands := ParseProbe(out)
	if len(cands) == 0 {
		if deployBlocked {
			return Diagnosis{Class: ClassDeploy, Message: deployMsg}
		}
		return Diagnosis{Class: ClassProtocol, Message: "ssh works, but the host's answer to the check could not be read."}
	}
	c := cands[0]
	switch {
	case c.Python == "":
		return Diagnosis{Class: ClassPythonFailed, Message: "The Python command was found on the host but did not run."}
	case !pyVersionAtLeastMin(c.Python):
		return Diagnosis{Class: ClassPythonOld, Python: c.Python,
			Message: fmt.Sprintf("The host's Python is %s; the worker needs %d.%d or newer (it starts with python -P).", c.Python, MinPythonMajor, MinPythonMinor)}
	case c.Numpy == "" || c.Astropy == "":
		miss := "numpy"
		if c.Numpy != "" {
			miss = "astropy"
		}
		return Diagnosis{Class: ClassPythonImport, Python: c.Python, Missing: miss,
			Message: fmt.Sprintf("Python %s on the host cannot import %s.", c.Python, miss)}
	case deployBlocked:
		return Diagnosis{Class: ClassDeploy, Python: c.Python, Message: deployMsg}
	}
	return Diagnosis{Class: ClassProtocol, Python: c.Python,
		Message: fmt.Sprintf("Python %s, numpy and astropy are fine on the host, but the worker did not answer correctly.", c.Python)}
}

// SSHProblem maps what ssh printed to one fixed sentence: the real text names the destination, which must not travel on.
func SSHProblem(stderr string, timedOut bool) string {
	l := strings.ToLower(stderr)
	switch {
	case timedOut || strings.Contains(l, "timed out"):
		return "ssh timed out reaching the host (is the VPN up?)."
	case strings.Contains(l, "permission denied"):
		return "ssh was refused: the key is not accepted or not loaded in the agent, or the host asks for a password or a code."
	case strings.Contains(l, "host key"):
		return "ssh does not trust the host's key yet: connect once by hand in a terminal to accept it."
	case strings.Contains(l, "could not resolve"):
		return "ssh could not resolve the host name (is the VPN up, and is the name right?)."
	case strings.Contains(l, "connection refused"):
		return "the host refused the ssh connection."
	case strings.Contains(l, "no route to host") || strings.Contains(l, "network is unreachable"):
		return "there is no network route to the host (is the VPN up?)."
	}
	return "ssh could not connect to the host: check that ssh works for it without a prompt and that the VPN is up."
}

// runScript runs a fixed script on the host: `sh -s -- ARG` with the script on stdin, so the remote command line stays one
// short line whatever the login shell is, and ARG (the one variable part, quoted) is $1 and never part of the script text. The
// script is wrapped in a group so the remote shell has read all of it before anything runs and no child can take the rest from
// stdin. It reports whether ssh itself failed (exit status 255, or no answer in time).
func (m *Manager) runScript(ctx context.Context, h Host, script, arg string, timeout time.Duration) (out []byte, sshErr string, failed bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Ssh, append(m.sshArgs(h), "sh -s -- "+quote(arg))...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME")}, m.env()...)
	cmd.Stdin = strings.NewReader("{\n" + script + "\n}\n")
	stdout, stderr := capped{max: maxShellOutput}, capped{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	killTree(cmd)
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil && stdout.b.Len() == 0:
		return nil, SSHProblem("", true), true
	case errors.As(err, &ee) && ee.ExitCode() == 255:
		return nil, SSHProblem(stderr.b.String(), false), true
	case err != nil && !errors.As(err, &ee) && ctx.Err() == nil:
		return nil, SSHProblem("", false), true // ssh could not even start
	}
	return bytes.Clone(stdout.b.Bytes()), "", false
}

// Diagnose finds out why the worker could not be used on h: ssh itself, the folder it deploys to, or the Python command
// (not found, too old, numpy or astropy missing). It runs a short fixed probe over the same ssh and the same sh the worker
// start uses.
func (m *Manager) Diagnose(ctx context.Context, h Host) Diagnosis {
	if err := h.Validate(); err != nil {
		return Diagnosis{Class: ClassPythonMissing, Message: err.Error()}
	}
	out, msg, failed := m.runScript(ctx, h, diagnoseScript, h.Python, 25*time.Second)
	if failed {
		return Diagnosis{Class: ClassSSH, Message: msg}
	}
	return parseDiagnosis(out)
}

// FindPython looks on h for interpreters that have numpy and astropy and returns every one it tested, suitable or not.
// A failed ssh is an error; a host with nothing found returns an empty list.
func (m *Manager) FindPython(ctx context.Context, h Host) ([]PythonCandidate, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	out, msg, failed := m.runScript(ctx, h, findScript, "", findTimeout)
	if failed {
		return nil, errors.New(msg)
	}
	return ParseProbe(out), nil
}

// SetPython changes a host's Python command (validated like at creation). The caller drops the host's worker afterwards.
func (s *Store) SetPython(id int64, python string) (Host, error) {
	h, err := s.Get(id)
	if err != nil {
		return Host{}, err
	}
	h.Python = python
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	if _, err := s.db.Exec(`UPDATE hosts SET python=? WHERE id=?`, python, id); err != nil {
		return Host{}, err
	}
	return h, nil
}
