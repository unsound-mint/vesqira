package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Dataset identity and locations (ADR 0053). A path proves nothing about bytes; what is known about them is stored per
// location, labelled with what it is (sampled fingerprint, full SHA-256) and when and by what it was computed.

const (
	defaultFullHashCap = 16 << 30 // the largest local file a full hash is computed for
	remoteFullHashCap  = 4 << 30  // through a host's worker and its request timeout
	thisComputer       = "this computer"
	codeCancelled      = 499 // internal: a measurement the caller stopped
)

func (s *Server) fullCap() int64 {
	if s.FullHashCap > 0 {
		return s.FullHashCap
	}
	return defaultFullHashCap
}

// measured is one computed value with the size and modification time of the file at the moment it was computed.
type measured struct {
	value, by   string
	size, mtime int64
}

func remoteFail(err error) (int, string) {
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		if re.Code == "unsupported" {
			return 422, re.Message
		}
		return 403, "not available"
	case errors.Is(err, worker.ErrTimeout):
		return 504, "the host did not finish in time"
	}
	return 502, "the host is not reachable (is the VPN up?)"
}

// localStat resolves a location on this computer inside the source roots and checks it is still what was recorded.
func (s *Server) localStat(ws workspace.ID, loc catalog.Location) (real string, code int, msg string) {
	real, err := s.roots(ws).Resolve(loc.Path)
	if err != nil {
		return "", 403, err.Error()
	}
	info, err := statFile(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", 404, "file not found"
	}
	if info.Size() != loc.SizeBytes || info.ModTime().UnixNano() != loc.ModifiedAt.UnixNano() {
		return "", 409, "the file changed since it was registered; register it again"
	}
	if differentFile(info, loc.FileDev, loc.FileIno) {
		return "", 409, msgOtherFile
	}
	if dev, ino := fileIDOf(info); loc.FileIno == 0 || dev != loc.FileDev {
		_ = s.Store.NoteFileID(ws, loc.HostID, loc.Path, dev, ino)
	}
	return real, 0, ""
}

func unchanged(real string, loc catalog.Location) bool {
	info, err := statFile(real)
	return err == nil && info.Size() == loc.SizeBytes && info.ModTime().UnixNano() == loc.ModifiedAt.UnixNano() && !differentFile(info, loc.FileDev, loc.FileIno)
}

// measureRemote asks the host's worker for a fingerprint op and checks the file there is still what was recorded.
func (s *Server) measureRemote(ctx context.Context, loc catalog.Location, op string, extra map[string]any) (measured, int, string) {
	if s.HostDB == nil || s.Remote == nil {
		return measured{}, 503, "remote hosts are not configured"
	}
	h, err := s.HostDB.Get(loc.HostID)
	if err != nil || !h.InRoots(loc.Path) {
		return measured{}, 403, "not available"
	}
	req := map[string]any{"op": op, "path": loc.Path}
	for k, v := range extra {
		req[k] = v
	}
	rep, err := s.Remote.Do(ctx, h, req)
	if err != nil {
		if ctx.Err() != nil {
			return measured{}, codeCancelled, "cancelled"
		}
		code, msg := remoteFail(err)
		return measured{}, code, msg
	}
	var r struct {
		Sampled string `json:"sampled"`
		SHA256  string `json:"sha256"`
		Size    int64  `json:"size"`
		MtimeNS int64  `json:"mtime_ns"`
	}
	if json.Unmarshal(rep.Head, &r) != nil || (r.Sampled == "" && r.SHA256 == "") {
		return measured{}, 502, "the host answered unexpectedly"
	}
	if r.Size != loc.SizeBytes || r.MtimeNS != loc.ModifiedAt.UnixNano() {
		return measured{}, 409, "the file changed since it was registered; register it again"
	}
	return measured{r.Sampled + r.SHA256, "worker on " + h.Name, r.Size, r.MtimeNS}, 0, ""
}

// measureSampled computes the sampled fingerprint of a location, on this computer or on its host.
func (s *Server) measureSampled(ctx context.Context, loc catalog.Location) (measured, int, string) {
	if loc.HostID != 0 {
		return s.measureRemote(ctx, loc, "fingerprint", nil)
	}
	real, code, msg := s.localStat(wsCtx(ctx), loc)
	if code != 0 {
		return measured{}, code, msg
	}
	v, err := idty.Sampled(real)
	if err != nil {
		return measured{}, 422, "cannot read the file"
	}
	if !unchanged(real, loc) {
		return measured{}, 409, "the file changed while it was being read"
	}
	return measured{v, thisComputer, loc.SizeBytes, loc.ModifiedAt.UnixNano()}, 0, ""
}

