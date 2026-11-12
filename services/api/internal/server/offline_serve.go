package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Serving from the offline store (ADR 0059). The owner listener's view, header-cards, plane and spectrum routes fall back to a copy
// kept by "prepare for offline" only when the file could not be reached, and only when the copy was made from the signature the
// catalog last saw. The answer says it is a copy (header X-Vesqira-Offline and the "offline" field of the reply's JSON head).
// The review listener has no route to any of this: it never sees a dataset id.

const (
	msgHostUnreachable   = "the host is not reachable (is the VPN up?)"
	msgSourceUnreachable = "the file could not be reached (is its network drive answering?)"
	// offlineProbeTimeout bounds the background look at a host that did not answer.
	offlineProbeTimeout = 15 * time.Second
)

// offlineProbeAfter is how long a host that did not answer is left alone before a request of a dataset with an offline copy starts
// another look at it. Without a short-circuit every plane request of a scrub would wait out the ssh connect timeout (10 s) before
// falling back to the copy. Server.OfflineProbeAfter overrides it (tests set a nanosecond rather than wait).
const offlineProbeAfter = 3 * time.Second

func (s *Server) probeAfter() time.Duration {
	if s.OfflineProbeAfter > 0 {
		return s.OfflineProbeAfter
	}
	return offlineProbeAfter
}

// probeHost asks a host that last failed to answer whether it does now, in the background and one at a time per host, and records
// the answer in the reachability state, which is what lets requests go live again. A request the browser gave up on never
// refreshes that state by itself; this does.
func (s *Server) probeHost(h hosts.Host, path string, lastAt time.Time) {
	if s.Remote == nil || time.Since(lastAt) < s.probeAfter() {
		return
	}
	r := &s.offlineRun
	r.pmu.Lock()
	if r.probing[h.ID] {
		r.pmu.Unlock()
		return
	}
	if r.probing == nil {
		r.probing = map[int64]bool{}
	}
	r.probing[h.ID] = true
	r.pmu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), offlineProbeTimeout)
		defer cancel()
		_, err := s.Remote.Do(ctx, h, map[string]any{"op": "stat", "path": path})
		var re *worker.RequestError
		s.reach.set(h.ID, err == nil || errors.As(err, &re)) // a refusal is an answer
		r.pmu.Lock()
		delete(r.probing, h.ID)
		r.pmu.Unlock()
	}()
}

// unreachable says a refusal from authorize means "the bytes cannot be reached now", the only case a copy may answer.
func unreachable(status int, msg string) bool {
	return status == 502 && (msg == msgHostUnreachable || msg == msgSourceUnreachable)
}

// networkErr is a stat or read error of a file on a network mount that is not answering. A missing file is not one.
func networkErr(err error) bool {
	var no syscall.Errno
	if errors.As(err, &no) {
		switch no {
		case syscall.ESTALE, syscall.EIO, syscall.ETIMEDOUT, syscall.EHOSTDOWN, syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ENETDOWN, syscall.ECONNREFUSED, syscall.ECONNRESET:
			return true
		}
	}
	return false
}

// locIdentity is the content identity recorded for a location, labelled with what it is; empty when none was computed.
func locIdentity(l catalog.Location) string {
	switch {
	case l.Full != nil:
		return "sha256:" + l.Full.Value
	case l.Sampled != nil:
		return "sampled:" + l.Sampled.Value
	}
	return ""
}

// locationOf finds the location of a dataset with this host, size and modification time (the signature a live read was made from).
func locationOf(d catalog.Dataset, host, size, mtime int64) (catalog.Location, bool) {
	for _, l := range d.Locations {
		if l.HostID == host && l.SizeBytes == size && l.ModifiedAt.UnixNano() == mtime {
			return l, true
		}
	}
	return catalog.Location{}, false
}

// offlineSource is what source hands ask when the file could not be reached: the signatures the catalog last saw, to match against
// the copies. It rides in the cache key, which is never stored for such a request.
type offlineSource struct {
	name  string
	cands []offlineCand
}

type offlineCand struct {
	host, size, mtime int64
	identity          string
}

// offlineFor returns the signatures of the locations that may be read, when the dataset has any offline copy.
func (s *Server) offlineFor(d catalog.Dataset) *offlineSource {
	if s.Offline == nil || !s.Offline.Has(d.ID) {
		return nil
	}
	o := &offlineSource{name: d.Name}
	locs := catalog.ReadOrder(d.Locations)
	if len(locs) == 0 {
		o.cands = []offlineCand{{d.HostID, d.SizeBytes, d.ModifiedAt.UnixNano(), ""}}
		return o
	}
	for _, l := range locs {
		o.cands = append(o.cands, offlineCand{l.HostID, l.SizeBytes, l.ModifiedAt.UnixNano(), locIdentity(l)})
	}
	return o
}

