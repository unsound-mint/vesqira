package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxReplyHead = 64 << 10 // header line of a reply
	maxPayload   = 64 << 20 // 16M float32 pixels
	maxReqBytes  = 8 << 10
)

// Reply is one worker answer: the JSON header line verbatim plus its binary payload.
type Reply struct {
	Head    json.RawMessage
	Payload []byte
}

// RequestError is a refusal the worker itself reported (bad index, unsupported layout, unreadable file).
type RequestError struct{ Code, Message string }

func (e *RequestError) Error() string { return e.Message }

// ErrUnavailable means the worker could not be run or answered wrongly; the next call starts a fresh one.
var ErrUnavailable = errors.New("scientific worker unavailable")
var ErrTimeout = errors.New("worker timed out")

// Persistent keeps long-lived scientific workers, so Python and astropy start once instead of per request.
//
// There are two lanes, each with its own process and one task at a time. Planes, coordinates and headers use the
// interactive lane; region spectra and orientation statistics, which read much more of the file, use the heavy
// lane. A slow heavy request therefore never delays channel scrubbing (interactive work keeps its latency while
// background work runs). A request that exceeds Timeout, or any broken reply, kills that lane's process group and
// its next request starts a clean worker.
//
// A caller that gives up (context cancelled) while queued leaves without touching the worker. On the interactive
// lane one that gives up while its request is running does not kill it: planes are small, the reply is drained to
// keep the stream in step, and restarting costs a cold Python start. On the heavy lane the running read is
// stopped by killing only the heavy process, because its work can last tens of seconds and nobody is waiting for it.
type Persistent struct {
	Command []string
	Timeout time.Duration
	// ExtraEnv adds variables to the worker's otherwise minimal environment (for ssh: the agent socket and HOME).
	ExtraEnv []string

	Starts atomic.Int64 // processes started so far (observable in tests and logs)

	once  sync.Once
	light lane
	heavy lane
}

// lane is one worker process with its own queue of one.
type lane struct {
	sem  chan struct{}
	proc *proc
}

// heavyOps are the worker operations that read a region over many channels or a whole map.
var heavyOps = map[string]bool{"spectrum": true, "axial": true}

type proc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func (p *Persistent) init() {
	p.once.Do(func() {
		p.light.sem = make(chan struct{}, 1)
		p.heavy.sem = make(chan struct{}, 1)
	})
}

func (p *Persistent) start() (*proc, error) {
	if len(p.Command) == 0 {
		return nil, ErrUnavailable
	}
	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME"), "LC_ALL=C.UTF-8"}, p.ExtraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := cmd.Start(); err != nil {
		return nil, ErrUnavailable
	}
	p.Starts.Add(1)
	return &proc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, maxReplyHead)}, nil
}

func (pr *proc) kill() {
	_ = syscall.Kill(-pr.cmd.Process.Pid, syscall.SIGKILL)
	_ = pr.in.Close()
	go pr.cmd.Wait() // reap; closes the stdout pipe too
}

// Close stops the workers. Safe to call once the server has stopped taking requests.
func (p *Persistent) Close() {
	p.init()
	for _, l := range []*lane{&p.light, &p.heavy} {
		l.sem <- struct{}{}
		if l.proc != nil {
			l.proc.kill()
			l.proc = nil
		}
		<-l.sem
	}
}

// Do sends one request and returns the reply, or a *RequestError when the worker refused it.
func (p *Persistent) Do(ctx context.Context, req map[string]any) (Reply, error) {
	p.init()
	l, killOnCancel := &p.light, false
	if op, _ := req["op"].(string); heavyOps[op] {
		l, killOnCancel = &p.heavy, true
	}
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
	defer func() { <-l.sem }()

	line, err := json.Marshal(req)
	if err != nil || len(line) > maxReqBytes {
		return Reply{}, errors.New("request too large")
	}
	if l.proc == nil {
		if l.proc, err = p.start(); err != nil {
			return Reply{}, err
		}
	}
	pr := l.proc
	var timedOut atomic.Bool
	timer := time.AfterFunc(p.Timeout, func() { timedOut.Store(true); pr.kill() })

	// state: 0 running, 1 the reply arrived first, 2 the caller gave up first and the process was killed.
	var state atomic.Int32
	stop := make(chan struct{})
	if killOnCancel {
		go func() {
			select {
			case <-ctx.Done():
				if state.CompareAndSwap(0, 2) {
					pr.kill()
				}
			case <-stop:
			}
		}()
	}
	reply, err := exchange(pr, append(line, '\n'))
	state.CompareAndSwap(0, 1)
	close(stop)
	if state.Load() == 2 {
		timer.Stop()
		l.proc = nil
		return Reply{}, ctx.Err()
	}
	if !timer.Stop() || timedOut.Load() {
		l.proc = nil
		return Reply{}, ErrTimeout
	}
	var re *RequestError
	if err != nil && !errors.As(err, &re) { // protocol break or crash: never reuse this process
		pr.kill()
		l.proc = nil
		return Reply{}, ErrUnavailable
	}
	return reply, err
}

func exchange(pr *proc, line []byte) (Reply, error) {
	if _, err := pr.in.Write(line); err != nil {
		return Reply{}, err
	}
	slice, err := pr.out.ReadSlice('\n')
	if err != nil {
		return Reply{}, err
	}
	// ReadSlice returns a view of the reader's buffer, which the payload read below overwrites.
	head := bytes.Clone(bytes.TrimSpace(slice))
	var h struct {
		OK     bool   `json:"ok"`
		Code   string `json:"code"`
		Error  string `json:"error"`
		NBytes int    `json:"nbytes"`
	}
	if err := json.Unmarshal(head, &h); err != nil {
		return Reply{}, err
	}
	if h.NBytes < 0 || h.NBytes > maxPayload || (!h.OK && h.NBytes != 0) {
		return Reply{}, fmt.Errorf("bad payload length %d", h.NBytes)
	}
	payload := make([]byte, h.NBytes)
	if _, err := io.ReadFull(pr.out, payload); err != nil {
		return Reply{}, err
	}
	if !h.OK {
		return Reply{}, &RequestError{Code: h.Code, Message: h.Error}
	}
	return Reply{Head: head, Payload: payload}, nil
}
