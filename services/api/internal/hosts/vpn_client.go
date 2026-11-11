package hosts

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// MaxVPNLog bounds the file a long-running client writes to: past it the file is emptied (the client keeps appending), so a
// client that logs for days does not fill the disk. The page shows the last MaxVPNOutput bytes.
const MaxVPNLog = 256 << 10

// A VPNClient is a VPN client Vesqira started and left running: many clients (gpclient connect, openconnect without -b) stay
// in the foreground for as long as the tunnel is up, so the time limit of RunVPN would end the tunnel. The client runs in its
// own session with stdin from /dev/null and stdout/stderr in a 0600 file under the state directory (a file, not a pipe, so
// the client does not get SIGPIPE if the API exits first).
type VPNClient struct {
	Pid     int
	Started time.Time
	log     string
	done    chan struct{}

	mu     sync.Mutex
	exit   int // -1 when ended by a signal
	exited time.Time
}

// StartVPNClient starts argv detached (exec, no shell, the user's environment). Output goes to logPath, which is truncated.
func StartVPNClient(argv []string, logPath string) (*VPNClient, error) {
	if err := ValidateVPNArgv(argv); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close() // the child holds its own copy
	_ = f.Chmod(0o600)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, f, f // nil stdin is /dev/null
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &VPNClient{Pid: cmd.Process.Pid, Started: time.Now(), log: logPath, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.exit, c.exited = 0, time.Now()
		if err != nil {
			c.exit = -1
			if ee, ok := err.(*exec.ExitError); ok {
				c.exit = ee.ExitCode()
			}
		}
		c.mu.Unlock()
		close(c.done)
	}()
	go func() { // keep the log bounded while the client runs
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-t.C:
				if st, err := os.Stat(logPath); err == nil && st.Size() > MaxVPNLog {
					_ = os.Truncate(logPath, 0)
				}
			}
		}
	}()
	return c, nil
}

// Done is closed when the client's process has ended.
func (c *VPNClient) Done() <-chan struct{} { return c.done }

// Running reports whether the client's process is still alive.
func (c *VPNClient) Running() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Exit returns the exit code (-1 when ended by a signal) and when it ended; ok is false while it runs.
func (c *VPNClient) Exit() (code int, at time.Time, ok bool) {
	if c.Running() {
		return 0, time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit, c.exited, true
}

// Output returns the end of what the client printed (at most MaxVPNOutput bytes, control sequences removed).
func (c *VPNClient) Output() (string, bool) {
	f, err := os.Open(c.log)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", false
	}
	cut := st.Size() > MaxVPNOutput
	if cut {
		if _, err := f.Seek(-MaxVPNOutput, io.SeekEnd); err != nil {
			return "", false
		}
	}
	b, _ := io.ReadAll(io.LimitReader(f, MaxVPNOutput))
	return CleanTerminalText(string(b)), cut
}

// Stop ends the client's whole process group: SIGTERM, then SIGKILL when it is still alive after grace. killed says whether
// SIGKILL was needed. A client that already ended is not signalled (its pid may be reused).
func (c *VPNClient) Stop(grace time.Duration) (killed bool, err error) {
	if !c.Running() {
		return false, nil
	}
	if err := signalGroup(c.Pid, syscall.SIGTERM); err != nil {
		return false, fmt.Errorf("could not signal the VPN client: %w", err)
	}
	select {
	case <-c.done:
		return false, nil
	case <-time.After(grace):
	}
	_ = signalGroup(c.Pid, syscall.SIGKILL)
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		return true, fmt.Errorf("the VPN client did not end after SIGKILL")
	}
	return true, nil
}
