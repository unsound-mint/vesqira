package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The VPN companion (ADR 0029 step 3). Vesqira does not implement a VPN: a host may carry the argv of the user's own client
// (for example gpclient or openconnect), which the owner runs from a button. The argv is stored exactly as written and is
// run with exec, never through a shell, so nothing in it is interpreted; it runs as the user, and privileges are the client's
// or the user's polkit rule's business (pkexec), never a password Vesqira sees. The page can only say "connect" or "disconnect".

const (
	MaxVPNArgs    = 16   // elements in one command
	MaxVPNArgLen  = 512  // bytes in one element
	MaxVPNArgv    = 2048 // bytes in all elements together
	MaxVPNOutput  = 16 << 10
	DefaultVPNRun = 2 * time.Minute
)

var vpnProgramRe = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]*$`) // a bare program name looked up in PATH when it runs

// ValidateVPNArgv checks a VPN command. The first element is an absolute path or a bare program name (resolved through PATH
// when it runs, so a client installed later still works); the others are free text, because they are never read by a shell.
// Only what cannot be a sensible argument is refused: an empty list, too many or too long parts, NUL, line breaks, invalid UTF-8.
func ValidateVPNArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("%w: the VPN command is empty", ErrInvalid)
	}
	if len(argv) > MaxVPNArgs {
		return fmt.Errorf("%w: the VPN command has more than %d parts", ErrInvalid, MaxVPNArgs)
	}
	total := 0
	for i, a := range argv {
		if a == "" && i == 0 {
			return fmt.Errorf("%w: the VPN program is empty", ErrInvalid)
		}
		if len(a) > MaxVPNArgLen {
			return fmt.Errorf("%w: a part of the VPN command is longer than %d bytes", ErrInvalid, MaxVPNArgLen)
		}
		if !utf8.ValidString(a) || strings.ContainsAny(a, "\x00\n\r") {
			return fmt.Errorf("%w: the VPN command may not contain NUL, line breaks or invalid text", ErrInvalid)
		}
		total += len(a)
	}
	if total > MaxVPNArgv {
		return fmt.Errorf("%w: the VPN command is longer than %d bytes", ErrInvalid, MaxVPNArgv)
	}
	prog := argv[0]
	if strings.HasPrefix(prog, "/") {
		if path.Clean(prog) != prog || prog == "/" {
			return fmt.Errorf("%w: the VPN program path must be a clean absolute path", ErrInvalid)
		}
	} else if !vpnProgramRe.MatchString(prog) {
		return fmt.Errorf("%w: the VPN program must be an absolute path or a bare program name found in PATH", ErrInvalid)
	}
	return nil
}

func encodeArgv(a []string) string {
	if len(a) == 0 {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func decodeArgv(s string) []string {
	if s == "" {
		return nil
	}
	var a []string
	if json.Unmarshal([]byte(s), &a) != nil {
		return nil
	}
	return a
}

// SetVPN replaces both VPN commands of a host (nil or empty clears one). The commands are validated and stored verbatim.
func (s *Store) SetVPN(id int64, connect, disconnect []string) (Host, error) {
	h, err := s.Get(id)
	if err != nil {
		return Host{}, err
	}
	h.VPNCommand, h.VPNDisconnectCommand = connect, disconnect
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	if _, err := s.db.Exec(`UPDATE hosts SET vpn_command=?, vpn_disconnect=? WHERE id=?`, encodeArgv(connect), encodeArgv(disconnect), id); err != nil {
		return Host{}, err
	}
	return s.Get(id)
}

// VPNResult is what one run of a VPN command did.
type VPNResult struct {
	ExitCode        int // -1 when the program did not start, was stopped, or was ended by a signal
	TimedOut        bool
	StartError      string // why the program did not start (not found, not executable), empty otherwise
	Output          string // stdout and stderr together, control sequences removed, at most MaxVPNOutput bytes
	OutputTruncated bool
	Duration        time.Duration
}

// RunVPN runs argv with exec (no shell, no stdin, the user's own environment) and stops it, with everything it started, when
// timeout passes or ctx ends. Output is bounded; what is past the bound is dropped, not buffered.
func RunVPN(ctx context.Context, argv []string, timeout time.Duration) VPNResult {
	start := time.Now()
	res := VPNResult{ExitCode: -1}
	if err := ValidateVPNArgv(argv); err != nil {
		res.StartError = err.Error()
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	killTree(cmd)
	cmd.WaitDelay = 2 * time.Second // a child that kept the pipe open does not hold the answer back
	out := &boundedBuffer{max: MaxVPNOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	res.Duration = time.Since(start)
	res.Output, res.OutputTruncated = out.text()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
	case err == nil:
		res.ExitCode = 0
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.StartError = err.Error()
		}
	}
	return res
}

type boundedBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.buf); room > 0 {
		if len(p) > room {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) text() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return CleanTerminalText(string(b.buf)), b.truncated
}

var (
	csiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	escRe = regexp.MustCompile(`\x1b[@-Z\\-_]?`)
)

// CleanTerminalText removes ANSI escape sequences and other control characters (keeping line breaks and tabs) from what a
// program wrote, so output is shown as plain text and cannot move a cursor or set a title. A progress line made with
// carriage returns keeps its last state.
func CleanTerminalText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = oscRe.ReplaceAllString(s, "")
	s = csiRe.ReplaceAllString(s, "")
	s = escRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = l
	}
	s = strings.Join(lines, "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 32 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
