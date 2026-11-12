package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// WCS and unit corrections (ADR 0061). A correction is a declaration at tier "declared" with a required reason, kept in an
// append-only history; the file is never written. The worker applies the state in force to a copy of the header, in memory,
// for every request that reads the file, and echoes the state's hash; a reply without the echo is refused, so an older worker
// can never pass uncorrected values off as corrected ones. A state made for other bytes than the file has now is stale and is
// applied nowhere until the researcher confirms it.

// corrPathRef names a file a dataset was read from, as the worker sees it: (workspace, host, resolved path). The workspace is
// part of it because one file may be registered in two workspaces (schema 31) as two datasets with their own corrections: one
// workspace's corrections must never be attached to the other's reads.
type corrPathRef struct {
	ws   workspace.ID
	host int64
	path string
}

// notePath remembers which dataset a resolved path belongs to, so the correcting service can attach that dataset's corrections
// to any worker request that names the path (the base, an overlay, the other map of a link, a reliability map).
func (s *Server) notePath(ws workspace.ID, host int64, real string, id int64) {
	if real != "" && id != 0 {
		s.corrPaths.Store(corrPathRef{ws, host, real}, id)
	}
}

// corrHash is the hash of the corrections in force on d ("" for none or stale ones): part of every cache key for d.
func corrHash(d catalog.Dataset) string {
	if st := d.Corrections.InForce(); st != nil {
		return st.Hash()
	}
	return ""
}

// corrPayload is what the worker receives for one file: the hash, the corrected unit (or null) and the keyword values.
func corrPayload(st catalog.CorrectionState) map[string]any {
	u, w := st.Values()
	var unit any
	if u != nil {
		unit = *u
	}
	h := st.Hash()
	if h == "" {
		h = emptyStateHash
	}
	return map[string]any{"hash": h, "bunit": unit, "wcs": w}
}

// emptyStateHash names "no corrections" for the preview (the worker needs a hash for every state it is sent).
const emptyStateHash = "0000000000000000"

var errCorrectionsIgnored = errors.New("the scientific worker did not apply the researcher's corrections")

// corrPathFields are the request fields that name a registered file the worker reads.
var corrPathFields = []string{"path", "overlay_path", "other_path", "weights_path", "reliability_path"}

// correctingService attaches, to every plane-worker request, the corrections in force for each registered file it names, and
// refuses a reply that does not say it applied them.
type correctingService struct {
	s     *Server
	host  int64
	inner PlaneService
}

func (c correctingService) Do(ctx context.Context, req map[string]any) (worker.Reply, error) {
	sent := map[string]any{}
	var hashes []string
	for _, f := range corrPathFields {
		p, _ := req[f].(string)
		if p == "" {
			continue
		}
		if st := c.s.correctionsAt(wsCtx(ctx), c.host, p); st != nil {
			pl := corrPayload(*st)
			sent[p] = pl
			hashes = append(hashes, pl["hash"].(string))
		}
	}
	if len(sent) > 0 {
		req["corrections"] = sent
	}
	rep, err := c.inner.Do(ctx, req)
	if err != nil || len(sent) == 0 {
		return rep, err
	}
	if !echoes(rep.Head, hashes) {
		return worker.Reply{}, errCorrectionsIgnored
	}
	return rep, nil
}

// sentHash is the hash of the corrections the correcting service attached for the request's own file ("" for none).
func sentHash(req map[string]any) string {
	m, _ := req["corrections"].(map[string]any)
	p, _ := req["path"].(string)
	pl, _ := m[p].(map[string]any)
	h, _ := pl["hash"].(string)
	return h
}

// correctionsAt is the state in force for the dataset read from (host, path), or nil.
func (s *Server) correctionsAt(ws workspace.ID, host int64, path string) *catalog.CorrectionState {
	v, ok := s.corrPaths.Load(corrPathRef{ws, host, path})
	if !ok {
		return nil
	}
	d, err := s.Store.Get(ws, v.(int64))
	if err != nil {
		return nil
	}
	return d.Corrections.InForce()
}

