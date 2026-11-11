package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func (e *watchEnv) sacctFile(name string, lines ...string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.bin, "state", name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// sacctCalls is every sacct call the stand-in saw, as its arguments.
func (e *watchEnv) sacctCalls() []string {
	b, _ := os.ReadFile(filepath.Join(e.bin, "state", "sacct.calls"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// endOne runs one job from RUNNING to COMPLETED through the watcher and returns its id.
func (e *watchEnv) endOne(name string, w *jobWatcher, t0 time.Time) (id int64) {
	e.t.Helper()
	j := e.submit(e.draft(name, nil), nil)
	e.setSlurm(j.SlurmID, "RUNNING")
	e.srv.watchOnce(context.Background(), w, t0)
	e.setSlurm(j.SlurmID, "COMPLETED")
	e.srv.watchOnce(context.Background(), w, t0.Add(2*MinWatchInterval))
	e.srv.waitRecords()
	e.srv.waitImports()
	return j.ID
}

const helpAll = "Account AdminComment AllocCPUS AllocNodes JobID State ExitCode Elapsed Start End Timelimit ReqMem ReqTRES TotalCPU MaxRSS NNodes"

func TestASiteWhoseSacctRejectsAColumnStillTracksStatesAndNamesWhatItLacks(t *testing.T) {
	e := newWatchEnv(t)
	e.sacctFile("sacct.reject", "ReqTRES")
	w, t0 := newJobWatcher(MinWatchInterval), time.Now()
	id := e.endOne("vgt-a", w, t0)
	j := e.job(id)
	if j.State != "completed" {
		t.Fatalf("the rejected column stopped state tracking: %s", j.State)
	}
	// the other columns still arrive: only what sacct named is missing, and it is said so
	if u := j.Usage; u == nil || !reflect.DeepEqual(u.Unsupported, []string{"ReqTRES"}) || u.ReqMemBytes == nil || *u.ReqMemBytes != 4<<30 || u.MaxRSSBytes == nil {
		t.Fatalf("%+v", u)
	}
	// the host remembers: the next job is asked for without ReqTRES at once, no failing call, no probe
	before := len(e.sacctCalls())
	e.endOne("vgt-b", w, t0.Add(time.Hour))
	for _, c := range e.sacctCalls()[before:] {
		if strings.Contains(c, "ReqTRES") || strings.Contains(c, "helpformat") {
			t.Errorf("asked again for a column the host rejected: %s", c)
		}
	}
}

func TestASiteWhoseSacctRejectsSeveralColumnsFallsBackToTheMinimalCallAndRemembersIt(t *testing.T) {
	e := newWatchEnv(t)
	e.sacctFile("sacct.reject", "ReqTRES", "NNodes")
	w, t0 := newJobWatcher(MinWatchInterval), time.Now()
	id := e.endOne("vgt-a", w, t0)
	j := e.job(id)
	if j.State != "completed" || j.Usage == nil {
		t.Fatalf("state %s, usage %+v", j.State, j.Usage)
	}
	u := j.Usage
	if u.State != "COMPLETED" || u.ElapsedSeconds != 9 || u.MaxRSSBytes != nil || u.ReqMemBytes != nil || u.TimelimitSec != nil || u.CPUEfficiency != nil {
		t.Errorf("usage must hold what the minimal columns give and nothing invented: %+v", u)
	}
	for _, f := range []string{"ReqTRES", "NNodes", "MaxRSS"} {
		found := false
		for _, g := range u.Unsupported {
			found = found || g == f
		}
		if !found {
			t.Errorf("%s not named among the unsupported: %v", f, u.Unsupported)
		}
	}
	calls := e.sacctCalls()
	last := calls[len(calls)-1]
	if !strings.Contains(last, "-X") || strings.Contains(last, "Timelimit") {
		t.Errorf("the last call must be the minimal one: %s", last)
	}
	// later refreshes go straight to the minimal call
	before := len(calls)
	e.endOne("vgt-b", w, t0.Add(time.Hour))
	for _, c := range e.sacctCalls()[before:] {
		if strings.Contains(c, "Timelimit") || strings.Contains(c, "helpformat") {
			t.Errorf("the extended call was tried again inside 6 hours: %s", c)
		}
	}
	// after 6 hours the host is asked again (it may have been upgraded): the probe, then the extended call
	e.sacctFile("sacct.reject", "NoSuchField")
	e.sacctFile("sacct.helpformat", helpAll)
	before = len(e.sacctCalls())
	k := e.endOne("vgt-c", w, t0.Add(7*time.Hour))
	re := strings.Join(e.sacctCalls()[before:], "\n")
	if !strings.Contains(re, "helpformat") || !strings.Contains(re, "ReqTRES") {
		t.Errorf("no re-probe after 6 hours: %s", re)
	}
	if u := e.job(k).Usage; u == nil || u.MaxRSSBytes == nil || len(u.Unsupported) != 0 {
		t.Errorf("an upgraded site must record full usage again: %+v", u)
	}
}

func TestASiteThatSupportsEveryColumnRecordsUsageWithOneProbeAndNoFallback(t *testing.T) {
	e := newWatchEnv(t)
	e.sacctFile("sacct.helpformat", helpAll)
	w, t0 := newJobWatcher(MinWatchInterval), time.Now()
	id := e.endOne("vgt-a", w, t0)
	u := e.job(id).Usage
	if u == nil || u.MaxRSSBytes == nil || *u.MaxRSSBytes != 1<<30 || u.ReqMemBytes == nil || len(u.Unsupported) != 0 || u.Unsupported == nil {
		t.Fatalf("%+v", u)
	}
	probes := 0
	for _, c := range e.sacctCalls() {
		if strings.Contains(c, "helpformat") {
			probes++
		} else if strings.Contains(c, "-X") {
			t.Errorf("a minimal call on a site that supports everything: %s", c)
		}
	}
	e.endOne("vgt-b", w, t0.Add(time.Hour))
	if probes != 1 {
		t.Errorf("%d probes", probes)
	}
}

func TestTheHelpformatProbeRequestsOnlyTheColumnsTheSiteLists(t *testing.T) {
	e := newWatchEnv(t)
	e.sacctFile("sacct.helpformat", "AllocCPUS JobID State ExitCode Elapsed Start End Timelimit ReqMem TotalCPU NNodes") // no ReqTRES, no MaxRSS
	e.sacctFile("sacct.reject", "ReqTRES", "MaxRSS")                                                                     // a call that asked for them would fail
	w, t0 := newJobWatcher(MinWatchInterval), time.Now()
	id := e.endOne("vgt-a", w, t0)
	j := e.job(id)
	u := j.Usage
	if j.State != "completed" || u == nil || !reflect.DeepEqual(u.Unsupported, []string{"ReqTRES", "MaxRSS"}) || u.ReqMemBytes == nil || *u.ReqMemBytes != 4<<30 || u.MaxRSSBytes != nil {
		t.Fatalf("%s %+v", j.State, u)
	}
	for _, c := range e.sacctCalls() {
		if strings.Contains(c, "ReqTRES") || strings.Contains(c, "MaxRSS") {
			t.Errorf("a column the probe did not list was requested: %s", c)
		}
	}
}

func TestAHostWhoseMinimalSacctFailsToo(t *testing.T) {
	e := newWatchEnv(t)
	e.sacctFile("sacct.reject", "State") // nothing can be asked: accounting is unusable
	j := e.submit(e.draft("vgt-a", nil), nil)
	e.setSlurm(j.SlurmID, "COMPLETED")
	w := newJobWatcher(MinWatchInterval)
	e.srv.watchOnce(context.Background(), w, time.Now())
	if got := e.job(j.ID).State; got == "completed" || got == "failed" {
		t.Errorf("a failing sacct changed the job: %s", got)
	}
	if w.host(e.host.ID).fails != 1 {
		t.Errorf("the failure must count as the host not answering")
	}
	// and the failure is not remembered as a capability: when sacct works again the end is read
	os.Remove(filepath.Join(e.bin, "state", "sacct.reject"))
	e.srv.watchOnce(context.Background(), w, time.Now().Add(time.Hour))
	e.srv.waitRecords()
	e.srv.waitImports()
	if got := e.job(j.ID); got.State != "completed" || got.Usage == nil || got.Usage.MaxRSSBytes == nil {
		t.Errorf("%s %+v", got.State, got.Usage)
	}
}
