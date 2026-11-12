package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Typed masks (ADR 0055). A mask is a derived uint8 FITS file next to the other derived outputs, with a row that says what it
// flags, why, who made it, how far the interpretation is established (tier) and which bytes it was made from. The dataset's file is
// never touched. Pixels with no value are the type `missing`, computed and never declared; zero is a value and no mask flags it
// unless its stated rule does.

func (s *Server) masksOn() bool { return s.Masks != nil && s.analysisDeps() }

// maskView is a mask as the client sees it: the row, and whether it was made from other bytes than the dataset has now.
type maskView struct {
	masks.Mask
	Stale bool `json:"stale"`
	// SourceDataset is the registered file an imported mask was made from, when the row records which one (masks made before the
	// id was recorded name only the file in their rule). It is read from the row, never guessed from file names.
	SourceDataset *maskSourceRef `json:"sourceDataset,omitempty"`
}

type maskSourceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Server) viewOf(ws workspace.ID, m masks.Mask, d catalog.Dataset) maskView {
	v := maskView{Mask: m, Stale: m.SourceSize != d.SizeBytes || m.SourceMtime != d.ModifiedAt.UnixNano()}
	if id := m.SourceDatasetID(); id != 0 {
		if src, err := s.Store.Get(ws, id); err == nil {
			v.SourceDataset = &maskSourceRef{ID: src.ID, Name: src.Name}
		}
	}
	return v
}

func (s *Server) listMasks(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	ms, err := s.Masks.ForDataset(wsOf(r), d.ID)
	if err != nil {
		log.Printf("masks: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := make([]maskView, len(ms))
	for i, m := range ms {
		out[i] = s.viewOf(wsOf(r), m, d)
	}
	writeJSON(w, 200, map[string]any{"masks": out})
}

type sigmaReq struct {
	Kind      string   `json:"kind"` // value | mad | map
	Value     *float64 `json:"value"`
	DatasetID int64    `json:"datasetId"`
}

type maskReq struct {
	Method string `json:"method"` // missing | low_snr | saturated | regions | import
	Type   string `json:"type"`   // only for an import, which the researcher types
	Name   string `json:"name"`
	// low_snr
	K     *float64  `json:"k"`
	Sigma *sigmaReq `json:"sigma"`
	// saturated
	Threshold *float64 `json:"threshold"`
	Source    string   `json:"source"`
	// regions
	RegionIDs []int64 `json:"regionIds"`
	// import: a registered image on the same grid, and what a nonzero pixel of it means
	ImportDatasetID int64  `json:"importDatasetId"`
	Nonzero         string `json:"nonzero"`
	Meaning         string `json:"meaning"`
}

// cleanText keeps a free text short and free of control characters.
func cleanText(t string, max int) string {
	t = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t))
	if r := []rune(t); len(r) > max {
		t = string(r[:max])
	}
	return t
}

func (s *Server) createMask(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	var in maskReq
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	v, code, msg := s.makeMask(r.Context(), d, in, "user")
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, v)
}