// measureFull computes the SHA-256 of every byte of a location. progress (local files only) is told the bytes read.
func (s *Server) measureFull(ctx context.Context, loc catalog.Location, progress func(int64)) (measured, int, string) {
	if loc.HostID != 0 {
		return s.measureRemote(ctx, loc, "fingerprint_full", map[string]any{"max_bytes": min(s.fullCap(), remoteFullHashCap)})
	}
	real, code, msg := s.localStat(wsCtx(ctx), loc)
	if code != 0 {
		return measured{}, code, msg
	}
	v, _, err := idty.Full(ctx, real, s.fullCap(), progress)
	switch {
	case errors.Is(err, idty.ErrTooLarge):
		return measured{}, 422, tooLargeText(s.fullCap())
	case ctx.Err() != nil:
		return measured{}, codeCancelled, "cancelled"
	case err != nil:
		return measured{}, 422, "cannot read the file"
	}
	if !unchanged(real, loc) {
		return measured{}, 409, "the file changed while it was being read"
	}
	return measured{v, thisComputer, loc.SizeBytes, loc.ModifiedAt.UnixNano()}, 0, ""
}

func tooLargeText(cap int64) string {
	return fmt.Sprintf("the file is larger than the cap for a full hash (%d GiB); its sampled fingerprint is what Vesqira can give for it", cap>>30)
}

func primaryOf(locs []catalog.Location) (catalog.Location, bool) {
	for _, l := range locs {
		if l.Primary {
			return l, true
		}
	}
	return catalog.Location{}, false
}

// recordIdentity stores what is cheap to know about a just-registered file's bytes: the sampled fingerprint always, and the
// full SHA-256 too when the file is small. Failure leaves the dataset registered without it (the location says "not
// computed"); registration never depends on it. Returns the dataset as stored.
func (s *Server) recordIdentity(ctx context.Context, d catalog.Dataset) catalog.Dataset {
	if p, ok := primaryOf(d.Locations); ok {
		s.storeMeasure(wsCtx(ctx), p, "sampled", func() (measured, int, string) { return s.measureSampled(ctx, p) })
		if p.HostID == 0 && p.SizeBytes <= idty.SmallFull {
			s.storeMeasure(wsCtx(ctx), p, "full", func() (measured, int, string) { return s.measureFull(ctx, p, nil) })
		}
	}
	s.reassessLinks(wsCtx(ctx), d.ID)
	if got, err := s.Store.Get(wsCtx(ctx), d.ID); err == nil {
		return got
	}
	return d
}

// reassessLinks compares each stale location with the registered file as it is now, once that has fingerprints again: equal full
// hashes make it verified again (a file that was only touched), equal sampled fingerprints and sizes probable, and anything
// else leaves it stale.
func (s *Server) reassessLinks(ws workspace.ID, datasetID int64) {
	locs, err := s.Store.Locations(ws, datasetID)
	if err != nil {
		return
	}
	p, ok := primaryOf(locs)
	if !ok {
		return
	}
	for _, l := range locs {
		if l.Link != catalog.LinkStale {
			continue
		}
		switch idty.Relate(l.Evidence(), p.Evidence()) {
		case idty.Verified:
			_ = s.Store.SetLink(ws, l.ID, catalog.LinkVerified)
		case idty.Probable:
			_ = s.Store.SetLink(ws, l.ID, catalog.LinkProbable)
		}
	}
}

// ensureFingerprinted gives a dataset registered before fingerprints existed its cheap one, so what a figure says about the
// file's bytes is what was recorded. Returns the dataset as stored.
func (s *Server) ensureFingerprinted(ctx context.Context, d catalog.Dataset) catalog.Dataset {
	locs, err := s.Store.Locations(wsCtx(ctx), d.ID)
	if err != nil {
		return d
	}
	if p, ok := primaryOf(locs); ok && p.Sampled == nil && p.Full == nil {
		s.storeMeasure(wsCtx(ctx), p, "sampled", func() (measured, int, string) { return s.measureSampled(ctx, p) })
		s.reassessLinks(wsCtx(ctx), d.ID)
		if got, err := s.Store.Get(wsCtx(ctx), d.ID); err == nil {
			return got
		}
	}
	return d
}

