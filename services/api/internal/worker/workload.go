package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Class is the workload class of a piece of scientific work (ADR 0056). Each class has its own concurrency budget and
// its own worker processes, so capacity reserved for interactive viewing is never taken by heavy or background work.
type Class int

const (
	// Interactive: planes, coordinate readouts, overlay geometry, small readouts. A person is watching the screen.
	Interactive Class = iota
	// Heavy: spectra over large regions, analyses, PV diagrams, channel grids, index builds, relative orientation.
	Heavy
	// Background: fingerprints, project rescans, header reads that catalog files, transfer verification.
	Background
	numClasses
)

func (c Class) String() string {
	switch c {
	case Interactive:
		return "interactive"
	case Heavy:
		return "heavy"
	}
	return "background"
}

// Classes lists the classes in the order they are reported.
func Classes() []Class { return []Class{Interactive, Heavy, Background} }

// Budgets is how many tasks of each class may run at once (for the worker classes, how many worker processes exist).
type Budgets struct{ Interactive, Heavy, Background int }

// MaxBudget bounds any one class: each slot of a worker class is a Python process holding up to the memory cap.
const MaxBudget = 16

// DefaultBudgets: two interactive processes (a plane and a coordinate readout never queue behind each other), two heavy
// slots (one analysis and one spectrum can overlap), one background slot (a rescan is never urgent).
func DefaultBudgets() Budgets { return Budgets{Interactive: 2, Heavy: 2, Background: 1} }

// Validate rejects a budget outside 1..MaxBudget; zero is not a way to switch a class off, because every class has work.
func (b Budgets) Validate() error {
	for _, c := range []struct {
		name string
		n    int
	}{{"interactive", b.Interactive}, {"heavy", b.Heavy}, {"background", b.Background}} {
		if c.n < 1 || c.n > MaxBudget {
			return fmt.Errorf("the %s budget must be between 1 and %d, got %d", c.name, MaxBudget, c.n)
		}
	}
	return nil
}

func (b Budgets) of(c Class) int {
	switch c {
	case Interactive:
		return b.Interactive
	case Heavy:
		return b.Heavy
	}
	return b.Background
}

// ClassOf is the class of a worker operation.
func ClassOf(op string) Class {
	switch {
	case backgroundOps[op]:
		return Background
	case heavyOps[op]:
		return Heavy
	}
	return Interactive
}

// opLabels are the names a person sees in a queue reason; an operation not listed is shown under its own name.
var opLabels = map[string]string{
	"spectrum": "region spectrum", "axial": "orientation statistics", "pv": "PV diagram", "channel_grid": "channel grid",
	"rohsa_index": "ROHSA index", "catalog_describe": "catalog read", "catalog_rows": "catalog read", "catalog_report": "catalog read",
	"catalog_positions": "catalog read", "catalog_transform": "catalog read", "overlay_catalog": "catalog overlay",
	"fingerprint_full": "full file hash", "fingerprint": "file fingerprint", "describe": "header read", "header": "header read",
	"plane": "plane", "coord": "coordinate readout",
}

// OpLabel is the person-facing name of a worker operation.
func OpLabel(op string) string {
	if l, ok := opLabels[op]; ok {
		return l
	}
	return op
}

type labelKey struct{}

// WithLabel names the work for queue reasons ("relative orientation"); without it a worker call is named after its operation.
func WithLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}

type task struct {
	class Class
	label string
	id    string // the API request id, "" when none
	since time.Time
}

// Load is the one place that knows what is running and what is waiting, per class. Worker lanes and analysis processes
// take their slot here, and the queue reasons shown to the user are computed from it. The zero value is not usable: NewLoad.
type Load struct {
	budgets Budgets
	sem     [numClasses]chan struct{} // blocks while the class is full
	free    [numClasses][]bool        // which slot numbers are in use; the lowest free one is taken, so warm worker processes are reused

	mu      sync.Mutex
	running map[*task]struct{}
	waiting map[*task]struct{}
}

func NewLoad(b Budgets) *Load {
	l := &Load{budgets: b, running: map[*task]struct{}{}, waiting: map[*task]struct{}{}}
	for _, c := range Classes() {
		l.sem[c] = make(chan struct{}, b.of(c))
		l.free[c] = make([]bool, b.of(c))
	}
	return l
}

