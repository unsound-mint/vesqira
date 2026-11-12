package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Where a location came from.
const (
	LocRegistered = "registered" // the file the dataset was registered from
	LocManaged    = "managed"    // a byte-for-byte copy under the data directory
	LocDownloaded = "downloaded" // a copy fetched from a host, checked at both ends (transfers)
	LocCopy       = "copy"       // another file the researcher added
)

// Why a location belongs to its dataset (ADR 0053). Only LinkVerified is proof of the same bytes.
const (
	LinkRegistered = "registered" // the primary: no claim is made
	LinkVerified   = "verified"   // the full SHA-256 values are equal
	LinkProbable   = "probable"   // same size and sampled fingerprint: probably the same, NOT verified
	LinkDeclared   = "declared"   // the researcher says it is the same; nothing was proven
	LinkStale      = "stale"      // was verified or probable against an earlier version of the registered file, which has changed since
)

var (
	ErrLocationExists = errors.New("this file is already a location of a dataset")
	ErrPrimary        = errors.New("the registered file of a dataset cannot be removed as a location")
	ErrStale          = errors.New("the file changed while its fingerprint was computed")
	ErrNoLocation     = errors.New("location not found")
)

// Stamp is a computed value with when and by what it was computed (for example "api" or "worker on <host>").
type Stamp struct {
	Value string    `json:"value"`
	At    time.Time `json:"at"`
	By    string    `json:"by"`
}

// Location is one place a dataset's bytes are. The primary location is the file the dataset was registered from.
type Location struct {
	ID         int64     `json:"id"`
	HostID     int64     `json:"hostId"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Primary    bool      `json:"primary"`
	Link       string    `json:"link"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
	AddedAt    time.Time `json:"addedAt"`
	// Sampled is the sampled fingerprint (identity.SampledAlgorithm): what it covers is identity.Covers; equal values do
	// not prove equal bytes. Full is the SHA-256 of every byte. nil means not computed.
	Sampled *Stamp `json:"sampledFingerprint"`
	Full    *Stamp `json:"fullSha256"`
	// FileDev and FileIno are the device and inode of a file on this computer when it was recorded (0: not recorded, and always 0
	// for a file on a host). They are compared when the file is used again, never shown or sent to a client.
	FileDev int64 `json:"-"`
	FileIno int64 `json:"-"`
}

// Evidence is what is known about this location's bytes.
func (l Location) Evidence() identity.Evidence {
	e := identity.Evidence{Size: l.SizeBytes}
	if l.Sampled != nil {
		e.Sampled = l.Sampled.Value
	}
	if l.Full != nil {
		e.Full = l.Full.Value
	}
	return e
}

const locCols = `id,dataset_id,host_id,path,kind,is_primary,link,size_bytes,modified_at,added_at,sampled_fp,sampled_at,sampled_by,full_sha256,full_at,full_by,file_dev,file_ino`

func scanLocation(row interface{ Scan(...any) error }) (int64, Location, error) {
	var l Location
	var dsID, mod, add, sAt, fAt int64
	var prim int
	var sFP, sBy, fSHA, fBy string
	if err := row.Scan(&l.ID, &dsID, &l.HostID, &l.Path, &l.Kind, &prim, &l.Link, &l.SizeBytes, &mod, &add, &sFP, &sAt, &sBy, &fSHA, &fAt, &fBy, &l.FileDev, &l.FileIno); err != nil {
		return 0, l, err
	}
	l.Primary = prim == 1
	l.ModifiedAt, l.AddedAt = time.Unix(0, mod).UTC(), time.Unix(add, 0).UTC()
	if sFP != "" {
		l.Sampled = &Stamp{sFP, time.Unix(sAt, 0).UTC(), sBy}
	}
	if fSHA != "" {
		l.Full = &Stamp{fSHA, time.Unix(fAt, 0).UTC(), fBy}
	}
	return dsID, l, nil
}