// echoes reports whether a worker reply head says it applied every one of the hashes.
func echoes(head []byte, hashes []string) bool {
	var h struct {
		Applied []string `json:"corrections_applied"`
	}
	if json.Unmarshal(head, &h) != nil {
		return false
	}
	for _, x := range hashes {
		if !slices.Contains(h.Applied, x) {
			return false
		}
	}
	return true
}

// corrArgs is the --corrections option of an analysis run on `host` (0: this computer): the corrections in force for every registered file it reads (by the
// --input, --reference, --weights and --ref-weights it was given), and their hashes, to check against the program's reply.
func (s *Server) corrArgs(ws workspace.ID, host int64, args []string) (opt string, hashes []string, snapshot []map[string]any) {
	sent := map[string]any{}
	read := func(role, p string) {
		if _, dup := sent[p]; dup { // the reference is both --input and a member of the family
			return
		}
		v, ok := s.corrPaths.Load(corrPathRef{ws, host, p})
		if !ok {
			return
		}
		d, err := s.Store.Get(ws, v.(int64))
		if err != nil {
			return
		}
		if st := d.Corrections.InForce(); st != nil {
			pl := corrPayload(*st)
			sent[p] = pl
			hashes = append(hashes, pl["hash"].(string))
			snapshot = append(snapshot, map[string]any{"role": role, "corrections": safeCorrections(*st), "hash": pl["hash"]})
		}
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--input", "--reference", "--third", "--weights", "--ref-weights":
			read(strings.TrimPrefix(args[i], "--"), args[i+1])
		case "--family": // the maps of a family agreement (ADR 0070), a JSON list of {path, ...}
			var fam []struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(args[i+1]), &fam) == nil {
				for _, m := range fam {
					read("family", m.Path)
				}
			}
		}
	}
	if len(sent) == 0 {
		return "", nil, nil
	}
	b, _ := json.Marshal(sent)
	return "--corrections=" + string(b), hashes, snapshot
}

// ---- header facts with the corrections applied, for decisions the API takes from the catalogued header ----

// effectiveHeader is d's catalogued header facts with the corrections in force applied: what grid compatibility (compat.Grids)
// and the figure's limits must be judged on. The stored header stays what the file said.
func effectiveHeader(d catalog.Dataset) json.RawMessage {
	st := d.Corrections.InForce()
	if st == nil {
		return d.Header
	}
	var h map[string]any
	if json.Unmarshal(d.Header, &h) != nil {
		return d.Header
	}
	if st.Unit != nil {
		h["bunit"], h["bunit_status"] = st.Unit.Value, "corrected"
	}
	axes, _ := h["axes"].([]any)
	extra, _ := h["wcs_extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	for k, e := range st.WCS {
		field, n := "", 0
		for _, p := range []string{"CRVAL", "CRPIX", "CDELT", "CTYPE", "CUNIT"} {
			if rest, ok := strings.CutPrefix(k, p); ok && len(rest) == 1 {
				field, n = strings.ToLower(p), int(rest[0]-'0')
			}
		}
		switch {
		case field != "":
			for _, a := range axes {
				if m, ok := a.(map[string]any); ok {
					if num, _ := m["axis"].(float64); int(num) == n {
						m[field], m[field+"_status"] = e.Value, "corrected"
					}
				}
			}
		case k == "SPECSYS" || k == "RADESYS":
			h[strings.ToLower(k)] = e.Value
		case strings.HasPrefix(k, "PC") || strings.HasPrefix(k, "CD") || k == "EQUINOX":
			extra[k] = e.Value
		}
	}
	h["wcs_extra"] = extra
	b, err := json.Marshal(h)
	if err != nil {
		return d.Header
	}
	return b
}

// ---- routes ----

type corrInput struct {
	Action  string                     `json:"action"` // set | revert | confirm | withdraw
	Unit    json.RawMessage            `json:"unit"`   // absent: unchanged; null: withdraw the unit correction; text: the corrected unit
	WCS     map[string]json.RawMessage `json:"wcs"`    // keyword: a number or text, or null to withdraw that keyword's correction
	Reason  string                     `json:"reason"`
	Source  string                     `json:"source"`
	Reverts int64                      `json:"reverts"`
	Expect  *int64                     `json:"expect"` // the latest record the client built on (0: none); a different one is a conflict
}

