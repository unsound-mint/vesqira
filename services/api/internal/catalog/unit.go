package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// UnitDeclaration is the physical unit a person declared for a file whose header has no BUNIT. It is an overlay: the
// file and the header facts the worker read are never changed, the declaration says who made it, and it is shown
// wherever the unit is shown as declared by the person, not by the file. A file that states its own BUNIT cannot be given
// one: that is a correction of what the file says, kept with its reason and history in corrections.go (ADR 0061).
type UnitDeclaration struct {
	Unit       string    `json:"unit"`
	DeclaredBy string    `json:"declaredBy"` // "user", or the rule that proposed it (the rohsa run adapter)
	DeclaredAt time.Time `json:"declaredAt"`
	// Tier and MappingVersion say how far the unit is established (ADR 0055): declared by the researcher, or inferred by a rule.
	Tier           string `json:"tier"`
	MappingVersion string `json:"mappingVersion"`
	// Stale is true when the file's registered size or modification time differs from what it was at the declaration:
	// the declaration may no longer describe these bytes.
	Stale bool `json:"stale"`
}

// ErrNotBrightness is returned for a dataset that is an orientation map, or the reliability map of one: its values are angles
// or a dimensionless reliability, so a brightness unit declared for it would be wrong, not merely unneeded.
var ErrNotBrightness = errors.New("this is an orientation map (or the reliability map of one): its values are not a brightness, so no brightness unit can be declared for it")

// ErrUnitInFile is returned when the file's header already states its unit.
var ErrUnitInFile = errors.New("the file states its own unit (BUNIT); a declared unit only fills a missing one (to replace the file's unit, correct it with a reason under Corrections)")

// MaxUnit is the longest unit text accepted.
const MaxUnit = 40

// ValidUnit reports whether s can be stored as a unit: 1 to MaxUnit characters of letters, numbers (superscripts such as ² count),
// a blank and the marks units use ( / * ^ . + - ( ) _ , % · ° ), with no leading, trailing or repeated blank. Markup, quotes and backslashes are not
// units and the text ends up in provenance and on the reviewer's page, so they are refused. It does not decide whether the
// text is a standard unit; it is kept exactly as typed and compared as text.
func ValidUnit(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > MaxUnit || s != strings.TrimSpace(s) || strings.Contains(s, "  ") {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune(" /*^.+-()_,%·°", r) {
			return false
		}
	}
	return true
}

func (s *Store) unitDeclaration(id int64, d Dataset) (*UnitDeclaration, error) {
	var u UnitDeclaration
	var at, size, mod int64
	err := s.db.QueryRow(`SELECT unit,declared_by,declared_at,size_bytes,modified_ns,tier,mapping_version FROM dataset_units WHERE dataset_id=?`, id).Scan(&u.Unit, &u.DeclaredBy, &at, &size, &mod, &u.Tier, &u.MappingVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.DeclaredAt = time.Unix(at, 0).UTC()
	u.Stale = size != d.SizeBytes || mod != d.ModifiedAt.UnixNano()
	return &u, nil
}

// fileStatesItsUnit reports whether the header the worker read has a BUNIT: its status says so, or a unit is there. Either is
// enough, so a header that lacks one of the two fields still cannot be given a conflicting declaration.
func fileStatesItsUnit(d Dataset) bool {
	var h struct {
		Status string  `json:"bunit_status"`
		Bunit  *string `json:"bunit"`
	}
	_ = json.Unmarshal(d.Header, &h)
	return h.Status == "declared" || h.Bunit != nil
}

// SetUnitDeclaration records the unit a person declared for d. The unit is validated and kept as typed.
func (s *Store) SetUnitDeclaration(d Dataset, unit, by string) error {
	_, err := s.SetUnitDeclarations([]Dataset{d}, unit, by)
	if err != nil {
		return err
	}
	// a single file asked for by name: a refusal is an error, not a skip
	if fileStatesItsUnit(d) {
		return ErrUnitInFile
	}
	if bad, err := s.noBrightness(d.ID); err != nil {
		return err
	} else if bad {
		return ErrNotBrightness
	}
	return nil
}

// SetUnitDeclarations declares the same unit for several datasets in one transaction (all or none) and returns the ones it
// applied. A dataset whose file states its own unit, or that is an orientation or reliability map, is skipped, not an error:
// the caller asked about a folder, not about each file.
func (s *Store) SetUnitDeclarations(ds []Dataset, unit, by string) (applied []Dataset, err error) {
	if !ValidUnit(unit) {
		return nil, ErrInvalidUnit
	}
	if by == "" {
		by = "user"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, d := range ds {
		if fileStatesItsUnit(d) || !d.HasPixelGrid() { // a point catalog or a HEALPix map has no brightness: its columns carry their own units
			continue
		}
		var bad int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM interpretations WHERE dataset_id=?1) + (SELECT COUNT(*) FROM interpretations WHERE reliability_id=?1)`, d.ID).Scan(&bad); err != nil {
			return nil, err
		}
		if bad > 0 {
			continue
		}
		tier, mapping := TierOf(by)
		if _, err := tx.Exec(`INSERT INTO dataset_units(dataset_id,unit,declared_by,declared_at,size_bytes,modified_ns,tier,mapping_version) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(dataset_id) DO UPDATE SET unit=excluded.unit,declared_by=excluded.declared_by,declared_at=excluded.declared_at,
				size_bytes=excluded.size_bytes,modified_ns=excluded.modified_ns,tier=excluded.tier,mapping_version=excluded.mapping_version`,
			d.ID, unit, by, time.Now().Unix(), d.SizeBytes, d.ModifiedAt.UnixNano(), tier, mapping); err != nil {
			return nil, err
		}
		applied = append(applied, d)
	}
	return applied, tx.Commit()
}

func (s *Store) noBrightness(id int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM interpretations WHERE dataset_id=?1) + (SELECT COUNT(*) FROM interpretations WHERE reliability_id=?1)`, id).Scan(&n)
	return n > 0, err
}

// NoBrightnessIDs returns the datasets that cannot be given a brightness unit: orientation maps (proposed or confirmed), the
// reliability maps declared for them, and point catalogs.
func (s *Store) NoBrightnessIDs(ws workspace.ID) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT i.dataset_id FROM interpretations i JOIN datasets d ON d.id=i.dataset_id WHERE d.workspace_id=?1
		UNION SELECT i.reliability_id FROM interpretations i JOIN datasets d ON d.id=i.dataset_id WHERE i.reliability_id IS NOT NULL AND d.workspace_id=?1
		UNION SELECT id FROM datasets WHERE workspace_id=?1 AND header LIKE '%"kind":"catalog"%'`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ErrInvalidUnit is returned for unit text that ValidUnit refuses.
var ErrInvalidUnit = errors.New("a unit is 1 to 40 printable characters with no leading, trailing or repeated blank")

// ClearUnitDeclaration withdraws a declared unit.
func (s *Store) ClearUnitDeclaration(id int64) error {
	_, err := s.db.Exec(`DELETE FROM dataset_units WHERE dataset_id=?`, id)
	return err
}

// FreshUnits returns the declared unit of every dataset whose declaration still describes the file as registered (a stale
// declaration, made about other bytes, is not applied anywhere and does not settle a question).
func (s *Store) FreshUnits(ws workspace.ID) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT u.dataset_id, u.unit FROM dataset_units u JOIN datasets d ON d.id=u.dataset_id
		WHERE d.workspace_id=? AND u.size_bytes=d.size_bytes AND u.modified_ns=d.modified_at`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var u string
		if err := rows.Scan(&id, &u); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}
