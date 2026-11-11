//go:build !linux

package hosts

import "os/exec"

// dieWithParent does nothing where the kernel has no parent-death signal; an orphaned rsync then ends by itself when it next
// writes its progress to the closed pipe (ADR 0051).
func dieWithParent(*exec.Cmd) {}