// Locations lists a dataset's locations, the registered file first.
func (s *Store) Locations(ws workspace.ID, datasetID int64) ([]Location, error) {
	rows, err := s.db.Query(`SELECT `+locCols+` FROM dataset_locations WHERE workspace_id=? AND dataset_id=? ORDER BY is_primary DESC, id`, ws, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		_, l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// allLocations is every dataset's locations in one query, for List.
func (s *Store) allLocations(ws workspace.ID) (map[int64][]Location, error) {
	rows, err := s.db.Query(`SELECT `+locCols+` FROM dataset_locations WHERE workspace_id=? ORDER BY is_primary DESC, id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Location{}
	for rows.Next() {
		id, l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out[id] = append(out[id], l)
	}
	return out, rows.Err()
}

// Location returns one location of a dataset.
func (s *Store) Location(ws workspace.ID, datasetID, locID int64) (Location, error) {
	_, l, err := scanLocation(s.db.QueryRow(`SELECT `+locCols+` FROM dataset_locations WHERE id=? AND dataset_id=? AND workspace_id=?`, locID, datasetID, ws))
	if errors.Is(err, sql.ErrNoRows) {
		return Location{}, ErrNoLocation
	}
	return l, err
}

// upsertPrimary keeps the primary location in step with the dataset row. When the file's size or modification time changed,
// the fingerprints stored for the old bytes are dropped: a hash must never stay attached to bytes it was not computed from.
func (s *Store) upsertPrimary(ws workspace.ID, datasetID int64, d Dataset) error {
	var oldSize, oldMod, oldDev, oldIno int64 // oldDev is read for completeness; only the inode tells files apart
	changed := false
	switch err := s.db.QueryRow(`SELECT size_bytes,modified_at,file_dev,file_ino FROM dataset_locations WHERE workspace_id=? AND host_id=? AND path=?`, ws, d.HostID, d.Path).Scan(&oldSize, &oldMod, &oldDev, &oldIno); {
	case err == nil:
		// another file under the same name counts as a change even when its size and time happen to be equal
		changed = oldSize != d.SizeBytes || oldMod != d.ModifiedAt.UnixNano() || (oldIno != 0 && d.FileIno != 0 && oldIno != d.FileIno) // a new device alone is a remount
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if changed {
		// what the other locations were compared with is gone: they were verified or probable against the earlier file, and
		// are not read from until they are compared with the file as it is now (the server does that once it has its hashes)
		if _, err := s.db.Exec(`UPDATE dataset_locations SET link=? WHERE workspace_id=? AND dataset_id=? AND is_primary=0 AND link IN (?,?)`, LinkStale, ws, datasetID, LinkVerified, LinkProbable); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`INSERT INTO dataset_locations(dataset_id,host_id,path,kind,is_primary,link,size_bytes,modified_at,added_at,file_dev,file_ino,workspace_id) VALUES(?,?,?,?,1,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id,host_id,path) DO UPDATE SET
			sampled_fp=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN sampled_fp ELSE '' END,
			sampled_at=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN sampled_at ELSE 0 END,
			sampled_by=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN sampled_by ELSE '' END,
			full_sha256=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN full_sha256 ELSE '' END,
			full_at=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN full_at ELSE 0 END,
			full_by=CASE WHEN size_bytes=excluded.size_bytes AND modified_at=excluded.modified_at AND (file_ino=0 OR excluded.file_ino=0 OR file_ino=excluded.file_ino) THEN full_by ELSE '' END,
			size_bytes=excluded.size_bytes, modified_at=excluded.modified_at,
			file_dev=CASE WHEN excluded.file_ino=0 THEN file_dev ELSE excluded.file_dev END, file_ino=CASE WHEN excluded.file_ino=0 THEN file_ino ELSE excluded.file_ino END`,
		datasetID, d.HostID, d.Path, LocRegistered, LinkRegistered, d.SizeBytes, d.ModifiedAt.UnixNano(), time.Now().Unix(), d.FileDev, d.FileIno, ws)
	return err
}

// NoteFileID records the device and inode of a location's file, for a caller that has just checked it against the recorded size,
// modification time and inode: it fills them in for a location made before schema 27, and records the new device number of a drive
// mounted again (same inode). It never replaces a recorded inode: a different file is reported to the person, who registers it
// again on purpose.
func (s *Store) NoteFileID(ws workspace.ID, hostID int64, path string, dev, ino int64) error {
	if ino == 0 || hostID != 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE dataset_locations SET file_dev=?, file_ino=? WHERE workspace_id=? AND host_id=0 AND path=? AND (file_ino=0 OR (file_ino=? AND file_dev<>?))`, dev, ino, ws, path, ino, dev)
	return err
}

// SetLink changes why a non-primary location belongs to its dataset (the server re-compares a stale one once the registered
// file has its fingerprints again).
func (s *Store) SetLink(ws workspace.ID, locID int64, link string) error {
	switch link {
	case LinkVerified, LinkProbable, LinkDeclared, LinkStale:
	default:
		return fmt.Errorf("catalog: bad link %q", link)
	}
	_, err := s.db.Exec(`UPDATE dataset_locations SET link=? WHERE id=? AND is_primary=0 AND workspace_id=?`, link, locID, ws)
	return err
}

// SetFingerprint records a computed value on a location. size and modNS are the file's size and modification time as seen
// when the value was computed; a location that no longer has them (it was registered again meanwhile) refuses the value.
// kind is "sampled" or "full"; by says what computed it.
func (s *Store) SetFingerprint(ws workspace.ID, locID int64, kind, value, by string, size, modNS int64) error {
	col := map[string]string{"sampled": "sampled", "full": "full"}[kind]
	if col == "" || value == "" {
		return fmt.Errorf("catalog: bad fingerprint kind %q", kind)
	}
	valueCol := map[string]string{"sampled": "sampled_fp", "full": "full_sha256"}[kind]
	res, err := s.db.Exec(fmt.Sprintf(`UPDATE dataset_locations SET %s=?, %s_at=?, %s_by=? WHERE id=? AND size_bytes=? AND modified_at=? AND workspace_id=?`, valueCol, col, col),
		value, time.Now().Unix(), by, locID, size, modNS, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrStale
	}
	return nil
}

// AddLocation links another file to a dataset. link is what established the identity (LinkVerified, LinkProbable or
// LinkDeclared); the caller has checked that. A (host, path) that already is a location of any dataset is refused with
// ErrLocationExists.
func (s *Store) AddLocation(ws workspace.ID, datasetID int64, l Location) (Location, error) {
	if l.Kind == "" {
		l.Kind = LocCopy
	}
	switch l.Link {
	case LinkVerified, LinkProbable, LinkDeclared:
	default:
		return Location{}, fmt.Errorf("catalog: a location needs a link, got %q", l.Link)
	}
	res, err := s.db.Exec(`INSERT INTO dataset_locations(dataset_id,host_id,path,kind,is_primary,link,size_bytes,modified_at,added_at,file_dev,file_ino,workspace_id) VALUES(?,?,?,?,0,?,?,?,?,?,?,?)`,
		datasetID, l.HostID, l.Path, l.Kind, l.Link, l.SizeBytes, l.ModifiedAt.UnixNano(), time.Now().Unix(), l.FileDev, l.FileIno, ws)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Location{}, ErrLocationExists
		}
		return Location{}, err
	}
	id, _ := res.LastInsertId()
	for kind, st := range map[string]*Stamp{"sampled": l.Sampled, "full": l.Full} {
		if st != nil {
			if err := s.SetFingerprint(ws, id, kind, st.Value, st.By, l.SizeBytes, l.ModifiedAt.UnixNano()); err != nil {
				return Location{}, err
			}
		}
	}
	return s.Location(ws, datasetID, id)
}

