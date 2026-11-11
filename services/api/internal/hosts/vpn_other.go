//go:build !unix

package hosts

import "os/exec"

// killTree does nothing extra where there are no process groups: a timeout kills the program itself.
func killTree(*exec.Cmd) {}
