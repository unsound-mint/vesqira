package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

// Disk and quota on a host, and how big the run folders are (ADR 0045). Read-only, on demand, and cached: the answer to a
// look at a host is kept for storageFresh, so an open Hosts or Jobs screen that asks every few minutes costs the login node
// one short command per ten minutes. Nothing here deletes, moves or writes on a host.

const (
	storageFresh      = 10 * time.Minute
	storageErrFresh   = time.Minute      // a failed look is not repeated at once either: a host behind a VPN that is down answers slowly
	storageMinRefresh = 15 * time.Second // the Refresh button cannot hammer a shared login node
	maxFolders        = 500
)

type storageEntry struct {
	rep  *hosts.StorageReport
	err  string
	at   time.Time // when the last look (good or failed) was made
	good time.Time // when rep was made
}

type sizeKey struct {
	host int64
	path string
}

// hostDisk is the cache and the per-host lanes (one look and one du at a time per host, which bounds the load).
type hostDisk struct {
	mu    sync.Mutex
	look  map[int64]*storageEntry
	sizes map[sizeKey]hosts.DiskUsage
	lanes map[int64]*[2]chan struct{} // [0] storage looks, [1] du runs
}

func (d *hostDisk) lane(host int64, which int) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lanes == nil {
		d.lanes = map[int64]*[2]chan struct{}{}
	}
	l := d.lanes[host]
	if l == nil {
		l = &[2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
		d.lanes[host] = l
	}
	return l[which]
}

type storageResponse struct {
	Report    *hosts.StorageReport `json:"report"`
	Error     string               `json:"error"`
	CheckedAt *time.Time           `json:"checkedAt"`
	Cached    bool                 `json:"cached"`
}

func (e *storageEntry) response(cached bool) storageResponse {
	out := storageResponse{Report: e.rep, Error: e.err, Cached: cached}
	if !e.good.IsZero() {
		t := e.good
		out.CheckedAt = &t
	}
	return out
}

// hostStorage returns the cached look at a host's storage, making a new one when it is older than storageFresh (or, for
// force, older than storageMinRefresh).
func (s *Server) hostStorage(force bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok := s.hostFrom(w, r)
		if !ok {
			return
		}
		d := &s.disk
		current := func() *storageEntry {
			d.mu.Lock()
			defer d.mu.Unlock()
			if e := d.look[h.ID]; e != nil {
				c := *e
				return &c
			}
			return nil
		}
		useCached := func(e *storageEntry, now time.Time) bool {
			if e == nil {
				return false
			}
			age := now.Sub(e.at)
			switch {
			case force:
				return age < storageMinRefresh
			case e.err != "":
				return age < storageErrFresh
			default:
				return age < storageFresh
			}
		}
		if e := current(); useCached(e, time.Now()) {
			writeJSON(w, 200, e.response(true))
			return
		}
		lane := d.lane(h.ID, 0)
		select {
		case lane <- struct{}{}:
			defer func() { <-lane }()
		case <-r.Context().Done():
			fail(w, 499, "cancelled")
			return
		}
		if e := current(); useCached(e, time.Now()) { // another request looked while this one waited
			writeJSON(w, 200, e.response(true))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
		defer cancel()
		rep, err := s.Remote.Storage(ctx, h)
		now := time.Now()
		d.mu.Lock()
		if d.look == nil {
			d.look = map[int64]*storageEntry{}
		}
		e := d.look[h.ID]
		if e == nil {
			e = &storageEntry{}
			d.look[h.ID] = e
		}
		e.at = now
		if err != nil {
			e.err = err.Error()
		} else {
			e.err, e.rep, e.good = "", &rep, now
		}
		c := *e
		d.mu.Unlock()
		writeJSON(w, 200, c.response(false))
	}
}

// runFolder is a folder on a host that holds (or held) the output of a run: a job's output folder or a run folder found
// by scanning a project. The size is what the last du said, absent until someone measures it.
type runFolder struct {
	Path       string     `json:"path"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"` // job | run
	JobID      *int64     `json:"jobId"`
	Bytes      *int64     `json:"bytes"`
	Partial    bool       `json:"partial"`
	MeasuredAt *time.Time `json:"measuredAt"`
}

// hostFolders lists the run folders of a host and the sizes measured so far, largest first, unmeasured ones after. It makes
// no call to the host.
func (s *Server) hostFolders(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	seen := map[string]bool{}
	out := []runFolder{}
	add := func(p, name, kind string, job *int64) {
		if len(out) >= maxFolders || seen[p] || !h.ValidFolder(p) || path.Clean(p) != p {
			return
		}
		seen[p] = true
		out = append(out, runFolder{Path: p, Name: name, Kind: kind, JobID: job})
	}
	if s.Jobs != nil {
		if js, err := s.Jobs.List(); err == nil {
			for _, j := range js {
				if j.HostID == h.ID && j.OutputDir != "" {
					id := j.ID
					add(j.OutputDir, j.Name, "job", &id)
				}
			}
		} else {
			log.Printf("hosts: folders: %v", err)
		}
	}
	if s.Projects != nil {
		if ps, err := s.Projects.List(200); err == nil {
			for _, p := range ps {
				if p.HostID != h.ID {
					continue
				}
				rows, err := s.Projects.RunRows(p.ID)
				if err != nil {
					continue
				}
				for _, row := range rows {
					add(path.Join(p.Path, row.Path), row.Name, "run", nil)
				}
			}
		}
	}
	s.disk.mu.Lock()
	for i := range out {
		if u, ok := s.disk.sizes[sizeKey{h.ID, out[i].Path}]; ok {
			b, at := u.Bytes, u.MeasuredAt
			out[i].Bytes, out[i].Partial, out[i].MeasuredAt = &b, u.Partial, &at
		}
	}
	s.disk.mu.Unlock()
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a].Bytes, out[b].Bytes
		switch {
		case x != nil && y != nil:
			return *x > *y
		default:
			return x != nil && y == nil
		}
	})
	writeJSON(w, 200, map[string]any{"folders": out})
}

// measureFolder runs du on one folder of a host, one at a time per host, and remembers the answer.
func (s *Server) measureFolder(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if !h.ValidFolder(in.Path) || path.Clean(in.Path) != in.Path {
		fail(w, 422, "give a folder inside one of the host's source folders")
		return
	}
	lane := s.disk.lane(h.ID, 1)
	select {
	case lane <- struct{}{}:
		defer func() { <-lane }()
	case <-r.Context().Done():
		fail(w, 499, "cancelled")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	u, err := s.Remote.DiskUsage(ctx, h, in.Path)
	if err != nil {
		if errors.Is(err, hosts.ErrInvalid) {
			fail(w, 422, err.Error())
		} else {
			fail(w, 502, err.Error())
		}
		return
	}
	s.disk.mu.Lock()
	if s.disk.sizes == nil {
		s.disk.sizes = map[sizeKey]hosts.DiskUsage{}
	}
	if len(s.disk.sizes) > 5000 { // a bound, not a policy: sizes are cheap to measure again
		s.disk.sizes = map[sizeKey]hosts.DiskUsage{}
	}
	s.disk.sizes[sizeKey{h.ID, in.Path}] = u
	s.disk.mu.Unlock()
	writeJSON(w, 200, u)
}