func (s *Server) correctionsDataset(w http.ResponseWriter, r *http.Request) (catalog.Dataset, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return catalog.Dataset{}, false
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return d, false
	}
	if d.Kind == catalog.KindHealpix {
		fail(w, 409, "a HEALPix map has no image WCS or brightness unit to correct: its frame and convention are stated in its own header")
		return d, false
	}
	if d.Kind == catalog.KindCatalog {
		fail(w, 409, "a point catalog has no image WCS or brightness unit to correct: its columns are declared instead")
		return d, false
	}
	return d, true
}

// fileStatedUnit is the unit the file states, or "" when it states none.
func fileStatedUnit(d catalog.Dataset) (string, bool) {
	var h struct {
		Bunit *string `json:"bunit"`
	}
	_ = json.Unmarshal(d.Header, &h)
	if h.Bunit == nil {
		return "", false
	}
	return *h.Bunit, true
}

func lastRecordID(hist []catalog.CorrectionRecord) int64 {
	if len(hist) == 0 {
		return 0
	}
	return hist[len(hist)-1].ID
}

// listCorrections is GET /api/datasets/{id}/corrections: what is in force, what the file says, and the whole history.
func (s *Server) listCorrections(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	hist, err := s.Store.CorrectionHistory(d)
	if err != nil {
		log.Printf("corrections: %v", err)
		fail(w, 500, "internal error")
		return
	}
	fu, has := fileStatedUnit(d)
	var fileBunit *string
	if has {
		fileBunit = &fu
	}
	noBright, _ := s.noBrightness(wsOf(r))
	writeJSON(w, 200, map[string]any{"datasetId": d.ID, "current": d.Corrections, "history": hist, "last": lastRecordID(hist),
		"fileBunit": fileBunit, "unitCorrectable": has && !noBright[d.ID]})
}

// proposal builds the state a request would put in force, from the state in force now. It never touches the store.
func (s *Server) proposal(ws workspace.ID, d catalog.Dataset, in corrInput) (next, base catalog.CorrectionState, code int, msg string) {
	if d.Corrections != nil {
		base = d.Corrections.State
	}
	stale := d.Corrections != nil && d.Corrections.Stale
	if in.Action != catalog.CorrConfirm && in.Action != catalog.CorrWithdraw && stale {
		return next, base, 409, catalog.ErrCorrectionStale.Error()
	}
	if !catalog.ValidNote(in.Reason, 3, catalog.MaxReason) {
		return next, base, 422, "a reason is required: 3 to 500 characters of plain text, without quotes, angle brackets or backslashes"
	}
	if !catalog.ValidNote(in.Source, 0, catalog.MaxSource) {
		return next, base, 422, "a source is up to 300 characters of plain text, without quotes, angle brackets or backslashes"
	}
	switch in.Action {
	case catalog.CorrConfirm:
		if !stale {
			return next, base, 409, "nothing to confirm: the corrections in force were made for the file as it is"
		}
		return base, catalog.CorrectionState{}, 0, "" // base empty: a confirmation changes nothing but the file it is made for
	case catalog.CorrWithdraw:
		if base.Empty() {
			return next, base, 409, "nothing is corrected"
		}
		return catalog.CorrectionState{}, base, 0, ""
	case catalog.CorrRevert:
		_, before, err := s.Store.CorrectionRecordOf(d, in.Reverts)
		if errors.Is(err, catalog.ErrNotFound) {
			return next, base, 404, "no such record in this dataset's history"
		}
		if err != nil {
			log.Printf("corrections: %v", err)
			return next, base, 500, "internal error"
		}
		if len(catalog.Diff(base, before)) == 0 {
			return next, base, 422, "reverting this record changes nothing: the corrections in force are already what they were before it"
		}
		return before, base, 0, ""
	case catalog.CorrSet:
	default:
		return next, base, 400, "action is set, revert, confirm or withdraw"
	}
	next = catalog.CorrectionState{WCS: map[string]catalog.CorrectionEntry{}}
	if base.Unit != nil {
		u := *base.Unit
		next.Unit = &u
	}
	for k, e := range base.WCS {
		next.WCS[k] = e
	}
	now := time.Now().UTC()
	if len(in.Unit) > 0 {
		if string(in.Unit) == "null" {
			next.Unit = nil
		} else {
			var u string
			if json.Unmarshal(in.Unit, &u) != nil || !catalog.ValidUnit(u) {
				return next, base, 422, catalog.ErrInvalidUnit.Error()
			}
			fu, has := fileStatedUnit(d)
			if !has {
				return next, base, 409, "the file has no BUNIT: declare its unit instead (a declaration fills a missing unit; a correction replaces one the file states)"
			}
			if bad, _ := s.noBrightness(ws); bad[d.ID] {
				return next, base, 409, catalog.ErrNotBrightness.Error()
			}
			if u == fu {
				return next, base, 422, "the file already says " + fu + "; withdraw the unit correction instead"
			}
			next.Unit = &catalog.CorrectionEntry{Value: u, File: fu, Reason: in.Reason, Source: in.Source, At: now}
		}
	}
	if len(in.WCS) > catalog.MaxCorrectedKeys {
		return next, base, 422, fmt.Sprintf("a correction names at most %d keywords", catalog.MaxCorrectedKeys)
	}
	for k, raw := range in.WCS {
		if string(raw) == "null" {
			delete(next.WCS, k)
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return next, base, 422, k + ": not a value"
		}
		cv, err := catalog.CheckKeyword(k, v)
		if err != nil {
			return next, base, 422, err.Error()
		}
		next.WCS[k] = catalog.CorrectionEntry{Value: cv, Reason: in.Reason, Source: in.Source, At: now}
	}
	if len(next.WCS) > catalog.MaxCorrectedKeys {
		return next, base, 422, fmt.Sprintf("at most %d keywords can be corrected", catalog.MaxCorrectedKeys)
	}
	if len(next.WCS) == 0 {
		next.WCS = nil
	}
	if len(catalog.Diff(base, next)) == 0 {
		return next, base, 422, "nothing changes"
	}
	return next, base, 0, ""
}

