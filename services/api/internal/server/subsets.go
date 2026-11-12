package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Virtual subsets (ADR 0063). A subset is a saved window onto a dataset: a box, an optional spatial step and a spectral interval,
// pinned to the identity of the source file it was made on. It is viewed, never written: the view, plane, spectrum, coordinate and
// header routes under /api/subsets/{sid} read the source file through the window (planes.go resolves it in sourceWith and ask), so
// a readout is the readout of the file a materialised copy would be. A source that changed makes the subset stale: it is not shown
// as current until the researcher re-pins it, which records a new version. Writing it as a file is the analysis program's
// "subset" operation, recorded as a derived dataset that names the subset, its version and the source identity.

// subsetView is a subset as the client sees it. Virtual is always true: a subset is never a file.
type subsetView struct {
	ID          int64              `json:"id"`
	SourceID    int64              `json:"sourceId"`
	SourceName  string             `json:"sourceName"`
	Name        string             `json:"name"`
	Reason      string             `json:"reason"`
	Version     int                `json:"version"`
	Kind        string             `json:"kind"`
	Definition  subsets.Definition `json:"definition"`
	Author      string             `json:"author"`
	CreatedAt   time.Time          `json:"createdAt"`
	EditedAt    time.Time          `json:"editedAt"`
	Virtual     bool               `json:"virtual"`
	Stale       bool               `json:"stale"`
	StaleReason string             `json:"staleReason,omitempty"`
	// Pinned is the source file this version was defined on.
	Pinned struct {
		Size       int64     `json:"size"`
		ModifiedAt time.Time `json:"modifiedAt"`
		Identity   string    `json:"identity"`
	} `json:"pinned"`
	// Materialised is the dataset written from this version of the subset for the source as it is now, if one exists and is unchanged.
	Materialised *int64 `json:"materialised"`
}

const staleSentence = "The file this subset was defined on has changed since (its size, modification time or recorded content differ from the ones it was pinned to), so the window may no longer cover the same pixels. It is not shown as current. Re-pin it to the current file to record a new version."

// subsetStale says whether the source is no longer the file the version was pinned to.
func subsetStale(v subsets.Version, src catalog.Dataset) bool {
	return catalog.Changed(v.SourceSize, v.SourceModified, v.SourceIdentity, src, catalog.ContentIdentity(src.Locations))
}

// subsetRecords are the records of the files written from subsets, read once per request (nil when there is no derived store).
func (s *Server) subsetRecords(ws workspace.ID) []derived.Record {
	if s.Derived == nil {
		return nil
	}
	recs, err := s.Derived.ByOp(ws, "subset")
	if err != nil {
		log.Printf("subsets: %v", err)
		return nil
	}
	return recs
}

func (s *Server) subsetOf(ws workspace.ID, sub subsets.Subset, src catalog.Dataset, recs []derived.Record) subsetView {
	v := subsetView{ID: sub.ID, SourceID: sub.SourceID, SourceName: src.Name, Name: sub.Current.Name, Reason: sub.Current.Reason, Version: sub.Current.Version,
		Kind: sub.Current.Kind, Definition: sub.Current.Def, Author: sub.Current.Author, CreatedAt: sub.CreatedAt, EditedAt: sub.Current.At, Virtual: true}
	v.Pinned.Size, v.Pinned.ModifiedAt, v.Pinned.Identity = sub.Current.SourceSize, sub.Current.SourceModified, sub.Current.SourceIdentity
	if subsetStale(sub.Current, src) {
		v.Stale, v.StaleReason = true, staleSentence
	} else if id, ok := s.materialisedFor(ws, recs, sub.ID, sub.Current.Version, src, "", true); ok {
		v.Materialised = &id
	}
	return v
}

