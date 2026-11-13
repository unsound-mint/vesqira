// Package catalog stores dataset references and the header facts the worker declared.
// Scientific data stays in the original files; only references and metadata live in SQLite.
package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"

	_ "modernc.org/sqlite"
)

type Dataset struct {
	ID         int64           `json:"id"`
	Path       string          `json:"path"`
	Name       string          `json:"name"`
	SizeBytes  int64           `json:"sizeBytes"`
	ModifiedAt time.Time       `json:"modifiedAt"`
	AddedAt    time.Time       `json:"addedAt"`
	Header     json.RawMessage `json:"header"`
	// HostID is 0 for a file on this machine, otherwise the remote host the file lives on (see package hosts).
	HostID int64 `json:"hostId"`
	// Nature is what the user says the data is: observed, simulated or a constructed control. Empty means not declared.
	Nature string `json:"nature"`
	// Interpretation is what the user declared about the data. The file itself declares none of it.
	Interpretation *Interpretation `json:"interpretation"`
	// UnitDeclaration is the unit the user declared for a file whose header has no BUNIT (see unit.go). The header
	// facts themselves are never changed.
	UnitDeclaration *UnitDeclaration `json:"unitDeclaration"`
	// Kind is "fits" (an image or cube), "catalog" (a table of sources, ADR 0044) or "healpix" (a HEALPix map, ADR 0067). It comes
	// from the header the worker read.
	Kind string `json:"kind"`
	// CatalogDeclaration is what the user declared about a catalog's columns (positions, frame, angle); nil for anything else.
	CatalogDeclaration *CatalogDeclaration `json:"catalogDeclaration"`
	// Archived entries are hidden from the lists but still resolve by id (ADR 0041).
	Archived bool `json:"archived"`
	// FileDev and FileIno are the device and inode the registered file had when it was recorded (0: not recorded, or a file on a
	// host); Upsert stores them on the primary location and Get fills them from it. Never sent to a client.
	FileDev int64 `json:"-"`
	FileIno int64 `json:"-"`
	// Locations are the files this dataset's bytes are in, the registered one first (ADR 0053). Path and HostID above are
	// the registered file. A location never changes what the dataset is; it says where else the same bytes may be read.
	Locations []Location `json:"locations"`
	// Corrections are the researcher's WCS and unit corrections in force (or stale), from the latest record of their history
	// (ADR 0061); nil when nothing is corrected. Header stays what the file said.
	Corrections *Corrections `json:"corrections"`
	// VGT is what the file's header says when it carries VGT.jl's keywords (the angle's frame and meaning, the product role, the run
	// parameters, the resolution), and where a declaration in force disagrees with it; nil for any other file. Computed on read.
	VGT *VGTHeader `json:"vgtHeader"`
	// OrientationHeader is VGT under its general name: what the header says of an orientation through the tool profile that read
	// it. Both are sent until schema 36; clients read this one.
	OrientationHeader *VGTHeader `json:"orientationHeader"`
}

// Interpretation is a user-declared overlay on a dataset (never inferred from values, never written to the file).
type Interpretation struct {
	Kind          string `json:"kind"` // "axial_orientation"
	Unit          string `json:"unit"` // "deg" or "rad", required
	ReliabilityID *int64 `json:"reliabilityId"`
	// Frame is what the angle is measured against: "pixel" (counter-clockwise from +axis 1 towards +axis 2 as stored) or
	// "sky" (east of north, in the file's own celestial frame). Nil means not declared; older files never say, VGT.jl's newer ones do (PSIFRAME).
	Frame *string `json:"frame"`
	// Quantity is what the angle describes: "b_field", "gradient", "dust_polarization", "starlight_polarization" or
	// "unspecified". Nil means not declared.
	Quantity *string `json:"quantity"`
	// SkyFrame is the celestial frame a "sky" angle is stated in when that is not the grid's own ("galactic" or "icrs"); nil means the
	// grid's own frame, which is what a person's "sky" declaration always means. Only a declaration the file's own header made
	// (DeclaredBy FileHeaderAuthor) has one; it is computed from the stored header on read and is not a stored column.
	SkyFrame   *string   `json:"skyFrame"`
	DeclaredBy string    `json:"declaredBy"` // "user", the file's header (FileHeaderAuthor) or the name of an adapter, e.g. "vgt-run-adapter"
	DeclaredAt time.Time `json:"declaredAt"`
	// Tier is how far this interpretation is established (ADR 0055): declared by the researcher, or inferred by DeclaredBy's
	// rule, whose mapping version is MappingVersion (empty for a declared one).
	Tier           string `json:"tier"`
	MappingVersion string `json:"mappingVersion"`
	// Stale is true when the file's registered size or modification time differs from what it was when the user
	// made the declaration: the declaration may no longer describe these bytes.
	Stale bool `json:"stale"`
}

