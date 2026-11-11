// Package regions keeps named regions per dataset: boxes and circles in whole 0-based image pixels, the same shapes
// and pixel-centre rule the worker uses to read spectra and statistics. They can be edited, duplicated and deleted, and
// written as a DS9 region file. A region is an author's selection, not a property of the file.
package regions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

const (
	MaxPerDataset = 200
	maxCoord      = 1 << 20
)

// MaxLinePoints bounds the vertices of a line; MaxLineWidth bounds its width (pixels or arcsec).
const (
	MaxLinePoints = 64
	MaxLineWidth  = 100000.0
)

// Shape is a rectangle (inclusive pixel bounds), a circle (centre pixel and radius in pixels) or a line (a path of whole-pixel
// vertices with a width: the cut a position-velocity diagram is taken along; it has no area, so no spectrum or histogram
// is ever taken of it).
type Shape struct {
	Shape string `json:"shape"` // rect | circle | line
	X0    int    `json:"x0"`
	Y0    int    `json:"y0"`
	X1    int    `json:"x1"`
	Y1    int    `json:"y1"`
	CX    int    `json:"cx"`
	CY    int    `json:"cy"`
	R     int    `json:"r"`
	// line: vertices [x, y] (0-based, y from the bottom), the width across the path, and its unit (pixel or arcsec)
	Points    [][]int `json:"points,omitempty"`
	Width     float64 `json:"width,omitempty"`
	WidthUnit string  `json:"widthUnit,omitempty"`
}

type Region struct {
	ID        int64     `json:"id"`
	DatasetID int64     `json:"datasetId"`
	Name      string    `json:"name"`
	Shape     Shape     `json:"shape"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s Shape) normalised() (Shape, error) {
	in := func(v int) bool { return v >= 0 && v < maxCoord }
	switch s.Shape {
	case "rect":
		if !in(s.X0) || !in(s.Y0) || !in(s.X1) || !in(s.Y1) || s.X0 > s.X1 || s.Y0 > s.Y1 {
			return s, fmt.Errorf("%w: a rectangle needs whole pixel numbers with the first corner before the second", ErrInvalid)
		}
		return Shape{Shape: "rect", X0: s.X0, Y0: s.Y0, X1: s.X1, Y1: s.Y1}, nil
	case "circle":
		if !in(s.CX) || !in(s.CY) || s.R < 0 || s.R >= maxCoord {
			return s, fmt.Errorf("%w: a circle needs a centre pixel and a radius of 0 or more", ErrInvalid)
		}
		return Shape{Shape: "circle", CX: s.CX, CY: s.CY, R: s.R}, nil
	case "line":
		if len(s.Points) < 2 || len(s.Points) > MaxLinePoints {
			return s, fmt.Errorf("%w: a line needs 2 to %d vertices", ErrInvalid, MaxLinePoints)
		}
		pts := make([][]int, len(s.Points))
		moved := false
		for i, p := range s.Points {
			if len(p) != 2 || !in(p[0]) || !in(p[1]) {
				return s, fmt.Errorf("%w: each vertex of a line is a pair of whole pixel numbers", ErrInvalid)
			}
			pts[i] = []int{p[0], p[1]}
			if p[0] != s.Points[0][0] || p[1] != s.Points[0][1] {
				moved = true
			}
		}
		if !moved {
			return s, fmt.Errorf("%w: a line needs two different vertices", ErrInvalid)
		}
		if s.WidthUnit != "pixel" && s.WidthUnit != "arcsec" {
			return s, fmt.Errorf("%w: the width of a line is in pixel or arcsec", ErrInvalid)
		}
		if math.IsNaN(s.Width) || math.IsInf(s.Width, 0) || s.Width <= 0 || s.Width > MaxLineWidth {
			return s, fmt.Errorf("%w: the width of a line is a positive number", ErrInvalid)
		}
		return Shape{Shape: "line", Points: pts, Width: s.Width, WidthUnit: s.WidthUnit}, nil
	}
	return s, fmt.Errorf("%w: the shape is a rect, a circle or a line", ErrInvalid)
}

// cleanName keeps a name short and free of control characters.
func cleanName(n string) (string, error) {
	n = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, n))
	if n == "" {
		return "", fmt.Errorf("%w: a region needs a name", ErrInvalid)
	}
	if r := []rune(n); len(r) > 80 {
		n = string(r[:80])
	}
	return n, nil
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS saved_regions(
		id INTEGER PRIMARY KEY AUTOINCREMENT, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, shape TEXT NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS saved_regions_dataset ON saved_regions(dataset_id)`); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