// makeMask makes one mask of d from a request and records it, written by `author` ("user" for the researcher's own request, "rule:<name>"
// for an import rule of theirs: either way the tier is declared, because the mask's type and meaning are the researcher's word). The
// file the mask is made from is read by the worker; d is never changed. It returns an HTTP-style code and a message on refusal.
//
// A file on a host gets its mask on that host (ADR 0064): the host's copy of the program reads the file there and writes the mask in the
// host's derived folder; only the summary comes back, and the row records the host.
func (s *Server) makeMask(ctx context.Context, d catalog.Dataset, in maskReq, author string) (maskView, int, string) {
	real, _, status, msg := s.authorize(ctx, d)
	if status != 0 {
		return maskView{}, status, msg
	}
	mtype := ""
	switch in.Method {
	case "missing":
		mtype = masks.Missing
	case "low_snr":
		mtype = masks.LowSNR
	case "saturated":
		mtype = masks.Saturated
	case "regions":
		mtype = masks.UserExcluded
	case "import":
		mtype = in.Type
		if !masks.ValidType(mtype) || mtype == masks.Missing {
			return maskView{}, 422, "an imported mask is invalid, low_snr, saturated, user_excluded or other: pixels with no value are computed from the data, never declared"
		}
	default:
		return maskView{}, 422, "the method is missing, low_snr, saturated, regions or import"
	}
	if mtype == masks.Other && cleanText(in.Meaning, 300) == "" {
		return maskView{}, 422, "a mask of type other needs a description of what it flags"
	}
	isAxial := d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation"
	if isAxial && (in.Method == "low_snr" || in.Method == "saturated") {
		return maskView{}, 422, "this map is declared as an axial orientation map: its values are angles, so a signal-to-noise or saturation threshold on them means nothing"
	}
	params := map[string]any{"method": in.Method, "type": mtype}
	byShapeOnly := false   // a second file whose WCS cannot confirm that it shares the grid
	var importedFrom int64 // the registered file an import was made from, kept in the rule so the explorer can tell mask sources apart
	switch in.Method {
	case "low_snr":
		if in.K == nil || in.Sigma == nil {
			return maskView{}, 422, "low signal-to-noise needs the threshold k and where sigma comes from"
		}
		params["k"] = *in.K
		sg := map[string]any{"kind": in.Sigma.Kind}
		switch in.Sigma.Kind {
		case "value":
			if in.Sigma.Value == nil {
				return maskView{}, 422, "give the value of sigma"
			}
			sg["value"] = *in.Sigma.Value
		case "mad":
		case "map":
			nm, code, msg := s.imageDataset(wsCtx(ctx), in.Sigma.DatasetID)
			if code != 0 {
				return maskView{}, 422, "noise map: " + msg
			}
			if nm.Corrections.InForce() != nil {
				// its path travels inside the mask's parameters, where the analysis program does not apply corrections (ADR 0061)
				return maskView{}, 409, "noise map: it has WCS or unit corrections in force, which a noise map for a mask does not take yet; withdraw them or use another noise map"
			}
			p, shapeOnly, code, msg := s.sameGrid(ctx, d, nm)
			if code != 0 {
				return maskView{}, code, "noise map: " + msg
			}
			sg["path"] = p
			byShapeOnly = byShapeOnly || shapeOnly
		default:
			return maskView{}, 422, "sigma is a value, a noise map or the robust estimate"
		}
		params["sigma"] = sg
	case "saturated":
		if in.Threshold == nil {
			return maskView{}, 422, "give the saturation threshold"
		}
		params["threshold"], params["source"] = *in.Threshold, in.Source
	case "regions":
		if len(in.RegionIDs) == 0 || s.Regions == nil {
			return maskView{}, 422, "name at least one saved region"
		}
		all, err := s.Regions.List(wsCtx(ctx), d.ID)
		if err != nil {
			return maskView{}, 500, "internal error"
		}
		byID := map[int64]int{}
		for i, g := range all {
			byID[g.ID] = i
		}
		var regs []map[string]any
		for _, id := range in.RegionIDs {
			i, ok := byID[id]
			if !ok {
				return maskView{}, 422, "a region does not belong to this dataset"
			}
			g := all[i]
			if g.Shape.Shape == "line" {
				return maskView{}, 422, "a line has no area: it cannot exclude pixels"
			}
			regs = append(regs, map[string]any{"name": g.Name, "shape": g.Shape.Shape, "x0": g.Shape.X0, "y0": g.Shape.Y0, "x1": g.Shape.X1, "y1": g.Shape.Y1,
				"cx": g.Shape.CX, "cy": g.Shape.CY, "r": g.Shape.R})
		}
		params["regions"] = regs
	case "import":
		if in.Nonzero != "flagged" && in.Nonzero != "valid" {
			return maskView{}, 422, "say what a nonzero pixel of the mask file means: flagged, or valid"
		}
		src, code, msg := s.imageDataset(wsCtx(ctx), in.ImportDatasetID)
		if code != 0 {
			return maskView{}, 422, "mask file: " + msg
		}
		if src.Corrections.InForce() != nil {
			return maskView{}, 409, "mask file: it has WCS or unit corrections in force, which an imported mask file does not take yet; withdraw them first"
		}
		if src.ID == d.ID {
			return maskView{}, 422, "the mask file is the dataset itself"
		}
		p, shapeOnly, code, msg := s.sameGrid(ctx, d, src)
		if code != 0 {
			return maskView{}, code, "mask file: " + msg
		}
		byShapeOnly = byShapeOnly || shapeOnly
		importedFrom = src.ID
		params["path"], params["nonzero"] = p, in.Nonzero
	}
	if mtype == masks.Other || in.Meaning != "" {
		params["meaning"] = cleanText(in.Meaning, 300)
	}
	pj, _ := json.Marshal(params)
	var b [4]byte
	_, _ = rand.Read(b[:])
	fileName := fmt.Sprintf("mask-%d-%s-%s.fits", d.ID, mtype, hex.EncodeToString(b[:]))
	var res analysisOutcome
	var out string
	if d.HostID != 0 {
		h, code, msg := s.hostOf(d.HostID)
		if code != 0 {
			return maskView{}, code, msg
		}
		// nothing to remove on a refusal: the host keeps no file unless the program succeeded (hostrun.py)
		if res, out, code, msg = s.hostRunAnalysis(ctx, h, "mask", fileName, []string{"--input", real, "--params", string(pj)}, nil); code != 0 {
			return maskView{}, code, msg
		}
	} else {
		out = filepath.Join(s.Analysis.DerivedDir, fileName)
		var code int
		res, code, msg = s.runAnalysisProgramCtx(wsCtx(ctx), ctx, []string{"-P", s.Analysis.Script, "mask", "--input", real, "--out", out, "--params", string(pj)})
		if code != 0 {
			_ = os.Remove(out)
			return maskView{}, code, msg
		}
	}
	drop := func() { // a mask file nobody records: removed here; on a host it stays, inert, in the derived folder (Vesqira deletes nothing there)
		if d.HostID == 0 {
			_ = os.Remove(out)
		}
	}
	var sum struct {
		Flagged  int64           `json:"flagged"`
		Total    int64           `json:"total"`
		Width    int             `json:"width"`
		Height   int             `json:"height"`
		Channels int             `json:"channels"`
		Method   string          `json:"method"`
		Rule     json.RawMessage `json:"rule"`
	}
	if json.Unmarshal(res.Summary, &sum) != nil {
		drop()
		return maskView{}, 502, "the mask program returned an unreadable summary"
	}
	if byShapeOnly || importedFrom != 0 {
		var rule map[string]any
		if json.Unmarshal(sum.Rule, &rule) == nil {
			if byShapeOnly { // the rule says so: the match of the two files is by pixel shape, not by a confirmed WCS
				rule["grid"] = "matched by pixel shape only: the WCS of one of the files does not say, so it was not confirmed that they share a grid"
			}
			if importedFrom != 0 {
				rule[masks.SourceDatasetKey] = importedFrom
			}
			sum.Rule, _ = json.Marshal(rule)
		}
	}
	// Verified is a check the app ran: only the no-value mask, computed from the data. Every other rule (a threshold, a region, a file) is
	// the researcher's word, which the app applied faithfully but did not establish.
	tier, mapping := catalog.TierDeclared, ""
	if mtype == masks.Missing {
		tier = catalog.TierVerified
	}
	name := cleanText(in.Name, 80)
	if name == "" {
		name = defaultMaskName(mtype, in)
	}
	m, err := s.storeMask(ctx, d, masks.Mask{HostID: d.HostID, Type: mtype, Name: name, Path: out, Width: sum.Width, Height: sum.Height, Channels: sum.Channels,
		Flagged: sum.Flagged, Total: sum.Total, Tier: tier, Mapping: mapping, Method: sum.Method, Rule: sum.Rule, Meaning: cleanText(in.Meaning, 300), Author: author})
	if err != nil {
		drop()
		log.Printf("masks: %v", err)
		if d.HostID != 0 {
			return maskView{}, 502, "the mask was written on the host but its identity could not be read there (is the VPN up?); nothing was recorded, make it again"
		}
		return maskView{}, 500, "internal error"
	}
	s.audit(ctx, "mask created", "dataset", d.ID, mtype+" mask, "+strconv.FormatInt(sum.Flagged, 10)+" pixels flagged")
	return s.viewOf(wsCtx(ctx), m, d), 0, ""
}

