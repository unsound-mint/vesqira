package server

import (
	"fmt"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// North and east arrows on a saved figure (ADR 0047, amendment): the reviewer's lines say that they were drawn and which WCS state they rest on.

func compassOf(sc *scene.Scene) *scene.Compass {
	if sc == nil {
		return nil
	}
	return sc.Compass
}

// compassTier is the reviewer's tier line and statement for the arrows. The directions are the worker's reading of the file's WCS as the
// browser reported them: this server did not read the WCS again, so it does not call them verified.
func compassTier(c scene.Compass) (safeTier, string) {
	at := fmt.Sprintf("pixel (%.1f, %.1f), 0-based, y up", c.X, c.Y)
	frame := ""
	if c.Frame != "" {
		frame = " in the " + c.Frame + " frame"
	}
	state := "the file's own WCS"
	if len(c.Corrected) > 0 {
		state = "the file's WCS with the researcher's corrections (" + strings.Join(c.Corrected, ", ") + "): corrected"
	}
	note := fmt.Sprintf("north %.1f\u00b0 and east %.1f\u00b0 counter-clockwise from the +x pixel axis, at %s%s, from %s; the angles are the scientific worker's reading as reported by the author's browser", c.NorthDeg, c.EastDeg, at, frame, state)
	return safeTier{What: "north and east arrows (WCS state)", Tier: catalog.TierDeclared, Note: note},
		"North (N) and east (E) arrows are drawn at the reference pixel from " + state + "; a rotated, mirrored or flipped WCS turns them and north is not assumed up."
}