func (s *Server) storeMeasure(ws workspace.ID, l catalog.Location, kind string, f func() (measured, int, string)) bool {
	m, code, msg := f()
	if code != 0 {
		log.Printf("identity: %s of location %d: %d %s", kind, l.ID, code, msg)
		return false
	}
	if err := s.Store.SetFingerprint(ws, l.ID, kind, m.value, m.by, m.size, m.mtime); err != nil {
		log.Printf("identity: %v", err)
		return false
	}
	return true
}

// ---- full hashes in the background ----

type hashTask struct {
	cancel  context.CancelFunc
	running atomic.Bool
	done    atomic.Int64
	total   int64
}

type hashRegistry struct {
	mu     sync.Mutex
	tasks  map[int64]*hashTask // by location id
	failed map[int64]string
	once   sync.Once
	sem    chan struct{}
	// beforeRead runs once a task holds its turn and before it reads; tests use it to stop a task at a known point.
	beforeRead func()
}

type hashStatus struct {
	State      string `json:"state"` // queued | running | failed
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
	Error      string `json:"error,omitempty"`
}

func (h *hashRegistry) status() map[string]hashStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]hashStatus{}
	for id, t := range h.tasks {
		st := "queued"
		if t.running.Load() {
			st = "running"
		}
		out[strconv.FormatInt(id, 10)] = hashStatus{State: st, DoneBytes: t.done.Load(), TotalBytes: t.total}
	}
	for id, msg := range h.failed {
		if _, busy := h.tasks[id]; !busy {
			out[strconv.FormatInt(id, 10)] = hashStatus{State: "failed", Error: msg}
		}
	}
	return out
}

// startFull queues the full SHA-256 of a location. One full hash runs at a time (they read whole files); the others wait.
func (s *Server) startFull(ws workspace.ID, datasetID int64, loc catalog.Location) (int, string) {
	limit := s.fullCap()
	if loc.HostID != 0 {
		limit = min(limit, remoteFullHashCap)
	}
	if loc.SizeBytes > limit {
		return 422, tooLargeText(limit)
	}
	h := &s.hashing
	h.once.Do(func() { h.sem = make(chan struct{}, 1); h.tasks = map[int64]*hashTask{}; h.failed = map[int64]string{} })
	ctx, cancel := context.WithCancel(context.Background())
	t := &hashTask{cancel: cancel, total: loc.SizeBytes}
	h.mu.Lock()
	if _, busy := h.tasks[loc.ID]; busy {
		h.mu.Unlock()
		cancel()
		return 409, "a full hash of this file is already being computed"
	}
	h.tasks[loc.ID] = t
	delete(h.failed, loc.ID)
	h.mu.Unlock()
	s.hashes.Add(1)
	go func() {
		defer s.hashes.Done()
		defer cancel()
		finish := func(failure string) {
			h.mu.Lock()
			delete(h.tasks, loc.ID)
			if failure != "" {
				h.failed[loc.ID] = failure
			}
			h.mu.Unlock()
		}
		select {
		case h.sem <- struct{}{}:
		case <-ctx.Done():
			finish("")
			return
		}
		defer func() { <-h.sem }()
		t.running.Store(true)
		if h.beforeRead != nil {
			h.beforeRead()
		}
		m, code, msg := s.measureFull(ctx, loc, t.done.Store)
		switch {
		case code == codeCancelled:
			finish("")
		case code != 0:
			finish(msg)
		default:
			if err := s.Store.SetFingerprint(ws, loc.ID, "full", m.value, m.by, m.size, m.mtime); err != nil {
				finish("the file changed while its hash was computed")
				return
			}
			s.Audit.Record(ws, "full sha256 computed", "dataset", datasetID, m.value[:12]+" by "+m.by)
			s.reassessLinks(ws, datasetID)
			finish("")
		}
	}()
	return 0, ""
}

func (s *Server) cancelFull(locID int64) bool {
	h := &s.hashing
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.tasks[locID]; ok {
		t.cancel()
		return true
	}
	return false
}

func (s *Server) waitHashes() { s.hashes.Wait() }

// ---- reading from the best location ----

