//go:build !unix

package hosts

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killTree does nothing extra where there are no process groups: a timeout kills the program itself.
func killTree(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }

func detach(*exec.Cmd) {}

func signalGroup(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
