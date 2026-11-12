package catalog

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Corrections (ADR 0061): a researcher's declared override of a unit the file states, or of specific WCS keywords, kept as an
// append-only history next to the dataset. The file and the header facts the worker read are never changed: the worker applies
// the state in force to a copy of the header, in memory, for each request. Every record carries the whole state in force after
// it, the reason, who and when, and the identity of the file it was made for; a record made for other bytes than the file has
// now is stale and its state is not applied anywhere until the researcher confirms it for the file as it is.

// Actions of a record.
const (
	CorrSet      = "set"      // keywords or the unit corrected (or one correction withdrawn: its value null)
	CorrRevert   = "revert"   // the state goes back to what it was before an earlier record
	CorrConfirm  = "confirm"  // the same state, confirmed for the file as it is now (after it changed)
	CorrImport   = "import"   // the state read from a sidecar file
	CorrWithdraw = "withdraw" // every correction withdrawn
)

// CorrectionEntry is one corrected value: what is in force, what the file says, and why.
type CorrectionEntry struct {
	Value  any       `json:"value"`  // a number or text (the unit, CTYPE, CUNIT, SPECSYS, RADESYS)
	File   any       `json:"file"`   // what the file says for it; null when the file has no such card
	Reason string    `json:"reason"` // the researcher's reason, required
	Source string    `json:"source"` // optional: where the value comes from (an observatory log, a DOI)
	At     time.Time `json:"at"`     // when it was set
}

// CorrectionState is every correction in force: the unit and the WCS keywords.
type CorrectionState struct {
	Unit *CorrectionEntry           `json:"unit,omitempty"`
	WCS  map[string]CorrectionEntry `json:"wcs,omitempty"`
}

// Empty reports whether nothing is corrected.
func (s CorrectionState) Empty() bool { return s.Unit == nil && len(s.WCS) == 0 }

// Values is the part of the state that changes what the data say: the unit and each keyword's value (not the reasons).
func (s CorrectionState) Values() (unit *string, wcs map[string]any) {
	if s.Unit != nil {
		if u, ok := s.Unit.Value.(string); ok {
			unit = &u
		}
	}
	wcs = map[string]any{}
	for k, e := range s.WCS {
		wcs[k] = e.Value
	}
	return unit, wcs
}

// Hash names the values of the state (SHA-256 of canonical JSON): two states with the same values give the same readings,
// whatever their reasons. The worker caches by it and echoes it; a figure records it.
func (s CorrectionState) Hash() string {
	if s.Empty() {
		return ""
	}
	u, w := s.Values()
	b, _ := json.Marshal(struct {
		Unit *string        `json:"unit"`
		WCS  map[string]any `json:"wcs"`
	}{u, w}) // encoding/json sorts map keys: canonical
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Keys lists the corrected keywords in order, "BUNIT" first for the unit.
func (s CorrectionState) Keys() []string {
	var out []string
	if s.Unit != nil {
		out = append(out, "BUNIT")
	}
	ks := make([]string, 0, len(s.WCS))
	for k := range s.WCS {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return append(out, ks...)
}

// CorrectionChange is what one record changed for one keyword ("BUNIT" for the unit): the file's value, the value in force
// before, the value in force after (null: not corrected, the file's own value applies).
type CorrectionChange struct {
	What   string `json:"what"`
	File   any    `json:"file"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// CorrectionRecord is one row of the history.
type CorrectionRecord struct {
	ID        int64              `json:"id"`
	Action    string             `json:"action"`
	State     CorrectionState    `json:"state"`
	Changes   []CorrectionChange `json:"changes"`
	Reason    string             `json:"reason"`
	Source    string             `json:"source"`
	Author    string             `json:"author"`
	Tier      string             `json:"tier"`
	At        time.Time          `json:"at"`
	RevertsID *int64             `json:"revertsId"`
	// The file it was made for: its registered size and modification time then, and its recorded content identity
	// (sha256:… or sampled:…, empty when none was recorded).
	SizeBytes      int64     `json:"sizeBytes"`
	ModifiedAt     time.Time `json:"modifiedAt"`
	SourceIdentity string    `json:"sourceIdentity"`
	// Stale: made for other bytes than the file has now.
	Stale bool `json:"stale"`
}

// Corrections is the state in force on a dataset, from its latest record. Stale: that record was made for other bytes than the
// file has now, so nothing of it is applied until the researcher confirms it (or withdraws it).
type Corrections struct {
	State    CorrectionState `json:"state"`
	Hash     string          `json:"hash"`
	Tier     string          `json:"tier"`
	RecordID int64           `json:"recordId"`
	At       time.Time       `json:"at"`
	Stale    bool            `json:"stale"`
}

// InForce returns the state that applies to the file as it is, or nil (no corrections, or stale ones).
func (c *Corrections) InForce() *CorrectionState {
	if c == nil || c.Stale || c.State.Empty() {
		return nil
	}
	return &c.State
}

var (
	ErrCorrectionConflict = errors.New("the corrections changed since you looked at them: look again before saving")
	ErrCorrectionStale    = errors.New("the corrections in force were made for other bytes than this file has now: confirm them for the file as it is, or withdraw them, first")
)

// MaxCorrectedKeys bounds the keywords one state corrects (the worker holds the same bound).
const MaxCorrectedKeys = 24

// MaxReason and MaxSource bound the texts of a record, which reach provenance and the reviewer's page.
const (
	MaxReason = 500
	MaxSource = 300
)

// ValidNote reports whether s can be stored as a reason or source: printable text of min to max characters, with no markup,
// quotes or backslashes (it is shown on the reviewer's page), no leading or trailing blank and no control characters.
func ValidNote(s string, min, max int) bool {
	n := utf8.RuneCountInString(s)
	if n < min || n > max || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || strings.ContainsRune("<>\"\\`", r) {
			return false
		}
	}
	return true
}

