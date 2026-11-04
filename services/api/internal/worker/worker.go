// Package worker runs the isolated scientific worker: one subprocess per request, JSON on stdout,
// a hard timeout and a bounded output size.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const maxOutput = 1 << 20 // 1 MiB

// Runner starts the header worker. Command is the interpreter and fixed leading arguments.
type Runner struct {
	Command []string
	Timeout time.Duration
}

type limitedBuffer struct {
	bytes.Buffer
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		b.over = true
		return 0, errors.New("worker output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// Header reads the declared header facts of the file at an already-authorized path.
// Worker diagnostics are never forwarded: failures return a generic error plus, when the worker
// itself reported one in its JSON, that short sanitised message.
func (r Runner) Header(ctx context.Context, path string) (json.RawMessage, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("worker not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	args := append(append([]string{}, r.Command[1:]...), path)
	cmd := exec.CommandContext(ctx, r.Command[0], args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME"), "LC_ALL=C.UTF-8"}
	// Run the worker in its own process group and kill the whole group on timeout, so a child it
	// spawned cannot keep running or hold the output pipe open. WaitDelay bounds the pipe drain.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out, errOut limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("worker timed out")
	}
	if out.over {
		return nil, errors.New("worker output too large")
	}
	var probe struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(out.Bytes(), &probe); jerr != nil {
		// The operator's own log may carry a little of the worker's output to explain why; clients never see it.
		log.Printf("worker: no valid result (exit %v): stdout=%.200q stderr=%.300q", err, out.String(), errOut.String())
		return nil, errors.New("worker returned no valid result")
	}
	if err != nil || !probe.OK {
		if probe.Error != "" {
			return nil, fmt.Errorf("cannot read file: %s", probe.Error)
		}
		return nil, errors.New("cannot read file")
	}
	return json.RawMessage(bytes.Clone(out.Bytes())), nil
}