// chooseRead picks the location a dataset is read from: the first of catalog.ReadOrder that is still what was recorded. A
// file on this computer comes before a file on a host. probeRemote=false does not contact hosts (a remote location is taken
// as it is, unchecked), for answering "where would it read from" without waiting on the network. When nothing can be read,
// the registered file's refusal is returned, so the message is what it always was.
func (s *Server) chooseRead(ctx context.Context, d catalog.Dataset, probeRemote bool) (eff catalog.Dataset, loc catalog.Location, real string, k cacheKey, status int, msg string) {
	order := catalog.ReadOrder(d.Locations)
	if len(order) == 0 { // a dataset read before its locations existed
		real, k, status, msg = s.authorize(ctx, d)
		return d, catalog.Location{Primary: true, HostID: d.HostID, Path: d.Path}, real, k, status, msg
	}
	var firstStatus int
	var firstMsg string
	for _, l := range order {
		cand := d
		cand.Path, cand.HostID, cand.SizeBytes, cand.ModifiedAt, cand.FileDev, cand.FileIno = l.Path, l.HostID, l.SizeBytes, l.ModifiedAt, l.FileDev, l.FileIno
		if l.HostID != 0 && !probeRemote {
			s.notePath(wsCtx(ctx), l.HostID, l.Path, d.ID)
			return cand, l, l.Path, cacheKey{id: d.ID, host: l.HostID, size: l.SizeBytes, mtime: l.ModifiedAt.UnixNano(), unit: declaredUnitOf(d), corr: corrHash(d)}, 0, ""
		}
		r, key, st, m := s.authorize(ctx, cand)
		if st == 0 {
			return cand, l, r, key, 0, ""
		}
		if l.Primary || firstStatus == 0 {
			firstStatus, firstMsg = st, m
		}
	}
	return d, catalog.Location{}, "", cacheKey{}, firstStatus, firstMsg
}

// ---- API ----

func failCode(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	out := map[string]any{"error": msg, "code": code}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, status, out)
}

type readsFrom struct {
	LocationID int64  `json:"locationId"`
	HostID     int64  `json:"hostId"`
	Primary    bool   `json:"primary"`
	Checked    bool   `json:"checked"` // false: a file on a host, which is not contacted to answer this
	Reason     string `json:"reason"`
}

func (s *Server) listLocations(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	out := map[string]any{"locations": d.Locations, "hashing": s.hashing.status(), "algorithm": idty.SampledAlgorithm, "sampledCovers": idty.Covers,
		"fullHashCapBytes": s.fullCap()}
	if _, loc, _, _, status, m := s.chooseRead(r.Context(), d, false); status == 0 {
		reason := "the registered file"
		if !loc.Primary {
			reason = "a location verified to hold the same bytes (equal full SHA-256), because the registered file is not the better choice or is not available"
		}
		out["readsFrom"] = readsFrom{LocationID: loc.ID, HostID: loc.HostID, Primary: loc.Primary, Checked: loc.HostID == 0, Reason: reason}
	} else {
		out["readsFrom"] = nil
		out["unavailable"] = m
	}
	writeJSON(w, 200, out)
}

// resolveRemote canonicalises a path on a host through its worker (roots enforced there), returning the real path and the
// size and modification time the host reports.
func (s *Server) resolveRemote(ctx context.Context, h hosts.Host, p string) (real string, size, mtime int64, code int, msg string) {
	if !h.InRoots(p) {
		return "", 0, 0, 403, "not available"
	}
	res, err := s.Remote.Do(ctx, h, map[string]any{"op": "resolve", "path": p})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return "", 0, 0, 403, "not available"
	case err != nil:
		return "", 0, 0, 502, "the host is not reachable (is the VPN up?)"
	}
	var rs struct {
		Real    string `json:"real"`
		Size    int64  `json:"size"`
		MtimeNS int64  `json:"mtime_ns"`
	}
	if json.Unmarshal(res.Head, &rs) != nil || !h.InRoots(rs.Real) {
		return "", 0, 0, 403, "not available"
	}
	return rs.Real, rs.Size, rs.MtimeNS, 0, ""
}