// subsetAndSource reads the subset named by {sid} and its source dataset, answering 404 itself.
func (s *Server) subsetAndSource(w http.ResponseWriter, r *http.Request) (subsets.Subset, catalog.Dataset, bool) {
	sid, ok := pathInt(w, r, "sid")
	if !ok {
		return subsets.Subset{}, catalog.Dataset{}, false
	}
	sub, err := s.Subsets.Get(wsOf(r), sid)
	if errors.Is(err, subsets.ErrNotFound) {
		fail(w, 404, "subset not found")
		return sub, catalog.Dataset{}, false
	}
	if err != nil {
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return sub, catalog.Dataset{}, false
	}
	src, err := s.Store.Get(wsOf(r), sub.SourceID)
	if err != nil {
		fail(w, 404, "the dataset this subset is a window onto is no longer in the catalog")
		return sub, src, false
	}
	return sub, src, true
}

// subsetSource is sourceWith for a request under /api/subsets/{sid}: the source file, authorised and unchanged, with the current
// version's window in the cache key, so ask sends it to the worker and refuses a reply that did not apply it. A stale subset is
// refused with its own code, never read.
func (s *Server) subsetSource(w http.ResponseWriter, r *http.Request) (d catalog.Dataset, real string, k cacheKey, ok bool) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	if subsetStale(sub.Current, src) {
		failCode(w, 409, "subset_stale", staleSentence, map[string]any{"subsetId": sub.ID, "version": sub.Current.Version})
		return d, "", k, false
	}
	d, real, k, ok = s.windowedRead(w, r, src, sub.Current.Def.Window())
	d.Name = sub.Current.Name
	return
}

// windowedRead resolves a source dataset to an authorised file read through the window.
func (s *Server) windowedRead(w http.ResponseWriter, r *http.Request, src catalog.Dataset, win map[string]any) (d catalog.Dataset, real string, k cacheKey, ok bool) {
	eff, loc, real, k, status, msg := s.chooseRead(r.Context(), src, true)
	if status != 0 {
		fail(w, status, msg) // no offline copy answers for a window: the offline store keys files, not windows
		return
	}
	raw, _ := json.Marshal(win) // a map marshals with sorted keys: one window, one key
	k.win = string(raw)
	w.Header().Set("X-Vesqira-Location", strconv.FormatInt(loc.ID, 10))
	return eff, real, k, true
}

// windowOf is the window a cache key carries, as the object the worker takes.
func windowOf(win string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(win), &m)
	return m
}

// windowEchoed reports whether a worker reply says it applied the window that was sent: the origin, step and size of the box (and the
// channels, when the window gave them) as the worker resolved them. A reply without the echo is from a worker that ignored the window.
func windowEchoed(head []byte, win string) bool {
	var h struct {
		Window *struct {
			X0, Y0, Step, C0, C1, Width, Height int
		} `json:"window"`
	}
	if json.Unmarshal(head, &h) != nil || h.Window == nil {
		return false
	}
	m := windowOf(win)
	num := func(k string, def int) int {
		if v, ok := m[k].(float64); ok {
			return int(v)
		}
		return def
	}
	step := num("step", 1)
	e := h.Window
	if e.X0 != num("x0", 0) || e.Y0 != num("y0", 0) || e.Step != step {
		return false
	}
	if _, box := m["x0"]; box && (e.Width != (num("x1", 0)-num("x0", 0))/step+1 || e.Height != (num("y1", 0)-num("y0", 0))/step+1) {
		return false
	}
	if _, ch := m["c0"]; ch && (e.C0 != num("c0", 0) || e.C1 != num("c1", 0)) {
		return false
	}
	return true
}

// ---- reading what the worker resolved ----

// subsetGeometry is the part of a windowed view the API keeps: what the window resolved to, and the reply to hand back.
type subsetGeometry struct {
	Width    int             `json:"width"`
	Height   int             `json:"height"`
	Channels int             `json:"channels"`
	Window   json.RawMessage `json:"window"`
}