// Budgets reports the configured budgets.
func (l *Load) Budgets() Budgets { return l.budgets }

// Acquire waits for a slot of the class (or for ctx), and returns the slot number and the function that gives it back.
// While it waits the request is listed as waiting, with the reason Reason computes. A caller that gives up leaves the queue
// without having run anything.
func (l *Load) Acquire(ctx context.Context, c Class, label string) (int, func(), error) {
	id, _ := ctx.Value(idKey{}).(string)
	if v, ok := ctx.Value(labelKey{}).(string); ok && v != "" {
		label = v
	}
	t := &task{class: c, label: label, id: id, since: time.Now()}
	select {
	case l.sem[c] <- struct{}{}:
	default:
		l.mu.Lock()
		l.waiting[t] = struct{}{}
		l.mu.Unlock()
		select {
		case l.sem[c] <- struct{}{}:
		case <-ctx.Done():
			l.mu.Lock()
			delete(l.waiting, t)
			l.mu.Unlock()
			return 0, nil, ctx.Err()
		}
	}
	l.mu.Lock()
	delete(l.waiting, t)
	l.running[t] = struct{}{}
	slot := l.take(c)
	l.mu.Unlock()
	var once sync.Once // giving a slot back twice must not free somebody else's
	return slot, func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.running, t)
			l.free[c][slot] = false
			l.mu.Unlock()
			<-l.sem[c]
		})
	}, nil
}

// take marks the lowest free slot of c as used. Called with mu held and a semaphore token owned, so one is free.
func (l *Load) take(c Class) int {
	for i, used := range l.free[c] {
		if !used {
			l.free[c][i] = true
			return i
		}
	}
	panic("worker: no free slot under a held semaphore token")
}

// ClassState is one class in a snapshot.
type ClassState struct {
	Class   string `json:"class"`
	Budget  int    `json:"budget"`
	Running int    `json:"running"`
	Waiting int    `json:"waiting"`
}

// Snapshot is the state of every class.
func (l *Load) Snapshot() []ClassState {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ClassState, 0, numClasses)
	for _, c := range Classes() {
		s := ClassState{Class: c.String(), Budget: l.budgets.of(c)}
		for t := range l.running {
			if t.class == c {
				s.Running++
			}
		}
		for t := range l.waiting {
			if t.class == c {
				s.Waiting++
			}
		}
		out = append(out, s)
	}
	return out
}

// RequestState is what is known about one API request: "waiting" (with the reason), "running", or "unknown" (not in the queue:
// finished, never queued, or not a request of the scientific workers).
type RequestState struct {
	State  string `json:"state"`
	Class  string `json:"class,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Request looks up an API request id. A request that did several things reports the first it finds, waiting before running.
func (l *Load) Request(id string) RequestState {
	if id == "" {
		return RequestState{State: "unknown"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for t := range l.waiting {
		if t.id == id {
			return RequestState{State: "waiting", Class: t.class.String(), Reason: l.reasonLocked(t)}
		}
	}
	for t := range l.running {
		if t.id == id {
			return RequestState{State: "running", Class: t.class.String()}
		}
	}
	return RequestState{State: "unknown"}
}

const maxNamed = 4

// reasonLocked says why t waits: what holds its class's slots. Other classes are never named as the cause, because they cannot
// be: the one thing a person needs to hear is that viewing is unaffected.
func (l *Load) reasonLocked(t *task) string {
	counts := map[string]int{}
	n := 0
	for r := range l.running {
		if r.class == t.class {
			counts[r.label]++
			n++
		}
	}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > maxNamed {
		extra := len(names) - maxNamed
		names = append(names[:maxNamed], fmt.Sprintf("%d more", extra))
	}
	what := map[Class][2]string{
		Interactive: {"interactive request", "interactive requests"},
		Heavy:       {"heavy analysis", "heavy analyses"},
		Background:  {"background task", "background tasks"},
	}[t.class]
	if n == 0 {
		return "waiting: a worker slot is being freed"
	}
	noun := what[1]
	verb := "are"
	if n == 1 {
		noun, verb = what[0], "is"
	}
	s := fmt.Sprintf("waiting: %d %s %s running (%s)", n, noun, verb, strings.Join(names, ", "))
	if t.class != Interactive {
		s += "; interactive viewing is unaffected"
	}
	return s
}
