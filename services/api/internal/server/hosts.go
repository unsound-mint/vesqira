package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// hostPlanes sends plane requests for datasets on one remote host to that host's worker.
type hostPlanes struct {
	m *hosts.Manager
	h hosts.Host
}

func (p hostPlanes) Do(ctx context.Context, req map[string]any) (worker.Reply, error) {
	return p.m.Do(ctx, p.h, req)
}

// planesFor picks the worker that holds a dataset's file: the local one, or its host's.
func (s *Server) planesFor(ws workspace.ID, datasetID int64) PlaneService {
	d, err := s.Store.Get(ws, datasetID)
	if err != nil {
		return s.planesOn(0)
	}
	return s.planesOn(d.HostID)
}

// planesOn is the worker on one machine: this computer (host 0) or a host.
// planesOn is the plane worker for a machine, with the researcher's corrections attached to every request and their echo checked
// (ADR 0061, corrections.go).
func (s *Server) planesOn(host int64) PlaneService {
	svc := s.planesOnRaw(host)
	if svc == nil {
		return nil
	}
	return correctingService{s: s, host: host, inner: svc}
}

// planesOnRaw is the plane worker without corrections attached: for the preview, which sends the states it compares itself.
func (s *Server) planesOnRaw(host int64) PlaneService {
	if host == 0 {
		if s.Planes == nil {
			return nil
		}
		return s.Planes
	}
	if s.HostDB == nil || s.Remote == nil {
		return nil
	}
	h, err := s.HostDB.Get(host)
	if err != nil {
		return nil
	}
	return hostPlanes{s.Remote, h}
}

// authorizeRemote is authorize for a file on a host: the stored path must still lie in the host's roots and the remote
// worker (which enforces the roots on the resolved path) must report the size and modification time registered.
func (s *Server) authorizeRemote(ctx context.Context, d catalog.Dataset) (real string, k cacheKey, status int, msg string) {
	if s.HostDB == nil || s.Remote == nil {
		return "", k, 503, "remote hosts are not configured"
	}
	h, err := s.HostDB.Get(d.HostID)
	if err != nil || !h.InRoots(d.Path) {
		return "", k, 403, "not available"
	}
	if state, at := s.reach.get(d.HostID); state == "no" && s.Offline.Has(d.ID) {
		// The host did not answer last time and a copy can answer: do not wait out another connect timeout for every request of a
		// scrub. One background probe at a time asks the host again and updates the state, so the first request after the VPN
		// is back may still be answered from the copy and the next ones are live (ADR 0059).
		s.probeHost(h, d.Path, at)
		return "", k, 502, msgHostUnreachable
	}
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "stat", "path": d.Path})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return "", k, 403, "not available"
	case err != nil:
		if ctx.Err() == nil { // a request the browser gave up on says nothing about the host
			s.reach.set(d.HostID, false)
		}
		return "", k, 502, msgHostUnreachable
	}
	s.reach.set(d.HostID, true)
	var st struct {
		Size    int64 `json:"size"`
		MtimeNS int64 `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &st) != nil {
		return "", k, 502, "the host answered unexpectedly"
	}
	s.Offline.NoteSeen(d.ID, d.HostID, st.Size, st.MtimeNS) // a copy made from another signature is stale from here on
	if st.Size != d.SizeBytes || st.MtimeNS != d.ModifiedAt.UnixNano() {
		return "", k, 409, "the file changed since it was registered; register it again"
	}
	s.notePath(wsCtx(ctx), d.HostID, d.Path, d.ID)
	return d.Path, cacheKey{id: d.ID, host: d.HostID, size: st.Size, mtime: st.MtimeNS, unit: declaredUnitOf(d), corr: corrHash(d)}, 0, ""
}

func (s *Server) hostFrom(w http.ResponseWriter, r *http.Request) (hosts.Host, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return hosts.Host{}, false
	}
	h, err := s.HostDB.Get(id)
	if err != nil {
		fail(w, 404, "host not found")
		return h, false
	}
	return h, true
}

func (s *Server) hostErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hosts.ErrInvalid):
		fail(w, 422, err.Error())
	case errors.Is(err, hosts.ErrNotFound):
		fail(w, 404, "host not found")
	default:
		log.Printf("hosts: %v", err)
		fail(w, 500, "internal error")
	}
}

// hostView is a host with what this session last learned about reaching it (a host check or the job watcher; not stored).
type hostView struct {
	hosts.Host
	Reachable   string         `json:"reachable"`            // "yes", "no" or "unknown"
	ReachableAt time.Time      `json:"reachableAt,omitzero"` // when that was learned
	VPNClient   *vpnClientView `json:"vpnClient,omitempty"`  // the VPN client Vesqira started for this host in this session
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hs, err := s.HostDB.List()
	if err != nil {
		s.hostErr(w, err)
		return
	}
	out := make([]hostView, len(hs))
	for i, h := range hs {
		out[i] = hostView{Host: h}
		out[i].Reachable, out[i].ReachableAt = s.reach.get(h.ID)
		out[i].VPNClient = s.vpnClientView(h.ID)
	}
	writeJSON(w, 200, map[string]any{"hosts": out})
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                 string   `json:"name"`
		Dest                 string   `json:"dest"`
		Python               string   `json:"python"`
		Roots                []string `json:"roots"`
		VPNCommand           []string `json:"vpnCommand"`
		VPNDisconnectCommand []string `json:"vpnDisconnectCommand"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if (len(in.VPNCommand) > 0 || len(in.VPNDisconnectCommand) > 0) && !s.VPNEnabled {
		fail(w, 403, "VPN commands are off: start Vesqira with --enable-vpn to set one")
		return
	}
	if strings.TrimSpace(in.Python) == "" {
		in.Python = "python3" // the app does not ask for it; the host check reports a missing astropy
	}
	h, err := s.HostDB.Create(hosts.Host{Name: in.Name, Dest: in.Dest, Python: in.Python, Roots: in.Roots, VPNCommand: in.VPNCommand, VPNDisconnectCommand: in.VPNDisconnectCommand})
	if err != nil {
		s.hostErr(w, err)
		return
	}
	s.Audit.RecordCtx(r.Context(), "host added", "host", h.ID, h.Name)
	writeJSON(w, 201, h)
}