// probe asks the worker for the view of src through win (nil: the file itself). It is how a definition is checked against the real
// file: every bound inside the source, the channel interval resolved. It answers the request itself on failure.
func (s *Server) probe(w http.ResponseWriter, r *http.Request, src catalog.Dataset, win map[string]any) (head json.RawMessage, g subsetGeometry, ok bool) {
	_, _, real, k, status, msg := s.chooseRead(r.Context(), src, true)
	if status != 0 {
		fail(w, status, msg)
		return nil, g, false
	}
	if win != nil {
		raw, _ := json.Marshal(win)
		k.win = string(raw)
	}
	k.op, k.channel = "view", -1
	rep, ok := s.ask(w, r, k, map[string]any{"op": "view", "path": real})
	if !ok {
		return nil, g, false
	}
	if json.Unmarshal(rep.Head, &g) != nil {
		fail(w, 502, "the scientific worker returned an unreadable view")
		return nil, g, false
	}
	return rep.Head, g, true
}

// regionBox is the bounding box of a saved region (a rectangle, or the square around a circle), clipped to the image: the box
// a subset takes "from a region". A line has no area and so no box.
func regionBox(rg regions.Region, width, height int) (subsets.Box, error) {
	sh := rg.Shape
	var b subsets.Box
	switch sh.Shape {
	case "rect":
		b = subsets.Box{X0: sh.X0, X1: sh.X1, Y0: sh.Y0, Y1: sh.Y1}
	case "circle":
		b = subsets.Box{X0: sh.CX - sh.R, X1: sh.CX + sh.R, Y0: sh.CY - sh.R, Y1: sh.CY + sh.R}
	default:
		return b, errors.New("a line has no area: a subset takes the bounding box of a rectangle or a circle")
	}
	b.X0, b.Y0 = max(b.X0, 0), max(b.Y0, 0)
	b.X1, b.Y1 = min(b.X1, width-1), min(b.Y1, height-1)
	if b.X0 > b.X1 || b.Y0 > b.Y1 {
		return b, errors.New("the region lies outside the image")
	}
	return b, nil
}

// ---- routes ----

type subsetInput struct {
	Name     string            `json:"name"`
	Reason   string            `json:"reason"`
	Box      *subsets.Box      `json:"box"`
	RegionID int64             `json:"regionId"` // take the box from this saved region of the source (instead of Box)
	Step     int               `json:"step"`
	Channels *subsets.Channels `json:"channels"`
	World    *subsets.World    `json:"world"`
	Expect   int               `json:"expectVersion"` // edit and re-pin: the version the person was looking at
}

func (in subsetInput) definition() subsets.Definition {
	return subsets.Definition{Box: in.Box, Step: in.Step, Channels: in.Channels, World: in.World}
}

func (s *Server) listSubsets(w http.ResponseWriter, r *http.Request) {
	all, err := s.Subsets.List(wsOf(r), 0)
	if err != nil {
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := make([]subsetView, 0, len(all))
	recs := s.subsetRecords(wsOf(r))
	for _, sub := range all {
		src, err := s.Store.Get(wsOf(r), sub.SourceID)
		if err != nil {
			continue // its source is gone: the delete removes the subset with it, so this is a row about to go
		}
		out = append(out, s.subsetOf(wsOf(r), sub, src, recs))
	}
	writeJSON(w, 200, map[string]any{"subsets": out})
}

func (s *Server) getSubset(w http.ResponseWriter, r *http.Request) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	versions, err := s.Subsets.Versions(wsOf(r), sub.ID)
	if err != nil {
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return
	}
	made := []map[string]any{}
	recs := s.subsetRecords(wsOf(r))
	for _, rec := range recs {
		if sm := subsetSummary(rec); sm.ID == sub.ID {
			made = append(made, map[string]any{"datasetId": rec.DatasetID, "version": sm.Version, "at": rec.CreatedAt})
		}
	}
	writeJSON(w, 200, map[string]any{"subset": s.subsetOf(wsOf(r), sub, src, recs), "versions": versions, "materialised": made})
}

