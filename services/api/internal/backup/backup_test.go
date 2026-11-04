package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeDataDir(t *testing.T) (dir, db string) {
	t.Helper()
	dir = t.TempDir()
	db = filepath.Join(dir, dbName)
	c, err := sql.Open("sqlite", "file:"+db)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, name TEXT, path TEXT, host_id INTEGER, size_bytes INTEGER, modified_at INTEGER)`,
		`INSERT INTO datasets(name,path,host_id,size_bytes,modified_at) VALUES('hi.fits','/data/hi.fits',0,100,5)`,
		`INSERT INTO datasets(name,path,host_id,size_bytes,modified_at) VALUES('psi.fits','` + filepath.Join(dir, "derived", "psi.fits") + `',0,10,6)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string]string{"figures/aa.png": "png-bytes", "derived/psi.fits": "derived-bytes", "managed/copy.fits": "managed-bytes", "figures/.tmp-x": "partial"} {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o444)
	}
	return
}

func TestExportThenRestoreIntoAnEmptyDirectory(t *testing.T) {
	dir, db := makeDataDir(t)
	var buf bytes.Buffer
	m, err := Export(db, dir, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 1 || len(m.Files) != 4 { // the catalog and three data files; the half-written one is skipped
		t.Fatalf("manifest: %+v", m)
	}
	if len(m.References) != 1 || m.References[0].Path != "/data/hi.fits" {
		t.Fatalf("only files outside the data directory are references: %+v", m.References)
	}
	into := t.TempDir()
	if _, err := Restore(bytes.NewReader(buf.Bytes()), into); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"figures/aa.png": "png-bytes", "derived/psi.fits": "derived-bytes", "managed/copy.fits": "managed-bytes"} {
		b, err := os.ReadFile(filepath.Join(into, rel))
		if err != nil || string(b) != want {
			t.Errorf("%s: %v %q", rel, err, b)
		}
	}
	if st, _ := os.Stat(filepath.Join(into, "managed/copy.fits")); st.Mode().Perm() != 0o444 {
		t.Errorf("a managed copy stays read-only: %v", st.Mode().Perm())
	}
	c, _ := sql.Open("sqlite", "file:"+filepath.Join(into, dbName))
	defer c.Close()
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 2 {
		t.Errorf("restored catalog: %v %d", err, n)
	}
	if _, err := Restore(bytes.NewReader(buf.Bytes()), into); err != ErrExists {
		t.Errorf("restoring over a catalog must be refused, got %v", err)
	}
}

func rewrite(t *testing.T, archive []byte, edit func(name string, body []byte) (string, []byte)) []byte {
	t.Helper()
	gz, _ := gzip.NewReader(bytes.NewReader(archive))
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	ogz := gzip.NewWriter(&out)
	tw := tar.NewWriter(ogz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b := make([]byte, h.Size)
		tr.Read(b)
		n, nb := edit(h.Name, b)
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: int64(len(nb))})
		tw.Write(nb)
	}
	tw.Close()
	ogz.Close()
	return out.Bytes()
}

func TestDamagedOrHostileArchivesAreRefusedAndLeaveNothingBehind(t *testing.T) {
	dir, db := makeDataDir(t)
	var buf bytes.Buffer
	if _, err := Export(db, dir, &buf); err != nil {
		t.Fatal(err)
	}
	damaged := rewrite(t, buf.Bytes(), func(n string, b []byte) (string, []byte) {
		if n == "derived/psi.fits" {
			return n, []byte("derived-BYTES")
		}
		return n, b
	})
	into := t.TempDir()
	if _, err := Restore(bytes.NewReader(damaged), into); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a damaged file must be caught: %v", err)
	}
	if _, err := os.Stat(filepath.Join(into, dbName)); err == nil {
		t.Fatal("a failed restore must not leave a catalog behind")
	}
	traversal := rewrite(t, buf.Bytes(), func(n string, b []byte) (string, []byte) {
		if n == manifestName {
			return n, []byte(strings.ReplaceAll(string(b), "figures/aa.png", "../evil"))
		}
		return n, b
	})
	if _, err := Restore(bytes.NewReader(traversal), t.TempDir()); err == nil {
		t.Fatal("an entry that climbs out of the directory must be refused")
	}
	if _, err := Restore(strings.NewReader("not an archive"), t.TempDir()); err == nil {
		t.Fatal("garbage must be refused")
	}
}