type Store struct{ db *sql.DB }

// WithPragmas adds the connection settings every Vesqira database needs. SQLite leaves foreign keys off unless asked
// per connection, so without them the ON DELETE CASCADE and REFERENCES clauses in the schema silently do nothing and
// orphan rows accumulate. WAL lets the backup and export readers run beside the writer; busy_timeout turns a short
// lock into a wait instead of an immediate "database is locked". Use it for every sql.Open of a writable database.
func WithPragmas(dsn string) string {
	const p = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	memory := dsn == ":memory:" || strings.Contains(dsn, "mode=memory") || strings.HasPrefix(dsn, "file::memory:")
	extra := p
	if !memory { // an in-memory database has no journal to switch
		extra += "&_pragma=journal_mode(WAL)"
	}
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&" + extra
	}
	return dsn + "?" + extra
}

var ErrNotFound = errors.New("dataset not found")

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", WithPragmas(dsn))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := schema.Guard(db, dsn); err != nil {
		db.Close()
		return nil, err
	}
	if err := schema.EnsureArchive(db); err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(schema.WorkspaceTables)
	if err == nil {
		err = schema.SeedLocalWorkspace(db)
	}
	if err == nil {
		_, err = db.Exec(schema.DatasetsTable) // modified_at is Unix nanoseconds
	}
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS interpretations(
			dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			unit TEXT NOT NULL,
			reliability_id INTEGER,
			declared_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			modified_ns INTEGER NOT NULL DEFAULT 0)`)
	}
	if err == nil {
		// older catalogs lack the column that records who made a declaration; adding it twice is harmless
		err = schema.AddColumn(db, "interpretations", "declared_by", "TEXT NOT NULL DEFAULT 'user'")
		if err == nil {
			err = schema.AddTierColumns(db, "interpretations")
		}
		if err == nil {
			err = schema.AddColumn(db, "interpretations", "frame", "TEXT")
		}
		if err == nil {
			err = schema.AddColumn(db, "interpretations", "quantity", "TEXT")
		}
		if err == nil {
			err = schema.AddColumn(db, "datasets", "host_id", "INTEGER NOT NULL DEFAULT 0")
		}
		if err == nil {
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS dataset_nature(dataset_id INTEGER PRIMARY KEY, nature TEXT NOT NULL, declared_at INTEGER NOT NULL)`)
		}
		if err == nil {
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS dataset_units(
				dataset_id INTEGER PRIMARY KEY REFERENCES datasets(id) ON DELETE CASCADE,
				unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL,
				size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0)`)
		}
		if err == nil {
			_, err = db.Exec(schema.CatalogDeclarationsTable)
		}
		if err == nil {
			_, err = db.Exec(schema.DatasetLocationsTable)
		}
		if err == nil {
			_, err = db.Exec(schema.CorrectionsTable)
		}
		for _, t := range []string{"dataset_units", "catalog_declarations"} {
			if err == nil {
				err = schema.AddTierColumns(db, t)
			}
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the shared SQLite handle so other stores (figures, boards, review) live in the same file.
func (s *Store) DB() *sql.DB { return s.db }

// Upsert registers a path or refreshes its metadata. The path is the identity of a reference;
// it does not prove bytewise identity, so size and mtime are stored for later comparison.
func (s *Store) Upsert(ws workspace.ID, by string, d Dataset) (Dataset, error) {
	var owner int64
	if err := s.db.QueryRow(`SELECT dataset_id FROM dataset_locations WHERE workspace_id=? AND host_id=? AND path=? AND is_primary=0`, ws, d.HostID, d.Path).Scan(&owner); err == nil {
		// the file is another location of a dataset: registering it too would give one file two datasets
		return Dataset{}, fmt.Errorf("%w (dataset %d)", ErrLocationExists, owner)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, err
	}
	key := pathKey(d.HostID, d.Path)
	_, err := s.db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,host_id,workspace_id,created_by) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id,host_id,path) DO UPDATE SET name=excluded.name,size_bytes=excluded.size_bytes,modified_at=excluded.modified_at,header=excluded.header`,
		key, d.Name, d.SizeBytes, d.ModifiedAt.UnixNano(), time.Now().Unix(), string(d.Header), d.HostID, ws, by)
	if err != nil {
		return Dataset{}, err
	}
	got, err := s.byPath(ws, d.HostID, key)
	if err != nil {
		return Dataset{}, err
	}
	if err := s.upsertPrimary(ws, got.ID, d); err != nil {
		return Dataset{}, err
	}
	if fileStatesItsUnit(d) { // the file says its unit now: a declaration made while it did not must not linger or come back
		if _, err := s.db.Exec(`DELETE FROM dataset_units WHERE dataset_id=?`, got.ID); err != nil {
			return Dataset{}, err
		}
	}
	return s.Get(ws, got.ID)
}