// correctable is the list of keywords a correction may set; the worker checks it again, with the axis count.
var correctable = regexp.MustCompile(`^(?:(?:CRVAL|CRPIX|CDELT|CTYPE|CUNIT)[1-9]|(?:CD|PC)[1-9]_[1-9]|RESTFRQ|RESTFREQ|SPECSYS|EQUINOX|RADESYS)$`)
var textKeyword = regexp.MustCompile(`^(?:CTYPE|CUNIT)[1-9]$|^(?:SPECSYS|RADESYS)$`)
var safeText = regexp.MustCompile(`^[A-Za-z0-9/*^.()+ -]{1,20}$`)

// CheckKeyword validates one keyword and value before the worker sees it: the keyword is correctable, a numeric keyword has a
// finite number and a text keyword short plain text. It returns the value in canonical form.
func CheckKeyword(k string, v any) (any, error) {
	if !correctable.MatchString(k) {
		return nil, errors.New(k + " is not a keyword that can be corrected (CRVAL, CRPIX, CDELT, CD, PC, CTYPE, CUNIT, RESTFRQ, RESTFREQ, SPECSYS, EQUINOX, RADESYS)")
	}
	if textKeyword.MatchString(k) {
		s, ok := v.(string)
		if !ok || !safeText.MatchString(s) {
			return nil, errors.New(k + " must be short plain text")
		}
		return s, nil
	}
	f, ok := v.(float64)
	if !ok || f != f || f > 1e300 || f < -1e300 {
		return nil, errors.New(k + " must be a finite number")
	}
	return f, nil
}

const corrCols = `id,action,state,changes,reason,source,author,tier,created_at,size_bytes,modified_ns,source_identity,reverts_id`

func scanCorrection(row interface{ Scan(...any) error }) (CorrectionRecord, error) {
	var r CorrectionRecord
	var state, changes string
	var at, mod int64
	var rev sql.NullInt64
	if err := row.Scan(&r.ID, &r.Action, &state, &changes, &r.Reason, &r.Source, &r.Author, &r.Tier, &at, &r.SizeBytes, &mod, &r.SourceIdentity, &rev); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(state), &r.State); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(changes), &r.Changes); err != nil {
		return r, err
	}
	if r.Changes == nil {
		r.Changes = []CorrectionChange{}
	}
	if rev.Valid {
		r.RevertsID = &rev.Int64
	}
	r.At, r.ModifiedAt = time.Unix(0, at).UTC(), time.Unix(0, mod).UTC()
	return r, nil
}

// staleFor says whether a record was made for other bytes than the dataset's registered file has now: its size or modification
// time differ, or both sides have a content identity of the same kind and they differ.
func staleFor(r CorrectionRecord, d Dataset, identity string) bool {
	if r.SizeBytes != d.SizeBytes || r.ModifiedAt.UnixNano() != d.ModifiedAt.UnixNano() {
		return true
	}
	if r.SourceIdentity != "" && identity != "" {
		a, _, _ := strings.Cut(r.SourceIdentity, ":")
		b, _, _ := strings.Cut(identity, ":")
		return a == b && r.SourceIdentity != identity
	}
	return false
}

// ContentIdentity is what is recorded about the bytes of the dataset's registered file, never more (ADR 0053): sha256:<hex>,
// sampled:<fingerprint>, or empty.
func ContentIdentity(locs []Location) string {
	for _, l := range locs {
		if !l.Primary {
			continue
		}
		switch {
		case l.Full != nil:
			return "sha256:" + l.Full.Value
		case l.Sampled != nil:
			return "sampled:" + l.Sampled.Value
		}
	}
	return ""
}