// workerPreview asks the worker what a state does to the coordinates, validating it exactly as it would be applied. It fills in
// what the file says for each corrected keyword (the worker read it from the file's header).
func (s *Server) workerPreview(ctx context.Context, d catalog.Dataset, cur *catalog.CorrectionState, next catalog.CorrectionState) (map[string]any, int, string) {
	real, _, code, msg := s.authorize(ctx, d)
	if code != 0 {
		return nil, code, msg
	}
	svc := s.planesOnRaw(d.HostID)
	if svc == nil {
		return nil, 503, "the scientific worker is not configured"
	}
	req := map[string]any{"op": "wcs_preview", "path": real, "proposed": corrPayload(next)}
	if cur != nil {
		req["current"] = corrPayload(*cur)
	}
	rep, err := svc.Do(ctx, req)
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return nil, 422, re.Message
	case err != nil:
		log.Printf("corrections preview: %v", err)
		return nil, 502, "the scientific worker is unavailable"
	}
	var out map[string]any
	if json.Unmarshal(rep.Head, &out) != nil {
		return nil, 502, "the worker answered unexpectedly"
	}
	delete(out, "ok")
	delete(out, "op")
	delete(out, "nbytes")
	delete(out, "corrections_applied")
	return out, 0, ""
}

// sameAsFile refuses a change that sets a keyword to what the file already says: that is not a correction (withdraw the
// keyword's correction instead).
func sameAsFile(changes []catalog.CorrectionChange) string {
	for _, c := range changes {
		if c.After != nil && c.After == c.File {
			return c.What + ": the file already says " + valueText(c.File) + "; withdraw that correction instead"
		}
	}
	return ""
}

// fillFileValues records, for each corrected keyword, what the file says, from the worker's reading of the header.
func fillFileValues(next *catalog.CorrectionState, preview map[string]any) {
	changes, _ := preview["changes"].([]any)
	file := map[string]any{}
	for _, c := range changes {
		if m, ok := c.(map[string]any); ok {
			if k, ok := m["keyword"].(string); ok {
				file[k] = m["file"]
			}
		}
	}
	for k, e := range next.WCS {
		if e.File == nil {
			if v, ok := file[k]; ok {
				e.File = v
				next.WCS[k] = e
			}
		}
	}
}

