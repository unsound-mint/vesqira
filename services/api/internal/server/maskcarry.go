package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
)

// Masks carried into the result of cutout, slab, virtual subset, align and convolve (ADR 0055). The researcher chooses, per mask, to
// carry it or leave it behind; nothing is carried unless asked. A carried mask is a new mask of the result (same type, tier unchanged,
// a rule that says what was done, the fingerprint of its own file); the source masks are never changed. The `missing` mask of a
// result is computed again from the result's own values, never carried: a source that has one gets that one back, computed.

// carryChoice is the researcher's decision about one mask of the source.
type carryChoice struct {
	ID    int64 `json:"id"`
	Carry bool  `json:"carry"` // true: carry it into the result; false: leave it behind (recorded)
}

// carryOps are the operations whose result can carry masks; align and convolve resample or smooth, so the choice is asked for every
// mask there rather than assumed (a cut keeps the same pixels, so leaving a mask out is the quiet default).
var carryOps = map[string]bool{"cutout": true, "slab": true, "subset": true, "align": true, "convolve": true}
var carryChoiceRequired = map[string]bool{"align": true, "convolve": true}

type carryOut struct {
	mask masks.Mask
	out  string // where the worker writes the carried copy
}

// carryPlan is what one operation will carry: the decision for every mask of the source, the worker's options, and the files to remove
// when the operation fails.
type carryPlan struct {
	op         string
	hostID     int64 // the machine the result is written on (0: this computer); the carried masks are written there too (ADR 0064)
	carried    []carryOut
	left       []masks.Mask // masks of the source that stay behind, with the no-value mask excluded (it is recomputed, below)
	missingOut string       // where the result's own no-value mask goes; empty when the source has none
}

// specsJSON is the --carry-masks value: a JSON list of {path, type, out}, or "" when nothing is carried.
func (p *carryPlan) specsJSON() string {
	if p == nil || len(p.carried) == 0 {
		return ""
	}
	specs := make([]map[string]any, 0, len(p.carried))
	for _, c := range p.carried {
		specs = append(specs, map[string]any{"path": c.mask.Path, "type": c.mask.Type, "out": c.out})
	}
	b, _ := json.Marshal(specs)
	return string(b)
}

func (p *carryPlan) args() []string {
	if p == nil {
		return nil
	}
	out := []string{}
	if t := p.specsJSON(); t != "" {
		out = append(out, "--carry-masks="+t)
	}
	if p.missingOut != "" {
		out = append(out, "--missing-out="+p.missingOut)
	}
	return out
}

// cleanup removes the files an operation that failed may have written. The sources are never touched.
func (p *carryPlan) cleanup() {
	if p == nil || p.hostID != 0 { // on a host the files stay, inert, in its derived folder: Vesqira deletes nothing there (ADR 0041, 0064)
		return
	}
	for _, c := range p.carried {
		_ = os.Remove(c.out)
	}
	if p.missingOut != "" {
		_ = os.Remove(p.missingOut)
	}
}

// key identifies what is carried, so a materialised subset is reused only for the same choice: the carried masks with their fingerprints.
func (p *carryPlan) key() string {
	if p == nil || len(p.carried) == 0 {
		return ""
	}
	var ids []string
	for _, c := range p.carried {
		ids = append(ids, fmt.Sprintf("%d:%s", c.mask.ID, c.mask.SHA256))
	}
	slices.Sort(ids)
	return "|carry " + strings.Join(ids, ",")
}

func (s *Server) derivedMaskPath(kind string, datasetID int64, mtype string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return filepath.Join(s.Analysis.DerivedDir, fmt.Sprintf("mask-%s-%d-%s-%s.fits", kind, datasetID, mtype, hex.EncodeToString(b[:])))
}