// browseHost lists folders on a host that is not saved yet, so the source folders are chosen, not typed. Owner-only.
func (s *Server) browseHost(w http.ResponseWriter, r *http.Request) {
	dir, dirs, err := s.Remote.BrowseDirs(r.Context(), r.URL.Query().Get("dest"), r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, hosts.ErrInvalid):
		fail(w, 400, err.Error())
	case err != nil:
		fail(w, 502, "could not list that folder on the host (is the VPN up, and does ssh work for it without a prompt?)")
	default:
		writeJSON(w, 200, map[string]any{"path": dir, "entries": dirs})
	}
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	if err := s.HostDB.Delete(h.ID); err != nil {
		s.hostErr(w, err)
		return
	}
	s.Remote.Forget(h.ID)
	s.Audit.RecordCtx(r.Context(), "host removed", "host", h.ID, h.Name)
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// checkHost deploys the worker if needed and asks it who it is: Python, astropy, and whether it enforces the roots.
func (s *Server) checkHost(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rep, status, msg, _ := s.pingHost(ctx, h)
	if status != 0 {
		fail(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rep.Head)
}

func (s *Server) listRemote(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = h.Roots[0]
	}
	if !h.InRoots(dir) {
		fail(w, 403, "not available")
		return
	}
	rep, err := s.Remote.Do(r.Context(), h, map[string]any{"op": "list", "path": dir})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		fail(w, 403, "not available")
		return
	case err != nil:
		fail(w, 502, "the host is not reachable (is the VPN up?)")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rep.Head)
}