// RemoveLocation forgets a location (the file is never touched). The registered file of a dataset stays.
func (s *Store) RemoveLocation(ws workspace.ID, datasetID, locID int64) error {
	l, err := s.Location(ws, datasetID, locID)
	if err != nil {
		return err
	}
	if l.Primary {
		return ErrPrimary
	}
	_, err = s.db.Exec(`DELETE FROM dataset_locations WHERE id=? AND workspace_id=?`, locID, ws)
	return err
}

// LocationOwner says which dataset a (host, path) belongs to as any of its locations, if any.
func (s *Store) LocationOwner(ws workspace.ID, hostID int64, path string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT dataset_id FROM dataset_locations WHERE workspace_id=? AND host_id=? AND path=?`, ws, hostID, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Match is a dataset whose locations say it may hold the same bytes as a file with the given evidence.
type Match struct {
	DatasetID  int64  `json:"datasetId"`
	LocationID int64  `json:"locationId"`
	Name       string `json:"name"`
	Relation   string `json:"relation"` // identity.Verified or identity.Probable
}

// FindSame returns the datasets (other than skip) with a location the evidence is verified or probably equal to, verified
// first. Only locations of the same size are looked at, and only the registered file and verified locations: what a dataset
// is, as far as its bytes go, is its registered file; a probable, declared or stale location proves nothing about it.
func (s *Store) FindSame(ws workspace.ID, ev identity.Evidence, skip int64) ([]Match, error) {
	rows, err := s.db.Query(`SELECT l.`+strings.ReplaceAll(locCols, ",", ",l.")+`, d.name FROM dataset_locations l JOIN datasets d ON d.id=l.dataset_id
		WHERE l.workspace_id=? AND d.workspace_id=l.workspace_id AND l.size_bytes=? AND l.dataset_id<>? AND (l.full_sha256<>'' OR l.sampled_fp<>'') AND (l.is_primary=1 OR l.link='verified') ORDER BY l.dataset_id, l.id`, ws, ev.Size, skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	best := map[int64]Match{}
	var order []int64
	for rows.Next() {
		var l Location
		var dsID, mod, add, sAt, fAt int64
		var prim int
		var sFP, sBy, fSHA, fBy, name string
		if err := rows.Scan(&l.ID, &dsID, &l.HostID, &l.Path, &l.Kind, &prim, &l.Link, &l.SizeBytes, &mod, &add, &sFP, &sAt, &sBy, &fSHA, &fAt, &fBy, &l.FileDev, &l.FileIno, &name); err != nil {
			return nil, err
		}
		rel := identity.Relate(ev, identity.Evidence{Size: l.SizeBytes, Sampled: sFP, Full: fSHA})
		if rel != identity.Verified && rel != identity.Probable {
			continue
		}
		cur, seen := best[dsID]
		if !seen {
			order = append(order, dsID)
		}
		if !seen || (rel == identity.Verified && cur.Relation != identity.Verified) {
			best[dsID] = Match{DatasetID: dsID, LocationID: l.ID, Name: name, Relation: rel}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Relation == identity.Verified && out[j].Relation != identity.Verified
	})
	return out, nil
}

// ReadOrder is the locations the viewer may read from, best first: files on this computer before files on a host, then the
// registered file before the others, then the order they were added. Only the registered file and locations whose identity
// is verified (equal full SHA-256) are eligible: a probable or declared location may hold other bytes than the header that
// was catalogued, so it is never read silently.
func ReadOrder(locs []Location) []Location {
	var out []Location
	for _, l := range locs {
		if l.Primary || l.Link == LinkVerified {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].HostID == 0) != (out[j].HostID == 0) {
			return out[i].HostID == 0
		}
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		return out[i].ID < out[j].ID
	})
	return out
}
