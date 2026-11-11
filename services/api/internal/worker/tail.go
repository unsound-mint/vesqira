package worker

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// Tail keeps the last lines a set of processes wrote to stderr, so a support bundle can show them. Lines are kept as written;
// redaction happens when the bundle is made (internal/support), never here, so nothing is lost for the log itself.
type Tail struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func NewTail(max int) *Tail { return &Tail{max: max} }

// Lines returns a copy of the kept lines, oldest first.
func (t *Tail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

func (t *Tail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(line) > 2000 {
		line = line[:2000]
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = append(t.lines[:0], t.lines[len(t.lines)-t.max:]...)
	}
}

// Writer is an io.Writer for one process's stderr: it passes everything on to dst (nil means os.Stderr) and keeps complete lines.
func (t *Tail) Writer(dst io.Writer) io.Writer {
	if dst == nil {
		dst = os.Stderr
	}
	return &tailWriter{t: t, dst: dst}
}

type tailWriter struct {
	t   *Tail
	dst io.Writer
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.t.add(string(bytes.TrimRight(w.buf[:i], "\r")))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4000 { // a process that never ends a line must not grow this without bound
		w.t.add(string(w.buf[:2000]))
		w.buf = nil
	}
	return n, err
}

// StderrTail is the process-wide tail of the scientific workers and analysis programs.
var StderrTail = NewTail(400)