// sourceOffline is source for the routes that may answer from an offline copy.
func (s *Server) sourceOffline(w http.ResponseWriter, r *http.Request) (d catalog.Dataset, real string, k cacheKey, ok bool) {
	return s.sourceWith(w, r, true)
}

// offlineMark says what an offline answer is: a copy, when it was taken, and the file signature it was taken from.
type offlineMark struct {
	Copy      bool      `json:"copy"`
	TakenAt   time.Time `json:"takenAt"`
	Signature struct {
		HostID   int64  `json:"hostId"`
		Size     int64  `json:"size"`
		MtimeNS  int64  `json:"mtimeNs"`
		Identity string `json:"identity"`
	} `json:"signature"`
}

func markHead(head []byte, m offlineMark) []byte {
	var o map[string]json.RawMessage
	if json.Unmarshal(head, &o) != nil {
		return head
	}
	raw, _ := json.Marshal(m)
	o["offline"] = raw
	out, err := json.Marshal(o)
	if err != nil {
		return head
	}
	return out
}

func storeKey(k cacheKey, c offlineCand) offline.Key {
	return offline.Key{DatasetID: k.id, HostID: c.host, Size: c.size, MTime: c.mtime, Op: k.op, Channel: k.channel, Step: k.step, X: k.x, Y: k.y, Q: k.q, Unit: k.unit}
}

// askOffline answers a request whose file could not be reached. It is ask's replacement for such a request: the copy whose key and
// signature match, marked as a copy, or a sentence naming what is missing or stale.
func (s *Server) askOffline(w http.ResponseWriter, r *http.Request, k cacheKey) (worker.Reply, bool) {
	o := k.off
	stale := false
	for _, c := range o.cands {
		e, head, payload, err := s.Offline.Get(storeKey(k, c))
		if err != nil {
			continue
		}
		if e.Stale() || (c.identity != "" && e.Identity != c.identity) {
			stale = true
			continue
		}
		m := offlineMark{Copy: true, TakenAt: e.TakenAt}
		m.Signature.HostID, m.Signature.Size, m.Signature.MtimeNS, m.Signature.Identity = e.HostID, e.Size, e.MTime, e.Identity
		hdr, _ := json.Marshal(m)
		w.Header().Set("X-Vesqira-Offline", string(hdr))
		return worker.Reply{Head: markHead(head, m), Payload: payload}, true
	}
	es, _ := s.Offline.Entries(k.id)
	if stale {
		failCode(w, 409, "offline_stale", fmt.Sprintf("the offline copy of %s was made from a version of the file that has since changed (or its recorded identity differs), so it is not served as the file. Prepare it again when the host is reachable", o.name), nil)
		return worker.Reply{}, false
	}
	failCode(w, 503, "offline_missing", offlineMissing(o, k, es), nil)
	return worker.Reply{}, false
}

// offlineMissing is the sentence for a request the offline copy does not hold: what was asked, what is kept, what it would take.
func offlineMissing(o *offlineSource, k cacheKey, es []offline.Entry) string {
	kept := coverage(es, func(e offline.Entry) bool {
		for _, c := range o.cands {
			if e.HostID == c.host && e.Size == c.size && e.MTime == c.mtime && !e.Stale() {
				return true
			}
		}
		return false
	})
	if kept == "" {
		kept = "nothing usable"
	}
	var what string
	switch k.op {
	case "plane":
		res := "at full resolution"
		if k.step > 1 {
			res = "as a 1/" + strconv.Itoa(k.step) + " preview"
		}
		what = fmt.Sprintf("channel %d %s is not in the offline copy of %s", k.channel, res, o.name)
	case "spectrum":
		what = "the spectrum of this region is not in the offline copy of " + o.name + " (only the spectra of regions saved when it was prepared are kept)"
	case "coord":
		what = "a coordinate readout needs the host (the offline copy of " + o.name + " keeps no per-pixel readouts)"
	case "cards":
		what = "the header listing is not in the offline copy of " + o.name
	default:
		what = "this is not in the offline copy of " + o.name
	}
	return fmt.Sprintf("%s. Kept: %s. The host is not reachable (is the VPN up?); the rest needs it.", what, kept)
}