// registerRemoteFile records a FITS file that lives on a host. Its header is read there; the data stays there.
// Returns the dataset, or an HTTP status and message. found=false means the path is not available (outside the
// roots, missing, or not a regular file): the same answer for all of them.
func (s *Server) registerRemoteFile(ctx context.Context, h hosts.Host, p string) (d catalog.Dataset, code int, msg string) {
	realPath, _, _, rcode, rmsg := s.resolveRemote(ctx, h, p)
	if rcode != 0 {
		return d, rcode, rmsg
	}
	var re *worker.RequestError
	hdr, err := s.Remote.Do(ctx, h, map[string]any{"op": "header", "path": realPath})
	if errors.As(err, &re) {
		return d, 422, "cannot read file: " + re.Message
	}
	if err != nil {
		return d, 502, "the host is not reachable (is the VPN up?)"
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(hdr.Head, &meta) != nil {
		return d, 502, "the host answered unexpectedly"
	}
	var size, mtime int64
	_ = json.Unmarshal(meta["size"], &size)
	_ = json.Unmarshal(meta["mtime_ns"], &mtime)
	return s.storeRemoteFile(ctx, h, realPath, size, mtime, meta, nil)
}

// storeRemoteFile catalogs a remote file whose header report (meta) and stat are known, then records what is known of its
// bytes. Registering one file and registering a batch (registerRemoteBatch) both end here, so they give the same rows.
// sampled, when not nil, is a fingerprint already measured on the host; nil measures it with a worker call.
func (s *Server) storeRemoteFile(ctx context.Context, h hosts.Host, realPath string, size, mtime int64, meta map[string]json.RawMessage, sampled func() (measured, int, string)) (d catalog.Dataset, code int, msg string) {
	for _, k := range []string{"nbytes", "op", "size", "mtime_ns"} {
		delete(meta, k)
	}
	clean, _ := json.Marshal(meta)
	d, err := s.Store.Upsert(wsCtx(ctx), actorID(ctx), catalog.Dataset{HostID: h.ID, Path: realPath, Name: path.Base(realPath), SizeBytes: size, ModifiedAt: time.Unix(0, mtime), Header: clean})
	if errors.Is(err, catalog.ErrLocationExists) {
		return d, 409, "this file is already another location of a dataset: remove that location first, or open that dataset"
	}
	if err != nil {
		log.Printf("register remote: %v", err)
		return d, 500, "internal error"
	}
	return s.recordIdentityWith(ctx, d, sampled), 0, ""
}

// Registering up to remoteBatchMax files in one worker call. The request line must fit the worker channel (8 KiB, so a
// batch is cut by the bytes of its paths too), and a long call must come back with what it did (the worker stops after
// its own time budget and says how far it got).
const (
	remoteBatchMax       = 32
	remoteBatchPathBytes = 6 << 10
)

// batchOf is how many of the leading paths go in one register_many call.
func batchOf(paths []string) int {
	n, bytes := 0, 0
	for _, p := range paths {
		bytes += len(p) + 4
		if n > 0 && (n == remoteBatchMax || bytes > remoteBatchPathBytes) {
			break
		}
		n++
	}
	return n
}

type remoteResult struct {
	d    catalog.Dataset
	code int
	msg  string
}

// registerRemoteBatch registers the leading files of paths with one worker call (op register_many) and returns one result
// per file the worker reached, in order; the caller sends the rest again. A file is refused alone, with the status and
// sentence registerRemoteFile gives. tooOld reports a worker that does not know the operation: nothing was done, and the
// caller registers one by one.
func (s *Server) registerRemoteBatch(ctx context.Context, h hosts.Host, paths []string) (out []remoteResult, tooOld bool) {
	n := batchOf(paths)
	out = make([]remoteResult, 0, n)
	var send []string
	for _, p := range paths[:n] {
		if !h.InRoots(p) {
			break // refused without a round trip, like the single route; keep the order by sending what precedes it
		}
		send = append(send, p)
	}
	if len(send) == 0 { // the first path is outside the roots
		return []remoteResult{{code: 403, msg: "not available"}}, false
	}
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "register_many", "paths": send})
	var re *worker.RequestError
	if errors.As(err, &re) && re.Code == "bad_request" {
		return nil, true
	}
	if err != nil {
		for range send {
			out = append(out, remoteResult{code: 502, msg: "the host is not reachable (is the VPN up?)"})
		}
		return out, false
	}
	var reply struct {
		Files []struct {
			OK      bool                       `json:"ok"`
			Stage   string                     `json:"stage"`
			Error   string                     `json:"error"`
			Real    string                     `json:"real"`
			Size    int64                      `json:"size"`
			MtimeNS int64                      `json:"mtime_ns"`
			Header  map[string]json.RawMessage `json:"header"`
			Sampled string                     `json:"sampled"`
			SampErr *struct {
				Error string `json:"error"`
			} `json:"sampled_error"`
		} `json:"files"`
	}
	if json.Unmarshal(rep.Head, &reply) != nil || len(reply.Files) == 0 || len(reply.Files) > len(send) {
		for range send {
			out = append(out, remoteResult{code: 502, msg: "the host answered unexpectedly"})
		}
		return out, false
	}
	for _, f := range reply.Files {
		switch {
		case !f.OK && f.Stage == "header":
			out = append(out, remoteResult{code: 422, msg: "cannot read file: " + f.Error})
		case !f.OK:
			out = append(out, remoteResult{code: 403, msg: "not available"})
		case !h.InRoots(f.Real) || f.Header == nil:
			out = append(out, remoteResult{code: 403, msg: "not available"})
		default:
			// the same check measureRemote makes: a fingerprint is stored only when the file is still what the header read saw
			sampled := func() (measured, int, string) {
				switch {
				case f.SampErr != nil:
					return measured{}, 409, f.SampErr.Error
				case f.Sampled == "":
					return measured{}, 502, "the host answered unexpectedly"
				}
				return measured{f.Sampled, "worker on " + h.Name, f.Size, f.MtimeNS}, 0, ""
			}
			d, code, msg := s.storeRemoteFile(ctx, h, f.Real, f.Size, f.MtimeNS, f.Header, sampled)
			out = append(out, remoteResult{d: d, code: code, msg: msg})
		}
	}
	return out, false
}

func (s *Server) registerRemote(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	d, code, msg := s.registerRemoteFile(ctx, h, in.Path)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, d)
}

// remoteText reads a small text file on a host (inside its roots) through the host's worker.
func (s *Server) remoteText(ctx context.Context, h hosts.Host, p string) (string, int, string) {
	if !h.InRoots(p) {
		return "", 403, "not available"
	}
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "readtext", "path": p})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return "", 403, "not available"
	case err != nil:
		return "", 502, "the host is not reachable (is the VPN up?)"
	}
	var t struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(rep.Head, &t) != nil {
		return "", 502, "the host answered unexpectedly"
	}
	return t.Text, 0, ""
}

// sshConfigHosts offers the host aliases written in the user's ssh configuration, so a host can be added by picking a name
// that already works with `ssh <name>`. Nothing else from the file (keys, proxy commands) is read out.
func (s *Server) sshConfigHosts(w http.ResponseWriter, r *http.Request) {
	if s.SSHConfig == "" {
		writeJSON(w, 200, map[string]any{"found": false, "hosts": []hosts.SSHHost{}})
		return
	}
	list, found := hosts.LoadSSHConfig(s.SSHConfig)
	writeJSON(w, 200, map[string]any{"found": found, "hosts": list})
}
