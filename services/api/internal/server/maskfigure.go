package server

import (
	"encoding/json"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// Masks on a saved figure and in a carried result, as the figure's scene and the reviewer's provenance say them (ADR 0055).

// sceneMasks holds the masks a figure's scene says it hatched to the catalog: each must be one of d's, fresh, with its file unchanged,
// and the fingerprint the browser drew must be the row's. The scene then keeps what the catalog says (type, name, tier, fingerprint),
// not what the client wrote. It returns the masks in scene order.
func (s *Server) sceneMasks(sc *scene.Scene, d catalog.Dataset) ([]masks.Mask, int, string) {
	if len(sc.Masks) == 0 {
		return nil, 0, ""
	}
	if !s.masksOn() {
		return nil, 422, "masks are not available on this server"
	}
	out := make([]masks.Mask, 0, len(sc.Masks))
	for i, sm := range sc.Masks {
		m, code, msg := s.usableMask(sm.ID, d)
		if code != 0 {
			return nil, code, msg
		}
		if sm.Fingerprint != "sha256:"+m.SHA256 {
			return nil, 409, "the mask \"" + m.Name + "\" is not the one the picture was drawn with (its fingerprint differs): draw the figure again"
		}
		sc.Masks[i] = scene.Mask{ID: m.ID, Type: m.Type, Name: m.Name, Tier: m.Tier, Fingerprint: "sha256:" + m.SHA256}
		out = append(out, m)
	}
	return out, 0, ""
}

// shownMasksTiers are the reviewer's lines for the masks hatched in a figure: type, name, tier, rule and fingerprint, no id and no path.
func shownMasksTiers(shown []masks.Mask) []safeTier {
	var out []safeTier
	for _, m := range shown {
		var rule struct {
			Rule string `json:"rule"`
		}
		_ = json.Unmarshal(m.Rule, &rule)
		note := "hatched over the plane of this figure: it flags " + strconv.FormatInt(m.Flagged, 10) + " of " + strconv.FormatInt(m.Total, 10) + " pixels as " + masks.TypeLabel(m.Type) +
			"; fingerprint sha256:" + m.SHA256
		if rule.Rule != "" {
			note += "; rule: " + rule.Rule
		}
		out = append(out, safeTier{What: "mask shown: " + masks.TypeLabel(m.Type) + " (" + m.Name + ")", Tier: m.Tier, MappingVersion: m.Mapping, Note: note})
	}
	return out
}

// carryTiers are the reviewer's lines for the masks a result carried from its source, the ones it left behind, and its recomputed
// no-value mask, read from the derived record's `maskCarry` (id-free).
func carryTiers(raw json.RawMessage) []safeTier {
	var c struct {
		By          string `json:"by"`
		FlagsFollow string `json:"flagsFollow"`
		Carried     []struct {
			appliedMask
			Fingerprint        string `json:"fingerprint"`
			CarriedFingerprint string `json:"carriedFingerprint"`
		} `json:"carried"`
		LeftBehind []struct {
			appliedMask
			Fingerprint string `json:"fingerprint"`
		} `json:"leftBehind"`
		Recomputed []string `json:"recomputed"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return nil
	}
	var out []safeTier
	for _, m := range c.Carried {
		out = append(out, safeTier{What: "mask carried into this result: " + masks.TypeLabel(m.Type) + " (" + m.Name + ")", Tier: m.Tier, MappingVersion: m.MappingVersion,
			Note: "cut or resampled with the data by " + c.By + "; " + c.FlagsFollow + "; source mask " + m.Fingerprint + ", carried copy " + m.CarriedFingerprint})
	}
	for _, m := range c.LeftBehind {
		out = append(out, safeTier{What: "mask left behind: " + masks.TypeLabel(m.Type) + " (" + m.Name + ")", Tier: m.Tier, MappingVersion: m.MappingVersion,
			Note: "a mask of the source that was not carried into this result, so its flags do not apply here; source mask " + m.Fingerprint})
	}
	for _, t := range c.Recomputed {
		if t == masks.Missing {
			out = append(out, safeTier{What: "mask: " + masks.TypeLabel(masks.Missing) + " (computed again)", Tier: catalog.TierVerified,
				Note: "computed from the values of this result, never carried from the source"})
		}
	}
	return out
}
