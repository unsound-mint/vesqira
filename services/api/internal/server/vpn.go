package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// vpnRuns allows one VPN command at a time per host and remembers, in memory only, the client Vesqira started for each host.
// A client is not adopted after a restart: it keeps running (it is the user's VPN) but is forgotten.
type vpnRuns struct {
	mu      sync.Mutex
	running map[int64]bool
	clients map[int64]*hosts.VPNClient
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

func (v *vpnRuns) setClient(id int64, c *hosts.VPNClient) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.clients == nil {
		v.clients = map[int64]*hosts.VPNClient{}
	}
	v.clients[id] = c
}

// client returns the last client started for the host (running or not), or nil.
func (v *vpnRuns) client(id int64) *hosts.VPNClient {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.clients[id]
}

// vpnClientView is what the host list shows about the client Vesqira started for a host.
type vpnClientView struct {
	Running   bool      `json:"running"`
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	ExitCode  *int      `json:"exitCode,omitempty"` // once it ended; -1 when ended by a signal
	Output    string    `json:"output"`
}

func (s *Server) vpnClientView(id int64) *vpnClientView {
	c := s.vpn.client(id)
	if c == nil {
		return nil
	}
	v := &vpnClientView{Running: c.Running(), Pid: c.Pid, StartedAt: c.Started}
	if code, _, ok := c.Exit(); ok {
		v.ExitCode = &code
	}
	v.Output, _ = c.Output()
	return v
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
	switch in.Action {
	case "connect":
		if len(h.VPNCommand) == 0 {
			fail(w, 409, "this host has no VPN connect command")
			return
		}
	case "disconnect":
		if c := s.vpn.client(h.ID); len(h.VPNDisconnectCommand) == 0 && (c == nil || !c.Running()) {
			fail(w, 409, "this host has no VPN disconnect command, and no VPN client started by Vesqira is running for it")
			return
		}
	default:
		fail(w, 422, `action must be "connect" or "disconnect"`)
		return
	}
	if !s.vpn.begin(h.ID) {
		fail(w, 409, "a VPN command is already running for this host")
		return
	}
	defer s.vpn.end(h.ID)
	// A closed tab does not stop a half-finished connect; the time limits do.
	ctx := context.WithoutCancel(r.Context())
	if in.Action == "connect" {
		s.vpnConnect(ctx, w, h)
		return
	}
	s.vpnDisconnect(ctx, w, h)
}

func (s *Server) vpnWindow() time.Duration {
	if s.VPNTimeout > 0 {
		return s.VPNTimeout
	}
	return hosts.DefaultVPNRun
}

func (s *Server) vpnLogPath(h hosts.Host) string {
	dir := s.VPNLogDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("vesqira-vpn-%d", os.Getuid()))
	}
	return filepath.Join(dir, fmt.Sprintf("host-%d.log", h.ID))
}

// vpnConnect starts the stored client detached and leaves it running. It then asks the host check every few seconds for up to
// the wait window: reachable means the tunnel works. A client that exits first is reported with its exit code and output (a
// client that daemonizes itself exits 0 right after forking and gets one host check, as before).
func (s *Server) vpnConnect(ctx context.Context, w http.ResponseWriter, h hosts.Host) {
	if c := s.vpn.client(h.ID); c != nil && c.Running() {
		fail(w, 409, fmt.Sprintf("a VPN client started by Vesqira is already running for this host (since %s); disconnect it first", c.Started.Format("15:04:05")))
		return
	}
	start := time.Now()
	c, err := hosts.StartVPNClient(h.VPNCommand, s.vpnLogPath(h))
	if err != nil {
		s.Audit.RecordCtx(ctx, "host vpn connect", "host", h.ID, h.Name+": did not start")
		writeJSON(w, 200, vpnReply{Action: "connect", ExitCode: -1, Output: err.Error(), Message: "VPN command could not be started; the host is still not reachable"})
		return
	}
	s.vpn.setClient(h.ID, c)
	s.Audit.RecordCtx(ctx, "host vpn connect", "host", h.ID, fmt.Sprintf("%s: client started (pid %d)", h.Name, c.Pid))
	go func() { // also records an exit long after the request ended
		<-c.Done()
		code, _, _ := c.Exit()
		s.Audit.RecordCtx(ctx, "host vpn client exit", "host", h.ID, fmt.Sprintf("%s: exit %d after %s", h.Name, code, time.Since(c.Started).Round(time.Second)))
	}()

	window, poll := s.vpnWindow(), s.VPNPoll
	if poll <= 0 {
		poll = 3 * time.Second
	}
	deadline := time.Now().Add(window)
	reachable, exited := false, false
	s.Remote.Forget(h.ID) // the client may change the network under a worker that holds the old path
wait:
	for time.Now().Before(deadline) {
		select {
		case <-c.Done():
			exited = true
			break wait
		case <-time.After(poll):
		}
		pctx, cancel := context.WithTimeout(ctx, min(15*time.Second, time.Until(deadline)+time.Second))
		_, _, _, reachable = s.pingHost(pctx, h)
		cancel()
		if reachable {
			break
		}
		s.Remote.Forget(h.ID)
	}
	if !reachable && !exited { // the client may have ended while the last check ran
		select {
		case <-c.Done():
			exited = true
		default:
		}
	}
	rep := vpnReply{Action: "connect", ExitCode: -1}
	if exited {
		code, _, _ := c.Exit()
		rep.ExitCode = code
		s.Remote.Forget(h.ID)
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, _, _, reachable = s.pingHost(pctx, h)
		cancel()
	}
	rep.DurationMs = time.Since(start).Milliseconds()
	rep.Reachable = reachable
	rep.Output, rep.OutputTruncated = c.Output()
	elapsed := time.Since(start).Round(time.Second)
	switch {
	case exited && rep.ExitCode == 0:
		rep.Message = "VPN command finished; " + reachText(reachable)
	case exited:
		rep.Message = fmt.Sprintf("VPN client exited with code %d; %s", rep.ExitCode, reachText(reachable))
		if rep.ExitCode < 0 {
			rep.Message = "VPN client was ended by a signal; " + reachText(reachable)
		}
	case reachable:
		rep.Message = fmt.Sprintf("VPN client started; the host became reachable after %d s", int(elapsed.Seconds()))
	default:
		rep.TimedOut = true
		rep.Message = fmt.Sprintf("VPN client is running but the host is still not reachable after %s", window.Round(time.Second))
	}
	if reachable && (!exited || rep.ExitCode == 0) {
		s.reach.kick(h.ID) // the job watcher need not wait out its backoff
	}
	writeJSON(w, 200, rep)
}