// impactFor lists what depends on the dataset's coordinates or unit, for the keywords that change.
func (s *Server) impactFor(ws workspace.ID, d catalog.Dataset, changes []catalog.CorrectionChange) lifecycle.CorrectionImpact {
	wcs, unit := false, false
	for _, c := range changes {
		if c.What == "BUNIT" {
			unit = true
		} else {
			wcs = true
		}
	}
	im, err := lifecycle.New(s.Store.DB()).CorrectionImpact(ws, d.ID, wcs, unit)
	if err != nil {
		log.Printf("corrections impact: %v", err)
	}
	return im
}

// previewCorrection is POST /api/datasets/{id}/corrections/preview: what a set, revert, confirm or withdrawal would put in force,
// keyword by keyword, its effect on the coordinates at the image centre and corners (before and after), and what depends on the
// dataset. Nothing is stored.
func (s *Server) previewCorrection(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	var in corrInput
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	next, base, code, msg := s.proposal(wsOf(r), d, in)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	changes := catalog.Diff(base, next)
	var cur *catalog.CorrectionState
	if !base.Empty() && !d.Corrections.Stale {
		cur = &base
	}
	pv, code, msg := s.workerPreview(r.Context(), d, cur, next)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	fillFileValues(&next, pv)
	changes = catalog.Diff(base, next)
	if in.Action == catalog.CorrSet {
		if why := sameAsFile(changes); why != "" {
			fail(w, 422, why)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"action": in.Action, "changes": changes, "state": next, "preview": pv, "impact": s.impactFor(wsOf(r), d, changes)})
}

// saveCorrection is POST /api/datasets/{id}/corrections: append a record (set, revert, confirm, withdraw). The proposal is
// validated by the worker as it would be applied; a refusal is 422 with the worker's reason. Nothing is ever deleted or edited.
func (s *Server) saveCorrection(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	var in corrInput
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	if in.Expect == nil {
		fail(w, 400, "expect is required: the latest record you looked at (0 for none)")
		return
	}
	next, base, code, msg := s.proposal(wsOf(r), d, in)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if !next.Empty() {
		pv, code, msg := s.workerPreview(r.Context(), d, nil, next)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if why, _ := pv["refused"].(string); why != "" {
			fail(w, 422, why)
			return
		}
		fillFileValues(&next, pv)
	}
	if in.Action == catalog.CorrSet {
		if why := sameAsFile(catalog.Diff(base, next)); why != "" {
			fail(w, 422, why)
			return
		}
	}
	rec := catalog.CorrectionRecord{Action: in.Action, State: next, Changes: catalog.Diff(base, next), Reason: in.Reason, Source: in.Source, Author: "user"}
	if in.Action == catalog.CorrConfirm {
		rec.Changes = []catalog.CorrectionChange{}
	}
	if in.Action == catalog.CorrRevert {
		rec.RevertsID = &in.Reverts
	}
	s.appendCorrection(r.Context(), w, d, rec, *in.Expect)
}

func (s *Server) appendCorrection(ctx context.Context, w http.ResponseWriter, d catalog.Dataset, rec catalog.CorrectionRecord, expect int64) {
	rec, err := s.Store.AppendCorrection(d, rec, expect)
	switch {
	case errors.Is(err, catalog.ErrCorrectionConflict):
		fail(w, 409, err.Error())
		return
	case err != nil:
		log.Printf("corrections: %v", err)
		fail(w, 500, "internal error")
		return
	}
	keys := make([]string, 0, len(rec.Changes))
	for _, c := range rec.Changes {
		keys = append(keys, c.What)
	}
	detail := d.Name + ": " + rec.Action
	if len(keys) > 0 {
		detail += " " + strings.Join(keys, ", ")
	}
	s.audit(ctx, "correction "+rec.Action, "dataset", d.ID, detail)
	// Offline copies (ADR 0059) are keyed by the file's signature, not by the corrections in force: a copy taken before this
	// record would show the old coordinates or unit as current. They are forgotten; preparing again keeps corrected ones.
	if s.Offline != nil {
		if n, _, err := s.Offline.Forget(d.ID); err != nil {
			log.Printf("corrections: forget offline copies of %d: %v", d.ID, err)
		} else if n > 0 {
			s.audit(ctx, "offline copies forgotten", "dataset", d.ID, d.Name+": corrections changed")
		}
	}
	nd, code, msg := s.dataset(wsCtx(ctx), d.ID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, map[string]any{"record": rec, "dataset": nd})
}

