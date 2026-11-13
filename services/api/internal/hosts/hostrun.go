package hosts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"
)

// DerivedFolder is where Vesqira's own outputs live on a host: masks, subsets written as files and analysis jobs' folders, all under the
// host's first source root (ADR 0064, the folder analysis jobs already use).
func (h Host) DerivedFolder() string { return path.Join(h.Roots[0], "vesqira_derived") }

// MaxHostRunReply bounds the JSON a host run answers with (a mask's or a subset's summary).
const MaxHostRunReply = 1 << 20

// ErrHostRunTimeout is a host run that did not finish within its bound.
var ErrHostRunTimeout = errors.New("the host run took too long")

// ErrHostRunFailed is a host that answered but whose program gave no reply (a missing module, a crash): not an unreachable host.
var ErrHostRunFailed = errors.New("the program failed on the host without an answer; check the host (Hosts, Check) for its Python and astropy")

// HostCarry is one mask a subset written on the host carries: the final file name (a plain mask-… name, chosen by Vesqira), the source mask's
// path on the host and its type. The program on the host writes it as a mask of the result (ADR 0055).
type HostCarry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// HostOutputs are the files a subset written on the host writes besides its own: the carried masks and the result's own no-value mask,
// which the program computes again. They are placed with the result, all or none (hostrun.py). Nil for none.
type HostOutputs struct {
	Carry   []HostCarry
	Missing string // file name; empty for none
}

// HostRun runs one bounded write on the host (hostrun.py, ADR 0064): op is "mask" or "subset", name the file to write in the host's derived
// folder, argv the analysis program's options. The command line is fixed (the deployed folder, the host's roots and derived folder, the
// host's Python); the request, which carries the researcher's free text, goes on stdin. The worker on the host enforces the roots on what it
// reads, writes only the new files named in the request in the derived folder (the result and, for a subset, its carried masks, all or none), and
// never replaces an existing one. It returns the program's JSON reply.
func (m *Manager) HostRun(ctx context.Context, h Host, op, name string, argv []string, outs *HostOutputs, timeout time.Duration) ([]byte, error) {
	if len(h.Roots) == 0 {
		return nil, fmt.Errorf("%w: the host has no source root", ErrInvalid)
	}
	derived := h.DerivedFolder()
	if unsafeText(derived) {
		return nil, fmt.Errorf("%w: the host's first source root has a character a remote command cannot carry", ErrInvalid)
	}
	if err := m.Deploy(ctx, h); err != nil {
		return nil, err
	}
	body := map[string]any{"op": op, "name": name, "argv": argv}
	if outs != nil {
		if len(outs.Carry) > 0 {
			body["carry"] = outs.Carry
		}
		if outs.Missing != "" {
			body["missing"] = outs.Missing
		}
	}
	return m.hostProgram(ctx, h, body, derived, false, timeout)
}

// hostProgram runs hostrun.py on h with one request and returns the last line of its output, its JSON reply. The cache folder is named on
// the command line only for a review, the one run that writes an index. A review reads a whole fit, so it writes a short progress line
// every few seconds and stops when that write fails: a request that is cancelled or timed out (ssh killed) or a connection that drops
// ends the pass on the host instead of leaving it to run. (stdin carries the request to its end, so an older hostrun.py that does not
// know the operation reads it and refuses at once, rather than waiting for a stream that never ends.)
func (m *Manager) hostProgram(ctx context.Context, h Host, body map[string]any, derived string, review bool, timeout time.Duration) ([]byte, error) {
	req, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(req) >= 64<<10 {
		return nil, fmt.Errorf("%w: the request is larger than the program on the host takes", ErrInvalid)
	}
	rj, _ := json.Marshal(h.Roots)
	cache := ""
	if review {
		cache = fmt.Sprintf(` VESQIRA_CACHE="%s"`, m.RemoteCacheDir())
	}
	remote := fmt.Sprintf(`cd "%s" && VESQIRA_ROOTS=%s VESQIRA_DERIVED=%s%s exec %s -P hostrun.py`, m.remoteDir(), quote(string(rj)), quote(derived), cache, h.Python)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Ssh, append(m.sshArgs(h), "sh -c "+quote(remote))...) // sh -c: the login shell may not be POSIX
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME")}, m.env()...)
	cmd.Stdin = bytes.NewReader(append(req, '\n'))
	out := capped{max: MaxHostRunReply}
	cmd.Stdout = &out
	killTree(cmd)
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ErrHostRunTimeout
	}
	b := bytes.TrimSpace(out.b.Bytes())
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 { // a login banner or a module message may come first; the reply is the last line
		b = b[i+1:]
	}
	if len(b) == 0 || b[0] != '{' {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() != 255 { // 255 is ssh's own failure: the host was not reached
			return nil, ErrHostRunFailed
		}
		if runErr != nil {
			return nil, errors.New("could not run the program on the host (is the VPN up, and does ssh work for it without a prompt?)")
		}
		return nil, ErrHostRunFailed
	}
	return b, nil
}
