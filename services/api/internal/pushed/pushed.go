// Package pushed is the group server's store of work pushed from linked local instances (ADR 0066 section 2, ADR 0050 slice 4):
// immutable frozen items addressed by content hash within a workspace, their pictures, the lineages that let a newer push
// supersede an older one, and the dataset references with what the server found about them among its own datasets.
//
// Every query names the workspace (internal/arch guard 5). Items are never edited: a new hash in a lineage supersedes the old
// head, which stays readable. Only the pusher's own instance can supersede, because a lineage is (account, instance, kind,
// origin).
package pushed

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

// MissingError refuses an item whose pictures are not on the server yet; Hashes lists them (ADR 0066: 409 with the hashes).
type MissingError struct{ Hashes []string }

func (e MissingError) Error() string {
	return "upload these first: " + strings.Join(e.Hashes, ", ")
}

// LimitError refuses a push over a daily limit or the workspace's byte quota; the sentence says which and the current use.
type LimitError struct{ Msg string }

func (e LimitError) Error() string { return e.Msg }

// Store is the pushed items of every workspace on one catalog, and the folder their pictures are kept in.
type Store struct {
	db  *sql.DB
	dir string
	Now func() time.Time
}

// Open creates the tables when they are missing (a fresh catalog; schema 32 adds them to an upgraded one). dir holds the pictures,
// one folder per workspace, 0700.
func Open(db *sql.DB, dir string) (*Store, error) {
	if _, err := db.Exec(schema.PushedTables); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dir, Now: time.Now}, nil
}

func (s *Store) now() time.Time { return s.Now() }

// Pusher is who pushes: the account, the token it came through and the instance it says it is.
type Pusher struct {
	Account, TokenID, Instance string
}

// ---- assets ----

func (s *Store) assetDir(ws workspace.ID) string {
	return filepath.Join(s.dir, strconv.FormatInt(int64(ws), 10))
}