// coverage describes copies in words: the view and header, plane ranges per resolution, and the number of region spectra.
func coverage(es []offline.Entry, keep func(offline.Entry) bool) string {
	var view, cards bool
	planes := map[int][]int{}
	spectra := 0
	for _, e := range es {
		if keep != nil && !keep(e) {
			continue
		}
		switch e.Op {
		case "view":
			view = true
		case "cards":
			cards = true
		case "plane":
			planes[e.Step] = append(planes[e.Step], e.Channel)
		case "spectrum":
			spectra++
		}
	}
	var parts []string
	if view {
		parts = append(parts, "view")
	}
	if cards {
		parts = append(parts, "header")
	}
	steps := make([]int, 0, len(planes))
	for st := range planes {
		steps = append(steps, st)
	}
	sort.Ints(steps)
	for _, st := range steps {
		res := "full resolution"
		if st > 1 {
			res = "1/" + strconv.Itoa(st) + " preview"
		}
		parts = append(parts, "planes "+ranges(planes[st])+" ("+res+")")
	}
	if spectra > 0 {
		parts = append(parts, plural(spectra, "region spectrum", "region spectra"))
	}
	return strings.Join(parts, "; ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ranges writes channel numbers as runs: 120–180, 200, 210–212.
func ranges(ch []int) string {
	sort.Ints(ch)
	var out []string
	for i := 0; i < len(ch); {
		j := i
		for j+1 < len(ch) && ch[j+1] <= ch[j]+1 {
			j++
		}
		if j == i {
			out = append(out, strconv.Itoa(ch[i]))
		} else {
			out = append(out, fmt.Sprintf("%d–%d", ch[i], ch[j]))
		}
		i = j + 1
	}
	return strings.Join(out, ", ")
}

// afterFailure is ask's second chance when a live read of a file on a host failed on the network between the stat and the read:
// the copy made from exactly the signature just verified answers, if there is one. Anything else keeps its original failure.
func (s *Server) afterFailure(w http.ResponseWriter, r *http.Request, k cacheKey) (worker.Reply, bool) {
	if s.Offline == nil || k.host == 0 || k.op == "" || k.win != "" || !s.Offline.Has(k.id) { // a window is never answered from a copy of the file (ADR 0063)
		return worker.Reply{}, false
	}
	d, err := s.Store.Get(k.id)
	if err != nil {
		return worker.Reply{}, false
	}
	id := ""
	if l, ok := locationOf(d, k.host, k.size, k.mtime); ok {
		id = locIdentity(l)
	}
	e, head, payload, gerr := s.Offline.Get(storeKey(k, offlineCand{k.host, k.size, k.mtime, id}))
	if gerr != nil || e.Stale() || (id != "" && e.Identity != id) {
		return worker.Reply{}, false
	}
	m := offlineMark{Copy: true, TakenAt: e.TakenAt}
	m.Signature.HostID, m.Signature.Size, m.Signature.MtimeNS, m.Signature.Identity = e.HostID, e.Size, e.MTime, e.Identity
	hdr, _ := json.Marshal(m)
	w.Header().Set("X-Vesqira-Offline", string(hdr))
	return worker.Reply{Head: markHead(head, m), Payload: payload}, true
}

// ---- capture: how prepare reuses the real handlers ----

type captureKey struct{}

// offlineCapture collects the replies the handlers produce for one dataset while it is being prepared, as the handlers keyed them.
type offlineCapture struct {
	s      *Server
	d      catalog.Dataset
	err    error // the first store failure (the size limit, a full disk, the byte budget); prepare stops on it
	bytes  int64
	copies int
	at     time.Time
	budget int64 // the most this preparation may still keep
}

func (c *offlineCapture) keep(k cacheKey, rep worker.Reply) {
	if c.err != nil || k.op == "" || k.id != c.d.ID {
		return
	}
	id := ""
	if l, ok := locationOf(c.d, k.host, k.size, k.mtime); ok {
		id = locIdentity(l)
	}
	if c.bytes+int64(len(rep.Head)+len(rep.Payload)+4) > c.budget {
		c.err = errBudget
		return
	}
	e, err := c.s.Offline.Put(storeKey(k, offlineCand{k.host, k.size, k.mtime, id}), id, rep.Head, rep.Payload, c.at)
	if err != nil {
		c.err = err
		return
	}
	c.bytes += e.Bytes
	c.copies++
}

// keepOffline hands a reply to the capture riding on the request, if there is one.
func (s *Server) keepOffline(ctx context.Context, k cacheKey, rep worker.Reply) {
	if k.win != "" { // the offline store keys a reply by the file, not by a window onto it (ADR 0063)
		return
	}
	if c, ok := ctx.Value(captureKey{}).(*offlineCapture); ok {
		c.keep(k, rep)
	}
}