// planCarry checks the researcher's choices against the source dataset d and the operation, and decides what the operation carries. A
// mask chosen to carry must belong to d, be fresh and have its file unchanged (usableMask, the check every analysis uses): a stale mask
// is refused. For align and convolve every mask of d must be decided. It returns nil when there is nothing to carry and no mask to
// recompute.
func (s *Server) planCarry(d catalog.Dataset, op string, choices []carryChoice) (*carryPlan, int, string) {
	if !carryOps[op] {
		if len(choices) > 0 {
			return nil, 422, "masks are carried by a cutout, a slab, a subset, an alignment and a convolution; this analysis does not take them"
		}
		return nil, 0, ""
	}
	if !s.masksOn() {
		if len(choices) > 0 {
			return nil, 422, "masks are not available on this server"
		}
		return nil, 0, ""
	}
	if len(choices) > 16 {
		return nil, 422, "at most 16 masks can be carried at once"
	}
	all, err := s.Masks.ForDataset(d.ID)
	if err != nil {
		log.Printf("masks: %v", err)
		return nil, 500, "internal error"
	}
	byID := map[int64]masks.Mask{}
	for _, m := range all {
		byID[m.ID] = m
	}
	decided := map[int64]bool{}
	for _, c := range choices {
		m, ok := byID[c.ID]
		if !ok {
			return nil, 422, "a mask is not one of this dataset's"
		}
		if _, dup := decided[c.ID]; dup {
			return nil, 422, "a mask is listed twice"
		}
		decided[c.ID] = c.Carry
		if !c.Carry {
			continue
		}
		if m.Type == masks.Missing {
			return nil, 422, "the no-value mask is computed again from the result's own values; it is never carried"
		}
		if _, code, msg := s.usableMask(m.ID, d); code != 0 {
			return nil, code, msg
		}
	}
	if carryChoiceRequired[op] {
		var undecided []string
		for _, m := range all {
			if _, ok := decided[m.ID]; !ok && m.Type != masks.Missing {
				undecided = append(undecided, "\""+m.Name+"\"")
			}
		}
		if len(undecided) > 0 {
			return nil, 422, "choose, for each mask of this file, to carry it into the result or leave it behind (nothing is chosen for you): " + strings.Join(undecided, ", ")
		}
	}
	plan := &carryPlan{op: op, hostID: d.HostID}
	for _, m := range all {
		switch {
		case m.Type == masks.Missing:
			plan.missingOut = s.derivedMaskPath("missing", d.ID, masks.Missing)
		case decided[m.ID]:
			plan.carried = append(plan.carried, carryOut{mask: m, out: s.derivedMaskPath("carried", d.ID, m.Type)})
		default:
			plan.left = append(plan.left, m)
		}
	}
	if len(plan.carried) == 0 && plan.missingOut == "" && len(plan.left) == 0 {
		return nil, 0, ""
	}
	return plan, 0, ""
}

// carrySummary is what the worker reports of the masks it wrote (no path).
type carrySummary struct {
	By      string  `json:"by"`
	Rule    *string `json:"rule"`
	Carried []struct {
		Index    int   `json:"index"`
		Flagged  int64 `json:"flagged"`
		Total    int64 `json:"total"`
		Width    int   `json:"width"`
		Height   int   `json:"height"`
		Channels int   `json:"channels"`
	} `json:"carried"`
	Missing *struct {
		Flagged  int64           `json:"flagged"`
		Total    int64           `json:"total"`
		Width    int             `json:"width"`
		Height   int             `json:"height"`
		Channels int             `json:"channels"`
		Method   string          `json:"method"`
		Rule     json.RawMessage `json:"rule"`
	} `json:"missing"`
}