// ---- sidecar ----

// SidecarFormat names the sidecar file (docs/adr/0061): a small JSON document with the corrections in force for one FITS file,
// the identity of the file they were made for (no path, no catalog id) and their history.
const (
	SidecarFormat  = "vesqira-corrections"
	SidecarVersion = 1
)

type sidecarFile struct {
	Name      string  `json:"name"`
	SizeBytes int64   `json:"sizeBytes"`
	SHA256    *string `json:"sha256"`
	Sampled   *string `json:"sampledFingerprint"`
	Algorithm string  `json:"sampledAlgorithm,omitempty"`
}

type sidecarHistory struct {
	Action  string                     `json:"action"`
	At      time.Time                  `json:"at"`
	Author  string                     `json:"author"`
	Reason  string                     `json:"reason"`
	Source  string                     `json:"source"`
	Changes []catalog.CorrectionChange `json:"changes"`
}

type sidecar struct {
	Format      string                  `json:"format"`
	Version     int                     `json:"version"`
	About       string                  `json:"about"`
	File        sidecarFile             `json:"file"`
	ExportedAt  time.Time               `json:"exportedAt"`
	Corrections catalog.CorrectionState `json:"corrections"`
	History     []sidecarHistory        `json:"history"`
}

const sidecarAbout = "WCS and unit corrections a researcher declared for one FITS file, with the reason for each. The file itself is not changed: these values override its header in memory. Made by Vesqira; see docs/adr/0061-wcs-and-unit-corrections.md."

func fileOf(d catalog.Dataset) sidecarFile {
	f := sidecarFile{Name: d.Name, SizeBytes: d.SizeBytes}
	if p, ok := primaryOf(d.Locations); ok {
		if p.Full != nil {
			v := p.Full.Value
			f.SHA256 = &v
		}
		if p.Sampled != nil {
			v := p.Sampled.Value
			f.Sampled, f.Algorithm = &v, idty.SampledAlgorithm
		}
	}
	return f
}

