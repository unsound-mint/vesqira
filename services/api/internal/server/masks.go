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
	"unicode"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
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
}

func (s *Server) viewOf(m masks.Mask, d catalog.Dataset) maskView {
	return maskView{Mask: m, Stale: m.SourceSize != d.SizeBytes || m.SourceMtime != d.ModifiedAt.UnixNano()}
}

func (s *Server) listMasks(w http.ResponseWriter, r *http.Request) {
	d, ok := s.datasetFor(w, r)
	if !ok {
		return
	}
	ms, err := s.Masks.ForDataset(d.ID)
	if err != nil {
		log.Printf("masks: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := make([]maskView, len(ms))
	for i, m := range ms {
		out[i] = s.viewOf(m, d)
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
func (s *Server) makeMask(ctx context.Context, d catalog.Dataset, in maskReq, author string) (maskView, int, string) {
	if d.HostID != 0 {
		return maskView{}, 409, "this file lives on another host: a mask is made on this computer, from a file read here (copy the file here first)"
	}
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
	byShapeOnly := false // a second file whose WCS cannot confirm that it shares the grid
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
			nm, code, msg := s.imageDataset(in.Sigma.DatasetID)
			if code != 0 {
				return maskView{}, 422, "noise map: " + msg
			}
			p, shapeOnly, code, msg := s.sameGridLocal(ctx, d, nm)
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
		all, err := s.Regions.List(d.ID)
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
		src, code, msg := s.imageDataset(in.ImportDatasetID)
		if code != 0 {
			return maskView{}, 422, "mask file: " + msg
		}
		if src.ID == d.ID {
			return maskView{}, 422, "the mask file is the dataset itself"
		}
		p, shapeOnly, code, msg := s.sameGridLocal(ctx, d, src)
		if code != 0 {
			return maskView{}, code, "mask file: " + msg
		}
		byShapeOnly = byShapeOnly || shapeOnly
		params["path"], params["nonzero"] = p, in.Nonzero
	}
	if mtype == masks.Other || in.Meaning != "" {
		params["meaning"] = cleanText(in.Meaning, 300)
	}
	pj, _ := json.Marshal(params)
	var b [4]byte
	_, _ = rand.Read(b[:])
	out := filepath.Join(s.Analysis.DerivedDir, fmt.Sprintf("mask-%d-%s-%s.fits", d.ID, mtype, hex.EncodeToString(b[:])))
	res, code, msg := s.runAnalysisProgramCtx(ctx, []string{"-P", s.Analysis.Script, "mask", "--input", real, "--out", out, "--params", string(pj)})
	if code != 0 {
		_ = os.Remove(out)
		return maskView{}, code, msg
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
		_ = os.Remove(out)
		return maskView{}, 502, "the mask program returned an unreadable summary"
	}
	if byShapeOnly { // the rule says so: the match of the two files is by pixel shape, not by a confirmed WCS
		var rule map[string]any
		if json.Unmarshal(sum.Rule, &rule) == nil {
			rule["grid"] = "matched by pixel shape only: the WCS of one of the files does not say, so it was not confirmed that they share a grid"
			sum.Rule, _ = json.Marshal(rule)
		}
	}
	digest, size, mtime, err := fileIdentity(out)
	if err != nil {
		_ = os.Remove(out)
		log.Printf("masks: %v", err)
		return maskView{}, 500, "internal error"
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
	m, err := s.Masks.Add(masks.Mask{DatasetID: d.ID, Type: mtype, Name: name, Path: out, Width: sum.Width, Height: sum.Height, Channels: sum.Channels,
		Flagged: sum.Flagged, Total: sum.Total, Tier: tier, Mapping: mapping, Method: sum.Method, Rule: sum.Rule, Meaning: cleanText(in.Meaning, 300), Author: author,
		SourceSize: d.SizeBytes, SourceMtime: d.ModifiedAt.UnixNano(), SourceIdentity: sourceIdentity(d), FileSize: size, FileMtime: mtime, SHA256: digest})
	if err != nil {
		_ = os.Remove(out)
		log.Printf("masks: %v", err)
		return maskView{}, 500, "internal error"
	}
	s.audit("mask created", "dataset", d.ID, mtype+" mask, "+strconv.FormatInt(sum.Flagged, 10)+" pixels flagged")
	return s.viewOf(m, d), 0, ""
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

// sameGridLocal returns the path of another dataset, read on this computer, once its pixels are known to line up with d's: the same
// shape, and no declared axis that differs. A grid whose WCS one of the files does not state cannot be confirmed equal; a mask or a
// noise map is still usable then (the worker checks the shape), and the second result says the match is by pixel shape only so the
// mask's rule can say it.
func (s *Server) sameGridLocal(ctx context.Context, d, other catalog.Dataset) (real string, byShapeOnly bool, code int, msg string) {
	if other.HostID != 0 {
		return "", false, 409, "that file lives on another host; both files must be on this computer"
	}
	real, _, status, m := s.authorize(ctx, other)
	if status != 0 {
		return "", false, status, m
	}
	why, err := compat.Grids(d.Header, other.Header)
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
	f, err := os.Open(path)
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
	m, err := s.Masks.Get(id)
	if err != nil {
		s.maskErr(w, err)
		return
	}
	if err := s.Masks.Delete(id); err != nil {
		s.maskErr(w, err)
		return
	}
	s.audit("mask forgotten", "dataset", m.DatasetID, m.Type+" mask; the file was kept")
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
func (s *Server) usableMask(id int64, d catalog.Dataset) (masks.Mask, int, string) {
	m, err := s.Masks.Get(id)
	if err != nil {
		return m, 422, "a mask is not in the catalog"
	}
	if m.DatasetID != d.ID {
		return m, 422, "a mask belongs to another dataset"
	}
	if v := s.viewOf(m, d); v.Stale {
		return m, 409, "the mask \"" + m.Name + "\" was made from other bytes than this file has now (the file changed): make it again"
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
func (s *Server) maskSpecs(uses []maskUse, d catalog.Dataset, weightOK bool) (specJSON string, safe []map[string]any, owner []maskUse, code int, msg string) {
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
		m, code, msg := s.usableMask(u.ID, d)
		if code != 0 {
			return "", nil, nil, code, msg
		}
		if m.Type == masks.Missing && u.Policy != masks.Exclude {
			return "", nil, nil, 422, "a pixel with no value cannot be kept or weighted: the policy for the no-value mask is exclude"
		}
		spec := map[string]any{"path": m.Path, "type": m.Type, "policy": u.Policy, "id": m.ID, "name": m.Name, "tier": m.Tier, "fingerprint": "sha256:" + m.SHA256}
		snap := map[string]any{"type": m.Type, "name": m.Name, "policy": u.Policy, "tier": m.Tier, "method": m.Method, "rule": m.Rule, "fingerprint": "sha256:" + m.SHA256,
			"flaggedPixels": m.Flagged}
		if m.Mapping != "" {
			snap["mappingVersion"] = m.Mapping
		}
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
	if k.host != 0 {
		fail(w, 409, "masks are applied to files on this computer; this file is read on another host")
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
	text, _, _, code, msg := s.maskSpecs(uses, d, false)
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
	m, err := s.Masks.Get(id)
	if err != nil {
		s.maskErr(w, err)
		return
	}
	real, err := s.roots().Resolve(m.Path)
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
		Masks json.RawMessage `json:"masks"`
	}
	if json.Unmarshal(der.Params, &p) != nil || len(p.Masks) == 0 {
		return nil
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
	return out
}