func scan(row interface{ Scan(...any) error }) (Region, error) {
	var r Region
	var shape string
	var c, u int64
	if err := row.Scan(&r.ID, &r.DatasetID, &r.Name, &shape, &c, &u); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(shape), &r.Shape); err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
	return r, nil
}

const cols = `id,dataset_id,name,shape,created_at,updated_at`

func (s *Store) List(datasetID int64) ([]Region, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM saved_regions WHERE dataset_id=? ORDER BY id`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Region{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Get(id int64) (Region, error) {
	r, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM saved_regions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Create(datasetID int64, name string, sh Shape) (Region, error) {
	name, err := cleanName(name)
	if err != nil {
		return Region{}, err
	}
	if sh, err = sh.normalised(); err != nil {
		return Region{}, err
	}
	raw, _ := json.Marshal(sh)
	now := time.Now().Unix()
	// count and insert in one statement so concurrent requests cannot pass the limit
	res, err := s.db.Exec(`INSERT INTO saved_regions(dataset_id,name,shape,created_at,updated_at)
		SELECT ?,?,?,?,? WHERE (SELECT COUNT(*) FROM saved_regions WHERE dataset_id=?) < ?`, datasetID, name, string(raw), now, now, datasetID, MaxPerDataset)
	if err != nil {
		return Region{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Region{}, fmt.Errorf("%w: at most %d regions per dataset", ErrInvalid, MaxPerDataset)
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

// Update changes the name and/or the shape. A nil argument leaves that part as it is.
func (s *Store) Update(id int64, name *string, sh *Shape) (Region, error) {
	cur, err := s.Get(id)
	if err != nil {
		return cur, err
	}
	if name != nil {
		if cur.Name, err = cleanName(*name); err != nil {
			return Region{}, err
		}
	}
	if sh != nil {
		if cur.Shape, err = sh.normalised(); err != nil {
			return Region{}, err
		}
	}
	raw, _ := json.Marshal(cur.Shape)
	if _, err := s.db.Exec(`UPDATE saved_regions SET name=?, shape=?, updated_at=? WHERE id=?`, cur.Name, string(raw), time.Now().Unix(), id); err != nil {
		return Region{}, err
	}
	return s.Get(id)
}

func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec(`DELETE FROM saved_regions WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DS9 writes the regions as a DS9 region file in image coordinates: 1-based, a pixel's centre at its integer
// number, so a box of whole pixels has a centre at half-integers and an odd or even size, and a circle selects the
// pixels whose centres lie within its radius, the rule used everywhere in Vesqira. Image coordinates are exact; no
// sky position is claimed.
func DS9(rs []Region) string {
	var b strings.Builder
	b.WriteString("# Region file format: DS9 version 4.1\n# Written by Vesqira. Image coordinates: 1-based pixel centres of the dataset's own pixel grid.\n")
	b.WriteString("global color=cyan\nimage\n")
	for _, r := range rs {
		label := strings.Map(func(c rune) rune {
			if c == '{' || c == '}' || c < 32 {
				return ' '
			}
			return c
		}, r.Name)
		switch r.Shape.Shape {
		case "rect":
			w, h := r.Shape.X1-r.Shape.X0+1, r.Shape.Y1-r.Shape.Y0+1
			fmt.Fprintf(&b, "box(%s,%s,%d,%d,0) # text={%s}\n", half(r.Shape.X0+r.Shape.X1+2), half(r.Shape.Y0+r.Shape.Y1+2), w, h, label)
		case "circle":
			fmt.Fprintf(&b, "circle(%d,%d,%d) # text={%s}\n", r.Shape.CX+1, r.Shape.CY+1, r.Shape.R, label)
		case "line":
			// DS9's projection is a two-vertex line with a width in image pixels. A polyline, or a width in arcsec (DS9 would need
			// the sky system to read it), has no exact image-coordinate DS9 shape: it is left as a comment, never approximated.
			p := r.Shape.Points
			wtxt := strconv.FormatFloat(r.Shape.Width, 'g', -1, 64)
			if len(p) == 2 && r.Shape.WidthUnit == "pixel" {
				fmt.Fprintf(&b, "# projection(%d,%d,%d,%d,%s) # text={%s}\n", p[0][0]+1, p[0][1]+1, p[1][0]+1, p[1][1]+1, wtxt, label)
			} else {
				fmt.Fprintf(&b, "# not written as a DS9 shape: line {%s} of %d vertices, width %s %s (DS9 has no polyline or angular width in image coordinates)\n", label, len(p), wtxt, r.Shape.WidthUnit)
			}
		}
	}
	return b.String()
}

// half renders n/2 as an integer or with .5.
func half(n int) string {
	if n%2 == 0 {
		return fmt.Sprintf("%d", n/2)
	}
	return fmt.Sprintf("%d.5", n/2)
}