// exportSidecar is GET /api/datasets/{id}/corrections/sidecar: the corrections in force and their history as a JSON file.
func (s *Server) exportSidecar(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	hist, err := s.Store.CorrectionHistory(d)
	if err != nil {
		log.Printf("corrections: %v", err)
		fail(w, 500, "internal error")
		return
	}
	sc := sidecar{Format: SidecarFormat, Version: SidecarVersion, About: sidecarAbout, File: fileOf(d), ExportedAt: time.Now().UTC(), History: []sidecarHistory{}}
	if d.Corrections != nil {
		sc.Corrections = d.Corrections.State
	}
	for _, h := range hist {
		sc.History = append(sc.History, sidecarHistory{Action: h.Action, At: h.At, Author: sidecarAuthor(h.Author), Reason: h.Reason, Source: h.Source, Changes: h.Changes})
	}
	b, _ := json.MarshalIndent(sc, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFileName(d.Name)+`.corrections.json"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(b, '\n'))
}

// sidecarAuthor names who made a record without naming an account: the file may travel.
func sidecarAuthor(by string) string {
	if by == "user" || by == "" {
		return "the researcher"
	}
	return by
}

func safeFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return r
		}
		return '_'
	}, name)
}

// readSidecar validates a sidecar's format and every value in it, as a new correction would be validated.
func readSidecar(raw json.RawMessage) (sidecar, error) {
	var sc sidecar
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&sc); err != nil {
		return sc, errors.New("this is not a corrections file (it is not JSON of the expected shape)")
	}
	if sc.Format != SidecarFormat {
		return sc, errors.New("this is not a Vesqira corrections file (format is not " + SidecarFormat + ")")
	}
	if sc.Version != SidecarVersion {
		return sc, fmt.Errorf("this corrections file is version %d; this program reads version %d", sc.Version, SidecarVersion)
	}
	st := sc.Corrections
	if st.Empty() {
		return sc, errors.New("the corrections file corrects nothing")
	}
	check := func(what string, e catalog.CorrectionEntry) error {
		if !catalog.ValidNote(e.Reason, 3, catalog.MaxReason) || !catalog.ValidNote(e.Source, 0, catalog.MaxSource) {
			return errors.New(what + ": its reason or source is missing or not plain text")
		}
		return nil
	}
	if st.Unit != nil {
		u, ok := st.Unit.Value.(string)
		if !ok || !catalog.ValidUnit(u) {
			return sc, errors.New("BUNIT: " + catalog.ErrInvalidUnit.Error())
		}
		if err := check("BUNIT", *st.Unit); err != nil {
			return sc, err
		}
	}
	if len(st.WCS) > catalog.MaxCorrectedKeys {
		return sc, fmt.Errorf("at most %d keywords can be corrected", catalog.MaxCorrectedKeys)
	}
	for k, e := range st.WCS {
		v, err := catalog.CheckKeyword(k, e.Value)
		if err != nil {
			return sc, err
		}
		e.Value = v
		if err := check(k, e); err != nil {
			return sc, err
		}
		st.WCS[k] = e
	}
	if !catalog.ValidNote(sc.File.Name, 1, 255) {
		return sc, errors.New("the file name in the corrections file is missing or not plain text")
	}
	return sc, nil
}

// sidecarMatch compares the file a sidecar was made for with the dataset's registered file, never claiming more than the evidence
// (ADR 0053): verified (equal SHA-256), probable (equal size and sampled fingerprint), different, or unknown.
func sidecarMatch(sc sidecar, d catalog.Dataset) (string, string) {
	a := idty.Evidence{Size: sc.File.SizeBytes}
	if sc.File.SHA256 != nil {
		a.Full = *sc.File.SHA256
	}
	if sc.File.Sampled != nil && (sc.File.Algorithm == "" || sc.File.Algorithm == idty.SampledAlgorithm) {
		a.Sampled = *sc.File.Sampled
	}
	b := idty.Evidence{Size: d.SizeBytes}
	if p, ok := primaryOf(d.Locations); ok {
		b = p.Evidence()
	}
	switch rel := idty.Relate(a, b); rel {
	case idty.Verified:
		return rel, "made for this file: its SHA-256 equals this file's"
	case idty.Probable:
		return rel, "probably made for this file: same size and sampled fingerprint (not a hash of the whole file)"
	case idty.Different:
		return rel, "made for a different file: " + sc.File.Name + " has other bytes than this one"
	default:
		return rel, "cannot be checked: nothing recorded on both sides can tell whether it is the same file"
	}
}

type sidecarInput struct {
	Sidecar       json.RawMessage `json:"sidecar"`
	Confirm       bool            `json:"confirm"`
	DifferentFile string          `json:"differentFile"` // the researcher's sentence for applying to a file it was not made for
	Expect        *int64          `json:"expect"`
}

// previewSidecar is POST /api/datasets/{id}/corrections/sidecar/preview: whether a sidecar was made for this file, what it would
// change, its effect on the coordinates and what depends on the dataset. Nothing is stored.
func (s *Server) previewSidecar(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	var in sidecarInput
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	sc, err := readSidecar(in.Sidecar)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	match, text := sidecarMatch(sc, d)
	var base catalog.CorrectionState
	var cur *catalog.CorrectionState
	if d.Corrections != nil {
		base = d.Corrections.State
		cur = d.Corrections.InForce()
	}
	pv, code, msg := s.workerPreview(r.Context(), d, cur, sc.Corrections)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	changes := catalog.Diff(base, sc.Corrections)
	writeJSON(w, 200, map[string]any{"match": match, "matchText": text, "madeFor": sc.File.Name, "applies": match == idty.Verified || match == idty.Probable,
		"changes": changes, "state": sc.Corrections, "history": len(sc.History), "preview": pv, "impact": s.impactFor(wsOf(r), d, changes)})
}

// importSidecar is POST /api/datasets/{id}/corrections/sidecar: apply a sidecar's corrections as one new record, only after the
// researcher confirmed the preview, and only when it was made for this file or the researcher says, in a sentence, why it applies
// to this different one.
func (s *Server) importSidecar(w http.ResponseWriter, r *http.Request) {
	d, ok := s.correctionsDataset(w, r)
	if !ok {
		return
	}
	var in sidecarInput
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	if !in.Confirm {
		fail(w, 422, "look at the preview and confirm it first")
		return
	}
	if in.Expect == nil {
		fail(w, 400, "expect is required: the latest record you looked at (0 for none)")
		return
	}
	sc, err := readSidecar(in.Sidecar)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	match, text := sidecarMatch(sc, d)
	reason := "imported from a corrections file " + text
	if match != idty.Verified && match != idty.Probable {
		if !catalog.ValidNote(in.DifferentFile, 10, catalog.MaxReason) {
			fail(w, 409, "this corrections file was not made for this file ("+text+"); to apply it anyway, say in a sentence (10 to 500 characters) why it applies to this one")
			return
		}
		reason = "imported from a corrections file made for " + sc.File.Name + " and applied to this different file: " + in.DifferentFile
	}
	if r := []rune(reason); len(r) > catalog.MaxReason {
		reason = strings.TrimSpace(string(r[:catalog.MaxReason]))
	}
	pv, code, msg := s.workerPreview(r.Context(), d, nil, sc.Corrections)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if why, _ := pv["refused"].(string); why != "" {
		fail(w, 422, why)
		return
	}
	next := sc.Corrections
	// what the file says is read from this file, not taken from the sidecar
	if next.Unit != nil {
		fu, has := fileStatedUnit(d)
		if !has {
			fail(w, 409, "the corrections file corrects BUNIT, but this file has none: declare its unit instead")
			return
		}
		u := *next.Unit
		u.File = fu
		next.Unit = &u
	}
	for k, e := range next.WCS {
		e.File = nil
		next.WCS[k] = e
	}
	fillFileValues(&next, pv)
	var base catalog.CorrectionState
	if d.Corrections != nil {
		base = d.Corrections.State
	}
	rec := catalog.CorrectionRecord{Action: catalog.CorrImport, State: next, Changes: catalog.Diff(base, next), Reason: reason, Author: "user"}
	s.appendCorrection(r.Context(), w, d, rec, *in.Expect)
}

// ---- reviewer-safe statements ----

// safeCorrection is one correction as a reviewer sees it: the keyword, what the file says, the value in force, the tier and the
// researcher's reason and source. No id, no path.
type safeCorrection struct {
	What   string `json:"what"`
	File   any    `json:"file"`
	Value  any    `json:"value"`
	Tier   string `json:"tier"`
	Reason string `json:"reason"`
	Source string `json:"source,omitempty"`
}

func safeCorrections(st catalog.CorrectionState) []safeCorrection {
	out := []safeCorrection{}
	if st.Unit != nil {
		out = append(out, safeCorrection{What: "BUNIT", File: st.Unit.File, Value: st.Unit.Value, Tier: catalog.TierDeclared, Reason: st.Unit.Reason, Source: st.Unit.Source})
	}
	for _, k := range st.Keys() {
		if k == "BUNIT" {
			continue
		}
		e := st.WCS[k]
		out = append(out, safeCorrection{What: k, File: e.File, Value: e.Value, Tier: catalog.TierDeclared, Reason: e.Reason, Source: e.Source})
	}
	return out
}

func valueText(v any) string {
	switch x := v.(type) {
	case nil:
		return "nothing (no such card)"
	case float64:
		return fmt.Sprintf("%.12g", x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// correctionStatements say, for a reviewer, what was corrected and why: "corrected by the researcher: <reason>", the tier, and
// what the file says.
func correctionStatements(st catalog.CorrectionState, of string) []string {
	var out []string
	for _, c := range safeCorrections(st) {
		what := "The WCS keyword " + c.What
		if c.What == "BUNIT" {
			what = "The brightness unit (BUNIT)"
		}
		src := ""
		if c.Source != "" {
			src = " Source: " + c.Source + "."
		}
		out = append(out, fmt.Sprintf("%s of %s is %s, corrected by the researcher (tier: declared): %s. The file says %s; the file was not changed.%s",
			what, of, valueText(c.Value), strings.TrimRight(c.Reason, "."), valueText(c.File), src))
	}
	return out
}
