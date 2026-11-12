// Package backup writes and restores a whole Vesqira data directory as one archive: a consistent copy of the catalog,
// the figure previews, derived results and managed copies, and a manifest with a checksum for each file and a list of
// the external files the catalog only refers to. References are not copied: a path is not the file, and the manifest
// says so.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const (
	FormatVersion = 1
	dbName        = "vesqira.db"
	manifestName  = "manifest.json"
	maxTotal      = 64 << 30
)

var dataDirs = []string{"figures", "derived", "managed"}

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// Reference is a file the catalog points at but the archive does not contain.
type Reference struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	HostID     int64  `json:"hostId"` // 0 is this computer
	SizeBytes  int64  `json:"sizeBytes"`
	ModifiedNS int64  `json:"modifiedNs"`
}

type Manifest struct {
	FormatVersion int         `json:"formatVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
	SchemaVersion int         `json:"schemaVersion"`
	Files         []File      `json:"files"`
	References    []Reference `json:"references"`
	Note          string      `json:"note"`
}

// Export writes the archive. The catalog is copied with VACUUM INTO, which is consistent while the server runs; the manifest lists
// the files the datasets of workspace ws point at.
func Export(ws workspace.ID, dbPath, dataDir string, out io.Writer) (Manifest, error) {
	tmp, err := os.MkdirTemp("", "vesqira-export-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbName)
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return Manifest{}, err
	}
	defer db.Close()
	if _, err := db.Exec(`VACUUM INTO ?`, snap); err != nil {
		return Manifest{}, fmt.Errorf("could not copy the catalog: %w", err)
	}
	m := Manifest{FormatVersion: FormatVersion, CreatedAt: time.Now().UTC(),
		Note: "References are files the catalog points at; they are not in this archive. On another computer they resolve only if the same paths exist, otherwise register them again."}
	sdb, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		return m, err
	}
	defer sdb.Close()
	_ = sdb.QueryRow(`PRAGMA user_version`).Scan(&m.SchemaVersion)
	if rows, err := sdb.Query(`SELECT name,path,host_id,size_bytes,modified_at FROM datasets WHERE workspace_id=? ORDER BY id`, ws); err == nil {
		for rows.Next() {
			var r Reference
			if rows.Scan(&r.Name, &r.Path, &r.HostID, &r.SizeBytes, &r.ModifiedNS) == nil && !inside(r.Path, dataDir) {
				m.References = append(m.References, r)
			}
		}
		rows.Close()
	}

	type item struct{ rel, abs string }
	items := []item{{dbName, snap}}
	for _, d := range dataDirs {
		root := filepath.Join(dataDir, d)
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
				return nil
			}
			rel, _ := filepath.Rel(dataDir, p)
			items = append(items, item{filepath.ToSlash(rel), p})
			return nil
		})
	}
	sort.Slice(items[1:], func(i, j int) bool { return items[1+i].rel < items[1+j].rel })
	for _, it := range items {
		f, err := describe(it.rel, it.abs)
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, f)
	}

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(mb)), ModTime: m.CreatedAt}); err != nil {
		return m, err
	}
	if _, err := tw.Write(mb); err != nil {
		return m, err
	}
	for i, it := range items {
		if err := copyIn(tw, it.abs, m.Files[i]); err != nil {
			return m, err
		}
	}
	if err := tw.Close(); err != nil {
		return m, err
	}
	return m, gz.Close()
}

func inside(p, dir string) bool {
	r, err := filepath.Rel(dir, p)
	return err == nil && !strings.HasPrefix(r, "..")
}

func describe(rel, abs string) (File, error) {
	f, err := os.Open(abs)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return File{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return File{}, err
	}
	return File{Path: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Mode: uint32(st.Mode().Perm())}, nil
}

func copyIn(tw *tar.Writer, abs string, want File) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Name: want.Path, Mode: int64(want.Mode), Size: want.Size, ModTime: time.Now()}); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(f, want.Size))
	if err != nil {
		return err
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%s changed while it was being archived; run the export again", want.Path)
	}
	return nil
}

var ErrExists = errors.New("the data directory already holds a catalog; restore only into an empty one")

// Restore unpacks an archive into an empty data directory. Every file is checked against the manifest, nothing is
// written outside the directory, and a failure leaves no catalog behind.
func Restore(in io.Reader, dataDir string) (m Manifest, err error) {
	if _, err := os.Stat(filepath.Join(dataDir, dbName)); err == nil {
		return m, ErrExists
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		return m, errors.New("this is not a Vesqira archive")
	}
	tr := tar.NewReader(gz)
	hd, err := tr.Next()
	if err != nil || hd.Name != manifestName || hd.Size > 64<<20 {
		return m, errors.New("this is not a Vesqira archive (no manifest first)")
	}
	mb, err := io.ReadAll(io.LimitReader(tr, hd.Size))
	if err != nil || json.Unmarshal(mb, &m) != nil || m.FormatVersion != FormatVersion {
		return Manifest{}, fmt.Errorf("unsupported archive (format version %d, this program reads %d)", m.FormatVersion, FormatVersion)
	}
	want := map[string]File{}
	var total int64
	for _, f := range m.Files {
		if !safe(f.Path) {
			return Manifest{}, fmt.Errorf("refusing an archive entry named %q", f.Path)
		}
		total += f.Size
		want[f.Path] = f
	}
	if total > maxTotal {
		return Manifest{}, errors.New("the archive is larger than this program restores")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return m, err
	}
	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				os.Remove(p)
			}
		}
	}()
	seen := 0
	for {
		h, nerr := tr.Next()
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			return Manifest{}, nerr
		}
		f, ok := want[h.Name]
		if !ok || h.Typeflag != tar.TypeReg || h.Size != f.Size {
			return Manifest{}, fmt.Errorf("the archive holds %q, which its manifest does not list", h.Name)
		}
		dst := filepath.Join(dataDir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return Manifest{}, err
		}
		tmp := dst + ".part"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return Manifest{}, err
		}
		written = append(written, tmp)
		sum := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(out, sum), io.LimitReader(tr, f.Size))
		if cerr == nil {
			cerr = out.Close()
		} else {
			out.Close()
		}
		if cerr != nil || n != f.Size || hex.EncodeToString(sum.Sum(nil)) != f.SHA256 {
			return Manifest{}, fmt.Errorf("%s is damaged in the archive (checksum does not match)", f.Path)
		}
		if err := os.Chmod(tmp, os.FileMode(f.Mode)&0o755|0o400); err != nil {
			return Manifest{}, err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return Manifest{}, err
		}
		written = append(written, dst)
		seen++
	}
	if seen != len(m.Files) {
		return Manifest{}, fmt.Errorf("the archive is incomplete: %d of %d files", seen, len(m.Files))
	}
	return m, nil
}

func safe(p string) bool {
	if p == dbName {
		return true
	}
	c := path.Clean(p)
	if c != p || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\\") {
		return false
	}
	for _, d := range dataDirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}
