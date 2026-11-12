// Package masks keeps typed masks (ADR 0055): derived FITS files that flag pixels of a dataset for a stated reason, with the
// rule, the author and the identity of what they were made from. A mask never changes the dataset it describes and is never
// applied by itself: an operation names the masks it takes and what to do with each type (exclude, keep, weight).
//
// Pixels with no value (NaN, BLANK) are `missing`, computed and never declared. Zero is a value, and is flagged by no mask unless
// a stated rule says so.
package masks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// Types of mask. Each means one thing; no type stands for "good" or "bad" in general.
const (
	Missing      = "missing"       // no value in the file (NaN, BLANK); computed from the data, never declared
	Invalid      = "invalid"       // a pixel known to be bad (declared or imported)
	LowSNR       = "low_snr"       // value / sigma below a stated threshold, sigma from a stated source
	Saturated    = "saturated"     // value at or above a stated threshold, with the source of the threshold
	UserExcluded = "user_excluded" // pixels the researcher chose to leave out (regions, or a mask file they provided)
	Other        = "other"         // anything else, with a required description
)

// Methods: how the mask was made.
const (
	MethodComputed = "computed" // by the worker from the data and a stated rule
	MethodRegions  = "regions"  // from saved regions, by the regions' pixel rule
	MethodImport   = "import"   // from a mask file the researcher provided
	MethodCarried  = "carried"  // cut, resampled or smoothed with the data it describes, by a cutout, slab, subset, align or convolve (ADR 0055)
)

// Policies an operation applies per mask. `weight` is accepted only by an operation that already has weights.
const (
	Exclude = "exclude"
	Keep    = "keep"
	Weight  = "weight"
)

var (
	ErrNotFound = errors.New("mask not found")
	// ErrInvalid is a malformed request (an unknown type, a missing description).
	ErrInvalid = errors.New("invalid")
)

// ValidType reports whether t is a mask type.
func ValidType(t string) bool {
	switch t {
	case Missing, Invalid, LowSNR, Saturated, UserExcluded, Other:
		return true
	}
	return false
}

// TypeLabel is the plain name of a type, for sentences.
func TypeLabel(t string) string {
	switch t {
	case Missing:
		return "no value"
	case Invalid:
		return "invalid"
	case LowSNR:
		return "low signal-to-noise"
	case Saturated:
		return "saturated"
	case UserExcluded:
		return "excluded by the researcher"
	}
	return "other"
}

// Mask is one stored mask. Rule is the worker's description of how it was made (estimator, threshold, source of sigma, the
// regions, the file imported): JSON, kept as the worker wrote it.
type Mask struct {
	ID        int64           `json:"id"`
	DatasetID int64           `json:"datasetId"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Path      string          `json:"-"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Channels  int             `json:"channels"` // 0 for a mask of one plane, which applies to every channel; else one plane per channel
	Flagged   int64           `json:"flagged"`
	Total     int64           `json:"total"`
	Tier      string          `json:"tier"`
	Mapping   string          `json:"mappingVersion"`
	Method    string          `json:"method"`
	Rule      json.RawMessage `json:"rule"`
	Meaning   string          `json:"meaning"`
	Author    string          `json:"author"`
	CreatedAt time.Time       `json:"createdAt"`
	// What it was made from, as registered then: a mask of other bytes is stale and is not applied.
	SourceSize     int64  `json:"-"`
	SourceMtime    int64  `json:"-"`
	SourceIdentity string `json:"sourceIdentity"`
	// The mask file's own identity.
	FileSize  int64  `json:"fileSize"`
	FileMtime int64  `json:"-"`
	SHA256    string `json:"sha256"`
}

// SourceDatasetKey is the key in an imported mask's rule that holds the id of the registered file it was made from. It lets the
// catalog tell a dataset that only serves as a mask file from real data without reading file names.
const SourceDatasetKey = "sourceDatasetId"

// SourceDatasetID is the registered file an imported mask was made from, or 0 when the row does not say (a mask made before the
// id was recorded names only the file's base name in its rule, which identifies nothing).
func (m Mask) SourceDatasetID() int64 {
	var r map[string]json.RawMessage
	if m.Method != MethodImport || json.Unmarshal(m.Rule, &r) != nil {
		return 0
	}
	var id int64
	if json.Unmarshal(r[SourceDatasetKey], &id) != nil || id < 0 {
		return 0
	}
	return id
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.MasksTable); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const cols = `id,dataset_id,type,name,path,width,height,channels,flagged,total,tier,mapping_version,method,rule,meaning,author,created_at,source_size,source_mtime,source_identity,file_size,file_mtime,sha256`

func scan(r interface{ Scan(...any) error }) (Mask, error) {
	var m Mask
	var rule string
	var at int64
	err := r.Scan(&m.ID, &m.DatasetID, &m.Type, &m.Name, &m.Path, &m.Width, &m.Height, &m.Channels, &m.Flagged, &m.Total, &m.Tier, &m.Mapping, &m.Method,
		&rule, &m.Meaning, &m.Author, &at, &m.SourceSize, &m.SourceMtime, &m.SourceIdentity, &m.FileSize, &m.FileMtime, &m.SHA256)
	m.Rule = json.RawMessage(rule)
	m.CreatedAt = time.Unix(at, 0).UTC()
	return m, err
}

// Add stores a mask whose file is already written.
func (s *Store) Add(m Mask) (Mask, error) {
	if !ValidType(m.Type) {
		return Mask{}, fmt.Errorf("%w: unknown mask type %q", ErrInvalid, m.Type)
	}
	if m.Rule == nil {
		m.Rule = json.RawMessage(`{}`)
	}
	res, err := s.db.Exec(`INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,mapping_version,method,rule,meaning,author,created_at,source_size,source_mtime,source_identity,file_size,file_mtime,sha256)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.DatasetID, m.Type, m.Name, m.Path, m.Width, m.Height, m.Channels, m.Flagged, m.Total, m.Tier, m.Mapping, m.Method, string(m.Rule), m.Meaning, m.Author,
		time.Now().Unix(), m.SourceSize, m.SourceMtime, m.SourceIdentity, m.FileSize, m.FileMtime, m.SHA256)
	if err != nil {
		return Mask{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (Mask, error) {
	m, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM masks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Mask{}, ErrNotFound
	}
	return m, err
}

// ForDataset lists a dataset's masks, oldest first.
func (s *Store) ForDataset(datasetID int64) ([]Mask, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM masks WHERE dataset_id=? ORDER BY id`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mask{}
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Delete forgets a mask. Its file is left where it is: Vesqira never deletes a data file (ADR 0041).
func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec(`DELETE FROM masks WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