// pathKey keeps one unique key per (host, path): a path on another machine is not the same file as the same path here.
func pathKey(host int64, p string) string {
	if host == 0 {
		return p
	}
	return fmt.Sprintf("h%d:%s", host, p)
}

func scan(row interface{ Scan(...any) error }) (Dataset, error) {
	var d Dataset
	var mod, add int64
	var hdr string
	if err := row.Scan(&d.ID, &d.Path, &d.Name, &d.SizeBytes, &mod, &add, &hdr, &d.HostID, &d.Archived); err != nil {
		return Dataset{}, err
	}
	if d.HostID != 0 {
		d.Path = strings.TrimPrefix(d.Path, fmt.Sprintf("h%d:", d.HostID))
	}
	d.ModifiedAt, d.AddedAt, d.Header = time.Unix(0, mod).UTC(), time.Unix(add, 0).UTC(), json.RawMessage(hdr)
	d.Kind = kindOf(d.Header)
	return d, nil
}

var cols = `id,path,name,size_bytes,modified_at,added_at,header,host_id,` + schema.ArchivedExpr("dataset", "datasets.id")

func (s *Store) byPath(ws workspace.ID, host int64, p string) (Dataset, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE workspace_id=? AND host_id=? AND path=?`, ws, host, p))
}

// Get reads a dataset of the workspace; one of another workspace answers ErrNotFound like a missing id.
func (s *Store) Get(ws workspace.ID, id int64) (Dataset, error) {
	d, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM datasets WHERE id=? AND workspace_id=?`, id, ws))
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Interpretation, err = s.interpretation(id, d)
	if err == nil {
		d.Nature, err = s.nature(id)
	}
	if err == nil {
		d.UnitDeclaration, err = s.unitDeclaration(id, d)
	}
	if err == nil && d.Kind == KindCatalog {
		d.CatalogDeclaration, err = s.catalogDeclaration(id, d)
	}
	if err == nil {
		d.Locations, err = s.Locations(ws, id)
		for _, l := range d.Locations {
			if l.Primary {
				d.FileDev, d.FileIno = l.FileDev, l.FileIno
			}
		}
	}
	if err == nil {
		d.Corrections, err = s.corrections(d)
	}
	d.attachVGT()
	return d, err
}