// checkNote validates the name and the reason of a version: plain printable text, both required.
func checkNote(in subsetInput) string {
	if !catalog.ValidNote(in.Name, 1, 80) {
		return "the name is 1 to 80 characters of plain text with no markup, quotes or leading and trailing blanks"
	}
	if !catalog.ValidNote(in.Reason, 1, catalog.MaxReason) {
		return "the reason is required: say why this window, in plain text of up to 500 characters with no markup or quotes"
	}
	return ""
}

// resolveDefinition turns a request into a definition checked against the real file: a region becomes its clipped bounding box, and
// the worker resolves every bound and the spectral interval. It answers the request itself on failure.
func (s *Server) resolveDefinition(w http.ResponseWriter, r *http.Request, src catalog.Dataset, in subsetInput) (subsets.Definition, json.RawMessage, bool) {
	def := in.definition()
	if in.RegionID != 0 {
		if in.Box != nil {
			fail(w, 422, "give a box or a saved region, not both")
			return def, nil, false
		}
		rg, err := s.Regions.Get(wsOf(r), in.RegionID)
		if err != nil || rg.DatasetID != src.ID {
			fail(w, 422, "that region is not a saved region of this dataset")
			return def, nil, false
		}
		_, g, ok := s.probe(w, r, src, nil)
		if !ok {
			return def, nil, false
		}
		box, err := regionBox(rg, g.Width, g.Height)
		if err != nil {
			fail(w, 422, err.Error())
			return def, nil, false
		}
		def.Box, def.Region = &box, &subsets.RegionRef{ID: rg.ID, Name: rg.Name}
	}
	def, err := def.Normalised()
	if err != nil {
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
		return def, nil, false
	}
	head, _, ok := s.probe(w, r, src, def.Window())
	if !ok {
		return def, nil, false
	}
	return def, head, true
}

// pinned is a version made now for src: its identity is the file's registered size and modification time and recorded identity.
func pinned(src catalog.Dataset, name, reason string, def subsets.Definition) subsets.Version {
	return subsets.Version{Name: name, Reason: reason, Def: def, SourceSize: src.SizeBytes, SourceModified: src.ModifiedAt,
		SourceIdentity: catalog.ContentIdentity(src.Locations), Author: "user"}
}

func (s *Server) createSubset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	src, code, msg := s.imageDataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	var in subsetInput
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if m := checkNote(in); m != "" {
		fail(w, 422, m)
		return
	}
	def, head, ok := s.resolveDefinition(w, r, src, in)
	if !ok {
		return
	}
	sub, err := s.Subsets.Create(wsOf(r), actorID(r.Context()), src.ID, pinned(src, in.Name, in.Reason, def))
	if errors.Is(err, subsets.ErrInvalid) {
		fail(w, 422, strings.TrimPrefix(err.Error(), "invalid: "))
		return
	}
	if err != nil {
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "virtual subset created", "dataset", src.ID, in.Name+" (subset "+strconv.FormatInt(sub.ID, 10)+")")
	writeJSON(w, 201, map[string]any{"subset": s.subsetOf(wsOf(r), sub, src, s.subsetRecords(wsOf(r))), "geometry": head})
}

// editSubset records a new version with another definition, name or reason, pinned to the source as it is now. The earlier versions
// stay, and a file written from one keeps the version it was written from.
func (s *Server) editSubset(w http.ResponseWriter, r *http.Request) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	var in subsetInput
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if m := checkNote(in); m != "" {
		fail(w, 422, m)
		return
	}
	def, head, ok := s.resolveDefinition(w, r, src, in)
	if !ok {
		return
	}
	s.saveVersion(r.Context(), w, sub, src, in.Expect, "edit", pinned(src, in.Name, in.Reason, def), head)
}