// HasAsset reports whether the workspace holds the picture.
func (s *Store) HasAsset(ws workspace.ID, sha string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM pushed_assets WHERE workspace_id=? AND sha=?`, ws, sha).Scan(&one) == nil
}

// PutAsset stores a picture the caller has already checked (hash, decode, size, metadata). Storing the same sha again is a no-op
// and reports created false. The file is written to a fresh 0600 temporary and renamed, then the row is added.
func (s *Store) PutAsset(ws workspace.ID, by, sha, mime string, body []byte) (bool, error) {
	if !pushwire.ValidSHA(sha) || ws <= 0 {
		return false, ErrInvalid
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != sha {
		return false, ErrInvalid
	}
	if s.HasAsset(ws, sha) {
		return false, nil
	}
	dir := s.assetDir(ws)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, sha)); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO pushed_assets(workspace_id,sha,mime,size,created_by,created_at) VALUES(?,?,?,?,?,?)`,
		ws, sha, mime, len(body), by, s.now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DailyRefusal says whether one more picture of size bytes would take the account past its daily limit (LimitError), checked
// before the picture is stored, so pictures that no item shows cannot exceed it either.
func (s *Store) DailyRefusal(ws workspace.ID, account string, size int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return checkDaily(tx, ws, account, s.now().Unix(), size)
}

// AssetBytes is what the workspace's pushed pictures take (they count towards its byte quota, ADR 0050 section 8).
func (s *Store) AssetBytes(ws workspace.ID) int64 {
	var n sql.NullInt64
	_ = s.db.QueryRow(`SELECT SUM(size) FROM pushed_assets WHERE workspace_id=?`, ws).Scan(&n)
	return n.Int64
}

// Asset returns a picture of item id, only when that item, in ws, shows it.
func (s *Store) Asset(ws workspace.ID, id int64, sha string) (string, []byte, error) {
	var mime string
	err := s.db.QueryRow(`SELECT a.mime FROM pushed_item_assets ia JOIN pushed_items i ON i.id=ia.item_id
		JOIN pushed_assets a ON a.workspace_id=i.workspace_id AND a.sha=ia.sha
		WHERE i.workspace_id=? AND i.id=? AND ia.sha=?`, ws, id, sha).Scan(&mime)
	if errors.Is(err, sql.ErrNoRows) || !pushwire.ValidSHA(sha) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	b, err := os.ReadFile(filepath.Join(s.assetDir(ws), sha))
	if err != nil {
		return "", nil, ErrNotFound
	}
	return mime, b, nil
}

// ---- which hashes are held ----

// Missing returns the hashes among hs that the workspace holds neither as an item nor as a picture, in the order given.
func (s *Store) Missing(ws workspace.ID, hs []string) ([]string, error) {
	out := []string{}
	for _, h := range hs {
		var one int
		err := s.db.QueryRow(`SELECT 1 FROM pushed_items WHERE workspace_id=? AND hash=? UNION ALL SELECT 1 FROM pushed_assets WHERE workspace_id=? AND sha=? LIMIT 1`, ws, h, ws, h).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, h)
		} else if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- items ----

// Result is what storing an item did.
type Result struct {
	ID     int64  `json:"id"`
	Hash   string `json:"hash"`
	Stored bool   `json:"stored"` // false: the server already held this hash, and nothing changed but added evidence
	// Superseded is the hash of the head this item replaced in its lineage, "" when none.
	Superseded string `json:"superseded,omitempty"`
	// SupersededBy is set when the hash was already held and a newer push has replaced it since: it stays what it was, and the
	// lineage's head is not moved back.
	SupersededBy string `json:"supersededBy,omitempty"`
	// Matches are the references that became verified or probable, for the audit log.
	Matches []Match `json:"-"`
}

// Put stores one item in one transaction (ADR 0066: each item is atomic). The caller has validated the envelope and the
// document, and recomputed it.Hash. The same hash already in the workspace answers the existing row: a retry never duplicates,
// and a hash that was superseded is not made the head again (A, B, then A answers A as it was). An item whose pictures are not
// all on the server is refused with MissingError, so nothing appears with holes. A new hash becomes the head of its lineage
// (account, instance, kind, origin), and the previous head records it as superseded_by.
func (s *Store) Put(ws workspace.ID, p Pusher, it pushwire.Item, ids []pushwire.Identity) (Result, error) {
	if ws <= 0 || len(ids) != len(it.References) {
		return Result{}, ErrInvalid
	}
	doc, err := json.Marshal(it)
	if err != nil {
		return Result{}, err
	}
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var id int64
	var supBy sql.NullInt64
	var by, inst string
	switch err := tx.QueryRow(`SELECT id, superseded_by, created_by, instance FROM pushed_items WHERE workspace_id=? AND hash=?`, ws, it.Hash).Scan(&id, &supBy, &by, &inst); {
	case err == nil:
		r := Result{ID: id, Hash: it.Hash}
		if supBy.Valid {
			_ = tx.QueryRow(`SELECT hash FROM pushed_items WHERE workspace_id=? AND id=?`, ws, supBy.Int64).Scan(&r.SupersededBy)
		}
		// only the instance that pushed an item may add evidence to it: anyone in the workspace can read the item and replay it,
		// and must not be able to make a reference read "verified" with a full hash of their choosing
		if by == p.Account && inst == p.Instance {
			if r.Matches, err = addEvidence(tx, ws, id, ids, now); err != nil {
				return Result{}, err
			}
		}
		return r, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return Result{}, err
	}
	var missing []string
	for _, a := range it.Assets {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM pushed_assets WHERE workspace_id=? AND sha=?`, ws, a).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, a)
		} else if err != nil {
			return Result{}, err
		}
	}
	if len(missing) > 0 {
		return Result{}, MissingError{missing}
	}
	if err := checkDaily(tx, ws, p.Account, now, int64(len(doc))); err != nil {
		return Result{}, err
	}
	var head int64
	var headHash string
	err = tx.QueryRow(`SELECT id, hash FROM pushed_items WHERE workspace_id=? AND created_by=? AND instance=? AND kind=? AND origin=? AND superseded_by IS NULL`,
		ws, p.Account, p.Instance, it.Kind, it.Origin).Scan(&head, &headHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	res, err := tx.Exec(`INSERT INTO pushed_items(workspace_id,created_by,token_id,instance,kind,origin,hash,title,doc,bytes,pushed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		ws, p.Account, p.TokenID, p.Instance, it.Kind, it.Origin, it.Hash, it.Title, string(doc), len(doc), now)
	if err != nil {
		return Result{}, err
	}
	id, _ = res.LastInsertId()
	if head != 0 {
		if _, err := tx.Exec(`UPDATE pushed_items SET superseded_by=?, superseded_at=? WHERE workspace_id=? AND id=? AND superseded_by IS NULL`, id, now, ws, head); err != nil {
			return Result{}, err
		}
	}
	for _, a := range it.Assets {
		if _, err := tx.Exec(`INSERT INTO pushed_item_assets(item_id, sha) VALUES(?,?)`, id, a); err != nil {
			return Result{}, err
		}
	}
	for n, r := range it.References {
		e := ids[n]
		sampled := e.Sampled
		if e.Algorithm != identity.SampledAlgorithm {
			sampled = "" // a fingerprint made another way is never compared with ours
		}
		if _, err := tx.Exec(`INSERT INTO pushed_references(item_id,n,role,name,size_bytes,sampled_fp,full_sha256) VALUES(?,?,?,?,?,?,?)`,
			id, n, r.Role, r.Name, r.SizeBytes, sampled, e.Full); err != nil {
			return Result{}, err
		}
	}
	matches, err := rematch(tx, ws, id, now)
	if err != nil {
		return Result{}, err
	}
	return Result{ID: id, Hash: it.Hash, Stored: true, Superseded: headHash, Matches: matches}, tx.Commit()
}

// addEvidence records a full hash that a re-push now carries for a reference that had none, when it does not contradict what is
// stored (a different sampled fingerprint is a different file, and the stored evidence is kept). It never removes evidence.
func addEvidence(tx *sql.Tx, ws workspace.ID, id int64, ids []pushwire.Identity, now int64) ([]Match, error) {
	changed := false
	for n, e := range ids {
		if e.Full == "" {
			continue
		}
		var sampled, full string
		if err := tx.QueryRow(`SELECT r.sampled_fp, r.full_sha256 FROM pushed_references r JOIN pushed_items i ON i.id=r.item_id
			WHERE i.workspace_id=? AND r.item_id=? AND r.n=?`, ws, id, n).Scan(&sampled, &full); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if full != "" || (sampled != "" && e.Algorithm == identity.SampledAlgorithm && e.Sampled != sampled) {
			continue
		}
		if _, err := tx.Exec(`UPDATE pushed_references SET full_sha256=? WHERE item_id=? AND n=? AND full_sha256=''`, e.Full, id, n); err != nil {
			return nil, err
		}
		changed = true
	}
	if changed {
		return rematch(tx, ws, id, now)
	}
	return nil, nil
}

// checkDaily refuses a push beyond the account's daily limits (600 items, 2 GiB of documents and pictures in 24 hours). The limit
// is the account's across every workspace it pushes to, so these two statements read across workspaces on purpose (internal/arch
// names them); they return counts to the caller, never rows. ws is the workspace being pushed to.
func checkDaily(tx *sql.Tx, ws workspace.ID, account string, now, docBytes int64) error {
	_ = ws
	since := now - 24*3600
	var items int64
	var bytes sql.NullInt64
	if err := tx.QueryRow(`SELECT COUNT(*), SUM(bytes) FROM pushed_items WHERE created_by=? AND pushed_at>?`, account, since).Scan(&items, &bytes); err != nil {
		return err
	}
	var assets sql.NullInt64
	if err := tx.QueryRow(`SELECT SUM(size) FROM pushed_assets WHERE created_by=? AND created_at>?`, account, since).Scan(&assets); err != nil {
		return err
	}
	if items >= pushwire.DailyItems {
		return LimitError{fmt.Sprintf("this account pushed %d items in the last 24 hours, the most a day allows; push again later", items)}
	}
	if used := bytes.Int64 + assets.Int64; used+docBytes > pushwire.DailyBytes {
		return LimitError{fmt.Sprintf("this account pushed %d MiB in the last 24 hours and may push %d MiB a day; push again later", used>>20, pushwire.DailyBytes>>20)}
	}
	return nil
}

// Head is one lineage head of an instance, for reconciling after a lost answer.
type Head struct {
	Kind   string `json:"kind"`
	Origin string `json:"origin"`
	Hash   string `json:"hash"`
	ID     int64  `json:"id"`
}

// Heads lists the instance's lineage heads in ws (the account's own: another account's instance is never listed).
func (s *Store) Heads(ws workspace.ID, account, instance string) ([]Head, error) {
	rows, err := s.db.Query(`SELECT kind, origin, hash, id FROM pushed_items WHERE workspace_id=? AND created_by=? AND instance=? AND superseded_by IS NULL ORDER BY id`, ws, account, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Head{}
	for rows.Next() {
		var h Head
		if err := rows.Scan(&h.Kind, &h.Origin, &h.Hash, &h.ID); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Summary is one pushed item in a list.
type Summary struct {
	ID           int64      `json:"id"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Hash         string     `json:"hash"`
	PushedBy     string     `json:"pushedBy"` // the account id; the server adds the name
	PushedAt     time.Time  `json:"pushedAt"`
	SupersededBy int64      `json:"supersededBy,omitempty"`
	SupersededAt *time.Time `json:"supersededAt,omitempty"`
	Archived     bool       `json:"archived"`
}

const summaryCols = `id, kind, title, hash, created_by, pushed_at, superseded_by, superseded_at, archived_at`

func scanSummary(sc interface{ Scan(...any) error }) (Summary, error) {
	var x Summary
	var at int64
	var sup, supAt, arch sql.NullInt64
	if err := sc.Scan(&x.ID, &x.Kind, &x.Title, &x.Hash, &x.PushedBy, &at, &sup, &supAt, &arch); err != nil {
		return Summary{}, err
	}
	x.PushedAt = time.Unix(at, 0).UTC()
	x.SupersededBy = sup.Int64
	if supAt.Valid {
		t := time.Unix(supAt.Int64, 0).UTC()
		x.SupersededAt = &t
	}
	x.Archived = arch.Valid
	return x, nil
}

// List returns the heads of the workspace's lineages, newest first; archived ones only when asked.
func (s *Store) List(ws workspace.ID, archived bool) ([]Summary, error) {
	q := `SELECT ` + summaryCols + ` FROM pushed_items WHERE workspace_id=? AND superseded_by IS NULL`
	if !archived {
		q += ` AND archived_at IS NULL`
	}
	rows, err := s.db.Query(q+` ORDER BY pushed_at DESC, id DESC LIMIT 1000`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		x, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Reference is a dataset reference as a member sees it: never the fingerprints themselves, only what they established.
type Reference struct {
	N         int    `json:"n"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	// Match is verified (equal full SHA-256: the same bytes), probable (equal size and sampled fingerprint, no full hash on both
	// sides: not verified) or none ("not on this server").
	Match       string `json:"match"`
	DatasetID   int64  `json:"datasetId,omitempty"` // the server's own dataset, for verified and probable
	DatasetName string `json:"datasetName,omitempty"`
}

// Detail is one pushed item with its document, its lineage and its references.
type Detail struct {
	Summary
	Item       pushwire.Item `json:"item"`
	Lineage    []Summary     `json:"lineage"` // oldest first, this item among them
	References []Reference   `json:"references"`
}

// Get returns one item of ws.
func (s *Store) Get(ws workspace.ID, id int64) (Detail, error) {
	var d Detail
	var doc string
	row := s.db.QueryRow(`SELECT `+summaryCols+`, doc FROM pushed_items WHERE workspace_id=? AND id=?`, ws, id)
	var x Summary
	var at int64
	var sup, supAt, arch sql.NullInt64
	err := row.Scan(&x.ID, &x.Kind, &x.Title, &x.Hash, &x.PushedBy, &at, &sup, &supAt, &arch, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	x.PushedAt, x.SupersededBy, x.Archived = time.Unix(at, 0).UTC(), sup.Int64, arch.Valid
	if supAt.Valid {
		t := time.Unix(supAt.Int64, 0).UTC()
		x.SupersededAt = &t
	}
	d.Summary = x
	if err := json.Unmarshal([]byte(doc), &d.Item); err != nil {
		return Detail{}, err
	}
	rows, err := s.db.Query(`SELECT `+summaryCols+` FROM pushed_items WHERE workspace_id=? AND (created_by, instance, kind, origin) =
		(SELECT created_by, instance, kind, origin FROM pushed_items WHERE workspace_id=? AND id=?) ORDER BY id`, ws, ws, id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		y, err := scanSummary(rows)
		if err != nil {
			rows.Close()
			return Detail{}, err
		}
		d.Lineage = append(d.Lineage, y)
	}
	rows.Close()
	d.References, err = s.references(ws, id)
	return d, err
}

func (s *Store) references(ws workspace.ID, id int64) ([]Reference, error) {
	rows, err := s.db.Query(`SELECT r.n, r.role, r.name, r.size_bytes, r.match, COALESCE(r.match_dataset_id, 0), COALESCE(d.name, '')
		FROM pushed_references r JOIN pushed_items i ON i.id=r.item_id
		LEFT JOIN datasets d ON d.id=r.match_dataset_id AND d.workspace_id=i.workspace_id
		WHERE i.workspace_id=? AND r.item_id=? ORDER BY r.n`, ws, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reference{}
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.N, &r.Role, &r.Name, &r.SizeBytes, &r.Match, &r.DatasetID, &r.DatasetName); err != nil {
			return nil, err
		}
		if r.Match == "none" {
			r.DatasetID, r.DatasetName = 0, ""
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Match is one reference whose tier changed when it was compared again.
type Match struct {
	N         int
	Name      string
	Match     string
	DatasetID int64
}

// Rematch compares the item's references with the workspace's datasets again (a server dataset may have gained or changed a
// fingerprint since) and returns the references whose tier changed, for the audit log.
func (s *Store) Rematch(ws workspace.ID, id int64) ([]Match, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ch, err := rematch(tx, ws, id, s.now().Unix())
	if err != nil {
		return nil, err
	}
	return ch, tx.Commit()
}

// rematch applies the ADR 0053 rule (identity.Relate) between each reference and every location of a dataset of ws: an equal
// full SHA-256 is verified; equal size and sampled fingerprint with no full hash on both sides is probable; anything else, and a
// file name alone, is none. Only candidates sharing a full hash, or the size and the sampled value, are read.
func rematch(tx *sql.Tx, ws workspace.ID, id int64, now int64) ([]Match, error) {
	type ref struct {
		n             int
		name          string
		size          int64
		sampled, full string
		match         string
		ds            int64
	}
	rows, err := tx.Query(`SELECT r.n, r.name, r.size_bytes, r.sampled_fp, r.full_sha256, r.match, COALESCE(r.match_dataset_id,0)
		FROM pushed_references r JOIN pushed_items i ON i.id=r.item_id WHERE i.workspace_id=? AND r.item_id=? ORDER BY r.n`, ws, id)
	if err != nil {
		return nil, err
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.n, &r.name, &r.size, &r.sampled, &r.full, &r.match, &r.ds); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	var changed []Match
	for _, r := range refs {
		if r.sampled == "" && r.full == "" {
			continue // nothing comparable: "not on this server", whatever the name says
		}
		full, sampled := r.full, r.sampled
		if full == "" {
			full = "-" // never equal to a stored value, which is '' or a sha
		}
		if sampled == "" {
			sampled = "-"
		}
		crows, err := tx.Query(`SELECT d.id, l.size_bytes, l.sampled_fp, l.full_sha256 FROM dataset_locations l
			JOIN datasets d ON d.id=l.dataset_id AND d.workspace_id=l.workspace_id
			WHERE l.workspace_id=? AND (l.full_sha256=? OR (l.size_bytes=? AND l.sampled_fp=?)) ORDER BY d.id`, ws, full, r.size, sampled)
		if err != nil {
			return nil, err
		}
		best, bestDS := "none", int64(0)
		for crows.Next() {
			var ds, size int64
			var lsampled, lfull string
			if err := crows.Scan(&ds, &size, &lsampled, &lfull); err != nil {
				crows.Close()
				return nil, err
			}
			switch identity.Relate(identity.Evidence{Size: r.size, Sampled: r.sampled, Full: r.full}, identity.Evidence{Size: size, Sampled: lsampled, Full: lfull}) {
			case identity.Verified:
				if best != "verified" {
					best, bestDS = "verified", ds
				}
			case identity.Probable:
				if best == "none" {
					best, bestDS = "probable", ds
				}
			}
		}
		crows.Close()
		if best == r.match && bestDS == r.ds {
			continue
		}
		var dsArg any
		if bestDS != 0 {
			dsArg = bestDS
		}
		if _, err := tx.Exec(`UPDATE pushed_references SET match=?, match_dataset_id=?, matched_at=? WHERE item_id=? AND n=?`, best, dsArg, now, id, r.n); err != nil {
			return nil, err
		}
		changed = append(changed, Match{N: r.n, Name: r.name, Match: best, DatasetID: bestDS})
	}
	return changed, nil
}

// Archive hides an item (and is idempotent). The route has already applied the ownership rule.
func (s *Store) Archive(ws workspace.ID, id int64, by string) error {
	res, err := s.db.Exec(`UPDATE pushed_items SET archived_at=COALESCE(archived_at, ?), archived_by=COALESCE(archived_by, ?) WHERE workspace_id=? AND id=?`, s.now().Unix(), by, ws, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count says how many rows the workspace holds for one hash (tests: a retry never duplicates).
func (s *Store) Count(ws workspace.ID, hash string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pushed_items WHERE workspace_id=? AND hash=?`, ws, hash).Scan(&n)
	return n
}

// Doc returns the stored document bytes of an item (tests compare them with what was sent).
func (s *Store) Doc(ws workspace.ID, id int64) []byte {
	var doc string
	_ = s.db.QueryRow(`SELECT doc FROM pushed_items WHERE workspace_id=? AND id=?`, ws, id).Scan(&doc)
	return bytes.TrimSpace([]byte(doc))
}