func (s *Store) interpretation(id int64, d Dataset) (*Interpretation, error) {
	var in Interpretation
	var rel sql.NullInt64
	var frame, quantity sql.NullString
	var at, size, mod int64
	err := s.db.QueryRow(`SELECT kind,unit,reliability_id,declared_at,size_bytes,modified_ns,declared_by,frame,quantity,tier,mapping_version FROM interpretations WHERE dataset_id=?`, id).Scan(&in.Kind, &in.Unit, &rel, &at, &size, &mod, &in.DeclaredBy, &frame, &quantity, &in.Tier, &in.MappingVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rel.Valid {
		in.ReliabilityID = &rel.Int64
	}
	if frame.Valid {
		in.Frame = &frame.String
	}
	if quantity.Valid {
		in.Quantity = &quantity.String
	}
	in.DeclaredAt = time.Unix(at, 0).UTC()
	in.Stale = size != d.SizeBytes || mod != d.ModifiedAt.UnixNano()
	applyFileSkyFrame(&in, d)
	return &in, nil
}

func (s *Store) nature(id int64) (string, error) {
	var n string
	err := s.db.QueryRow(`SELECT nature FROM dataset_nature WHERE dataset_id=?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// SetNature records what the user says the data is ("" clears it).
func (s *Store) SetNature(id int64, nature string) error {
	if nature == "" {
		_, err := s.db.Exec(`DELETE FROM dataset_nature WHERE dataset_id=?`, id)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO dataset_nature(dataset_id,nature,declared_at) VALUES(?,?,?) ON CONFLICT(dataset_id) DO UPDATE SET nature=excluded.nature,declared_at=excluded.declared_at`, id, nature, time.Now().Unix())
	return err
}

// SetInterpretation records what the user declared. Callers validate the kind, unit and companion first.
func (s *Store) SetInterpretation(d Dataset, in Interpretation) error {
	id := d.ID
	var rel any
	if in.ReliabilityID != nil {
		rel = *in.ReliabilityID
	}
	by := in.DeclaredBy
	if by == "" {
		by = "user"
	}
	var frame, quantity any
	if in.Frame != nil {
		frame = *in.Frame
	}
	if in.Quantity != nil {
		quantity = *in.Quantity
	}
	tier, mapping := TierOf(by)
	_, err := s.db.Exec(`INSERT INTO interpretations(dataset_id,kind,unit,reliability_id,declared_at,size_bytes,modified_ns,declared_by,frame,quantity,tier,mapping_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dataset_id) DO UPDATE SET kind=excluded.kind,unit=excluded.unit,reliability_id=excluded.reliability_id,declared_at=excluded.declared_at,
			size_bytes=excluded.size_bytes,modified_ns=excluded.modified_ns,declared_by=excluded.declared_by,frame=excluded.frame,quantity=excluded.quantity,
			tier=excluded.tier,mapping_version=excluded.mapping_version`,
		id, in.Kind, in.Unit, rel, time.Now().Unix(), d.SizeBytes, d.ModifiedAt.UnixNano(), by, frame, quantity, tier, mapping)
	return err
}

func (s *Store) ClearInterpretation(id int64) error {
	_, err := s.db.Exec(`DELETE FROM interpretations WHERE dataset_id=?`, id)
	return err
}

func (s *Store) List(ws workspace.ID) ([]Dataset, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM datasets WHERE workspace_id=? ORDER BY name, id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dataset{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	locs, err := s.allLocations(ws)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Locations = locs[out[i].ID]; out[i].Locations == nil {
			out[i].Locations = []Location{}
		}
		if out[i].Interpretation, err = s.interpretation(out[i].ID, out[i]); err != nil {
			return nil, err
		}
		if out[i].Nature, err = s.nature(out[i].ID); err != nil {
			return nil, err
		}
		if out[i].UnitDeclaration, err = s.unitDeclaration(out[i].ID, out[i]); err != nil {
			return nil, err
		}
		if out[i].Corrections, err = s.corrections(out[i]); err != nil {
			return nil, err
		}
		out[i].attachVGT()
		if out[i].Kind == KindCatalog {
			if out[i].CatalogDeclaration, err = s.catalogDeclaration(out[i].ID, out[i]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// HintKey is a registered file's name and size, the only facts a run record gives about its input cube.
type HintKey struct {
	Name string
	Size int64
}

// HintIndex maps (name, size) to the first registered dataset (by name, then id) with that name and size, in one
// query and without reading any header: what the runs list needs to suggest an input, for every run at once.
func (s *Store) HintIndex(ws workspace.ID) (map[HintKey]int64, error) {
	rows, err := s.db.Query(`SELECT id,name,size_bytes FROM datasets WHERE workspace_id=? ORDER BY name, id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[HintKey]int64{}
	for rows.Next() {
		var id int64
		var k HintKey
		if err := rows.Scan(&id, &k.Name, &k.Size); err != nil {
			return nil, err
		}
		if _, ok := out[k]; !ok {
			out[k] = id
		}
	}
	return out, rows.Err()
}

// PathFacts is what the project scan needs to know about a registered file without reading its whole header.
type PathFacts struct {
	ID          int64
	Size        int64
	MtimeNS     int64
	BunitStatus string // the header's bunit_status: declared, unresolved, ...
}

// FactsByPath returns, for the datasets on one host, the facts above keyed by the file's path on that host.
func (s *Store) FactsByPath(ws workspace.ID, hostID int64) (map[string]PathFacts, error) {
	rows, err := s.db.Query(`SELECT id,path,size_bytes,modified_at,COALESCE(json_extract(header,'$.bunit_status'),'') FROM datasets WHERE workspace_id=? AND host_id=?`, ws, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PathFacts{}
	for rows.Next() {
		var f PathFacts
		var p string
		if err := rows.Scan(&f.ID, &p, &f.Size, &f.MtimeNS, &f.BunitStatus); err != nil {
			return nil, err
		}
		if hostID != 0 {
			p = strings.TrimPrefix(p, fmt.Sprintf("h%d:", hostID))
		}
		out[p] = f
	}
	return out, rows.Err()
}

// ProposedInterpretations returns the interpretations someone other than the user declared (a run import's proposal),
// by dataset id. Until the user confirms one it stays labelled with its author.
func (s *Store) ProposedInterpretations(ws workspace.ID) (map[int64]Interpretation, error) {
	rows, err := s.db.Query(`SELECT i.dataset_id,i.kind,i.unit,i.reliability_id,i.declared_by,i.frame,i.quantity,i.tier,i.mapping_version FROM interpretations i
		JOIN datasets d ON d.id=i.dataset_id WHERE d.workspace_id=? AND i.declared_by<>'user'`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Interpretation{}
	for rows.Next() {
		var id int64
		var in Interpretation
		var rel sql.NullInt64
		var frame, quantity sql.NullString
		if err := rows.Scan(&id, &in.Kind, &in.Unit, &rel, &in.DeclaredBy, &frame, &quantity, &in.Tier, &in.MappingVersion); err != nil {
			return nil, err
		}
		if rel.Valid {
			in.ReliabilityID = &rel.Int64
		}
		if frame.Valid {
			in.Frame = &frame.String
		}
		if quantity.Valid {
			in.Quantity = &quantity.String
		}
		out[id] = in
	}
	return out, rows.Err()
}

// PreProfileHeader is a stored header of a VGT.jl product that has no orientation reading (ADR 0068, slice 2): it was stored before
// profiles existed. SizeBytes and ModifiedAt are what was recorded when the header was read.
type PreProfileHeader struct {
	ID         int64
	Path       string
	SizeBytes  int64
	ModifiedAt int64 // Unix nanoseconds
}

// PreProfileHeaders lists the datasets of a workspace on this computer (host 0) whose header has VGT.jl's cards but no
// orientation reading.
func (s *Store) PreProfileHeaders(ws workspace.ID) ([]PreProfileHeader, error) {
	rows, err := s.db.Query(`SELECT id,path,size_bytes,modified_at FROM datasets WHERE workspace_id=? AND host_id=0 AND header LIKE '%"psiframe"%' AND header NOT LIKE '%"orientation"%'`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PreProfileHeader
	for rows.Next() {
		var h PreProfileHeader
		if err := rows.Scan(&h.ID, &h.Path, &h.SizeBytes, &h.ModifiedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ReplaceHeader stores a freshly read header for a dataset whose file is unchanged; nothing else about the dataset moves.
func (s *Store) ReplaceHeader(ws workspace.ID, id int64, header json.RawMessage) error {
	_, err := s.db.Exec(`UPDATE datasets SET header=? WHERE id=? AND workspace_id=?`, string(header), id, ws)
	return err
}