// repinSubset records the same definition again, pinned to the source as it is now, after the file changed. The definition is checked
// against the new file first: a window that no longer fits it is refused, with the reason, and the subset stays stale.
func (s *Server) repinSubset(w http.ResponseWriter, r *http.Request) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	var in subsetInput
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if !subsetStale(sub.Current, src) {
		fail(w, 409, "this subset is already pinned to the current file")
		return
	}
	in.Name = sub.Current.Name
	if !catalog.ValidNote(in.Reason, 1, catalog.MaxReason) {
		fail(w, 422, "the reason is required: say why it is right to pin this window to the changed file")
		return
	}
	head, _, ok := s.probe(w, r, src, sub.Current.Def.Window())
	if !ok {
		return
	}
	s.saveVersion(r.Context(), w, sub, src, in.Expect, "repin", pinned(src, sub.Current.Name, in.Reason, sub.Current.Def), head)
}

func (s *Server) saveVersion(ctx context.Context, w http.ResponseWriter, sub subsets.Subset, src catalog.Dataset, expect int, kind string, v subsets.Version, head json.RawMessage) {
	if expect == 0 {
		expect = sub.Current.Version
	}
	next, err := s.Subsets.AddVersion(wsCtx(ctx), sub.ID, expect, kind, v)
	switch {
	case errors.Is(err, subsets.ErrConflict):
		fail(w, 409, err.Error())
		return
	case err != nil:
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.audit(ctx, "virtual subset "+kind, "dataset", src.ID, v.Name+" (subset "+strconv.FormatInt(sub.ID, 10)+", version "+strconv.Itoa(next.Current.Version)+")")
	writeJSON(w, 200, map[string]any{"subset": s.subsetOf(wsCtx(ctx), next, src, s.subsetRecords(wsCtx(ctx))), "geometry": head})
}

func (s *Server) deleteSubset(w http.ResponseWriter, r *http.Request) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	if err := s.Subsets.Delete(wsOf(r), sub.ID); err != nil {
		log.Printf("subsets: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "virtual subset deleted", "dataset", src.ID, sub.Current.Name+" (subset "+strconv.FormatInt(sub.ID, 10)+")")
	writeJSON(w, 200, map[string]any{"deleted": sub.ID})
}

// ---- materialising ----

// subsetSummaryOf is what a subset's derived record keeps in its summary, for the owner: which subset and version it was written
// from, and the key that says whether a later request is the same one. The record's params, which a reviewer sees, carry the name,
// the reason and the definition but no id.
type subsetSummaryOf struct {
	ID       int64  `json:"id"`
	Version  int    `json:"version"`
	ReuseKey string `json:"reuseKey"`
}

func subsetSummary(rec derived.Record) subsetSummaryOf {
	var sm struct {
		Subset struct {
			ID      int64 `json:"id"`
			Version int   `json:"version"`
		} `json:"subset"`
		ReuseKey string `json:"reuseKey"`
	}
	_ = json.Unmarshal(rec.Summary, &sm)
	return subsetSummaryOf{ID: sm.Subset.ID, Version: sm.Subset.Version, ReuseKey: sm.ReuseKey}
}

// reuseKey names a materialisation: the subset and version, the source bytes (size, modification time, recorded identity), and what
// else changes the values or header a file would carry: the corrections in force and the declared unit.
// carry says which masks the file carries (carryPlan.key, empty when none), so a file written with masks is not what a request for none
// reuses, and the other way round.
func reuseKey(subsetID int64, version int, src catalog.Dataset, carry string) string {
	return fmt.Sprintf("subset %d v%d|%d|%d|%s|corr %s|unit %s", subsetID, version, src.SizeBytes, src.ModifiedAt.UnixNano(), catalog.ContentIdentity(src.Locations), corrHash(src), declaredUnitOf(src)) + carry
}

// materialisedFor finds the dataset written from this version of the subset for the source as it is, if its file is still the one that
// was registered. It never makes one.
// With anyCarry, a file made with any choice of masks counts (the explorer's "written as a file" mark); without it, only the file made with
// exactly this carry key (a request to write it).
func (s *Server) materialisedFor(ws workspace.ID, recs []derived.Record, subsetID int64, version int, src catalog.Dataset, carry string, anyCarry bool) (int64, bool) {
	want := reuseKey(subsetID, version, src, carry)
	for _, rec := range recs {
		if got := subsetSummary(rec).ReuseKey; got != want && !(anyCarry && strings.HasPrefix(got, want)) {
			continue
		}
		nd, err := s.Store.Get(ws, rec.DatasetID)
		if err != nil {
			continue
		}
		if _, _, status, _ := s.authorize(context.Background(), nd); status == 0 { // still the file that was written
			return nd.ID, true
		}
	}
	return 0, false
}

// materialiseSubset writes the current version of a subset as an ordinary FITS file through the analysis program's subset operation
// and registers it as a derived dataset. Asking again for the same subset, version and source bytes (with the same corrections and
// declared unit) answers with the file already written, if it is unchanged.
func (s *Server) materialiseSubset(w http.ResponseWriter, r *http.Request) {
	sub, src, ok := s.subsetAndSource(w, r)
	if !ok {
		return
	}
	if !s.analysisDeps() {
		fail(w, 503, "analysis is not configured")
		return
	}
	var in struct {
		Expect int `json:"expectVersion"`
		// What to do with each mask of the source in the file: carry it (cut by the same window) or leave it behind. Nothing is carried unless asked.
		CarryMasks []carryChoice `json:"carryMasks"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	cur := sub.Current
	if in.Expect != 0 && in.Expect != cur.Version {
		fail(w, 409, subsets.ErrConflict.Error())
		return
	}
	if subsetStale(cur, src) {
		failCode(w, 409, "subset_stale", staleSentence, map[string]any{"subsetId": sub.ID, "version": cur.Version})
		return
	}
	carry, code, msg := s.planCarry(wsOf(r), src, "subset", in.CarryMasks)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	s.subsetMu.Lock() // one writer at a time: two identical requests make one file, not two
	defer s.subsetMu.Unlock()
	if id, ok := s.materialisedFor(wsOf(r), s.subsetRecords(wsOf(r)), sub.ID, cur.Version, src, carry.key(), false); ok {
		nd, err := s.Store.Get(wsOf(r), id)
		if err == nil {
			writeJSON(w, 200, map[string]any{"dataset": nd, "reused": true, "subset": s.subsetOf(wsOf(r), sub, src, s.subsetRecords(wsOf(r)))})
			return
		}
	}
	if in := src.Interpretation; in != nil && in.Kind == "axial_orientation" && in.Stale {
		fail(w, 409, "the file changed since you declared it as an orientation map; declare it again to confirm the unit and frame still apply, then write the subset")
		return
	}
	real, _, status, m := s.authorize(r.Context(), src)
	if status != 0 {
		fail(w, status, m)
		return
	}
	win, _ := json.Marshal(cur.Def.Window())
	identity := catalog.ContentIdentity(src.Locations)
	note := fmt.Sprintf("virtual subset %s version %d of %s, source %s", asciiOnly(cur.Name), cur.Version, asciiOnly(src.Name), orNone(identity))
	var b [4]byte
	_, _ = rand.Read(b[:])
	fileName := fmt.Sprintf("subset-%d-v%d-%s.fits", sub.ID, cur.Version, hex.EncodeToString(b[:]))
	opts := []string{"--input", real, "--window=" + string(win), "--note=" + note}
	declared := map[string]string{}
	if u := declaredUnitOf(src); u != "" {
		opts = append(opts, "--declared-unit="+u)
		declared["input"] = u
	}
	var res analysisOutcome
	var nd catalog.Dataset
	outPath := ""
	if src.HostID != 0 {
		// written on the host, in its derived folder, by the host's copy of the program (ADR 0064): the bytes stay there, and the file is
		// registered as a dataset on that host like any other file there
		h, code, msg := s.hostOf(src.HostID)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if res, outPath, code, msg = s.hostRunAnalysis(r.Context(), h, "subset", fileName, opts, carry); code != 0 {
			fail(w, code, msg)
			return
		}
		if nd, code, msg = s.registerRemoteFile(waiting(r.Context()), h, outPath); code != 0 {
			fail(w, code, "the file was written on the host ("+path.Base(outPath)+" in its derived folder) but could not be registered: "+msg)
			return
		}
	} else {
		outPath = filepath.Join(s.derivedDirOf(wsOf(r)), fileName)
		args := append([]string{"-P", s.Analysis.Script, "subset"}, opts...)
		args = append(args, "--out", outPath)
		args = append(args, carry.args()...)
		var code int
		res, code, msg = s.runAnalysisProgram(r, args)
		if code != 0 {
			_ = os.Remove(outPath)
			carry.cleanup()
			fail(w, code, msg)
			return
		}
		if nd, code, msg = s.registerPath(waiting(r.Context()), outPath); code != 0 {
			carry.cleanup()
			fail(w, code, msg)
			return
		}
	}
	carried, code, msg := s.recordCarried(r.Context(), carry, res.Summary, src, nd, "")
	if code != 0 {
		fail(w, code, msg)
		return
	}
	pm := map[string]any{
		"subset": map[string]any{"name": cur.Name, "version": cur.Version, "reason": cur.Reason, "definition": reviewerDefinition(cur.Def)},
		"source": map[string]any{"identity": identity, "name": src.Name},
	}
	if carried != nil {
		pm["maskCarry"] = carried // masks carried or left behind, and the recomputed no-value mask: no ids, no paths
	}
	if len(declared) > 0 {
		pm["declaredUnits"] = declared
	}
	if res.Corrections != nil {
		pm["corrections"] = res.Corrections // the correction state the source was read with (ADR 0061), pinned with the file
	}
	params, _ := json.Marshal(pm)
	summary := map[string]any{}
	_ = json.Unmarshal(res.Summary, &summary)
	summary["subset"] = map[string]any{"id": sub.ID, "version": cur.Version}
	summary["reuseKey"] = reuseKey(sub.ID, cur.Version, src, carry.key())
	sraw, _ := json.Marshal(summary)
	if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: "subset", Params: params, Sources: []int64{src.ID}, Summary: sraw, Software: res.Software}); err != nil {
		log.Printf("derived: %v", err)
	}
	if in := src.Interpretation; in != nil && in.Kind == "axial_orientation" {
		// a window does not turn angles (a step keeps source pixels, nothing is averaged), so the file is the same kind of map, said by the
		// operation and not by the researcher; its reliability map is another file and is not carried
		out := catalog.Interpretation{Kind: in.Kind, Unit: in.Unit, Frame: in.Frame, Quantity: in.Quantity, DeclaredBy: analysisAuthor}
		if err := s.Store.SetInterpretation(nd, out); err != nil {
			log.Printf("subsets: interpretation of the file: %v", err)
		} else if got, err := s.Store.Get(wsOf(r), nd.ID); err == nil {
			nd = got
		}
	}
	s.audit(r.Context(), "virtual subset written as a file", "dataset", src.ID, cur.Name+" (subset "+strconv.FormatInt(sub.ID, 10)+", version "+strconv.Itoa(cur.Version)+") as "+filepath.Base(outPath))
	writeJSON(w, 201, map[string]any{"dataset": nd, "reused": false, "summary": json.RawMessage(sraw), "subset": s.subsetOf(wsOf(r), sub, src, s.subsetRecords(wsOf(r)))})
}

// reviewerDefinition is the definition as a derived record keeps it for a reviewer: the region a box came from by name only, no id.
func reviewerDefinition(d subsets.Definition) map[string]any {
	raw, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if d.Region != nil {
		m["region"] = map[string]any{"name": d.Region.Name}
	}
	return m
}

func orNone(s string) string {
	if s == "" {
		return "(no recorded identity)"
	}
	return s
}

// asciiOnly keeps a name a FITS header card can hold.
func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}