func reachText(reachable bool) string {
	if reachable {
		return "the host is reachable"
	}
	return "the host is still not reachable"
}

// vpnDisconnect runs the configured disconnect command; without one it ends the client Vesqira started (SIGTERM to its process
// group, SIGKILL after five seconds).
func (s *Server) vpnDisconnect(ctx context.Context, w http.ResponseWriter, h hosts.Host) {
	c := s.vpn.client(h.ID)
	rep := vpnReply{Action: "disconnect"}
	start := time.Now()
	if len(h.VPNDisconnectCommand) > 0 {
		res := hosts.RunVPN(ctx, h.VPNDisconnectCommand, s.vpnWindow())
		ended := fmt.Sprintf("exit %d", res.ExitCode)
		switch {
		case res.TimedOut:
			ended = "timed out"
		case res.StartError != "":
			ended = "did not start"
		}
		s.Audit.RecordCtx(ctx, "host vpn disconnect", "host", h.ID, fmt.Sprintf("%s: command %s after %d ms", h.Name, ended, res.Duration.Milliseconds()))
		rep.ExitCode, rep.TimedOut, rep.Output, rep.OutputTruncated = res.ExitCode, res.TimedOut, res.Output, res.OutputTruncated
		if res.StartError != "" {
			rep.Output = res.StartError
		}
		rep.Message = "VPN command finished"
		switch {
		case res.TimedOut:
			rep.Message = fmt.Sprintf("VPN command did not finish within %s and was stopped", s.vpnWindow().Round(time.Second))
		case res.StartError != "":
			rep.Message = "VPN command could not be started"
		case res.ExitCode != 0:
			rep.Message = fmt.Sprintf("VPN command failed (exit %d)", res.ExitCode)
		}
		if c != nil && c.Running() { // a client that the command asked to end gets a moment; it is not killed
			select {
			case <-c.Done():
			case <-time.After(5 * time.Second):
			}
		}
	} else {
		killed, err := c.Stop(5 * time.Second)
		how := "SIGTERM"
		if killed {
			how = "SIGKILL"
		}
		if err != nil {
			s.Audit.RecordCtx(ctx, "host vpn disconnect", "host", h.ID, fmt.Sprintf("%s: client (pid %d) could not be stopped", h.Name, c.Pid))
			fail(w, 500, err.Error())
			return
		}
		s.Audit.RecordCtx(ctx, "host vpn disconnect", "host", h.ID, fmt.Sprintf("%s: client (pid %d) stopped with %s", h.Name, c.Pid, how))
		rep.Output, rep.OutputTruncated = c.Output()
		rep.Message = "VPN client stopped"
		if killed {
			rep.Message = "VPN client did not end on SIGTERM and was killed"
		}
	}
	s.Remote.Forget(h.ID)
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, _, _, rep.Reachable = s.pingHost(pctx, h)
	rep.DurationMs = time.Since(start).Milliseconds()
	rep.Message += "; " + reachText(rep.Reachable)
	writeJSON(w, 200, rep)
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
	s.Audit.RecordCtx(r.Context(), "host vpn commands", "host", h.ID, fmt.Sprintf("%s: connect %s, disconnect %s", h.Name, setOrCleared(nh.VPNCommand), setOrCleared(nh.VPNDisconnectCommand)))
	writeJSON(w, 200, nh)
}

func setOrCleared(a []string) string {
	if len(a) == 0 {
		return "cleared"
	}
	return "set"
}