// storeMask records a mask file just written for d: it fills in what the mask was made from (d's size, modification time and recorded
// identity, so it is fresh against d and stale the day d changes) and the mask file's own identity (size, mtime, SHA-256). Every way of
// making a mask (a request, an import rule, carrying one through an operation) ends here. A mask on a host (m.HostID) has its identity read
// there: the SHA-256 by the host's hashing path, the size and modification time by its worker (ADR 0064).
func (s *Server) storeMask(ctx context.Context, d catalog.Dataset, m masks.Mask) (masks.Mask, error) {
	var digest string
	var size, mtime int64
	var err error
	if m.HostID != 0 {
		h, code, msg := s.hostOf(m.HostID)
		if code != 0 {
			return masks.Mask{}, errors.New(msg)
		}
		digest, size, mtime, err = s.hostFileIdentity(ctx, h, m.Path)
	} else {
		digest, size, mtime, err = fileIdentity(m.Path)
	}
	if err != nil {
		return masks.Mask{}, err
	}
	m.DatasetID, m.SourceSize, m.SourceMtime, m.SourceIdentity = d.ID, d.SizeBytes, d.ModifiedAt.UnixNano(), sourceIdentity(d)
	m.FileSize, m.FileMtime, m.SHA256 = size, mtime, digest
	return s.Masks.Add(wsCtx(ctx), actorID(ctx), m)
}