func (s *Store) corrections(d Dataset) (*Corrections, error) {
	r, err := scanCorrection(s.db.QueryRow(`SELECT `+corrCols+` FROM dataset_corrections WHERE dataset_id=? ORDER BY id DESC LIMIT 1`, d.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.State.Empty() {
		return nil, nil
	}
	return &Corrections{State: r.State, Hash: r.State.Hash(), Tier: r.Tier, RecordID: r.ID, At: r.At, Stale: staleFor(r, d, ContentIdentity(d.Locations))}, nil
}

// CorrectionHistory lists every record of a dataset, oldest first, each marked stale when it was made for other bytes.
func (s *Store) CorrectionHistory(d Dataset) ([]CorrectionRecord, error) {
	rows, err := s.db.Query(`SELECT `+corrCols+` FROM dataset_corrections WHERE dataset_id=? ORDER BY id`, d.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CorrectionRecord{}
	id := ContentIdentity(d.Locations)
	for rows.Next() {
		r, err := scanCorrection(rows)
		if err != nil {
			return nil, err
		}
		r.Stale = staleFor(r, d, id)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Diff lists what changes from one state to another, keyword by keyword ("BUNIT" for the unit). file gives the file's value of
// a keyword that neither state carries (nil when unknown).
func Diff(before, after CorrectionState) []CorrectionChange {
	val := func(s CorrectionState, k string) (any, any, bool) {
		if k == "BUNIT" {
			if s.Unit == nil {
				return nil, nil, false
			}
			return s.Unit.Value, s.Unit.File, true
		}
		e, ok := s.WCS[k]
		return e.Value, e.File, ok
	}
	keys := map[string]bool{}
	for _, k := range append(before.Keys(), after.Keys()...) {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if (ordered[i] == "BUNIT") != (ordered[j] == "BUNIT") {
			return ordered[i] == "BUNIT"
		}
		return ordered[i] < ordered[j]
	})
	out := []CorrectionChange{}
	for _, k := range ordered {
		bv, bf, bok := val(before, k)
		av, af, aok := val(after, k)
		if bok == aok && bv == av {
			continue
		}
		file := af
		if !aok {
			file = bf
		}
		out = append(out, CorrectionChange{What: k, File: file, Before: bv, After: av})
	}
	return out
}

// AppendCorrection adds a record to a dataset's history, in one transaction with a check that the latest record is still the one
// the caller built on (expectLast, 0 for none): two windows saving at once cannot silently overwrite each other's state.
func (s *Store) AppendCorrection(d Dataset, r CorrectionRecord, expectLast int64) (CorrectionRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	var last int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM dataset_corrections WHERE dataset_id=?`, d.ID).Scan(&last); err != nil {
		return r, err
	}
	if last != expectLast {
		return r, ErrCorrectionConflict
	}
	state, err := json.Marshal(r.State)
	if err != nil {
		return r, err
	}
	if r.Changes == nil {
		r.Changes = []CorrectionChange{}
	}
	changes, _ := json.Marshal(r.Changes)
	if r.Author == "" {
		r.Author = "user"
	}
	r.Tier, _ = TierOf(r.Author)
	r.At = time.Now().UTC()
	r.SizeBytes, r.ModifiedAt, r.SourceIdentity = d.SizeBytes, d.ModifiedAt, ContentIdentity(d.Locations)
	var rev any
	if r.RevertsID != nil {
		rev = *r.RevertsID
	}
	res, err := tx.Exec(`INSERT INTO dataset_corrections(dataset_id,action,state,changes,reason,source,author,tier,created_at,size_bytes,modified_ns,source_identity,reverts_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, r.Action, string(state), string(changes), r.Reason, r.Source, r.Author, r.Tier, r.At.UnixNano(),
		r.SizeBytes, r.ModifiedAt.UnixNano(), r.SourceIdentity, rev)
	if err != nil {
		return r, err
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// CorrectionRecordOf returns one record of a dataset and the state in force just before it (empty for the first).
func (s *Store) CorrectionRecordOf(d Dataset, id int64) (rec CorrectionRecord, before CorrectionState, err error) {
	rec, err = scanCorrection(s.db.QueryRow(`SELECT `+corrCols+` FROM dataset_corrections WHERE dataset_id=? AND id=?`, d.ID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return rec, before, ErrNotFound
	}
	if err != nil {
		return rec, before, err
	}
	prev, err := scanCorrection(s.db.QueryRow(`SELECT `+corrCols+` FROM dataset_corrections WHERE dataset_id=? AND id<? ORDER BY id DESC LIMIT 1`, d.ID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return rec, CorrectionState{}, nil
	}
	return rec, prev.State, err
}
