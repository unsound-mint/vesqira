package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
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
func (s *Server) planesFor(datasetID int64) PlaneService {
	d, err := s.Store.Get(datasetID)
	if err != nil || d.HostID == 0 {
		if s.Planes == nil {
			return nil
		}
		return s.Planes
	}
	if s.HostDB == nil || s.Remote == nil {
		return nil
	}
	h, err := s.HostDB.Get(d.HostID)
	if err != nil {
		return nil
	}
	return hostPlanes{s.Remote, h}
}

// authorizeRemote is authorize for a file on a host: the stored path must still lie in the host's roots and the remote
// worker (which enforces the roots on the resolved path) must report the size and modification time registered.
func (s *Server) authorizeRemote(d catalog.Dataset) (real string, k cacheKey, status int, msg string) {
	if s.HostDB == nil || s.Remote == nil {
		return "", k, 503, "remote hosts are not configured"
	}
	h, err := s.HostDB.Get(d.HostID)
	if err != nil || !h.InRoots(d.Path) {
		return "", k, 403, "not available"
	}
	rep, err := s.Remote.Do(context.Background(), h, map[string]any{"op": "stat", "path": d.Path})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return "", k, 403, "not available"
	case err != nil:
		return "", k, 502, "the host is not reachable (is the VPN up?)"
	}
	var st struct {
		Size    int64 `json:"size"`
		MtimeNS int64 `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &st) != nil {
		return "", k, 502, "the host answered unexpectedly"
	}
	if st.Size != d.SizeBytes || st.MtimeNS != d.ModifiedAt.UnixNano() {
		return "", k, 409, "the file changed since it was registered; register it again"
	}
	return d.Path, cacheKey{id: d.ID, size: st.Size, mtime: st.MtimeNS}, 0, ""
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

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hs, err := s.HostDB.List()
	if err != nil {
		s.hostErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"hosts": hs})
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Dest   string   `json:"dest"`
		Python string   `json:"python"`
		Roots  []string `json:"roots"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	h, err := s.HostDB.Create(hosts.Host{Name: in.Name, Dest: in.Dest, Python: in.Python, Roots: in.Roots})
	if err != nil {
		s.hostErr(w, err)
		return
	}
	writeJSON(w, 201, h)
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
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "ping"})
	if err != nil {
		var re *worker.RequestError
		if errors.As(err, &re) {
			fail(w, 422, re.Message)
			return
		}
		fail(w, 502, "could not run the worker on the host: check that ssh works for it without a prompt, the VPN is up, and the Python command has astropy")
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
	if !h.InRoots(p) {
		return d, 403, "not available"
	}
	res, err := s.Remote.Do(ctx, h, map[string]any{"op": "resolve", "path": p})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return d, 403, "not available"
	case err != nil:
		return d, 502, "the host is not reachable (is the VPN up?)"
	}
	var rs struct {
		Real string `json:"real"`
	}
	if json.Unmarshal(res.Head, &rs) != nil || !h.InRoots(rs.Real) {
		return d, 403, "not available"
	}
	hdr, err := s.Remote.Do(ctx, h, map[string]any{"op": "header", "path": rs.Real})
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
	for _, k := range []string{"nbytes", "op", "size", "mtime_ns"} {
		delete(meta, k)
	}
	clean, _ := json.Marshal(meta)
	d, err = s.Store.Upsert(catalog.Dataset{HostID: h.ID, Path: rs.Real, Name: path.Base(rs.Real), SizeBytes: size, ModifiedAt: time.Unix(0, mtime), Header: clean})
	if err != nil {
		log.Printf("register remote: %v", err)
		return d, 500, "internal error"
	}
	return d, 0, ""
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