// maskSnapshot is the id-free account of a mask that a result's parameters keep (and so the reviewer sees): no id, no path.
func maskSnapshot(m masks.Mask) map[string]any {
	snap := map[string]any{"type": m.Type, "name": m.Name, "tier": m.Tier, "method": m.Method, "rule": m.Rule, "fingerprint": "sha256:" + m.SHA256, "flaggedPixels": m.Flagged}
	if m.Mapping != "" {
		snap["mappingVersion"] = m.Mapping
	}
	return snap
}

func defaultMaskName(mtype string, in maskReq) string {
	switch mtype {
	case masks.Missing:
		return "No value (NaN, BLANK)"
	case masks.LowSNR:
		sg := ""
		if in.Sigma != nil {
			sg = map[string]string{"value": "stated σ", "mad": "robust σ (MAD)", "map": "noise map"}[in.Sigma.Kind]
		}
		if in.K != nil {
			return fmt.Sprintf("Low S/N < %g (%s)", *in.K, sg)
		}
	case masks.Saturated:
		if in.Threshold != nil {
			return fmt.Sprintf("Saturated ≥ %g", *in.Threshold)
		}
	case masks.UserExcluded:
		return "Excluded by me"
	}
	return masks.TypeLabel(mtype) + " mask"
}

// sameGrid returns the path of another dataset, read on the same machine as d (a mask file or a noise map is read by the program that
// reads d, here or on d's host), once its pixels are known to line up with d's: the same shape, and no declared axis that differs. A grid whose WCS one of the files does not state cannot be confirmed equal; a mask or a
// noise map is still usable then (the worker checks the shape), and the second result says the match is by pixel shape only so the
// mask's rule can say it.
func (s *Server) sameGrid(ctx context.Context, d, other catalog.Dataset) (real string, byShapeOnly bool, code int, msg string) {
	if other.HostID != d.HostID {
		if d.HostID == 0 {
			return "", false, 409, "that file lives on a host and this one on this computer; both must be on the same machine (transfer one of them)"
		}
		return "", false, 409, "that file is not on the same host as this one; both must be on the same machine (register a copy on this file's host)"
	}
	real, _, status, m := s.authorize(ctx, other)
	if status != 0 {
		return "", false, status, m
	}
	why, err := compat.Grids(effectiveHeader(d), effectiveHeader(other))
	if err != nil {
		return "", false, 422, "its header could not be compared"
	}
	var hard []string
	for _, w := range why {
		if !strings.Contains(w, "unresolved") {
			hard = append(hard, w)
		}
	}
	if len(hard) > 0 {
		return "", false, 409, "it is not on the dataset's grid: " + strings.Join(hard, "; ")
	}
	return real, len(why) > 0, 0, ""
}

