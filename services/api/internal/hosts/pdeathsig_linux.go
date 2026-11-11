package hosts

import (
	"os/exec"
	"syscall"
)

// dieWithParent makes the kernel send rsync SIGTERM if this process dies, so a crash or a kill -9 of Vesqira does not leave an
// rsync running on its own (SIGTERM, like a cancel: the unfinished file stays in PartialDir for the next attempt). The signal
// is tied to the OS thread that started the process; the Go runtime does not retire threads that are not locked, so in
// practice this is the life of the process.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
