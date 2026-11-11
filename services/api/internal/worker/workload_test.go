package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassOf(t *testing.T) {
	for op, want := range map[string]Class{
		"plane": Interactive, "coord": Interactive, "overlay_geometry": Interactive,
		"spectrum": Heavy, "pv": Heavy, "channel_grid": Heavy, "rohsa_index": Heavy, "axial": Heavy,
		"describe": Background, "header": Background, "fingerprint": Background, "fingerprint_full": Background,
	} {
		if got := ClassOf(op); got != want {
			t.Errorf("%s: %v, want %v", op, got, want)
		}
	}
}

func TestBudgetsAreValidated(t *testing.T) {
	if err := DefaultBudgets().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, b := range []Budgets{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {1, 1, MaxBudget + 1}, {-1, 1, 1}} {
		if b.Validate() == nil {
			t.Errorf("%+v accepted", b)
		}
	}
}

func TestBudgetLimitsConcurrencyPerClass(t *testing.T) {
	l := NewLoad(Budgets{Interactive: 2, Heavy: 2, Background: 1})
	ctx := context.Background()
	var rel []func()
	for i := 0; i < 2; i++ {
		_, r, err := l.Acquire(ctx, Heavy, "x")
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, r)
	}
	// a third heavy task waits, while the other classes are untouched
	_, r, err := l.Acquire(ctx, Interactive, "plane")
	if err != nil {
		t.Fatal(err)
	}
	r()
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := l.Acquire(short, Heavy, "third"); err == nil {
		t.Fatal("a third heavy task ran with a budget of two")
	}
	if s := l.Snapshot()[Heavy]; s.Running != 2 || s.Waiting != 0 || s.Budget != 2 {
		t.Fatalf("after the waiter left: %+v", s)
	}
	rel[0]()
	rel[1]()
}

func TestQueueReasonNamesWhatIsRunning(t *testing.T) {
	l := NewLoad(Budgets{Interactive: 1, Heavy: 2, Background: 1})
	_, r1, _ := l.Acquire(WithLabel(context.Background(), "relative orientation"), Heavy, "analysis")
	_, r2, _ := l.Acquire(context.Background(), Heavy, OpLabel("pv"))
	defer r1()
	ctx := WithRequestID(context.Background(), "req-waiting-1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, r, err := l.Acquire(ctx, Heavy, OpLabel("spectrum"))
		if err == nil {
			r()
		}
	}()
	var st RequestState
	for i := 0; i < 400 && st.State != "waiting"; i++ {
		time.Sleep(5 * time.Millisecond)
		st = l.Request("req-waiting-1")
	}
	const want = "waiting: 2 heavy analyses are running (PV diagram, relative orientation); interactive viewing is unaffected"
	if st.State != "waiting" || st.Class != "heavy" || st.Reason != want {
		t.Fatalf("%+v", st)
	}
	if s := l.Snapshot()[Heavy]; s.Waiting != 1 || s.Running != 2 {
		t.Fatalf("%+v", s)
	}
	r2()
	<-done
	if st := l.Request("req-waiting-1"); st.State != "unknown" {
		t.Fatalf("finished request: %+v", st)
	}
	if got := l.Request("nobody-here").State; got != "unknown" {
		t.Fatal(got)
	}
}

func TestQueueReasonForOneAndForInteractive(t *testing.T) {
	l := NewLoad(Budgets{Interactive: 1, Heavy: 1, Background: 1})
	_, r, _ := l.Acquire(context.Background(), Background, "project rescan")
	defer r()
	l.mu.Lock()
	got := l.reasonLocked(&task{class: Background})
	l.mu.Unlock()
	if got != "waiting: 1 background task is running (project rescan); interactive viewing is unaffected" {
		t.Fatal(got)
	}
	_, ri, _ := l.Acquire(context.Background(), Interactive, "plane")
	defer ri()
	l.mu.Lock()
	got = l.reasonLocked(&task{class: Interactive})
	l.mu.Unlock()
	if got != "waiting: 1 interactive request is running (plane)" {
		t.Fatal(got)
	}
	if !strings.Contains(OpLabel("pv"), "PV") || OpLabel("unlisted_op") != "unlisted_op" {
		t.Fatal("labels")
	}
}

// Heavy and background work saturates its own budgets while plane requests are made; they are served from reserved
// interactive capacity within their budget. The fake worker's "slow" request takes 300 ms, so a plane that queued behind
// that work would take at least that long.
func TestInteractiveRequestIsNotDelayedByAFloodOfHeavyAndBackgroundWork(t *testing.T) {
	p := newFake(t, 10*time.Second)
	p.Load = NewLoad(Budgets{Interactive: 2, Heavy: 2, Background: 1})
	if _, err := do(p, "ok"); err != nil { // the interactive worker is warm, as it is while someone is viewing
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		for _, op := range []string{"spectrum", "describe"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = p.Do(context.Background(), map[string]any{"op": op, "path": "slow"})
			}()
		}
	}
	for { // wait until both classes are full and queueing
		s := p.Load.Snapshot()
		if s[Heavy].Running == 2 && s[Heavy].Waiting >= 1 && s[Background].Running == 1 && s[Background].Waiting >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	const budget = 150 * time.Millisecond
	for i := 0; i < 5; i++ {
		start := time.Now()
		if _, err := do(p, "ok"); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > budget {
			t.Fatalf("plane request %d took %v under load, budget %v", i, d, budget)
		}
	}
	if s := p.Load.Snapshot(); s[Heavy].Waiting == 0 && s[Background].Waiting == 0 {
		t.Fatal("the flood ended before the plane requests did; the test measured nothing")
	}
	wg.Wait()
}
