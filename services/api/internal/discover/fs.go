// Package discover finds the runs, data, configs and job templates in a project folder by good practice only: a run
// is a folder with a machine-readable run record, data is a file whose format describes itself, and so on. It never
// follows symbolic links out of the folder, never opens an archive, and never reads or stats anything marked as not to
// be opened. The scanner is pure: it sees the folder only through the small FS interface below, so the same code
// scans a folder on this computer and, through a host's worker, a folder on another machine.
package discover

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Entry is one name in a folder. ReadDir fills only Name, Dir and Link (what the directory itself says, no stat);
// Stat adds the size, the time and, for a link, where it points.
type Entry struct {
	Name         string `json:"name"`
	Dir          bool   `json:"dir"`
	Link         bool   `json:"link"` // a symbolic link: never followed
	LinkOutside  bool   `json:"linkOutside,omitempty"`
	LinkDangling bool   `json:"linkDangling,omitempty"`
	Size         int64  `json:"size"`
	MtimeNS      int64  `json:"mtimeNs"`
}

// FS reads a project folder. Paths are slash-separated and relative to the folder ("" is the folder itself); a path
// that leaves the folder is refused.
type FS interface {
	ReadDir(rel string) ([]Entry, error)
	Stat(rel string) (Entry, error)
	// ReadFile returns at most max bytes of a regular file inside the folder (never more than MaxReadBytes) and says
	// whether the file was longer.
	ReadFile(rel string, max int64) (data []byte, truncated bool, err error)
}

// MaxReadBytes is the most the scanner ever reads from one file.
const MaxReadBytes = 256 << 10

var ErrOutside = errors.New("path is outside the project folder")

func validRel(rel string) bool {
	if rel == "" {
		return true
	}
	if strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, p := range strings.Split(rel, "/") {
		if p == ".." || p == "" {
			return false
		}
	}
	return true
}

// LocalFS is a project folder on this computer. Root must be canonical (symlinks resolved).
type LocalFS struct{ Root string }

func (l LocalFS) abs(rel string) (string, error) {
	if !validRel(rel) {
		return "", ErrOutside
	}
	return filepath.Join(l.Root, filepath.FromSlash(rel)), nil
}

func (l LocalFS) ReadDir(rel string) ([]Entry, error) {
	p, err := l.abs(rel)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, d := range des { // d.Type() comes with the directory entry; Info() (a stat) is deliberately not called
		out = append(out, Entry{Name: d.Name(), Dir: d.IsDir(), Link: d.Type()&os.ModeSymlink != 0})
	}
	return out, nil
}

func (l LocalFS) inside(real string) bool {
	rel, err := filepath.Rel(l.Root, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (l LocalFS) Stat(rel string) (Entry, error) {
	p, err := l.abs(rel)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Name: path.Base(rel), Dir: fi.IsDir(), Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano()}
	if fi.Mode()&os.ModeSymlink != 0 {
		e.Link, e.Dir = true, false
		real, err := filepath.EvalSymlinks(p)
		switch {
		case err != nil:
			e.LinkDangling = true
		case !l.inside(real):
			e.LinkOutside = true
		}
	}
	return e, nil
}

func (l LocalFS) ReadFile(rel string, max int64) ([]byte, bool, error) {
	p, err := l.abs(rel)
	if err != nil {
		return nil, false, err
	}
	if max <= 0 || max > MaxReadBytes {
		max = MaxReadBytes
	}
	real, err := filepath.EvalSymlinks(p) // an intermediate link must not lead out of the folder either
	if err != nil || !l.inside(real) {
		return nil, false, ErrOutside
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

// MemFS is a folder whose listing is already in memory (a remote folder walked by its host's worker) and whose file
// contents are fetched on demand through Read.
type MemFS struct {
	dirs  map[string][]Entry
	stats map[string]Entry
	Read  func(rel string, max int64) ([]byte, bool, error)
}

func NewMemFS(read func(rel string, max int64) ([]byte, bool, error)) *MemFS {
	return &MemFS{dirs: map[string][]Entry{"": nil}, stats: map[string]Entry{}, Read: read}
}

// Add records one entry at a slash-separated relative path whose parent was added before it.
func (m *MemFS) Add(rel string, e Entry) {
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	e.Name = path.Base(rel)
	m.dirs[parent] = append(m.dirs[parent], Entry{Name: e.Name, Dir: e.Dir, Link: e.Link})
	m.stats[rel] = e
	if e.Dir {
		if _, ok := m.dirs[rel]; !ok {
			m.dirs[rel] = nil
		}
	}
}

func (m *MemFS) ReadDir(rel string) ([]Entry, error) {
	es, ok := m.dirs[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return es, nil
}

func (m *MemFS) Stat(rel string) (Entry, error) {
	e, ok := m.stats[rel]
	if !ok {
		return Entry{}, os.ErrNotExist
	}
	return e, nil
}

func (m *MemFS) ReadFile(rel string, max int64) ([]byte, bool, error) {
	if !validRel(rel) {
		return nil, false, ErrOutside
	}
	if max <= 0 || max > MaxReadBytes {
		max = MaxReadBytes
	}
	return m.Read(rel, max)
}
