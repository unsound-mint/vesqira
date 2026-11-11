package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The VPN companion (ADR 0029 step 3, SECURITY.md). Owner listener only, behind --enable-vpn. The page may say "connect" or
// "disconnect" and nothing else: the argv comes from the stored host and runs without a shell (hosts.RunVPN).

// reachState is what is known about one host: the last answer of a host check or of the job watcher.
type reachState struct {
	up   bool
	at   time.Time
	kick bool // the job watcher should ask this host now (a VPN connect just succeeded)
}

// reachability remembers, in memory only, whether each host last answered. It is what the host list shows as "reachable"
// and what lets the watcher retry at once after a VPN comes up. The zero value is ready to use.
type reachability struct {
	mu   sync.Mutex
	m    map[int64]*reachState
	wake chan struct{}
}

func (r *reachability) set(id int64, up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[int64]*reachState{}
	}
	st := r.m[id]
	if st == nil {
		st = &reachState{}
		r.m[id] = st
	}
	st.up, st.at = up, time.Now()
}

// get returns "yes", "no" or "unknown" (never asked in this session) and when it was asked.
func (r *reachability) get(id int64) (string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.m[id]
	switch {
	case st == nil:
		return "unknown", time.Time{}
	case st.up:
		return "yes", st.at
	}
	return "no", st.at
}

// kick asks the job watcher to call the host at once and forget its backoff.
func (r *reachability) kick(id int64) {
	r.mu.Lock()
	if r.m == nil {
		r.m = map[int64]*reachState{}
	}
	if r.m[id] == nil {
		r.m[id] = &reachState{}
	}
	r.m[id].kick = true
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	ch := r.wake
	r.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (r *reachability) takeKick(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.m[id]
	if st == nil || !st.kick {
		return false
	}
	st.kick = false
	return true
}

func (r *reachability) wakeCh() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	return r.wake
}

// vpnRuns allows one VPN command at a time per host.
type vpnRuns struct {
	mu      sync.Mutex
	running map[int64]bool
}

func (v *vpnRuns) begin(id int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running[id] {
		return false
	}
	if v.running == nil {
		v.running = map[int64]bool{}
	}
	v.running[id] = true
	return true
}

func (v *vpnRuns) end(id int64) {
	v.mu.Lock()
	delete(v.running, id)
	v.mu.Unlock()
}

// pingHost asks the host's worker who it is (deploying it if needed) and records whether the host answered. A worker that
// answers with an error still proves the host is reachable. status is 0 on success, 422 for a worker error, 502 when the
// host could not be reached.
func (s *Server) pingHost(ctx context.Context, h hosts.Host) (rep worker.Reply, status int, msg string, reachable bool) {
	do := s.Remote.Do
	if s.pingHook != nil { // tests stand in for the host check
		do = s.pingHook
	}
	rep, err := do(ctx, h, map[string]any{"op": "ping"})
	if err == nil {
		s.reach.set(h.ID, true)
		return rep, 0, "", true
	}
	var re *worker.RequestError
	if errors.As(err, &re) {
		s.reach.set(h.ID, true)
		return rep, 422, re.Message, true
	}
	s.reach.set(h.ID, false)
	return rep, 502, "could not run the worker on the host: check that ssh works for it without a prompt, the VPN is up, and the Python command has astropy", false
}

// vpnReply is what a run of the VPN command reports. The output is shown to the owner and never written to the audit log.
type vpnReply struct {
	Action          string `json:"action"`
	ExitCode        int    `json:"exitCode"`
	TimedOut        bool   `json:"timedOut"`
	DurationMs      int64  `json:"durationMs"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"outputTruncated"`
	Reachable       bool   `json:"reachable"`
	Message         string `json:"message"`
}

func (s *Server) hostVPN(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"` // the only thing the page may send
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	var argv []string
	switch in.Action {
	case "connect":
		argv = h.VPNCommand
	case "disconnect":
		argv = h.VPNDisconnectCommand
	default:
		fail(w, 422, `action must be "connect" or "disconnect"`)
		return
	}
	if len(argv) == 0 {
		fail(w, 409, "this host has no VPN "+in.Action+" command")
		return
	}
	if !s.vpn.begin(h.ID) {
		fail(w, 409, "a VPN command is already running for this host")
		return
	}
	defer s.vpn.end(h.ID)

	timeout := s.VPNTimeout
	if timeout <= 0 {
		timeout = hosts.DefaultVPNRun
	}
	// A closed tab does not stop a half-finished connect; the time limit does.
	res := hosts.RunVPN(context.WithoutCancel(r.Context()), argv, timeout)
	ended := fmt.Sprintf("exit %d", res.ExitCode)
	switch {
	case res.TimedOut:
		ended = "timed out"
	case res.StartError != "":
		ended = "did not start"
	}
	s.Audit.Record("host vpn "+in.Action, "host", h.ID, fmt.Sprintf("%s: %s after %d ms", h.Name, ended, res.Duration.Milliseconds()))

	// The command may have changed the network under a worker that is still holding the old path: start the check fresh.
	s.Remote.Forget(h.ID)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	_, _, _, reachable := s.pingHost(pctx, h)
	if in.Action == "connect" && res.ExitCode == 0 && reachable {
		s.reach.kick(h.ID) // the job watcher need not wait out its backoff
	}

	what := "VPN command finished"
	switch {
	case res.TimedOut:
		what = fmt.Sprintf("VPN command did not finish within %s and was stopped", timeout.Round(time.Second))
	case res.StartError != "":
		what = "VPN command could not be started"
	case res.ExitCode != 0:
		what = fmt.Sprintf("VPN command failed (exit %d)", res.ExitCode)
	}
	state := "the host is reachable"
	if !reachable {
		state = "the host is still not reachable"
	}
	out := res.Output
	if res.StartError != "" {
		out = res.StartError
	}
	writeJSON(w, 200, vpnReply{Action: in.Action, ExitCode: res.ExitCode, TimedOut: res.TimedOut, DurationMs: res.Duration.Milliseconds(),
		Output: out, OutputTruncated: res.OutputTruncated, Reachable: reachable, Message: what + "; " + state})
}

// setHostVPN replaces a host's VPN commands. Only the owner's host form calls it, and only with --enable-vpn.
func (s *Server) setHostVPN(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		VPNCommand           []string `json:"vpnCommand"`
		VPNDisconnectCommand []string `json:"vpnDisconnectCommand"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	nh, err := s.HostDB.SetVPN(h.ID, in.VPNCommand, in.VPNDisconnectCommand)
	if err != nil {
		s.hostErr(w, err)
		return
	}
	s.Audit.Record("host vpn commands", "host", h.ID, fmt.Sprintf("%s: connect %s, disconnect %s", h.Name, setOrCleared(nh.VPNCommand), setOrCleared(nh.VPNDisconnectCommand)))
	writeJSON(w, 200, nh)
}

func setOrCleared(a []string) string {
	if len(a) == 0 {
		return "cleared"
	}
	return "set"
}