// sourceIdentity says what is recorded about the bytes the mask was made from, never more (ADR 0053).
func sourceIdentity(d catalog.Dataset) string {
	if p, ok := primaryOf(d.Locations); ok {
		switch {
		case p.Full != nil:
			return "sha256:" + p.Full.Value
		case p.Sampled != nil:
			return "sampled:" + p.Sampled.Value
		}
	}
	return ""
}

func fileIdentity(path string) (digest string, size, mtime int64, err error) {
	f, err := sourceroot.OpenFile(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, st.ModTime().UnixNano(), nil
}

func (s *Server) deleteMask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	m, err := s.Masks.Get(wsOf(r), id)
	if err != nil {
		s.maskErr(w, err)
		return
	}
	if err := s.Masks.Delete(wsOf(r), id); err != nil {
		s.maskErr(w, err)
		return
	}
	s.audit(r.Context(), "mask forgotten", "dataset", m.DatasetID, m.Type+" mask; the file was kept")
	writeJSON(w, 200, map[string]any{"removed": id})
}

func (s *Server) maskErr(w http.ResponseWriter, err error) {
	if errors.Is(err, masks.ErrNotFound) {
		fail(w, 404, "mask not found")
		return
	}
	log.Printf("masks: %v", err)
	fail(w, 500, "internal error")
}

// usableMask returns a mask that may be applied now: it belongs to the dataset, was made from the bytes the dataset has, and its own
// file is the one that was written.
func (s *Server) usableMask(ws workspace.ID, id int64, d catalog.Dataset) (masks.Mask, int, string) {
	m, err := s.Masks.Get(ws, id)
	if err != nil {
		return m, 422, "a mask is not in the catalog"
	}
	if m.DatasetID != d.ID {
		return m, 422, "a mask belongs to another dataset"
	}
	if v := s.viewOf(ws, m, d); v.Stale {
		return m, 409, "the mask \"" + m.Name + "\" was made from other bytes than this file has now (the file changed): make it again"
	}
	if m.HostID != d.HostID { // a dataset has one primary location; a mask is read by the program that reads that file
		return m, 409, "the mask \"" + m.Name + "\" lives on another machine than the file it is applied to: make it again where the file is read"
	}
	if m.HostID != 0 {
		h, code, msg := s.hostOf(m.HostID)
		if code != 0 {
			return m, code, msg
		}
		// a bounded look of its own: callers without a request (a figure redraw check) use it too
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		size, mtime, err := s.hostStat(ctx, h, m.Path)
		var re *worker.RequestError
		switch {
		case errors.As(err, &re):
			return m, 409, "the file of the mask \"" + m.Name + "\" changed or is gone on the host: make it again"
		case err != nil:
			return m, 502, msgHostUnreachable
		case size != m.FileSize || mtime != m.FileMtime:
			return m, 409, "the file of the mask \"" + m.Name + "\" changed or is gone on the host: make it again"
		}
		return m, 0, ""
	}
	st, err := os.Stat(m.Path)
	if err != nil || st.Size() != m.FileSize || st.ModTime().UnixNano() != m.FileMtime {
		return m, 409, "the file of the mask \"" + m.Name + "\" changed or is gone: make it again"
	}
	return m, 0, ""
}

// maskUse is one mask an operation is asked to apply, with its policy. There is no default policy: each one is stated.
type maskUse struct {
	ID     int64    `json:"id"`
	Policy string   `json:"policy"` // exclude | keep | weight
	Weight *float64 `json:"weight"` // a factor from 0 to 1 for a flagged pixel, with the weight policy
}