// recordCarried registers what the worker wrote as masks of the new dataset nd, made from src, and returns what the result's derived
// record keeps (`maskCarry`: no ids, no paths): the masks carried with their source's and their own fingerprints, the masks left behind,
// and that the no-value mask was computed again. `extra` names what the operation used besides the source (the reference of an
// alignment), for the rule's sentence.
func (s *Server) recordCarried(p *carryPlan, summary json.RawMessage, src, nd catalog.Dataset, extra string) (map[string]any, int, string) {
	if p == nil {
		return nil, 0, ""
	}
	ctx := context.Background()
	if p.hostID != 0 { // reading a file's identity on a host is a round trip over the VPN: bounded
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	cs := &carrySummary{By: p.op}
	if len(p.carried) > 0 || p.missingOut != "" { // otherwise the program was not asked to write a mask and says nothing of them
		var wrap struct {
			MaskCarry *carrySummary `json:"mask_carry"`
		}
		if err := json.Unmarshal(summary, &wrap); err != nil || wrap.MaskCarry == nil {
			p.cleanup()
			return nil, 502, "the analysis program did not report the masks it was asked to carry; no result was recorded"
		}
		cs = wrap.MaskCarry
		if len(cs.Carried) != len(p.carried) || (p.missingOut != "") != (cs.Missing != nil) {
			p.cleanup()
			return nil, 502, "the analysis program did not write the masks it was asked to carry; no result was recorded"
		}
	}
	by := cs.By + extra
	follows := "each flagged pixel of the source mask is kept at the pixel the data keeps; nothing is resampled"
	if cs.Rule != nil {
		follows = *cs.Rule
	}
	var carried, left []map[string]any
	for i, c := range p.carried {
		got := cs.Carried[i]
		sentence := fmt.Sprintf("carried from mask \"%s\" of %s by %s", c.mask.Name, src.Name, by)
		var orig any
		if len(c.mask.Rule) > 0 {
			_ = json.Unmarshal(c.mask.Rule, &orig)
		}
		rule, _ := json.Marshal(map[string]any{
			"rule": sentence, "estimator": nil, "threshold": nil, "operation": p.op, "by": by, "flagsFollow": follows,
			"carriedFrom": map[string]any{"mask": c.mask.Name, "dataset": src.Name, "type": c.mask.Type, "tier": c.mask.Tier, "mappingVersion": c.mask.Mapping,
				"method": c.mask.Method, "fingerprint": "sha256:" + c.mask.SHA256, "rule": orig},
		})
		m, err := s.storeMask(ctx, nd, masks.Mask{Type: c.mask.Type, Name: c.mask.Name, Path: c.out, Width: got.Width, Height: got.Height, Channels: got.Channels,
			Flagged: got.Flagged, Total: got.Total, Tier: c.mask.Tier, Mapping: c.mask.Mapping, Method: masks.MethodCarried, Rule: rule,
			Meaning: c.mask.Meaning, Author: "user", HostID: p.hostID})
		if err != nil {
			log.Printf("masks: carrying: %v", err)
			p.cleanup()
			return nil, 500, "the result was written but its carried masks could not be recorded"
		}
		snap := maskSnapshot(c.mask)
		snap["carriedFingerprint"], snap["carriedFlaggedPixels"] = "sha256:"+m.SHA256, m.Flagged
		carried = append(carried, snap)
		s.audit("mask carried", "dataset", nd.ID, c.mask.Type+" mask \""+c.mask.Name+"\" from "+src.Name+" by "+p.op)
	}
	recomputed := []string{}
	if cs.Missing != nil {
		m, err := s.storeMask(ctx, nd, masks.Mask{Type: masks.Missing, Name: defaultMaskName(masks.Missing, maskReq{}), Path: p.missingOut, Width: cs.Missing.Width,
			Height: cs.Missing.Height, Channels: cs.Missing.Channels, Flagged: cs.Missing.Flagged, Total: cs.Missing.Total, Tier: catalog.TierVerified,
			Method: masks.MethodComputed, Rule: cs.Missing.Rule, Author: analysisAuthor, HostID: p.hostID})
		if err != nil {
			log.Printf("masks: recomputing the no-value mask: %v", err)
			return nil, 500, "the result was written but its no-value mask could not be recorded"
		}
		_ = m
		recomputed = append(recomputed, masks.Missing)
	}
	for _, m := range p.left {
		left = append(left, maskSnapshot(m))
	}
	if carried == nil {
		carried = []map[string]any{}
	}
	if left == nil {
		left = []map[string]any{}
	}
	return map[string]any{"operation": p.op, "by": by, "flagsFollow": follows, "carried": carried, "leftBehind": left, "recomputed": recomputed}, 0, ""
}
