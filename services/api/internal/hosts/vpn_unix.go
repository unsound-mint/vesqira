//go:build unix

package hosts

import (
	"os/exec"
	"syscall"
	"time"
)

// killTree gives the command its own process group and makes a timeout kill the whole group, so a VPN client that forked
// helpers does not outlive its time limit. WaitDelay bounds Wait on the output pipes: a descendant that escaped the group
// (setsid) and still holds stdout would otherwise keep Wait, the heavy slot and the request until it exits on its own.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
}

// detach starts the command in a new session (so it is its own process group and survives this process and its terminal).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// signalGroup signals the client's process group; the client is the session and group leader, so its pid is the group id.
func signalGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