// maskSpecs checks the masks an operation was asked to apply against the dataset and returns the worker's --masks JSON, and the
// reviewer-safe snapshot of what was applied (no ids, no paths), which the result's provenance keeps whatever happens to the masks
// later. weightOK is true for an operation that already has weights.
func (s *Server) maskSpecs(ws workspace.ID, uses []maskUse, d catalog.Dataset, weightOK bool) (specJSON string, safe []map[string]any, owner []maskUse, code int, msg string) {
	if len(uses) == 0 {
		return "", nil, nil, 0, ""
	}
	if !s.masksOn() {
		return "", nil, nil, 422, "masks are not available on this server"
	}
	if len(uses) > 16 {
		return "", nil, nil, 422, "at most 16 masks can be applied at once"
	}
	seen := map[int64]bool{}
	var specs []map[string]any
	for _, u := range uses {
		if seen[u.ID] {
			return "", nil, nil, 422, "a mask is listed twice"
		}
		seen[u.ID] = true
		if u.Policy != masks.Exclude && u.Policy != masks.Keep && u.Policy != masks.Weight {
			return "", nil, nil, 422, "every mask needs an explicit policy: exclude, keep or weight"
		}
		if u.Policy == masks.Weight {
			if !weightOK {
				return "", nil, nil, 422, "this analysis has no weights, so a mask cannot be applied with the weight policy: exclude it or keep it"
			}
			if u.Weight == nil || *u.Weight < 0 || *u.Weight > 1 {
				return "", nil, nil, 422, "the weight policy needs a factor from 0 to 1 for a flagged pixel"
			}
		}
		m, code, msg := s.usableMask(ws, u.ID, d)
		if code != 0 {
			return "", nil, nil, code, msg
		}
		if m.Type == masks.Missing && u.Policy != masks.Exclude {
			return "", nil, nil, 422, "a pixel with no value cannot be kept or weighted: the policy for the no-value mask is exclude"
		}
		spec := map[string]any{"path": m.Path, "type": m.Type, "policy": u.Policy, "id": m.ID, "name": m.Name, "tier": m.Tier, "fingerprint": "sha256:" + m.SHA256}
		snap := maskSnapshot(m)
		snap["policy"] = u.Policy
		if u.Policy == masks.Weight {
			spec["weight"], snap["weight"] = *u.Weight, *u.Weight
		}
		specs = append(specs, spec)
		safe = append(safe, snap)
	}
	b, _ := json.Marshal(specs)
	return string(b), safe, uses, 0, ""
}

// regionMasks reads the masks a region spectrum or region histogram is asked to apply from the query (`mask=<id>:<policy>`, repeated),
// checks them with the same rules as every other analysis (maskSpecs: an explicit policy per mask, weight refused, a mask of this
// dataset, fresh) and returns the list for the plane worker, and the text that makes the cache key differ per mask, policy and file.
// ok is false when a refusal was already written. No mask named: nil, "", true (the unmasked path is untouched).
func (s *Server) regionMasks(w http.ResponseWriter, r *http.Request, d catalog.Dataset, k cacheKey) (specs []any, key string, ok bool) {
	raw := r.URL.Query()["mask"]
	if len(raw) == 0 {
		return nil, "", true
	}
	if k.win != "" { // masks are in the source's pixels; a window would need them cut the same way
		fail(w, 409, "masks are not applied to a virtual subset yet: they are in the source's pixels. Write the subset as a file, or take the spectrum of the source")
		return nil, "", false
	}
	if k.off != nil { // offline copies are unmasked replies (ADR 0059); a masked answer needs the file
		fail(w, 503, "masks cannot be applied while the file's host is unreachable; offline copies are unmasked")
		return nil, "", false
	}
	uses := make([]maskUse, 0, len(raw))
	for _, t := range raw {
		idText, policy, found := strings.Cut(t, ":")
		id, err := strconv.ParseInt(idText, 10, 64)
		if !found || err != nil {
			fail(w, 400, "mask must be <id>:<policy>, for example 3:exclude")
			return nil, "", false
		}
		uses = append(uses, maskUse{ID: id, Policy: policy})
	}
	if len(uses) > 8 {
		fail(w, 422, "at most 8 masks can be applied to a region at once")
		return nil, "", false
	}
	text, _, _, code, msg := s.maskSpecs(wsOf(r), uses, d, false)
	if code != 0 {
		fail(w, code, msg)
		return nil, "", false
	}
	if len(text) > 6000 { // the plane worker reads one request line of at most 8 KiB
		fail(w, 422, "these masks are too many or have too long names for one region request: apply fewer")
		return nil, "", false
	}
	if err := json.Unmarshal([]byte(text), &specs); err != nil {
		log.Printf("masks: %v", err)
		fail(w, 500, "internal error")
		return nil, "", false
	}
	return specs, "|masks=" + text, true
}

