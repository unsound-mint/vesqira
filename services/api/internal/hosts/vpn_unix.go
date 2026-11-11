//go:build unix

package hosts

import (
	"os/exec"
	"syscall"
)

// killTree gives the command its own process group and makes a timeout kill the whole group, so a VPN client that forked
// helpers does not outlive its time limit.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
