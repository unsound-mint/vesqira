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

// HostRun runs one bounded write on the host (hostrun.py, ADR 0064): op is "mask" or "subset", name the file to write in the host's derived
// folder, argv the analysis program's options. The command line is fixed (the deployed folder, the host's roots and derived folder, the
// host's Python); the request, which carries the researcher's free text, goes on stdin. The worker on the host enforces the roots on what it
// reads, writes only that one new file in the derived folder, and never replaces an existing one. It returns the program's JSON reply.
func (m *Manager) HostRun(ctx context.Context, h Host, op, name string, argv []string, timeout time.Duration) ([]byte, error) {
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
	req, err := json.Marshal(map[string]any{"op": op, "name": name, "argv": argv})
	if err != nil {
		return nil, err
	}
	rj, _ := json.Marshal(h.Roots)
	remote := fmt.Sprintf(`cd "%s" && VESQIRA_ROOTS=%s VESQIRA_DERIVED=%s exec %s -P hostrun.py`, m.remoteDir(), quote(string(rj)), quote(derived), h.Python)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Ssh, append(m.sshArgs(h), "sh -c "+quote(remote))...) // sh -c: the login shell may not be POSIX
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME")}, m.env()...)
	cmd.Stdin = bytes.NewReader(req)
	out := capped{max: MaxHostRunReply}
	cmd.Stdout = &out
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