// maskPlane answers with one plane of a mask for the viewer's hatch overlay: a frame whose payload is uint8, row 0 = FITS y 1, 1 = flagged.
func (s *Server) maskPlane(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	ch, ok := index(w, r, "channel")
	if !ok {
		return
	}
	m, err := s.Masks.Get(wsOf(r), id)
	if err != nil {
		s.maskErr(w, err)
		return
	}
	if m.HostID != 0 { // read by the host's worker: one plane of uint8 crosses, never the mask file (ADR 0064)
		h, code, msg := s.hostOf(m.HostID)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		size, mtime, err := s.hostStat(r.Context(), h, m.Path)
		var re *worker.RequestError
		switch {
		case errors.As(err, &re):
			fail(w, 409, "the mask file changed or is gone on the host: make the mask again")
			return
		case err != nil:
			fail(w, 502, msgHostUnreachable)
			return
		case size != m.FileSize || mtime != m.FileMtime:
			fail(w, 409, "the mask file changed or is gone on the host: make the mask again")
			return
		}
		k := cacheKey{id: -m.ID, host: m.HostID, size: size, mtime: mtime, op: "mask_plane", channel: ch}
		rep, ok := s.ask(w, r, k, map[string]any{"op": "mask_plane", "path": m.Path, "channel": ch})
		if !ok {
			return
		}
		writeFrame(w, rep)
		return
	}
	real, err := s.roots(wsOf(r)).Resolve(m.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.Size() != m.FileSize || st.ModTime().UnixNano() != m.FileMtime {
		fail(w, 409, "the mask file changed or is gone: make the mask again")
		return
	}
	k := cacheKey{id: -m.ID, size: st.Size(), mtime: st.ModTime().UnixNano(), op: "mask_plane", channel: ch}
	rep, ok := s.ask(w, r, k, map[string]any{"op": "mask_plane", "path": real, "channel": ch})
	if !ok {
		return
	}
	writeFrame(w, rep)
}

// appliedMask is the id-free snapshot of a mask an analysis applied, as kept in a result's parameters.
type appliedMask struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Policy         string          `json:"policy"`
	Weight         *float64        `json:"weight"`
	Tier           string          `json:"tier"`
	MappingVersion string          `json:"mappingVersion"`
	Rule           json.RawMessage `json:"rule"`
}

// masksTiers turns the masks recorded in a derived result's parameters into reviewer-visible tier lines. A result without masks,
// or with parameters in another shape, says nothing.
func masksTiers(der *derived.Summary) []safeTier {
	if der == nil {
		return nil
	}
	var p struct {
		Masks     json.RawMessage `json:"masks"`
		MaskCarry json.RawMessage `json:"maskCarry"`
	}
	if json.Unmarshal(der.Params, &p) != nil {
		return nil
	}
	carried := carryTiers(p.MaskCarry) // masks carried into this result, left behind, and the no-value mask computed again
	if len(p.Masks) == 0 {
		return carried
	}
	var list []appliedMask
	if json.Unmarshal(p.Masks, &list) != nil {
		var two struct {
			Map   []appliedMask `json:"map"`
			Other []appliedMask `json:"other"`
		}
		if json.Unmarshal(p.Masks, &two) != nil {
			return nil
		}
		list = append(append(list, two.Map...), two.Other...)
	}
	out := []safeTier{}
	for _, m := range list {
		how := map[string]string{masks.Exclude: "excluded", masks.Keep: "kept (not excluded)", masks.Weight: "weighted"}[m.Policy]
		if m.Policy == masks.Weight && m.Weight != nil {
			how += " by " + strconv.FormatFloat(*m.Weight, 'g', 6, 64)
		}
		var rule struct {
			Rule string `json:"rule"`
		}
		_ = json.Unmarshal(m.Rule, &rule)
		note := "pixels flagged " + masks.TypeLabel(m.Type) + " were " + how + " in this result"
		if rule.Rule != "" {
			note += "; rule: " + rule.Rule
		}
		out = append(out, safeTier{What: "mask: " + masks.TypeLabel(m.Type) + " (" + m.Name + ")", Tier: m.Tier, MappingVersion: m.MappingVersion, Note: note})
	}
	return append(out, carried...)
}