// addLocation links another file to a dataset, but only on evidence: equal full SHA-256 (verified), equal size and sampled
// fingerprint (probable, said to be so and not treated as verified), or the researcher's declaration when nothing computed
// can say. A file that is proven different is refused.
func (s *Server) addLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Path    string `json:"path"`
		HostID  int64  `json:"hostId"`
		Declare bool   `json:"declare"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	nl := catalog.Location{HostID: in.HostID, Kind: catalog.LocCopy}
	if in.HostID == 0 {
		real, err := s.roots(wsOf(r)).Resolve(in.Path)
		if err != nil {
			fail(w, 403, err.Error())
			return
		}
		info, err := statFile(real)
		if err != nil || !info.Mode().IsRegular() {
			fail(w, 404, "file not found")
			return
		}
		nl.Path, nl.SizeBytes, nl.ModifiedAt = real, info.Size(), info.ModTime()
		nl.FileDev, nl.FileIno = fileIDOf(info)
		if s.ManagedDir != "" && isUnder(real, s.ManagedDir) {
			nl.Kind = catalog.LocManaged
		}
	} else {
		if s.HostDB == nil || s.Remote == nil {
			fail(w, 503, "remote hosts are not configured")
			return
		}
		h, err := s.HostDB.Get(in.HostID)
		if err != nil {
			fail(w, 404, "host not found")
			return
		}
		real, size, mtime, code, msg := s.resolveRemote(ctx, h, in.Path)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		nl.Path, nl.SizeBytes, nl.ModifiedAt = real, size, time.Unix(0, mtime)
	}
	if owner, found, err := s.Store.LocationOwner(wsOf(r), nl.HostID, nl.Path); err != nil {
		log.Printf("locations: %v", err)
		fail(w, 500, "internal error")
		return
	} else if found {
		failCode(w, 409, "already_location", "this file is already a location of a dataset", map[string]any{"datasetId": owner})
		return
	}
	// what is known about the new file
	if m, code, msg := s.measureSampled(ctx, nl); code == 0 {
		nl.Sampled = &catalog.Stamp{Value: m.value, At: time.Now(), By: m.by}
	} else if code != 502 && code != 504 { // an unreachable host leaves the file unfingerprinted: only a declaration can link it
		fail(w, code, msg)
		return
	}
	if nl.HostID == 0 && nl.SizeBytes <= idty.SmallFull {
		if m, code, _ := s.measureFull(ctx, nl, nil); code == 0 {
			nl.Full = &catalog.Stamp{Value: m.value, At: time.Now(), By: m.by}
		}
	}
	// what is known about the files already linked: a registered file from before fingerprints gets its sampled one now
	existing := d.Locations
	for _, l := range existing {
		if l.Sampled == nil && l.Full == nil {
			s.storeMeasure(wsOf(r), l, "sampled", func() (measured, int, string) { return s.measureSampled(ctx, l) })
		}
	}
	if fresh, err := s.Store.Locations(wsOf(r), id); err == nil {
		existing = fresh
	}
	link, why := decideLink(nl, existing, in.Declare)
	switch link {
	case "":
		status, c := 409, "identity_not_established"
		if why.different {
			c = "bytes_differ"
		}
		failCode(w, status, c, why.text, nil)
		return
	}
	nl.Link = link
	got, err := s.Store.AddLocation(wsOf(r), id, nl)
	switch {
	case errors.Is(err, catalog.ErrLocationExists):
		failCode(w, 409, "already_location", "this file is already a location of a dataset", nil)
		return
	case err != nil:
		log.Printf("locations: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "location added", "dataset", id, link+": "+filepath.Base(nl.Path))
	writeJSON(w, 201, got)
}

func isUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

type linkWhy struct {
	different bool
	text      string
}

// decideLink is the linking rule (ADR 0053). verified needs equal full hashes; probable needs equal size and sampled
// fingerprints and nothing proving a difference; declared is only for a file nothing computed can tell apart (a proven
// difference is never overridden by a declaration). The empty link means refused, with the reason.
func decideLink(nl catalog.Location, existing []catalog.Location, declare bool) (string, linkWhy) {
	verified, probable, different := false, false, ""
	for _, l := range existing {
		if !l.Primary && l.Link != catalog.LinkVerified {
			continue // only the registered file and verified locations are what the dataset's bytes are; the others prove nothing
		}
		switch idty.Relate(nl.Evidence(), l.Evidence()) {
		case idty.Verified:
			verified = true
		case idty.Probable:
			probable = true
		case idty.Different:
			if different == "" {
				different = filepath.Base(l.Path)
			}
		}
	}
	switch {
	case verified:
		return catalog.LinkVerified, linkWhy{}
	case different != "":
		return "", linkWhy{true, "This file's bytes differ from " + different + " (a size, sampled fingerprint or SHA-256 differs), so it is not another location of this dataset. Register it as its own dataset."}
	case probable:
		return catalog.LinkProbable, linkWhy{}
	case declare:
		return catalog.LinkDeclared, linkWhy{}
	}
	return "", linkWhy{false, "Nothing computed shows that this file holds the same bytes. Compute the fingerprints, or declare that it is the same."}
}

func (s *Server) removeLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	lid, ok2 := pathInt(w, r, "lid")
	if !ok || !ok2 {
		return
	}
	switch err := s.Store.RemoveLocation(wsOf(r), id, lid); {
	case errors.Is(err, catalog.ErrNoLocation):
		fail(w, 404, "location not found")
	case errors.Is(err, catalog.ErrPrimary):
		fail(w, 409, err.Error())
	case err != nil:
		log.Printf("locations: %v", err)
		fail(w, 500, "internal error")
	default:
		s.cancelFull(lid)
		s.Audit.RecordCtx(r.Context(), "location removed", "dataset", id, "location "+strconv.FormatInt(lid, 10))
		writeJSON(w, 200, map[string]any{"removed": lid})
	}
}

// fingerprintLocation computes the sampled fingerprint now, starts the full SHA-256 in the background, or cancels it.
func (s *Server) fingerprintLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	lid, ok2 := pathInt(w, r, "lid")
	if !ok || !ok2 {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	loc, err := s.Store.Location(wsOf(r), id, lid)
	if errors.Is(err, catalog.ErrNoLocation) {
		fail(w, 404, "location not found")
		return
	}
	if err != nil {
		log.Printf("locations: %v", err)
		fail(w, 500, "internal error")
		return
	}
	switch in.Mode {
	case "sampled":
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		m, code, msg := s.measureSampled(ctx, loc)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if err := s.Store.SetFingerprint(wsOf(r), loc.ID, "sampled", m.value, m.by, m.size, m.mtime); err != nil {
			fail(w, 409, "the file changed while it was being read")
			return
		}
		s.Audit.RecordCtx(r.Context(), "sampled fingerprint computed", "dataset", id, m.value[:12]+" by "+m.by)
		got, _ := s.Store.Location(wsOf(r), id, lid)
		writeJSON(w, 200, got)
	case "full":
		if code, msg := s.startFull(wsOf(r), id, loc); code != 0 {
			fail(w, code, msg)
			return
		}
		writeJSON(w, 202, map[string]any{"hashing": s.hashing.status()})
	case "cancel":
		if !s.cancelFull(lid) {
			fail(w, 409, "no full hash of this file is running")
			return
		}
		writeJSON(w, 202, map[string]any{"cancelled": lid})
	default:
		fail(w, 400, `mode must be "sampled", "full" or "cancel"`)
	}
}

// ---- registering a file whose bytes an existing dataset already has ----

// sameContent looks, before a new file is registered, for datasets whose locations say they hold the same bytes (equal full
// SHA-256, or equal size and sampled fingerprint). Nothing is linked: the caller offers the choice.
func (s *Server) sameContent(ctx context.Context, real string) []catalog.Match {
	info, err := statFile(real)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	probe := catalog.Location{Path: real, SizeBytes: info.Size(), ModifiedAt: info.ModTime()}
	ev := probe.Evidence()
	if v, code, _ := s.measureSampled(ctx, probe); code == 0 {
		ev.Sampled = v.value
	} else {
		return nil
	}
	if info.Size() <= idty.SmallFull {
		if v, code, _ := s.measureFull(ctx, probe, nil); code == 0 {
			ev.Full = v.value
		}
	}
	got, err := s.Store.FindSame(wsCtx(ctx), ev, 0)
	if err != nil {
		log.Printf("identity: %v", err)
		return nil
	}
	return got
}

// readableLocally says whether some location on this computer that the viewer may read from (the registered file or a verified
// copy) is still what was recorded. It never contacts a host.
func (s *Server) readableLocally(ctx context.Context, d catalog.Dataset) bool {
	for _, l := range catalog.ReadOrder(d.Locations) {
		if l.HostID != 0 {
			continue
		}
		cand := d
		cand.Path, cand.HostID, cand.SizeBytes, cand.ModifiedAt, cand.FileDev, cand.FileIno = l.Path, l.HostID, l.SizeBytes, l.ModifiedAt, l.FileDev, l.FileIno
		if _, _, status, _ := s.authorize(ctx, cand); status == 0 {
			return true
		}
	}
	return false
}

// waiting marks a request a person is watching, so its header read is not queued behind project scans (ADR 0056).
func waiting(ctx context.Context) context.Context { return worker.WithClass(ctx, worker.Interactive) }
