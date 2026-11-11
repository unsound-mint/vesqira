package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// What a figure records about the declarations it was drawn under (ADR 0042, section 5). A declared unit, an orientation's
// angle frame and quantity, a catalog's columns and frame are the owner's statements about a file; the file says none of them.
// Drawing again reads the declarations in force now, so without this record a changed declaration would silently change what
// the figure means. Owner-side, in figures.identity (additive; figures saved before it have none and say so).

type declUnit struct {
	Unit       string `json:"unit"`
	DeclaredBy string `json:"declaredBy"`
}

type declOrientation struct {
	Kind          string  `json:"kind"`
	Unit          string  `json:"unit"`
	Frame         *string `json:"frame"`
	Quantity      *string `json:"quantity"`
	ReliabilityID *int64  `json:"reliabilityId"`
	DeclaredBy    string  `json:"declaredBy"`
}

type declCatalog struct {
	// Hash is the SHA-256 of the declaration's content (columns, format, frame, equinox, angle, value columns); the time it was
	// made and its staleness flag are not part of it.
	Hash       string `json:"hash"`
	DeclaredBy string `json:"declaredBy"`
}

// figureDeclarations: a non-nil value on a new figure even when a dataset declares nothing ("recorded: none"), so that a
// declaration added later is told apart from a figure that recorded nothing at all.
type figureDeclarations struct {
	Unit        *declUnit        `json:"unit,omitempty"`
	Orientation *declOrientation `json:"orientation,omitempty"`
	Catalog     *declCatalog     `json:"catalog,omitempty"`
}

func (r *figureDeclarations) empty() bool {
	return r == nil || (r.Unit == nil && r.Orientation == nil && r.Catalog == nil)
}

func declarationsOf(d catalog.Dataset) *figureDeclarations {
	out := &figureDeclarations{}
	if u := d.UnitDeclaration; u != nil {
		out.Unit = &declUnit{Unit: u.Unit, DeclaredBy: u.DeclaredBy}
	}
	if o := d.Interpretation; o != nil {
		out.Orientation = &declOrientation{Kind: o.Kind, Unit: o.Unit, Frame: o.Frame, Quantity: o.Quantity, ReliabilityID: o.ReliabilityID, DeclaredBy: o.DeclaredBy}
	}
	if c := d.CatalogDeclaration; c != nil {
		b, _ := json.Marshal(struct {
			Lon, Lat, Format, Frame string
			Equinox                 *string
			Angle                   *catalog.CatalogAngle
			Values                  []string
		}{c.Lon, c.Lat, c.Format, c.Frame, c.Equinox, c.Angle, c.Values})
		h := sha256.Sum256(b)
		out.Catalog = &declCatalog{Hash: hex.EncodeToString(h[:]), DeclaredBy: c.DeclaredBy}
	}
	return out
}

// Declaration statuses of one source.
const (
	declUnchanged   = "unchanged"
	declChangedSt   = "changed"
	declRemoved     = "removed"
	declNotRecorded = "not_recorded"
)

const (
	textDeclUnchanged   = "declaration unchanged"
	textDeclRemoved     = "declaration removed"
	textDeclNotRecorded = "declarations at save time were not recorded"
)

func declBy(who string) string {
	if who == "user" || who == "" {
		return "declared by you"
	}
	return "declared by " + who
}

func optText(p *string) string {
	if p == nil {
		return "not declared"
	}
	return *p
}

func declUnitText(u *declUnit) string {
	if u == nil {
		return "not declared"
	}
	return fmt.Sprintf("%s (%s)", u.Unit, declBy(u.DeclaredBy))
}

func orientationText(o *declOrientation) string {
	if o == nil {
		return "not declared"
	}
	rel := "none"
	if o.ReliabilityID != nil {
		rel = fmt.Sprintf("dataset %d", *o.ReliabilityID)
	}
	return fmt.Sprintf("%s in %s, frame %s, quantity %s, reliability %s (%s)", o.Kind, o.Unit, optText(o.Frame), optText(o.Quantity), rel, declBy(o.DeclaredBy))
}

func catalogText(c *declCatalog) string {
	if c == nil {
		return "not declared"
	}
	return fmt.Sprintf("%s (%s)", c.Hash[:8], declBy(c.DeclaredBy))
}

func sameStr(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

// compareDeclarations says whether the declarations in force now are the ones the figure was drawn under (rec nil: it recorded
// none). Exact sentences are pinned by tests; the web client shows them as they are.
func compareDeclarations(rec, cur *figureDeclarations) (status, text string) {
	if rec == nil {
		return declNotRecorded, textDeclNotRecorded
	}
	var parts []string
	if (rec.Unit == nil) != (cur.Unit == nil) || (rec.Unit != nil && *rec.Unit != *cur.Unit) {
		parts = append(parts, fmt.Sprintf("unit was %s, now %s", declUnitText(rec.Unit), declUnitText(cur.Unit)))
	}
	ro, co := rec.Orientation, cur.Orientation
	if (ro == nil) != (co == nil) || (ro != nil && (ro.Kind != co.Kind || ro.Unit != co.Unit || !sameStr(ro.Frame, co.Frame) ||
		!sameStr(ro.Quantity, co.Quantity) || ro.DeclaredBy != co.DeclaredBy || (ro.ReliabilityID == nil) != (co.ReliabilityID == nil) ||
		(ro.ReliabilityID != nil && *ro.ReliabilityID != *co.ReliabilityID))) {
		parts = append(parts, fmt.Sprintf("orientation was %s, now %s", orientationText(ro), orientationText(co)))
	}
	if (rec.Catalog == nil) != (cur.Catalog == nil) || (rec.Catalog != nil && *rec.Catalog != *cur.Catalog) {
		parts = append(parts, fmt.Sprintf("catalog declaration was %s, now %s", catalogText(rec.Catalog), catalogText(cur.Catalog)))
	}
	switch {
	case len(parts) == 0:
		return declUnchanged, textDeclUnchanged
	case cur.empty():
		return declRemoved, textDeclRemoved
	}
	return declChangedSt, "declaration changed: " + strings.Join(parts, "; ")
}
