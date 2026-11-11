package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type edEnv struct {
	ts    *httptest.Server
	root  string
	other string // a sibling folder outside the roots
	au    *audit.Store
}

func newEditor(t *testing.T, enabled bool) *edEnv {
	t.Helper()
	base := t.TempDir()
	root, other := filepath.Join(base, "data"), filepath.Join(base, "data2")
	for _, d := range []string{root, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	au, err := audit.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Roots: rs, Store: st, Worker: &fakeWorker{}, Audit: au, EditorEnabled: enabled}).Handler())
	t.Cleanup(ts.Close)
	return &edEnv{ts: ts, root: root, other: other, au: au}
}

func (e *edEnv) do(t *testing.T, method, url string, body any, hdr map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *edEnv) open(t *testing.T, path string) (int, textFile) {
	t.Helper()
	code, b := e.do(t, "GET", e.ts.URL+"/api/files/text?path="+urlEscape(path), nil, nil)
	var f textFile
	_ = json.Unmarshal(b, &f)
	return code, f
}

func (e *edEnv) save(t *testing.T, path, content string, base textVersion) (int, []byte) {
	t.Helper()
	return e.do(t, "PUT", e.ts.URL+"/api/files/text", textSave{Path: path, Content: content, Base: base}, nil)
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", " ", "%20", "&", "%26", "#", "%23", "+", "%2B").Replace(s)
}

func write(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestEditorIsOffByDefaultAndReportedInCapabilities(t *testing.T) {
	off := newEditor(t, false)
	p := filepath.Join(off.root, "a.toml")
	write(t, p, []byte("x = 1\n"), 0o644)
	if code, _ := off.do(t, "GET", off.ts.URL+"/api/files/text?path="+p, nil, nil); code != 404 {
		t.Errorf("GET with the editor off: %d", code)
	}
	if code, _ := off.save(t, p, "x = 2\n", textVersion{Mtime: "1", Sha256: "a"}); code != 404 && code != 405 {
		t.Errorf("PUT with the editor off: %d", code)
	}
	if b, _ := os.ReadFile(p); string(b) != "x = 1\n" {
		t.Error("the file changed with the editor off")
	}
	feature := func(e *edEnv) bool {
		_, b := e.do(t, "GET", e.ts.URL+"/api/capabilities", nil, nil)
		var c struct{ Features map[string]bool }
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		return c.Features["editor"]
	}
	if feature(off) {
		t.Error("capabilities say the editor is on")
	}
	if !feature(newEditor(t, true)) {
		t.Error("capabilities do not say the editor is on")
	}
}

func TestEditorOpensAndSavesATextFileInsideTheRoots(t *testing.T) {
	e := newEditor(t, true)
	p := filepath.Join(e.root, "pipeline.toml")
	write(t, p, []byte("ker = 8\n"), 0o640)
	code, f := e.open(t, p)
	if code != 200 || f.Content != "ker = 8\n" || f.Size != 8 || f.Mtime == "" || len(f.Sha256) != 64 || f.Name != "pipeline.toml" {
		t.Fatalf("open: %d %+v", code, f)
	}
	code, b := e.save(t, p, "ker = 16\n", f.textVersion)
	if code != 200 {
		t.Fatalf("save: %d %s", code, b)
	}
	var saved textFile
	_ = json.Unmarshal(b, &saved)
	if got, _ := os.ReadFile(p); string(got) != "ker = 16\n" {
		t.Errorf("on disk: %q", got)
	}
	if saved.Content != "ker = 16\n" || saved.Sha256 == f.Sha256 {
		t.Errorf("answer: %+v", saved)
	}
	// the answer is the new base: a second save from it works, a repeat of the old base does not
	if code, _ := e.save(t, p, "ker = 32\n", saved.textVersion); code != 200 {
		t.Errorf("second save from the new base: %d", code)
	}
	if code, _ := e.save(t, p, "ker = 1\n", f.textVersion); code != 409 {
		t.Errorf("save from the old base: %d", code)
	}
}

func TestEditorRefusesPathsOutsideTheRootsDotDotAndSymlinkEscapes(t *testing.T) {
	e := newEditor(t, true)
	outside := filepath.Join(e.other, "secret.txt")
	write(t, outside, []byte("private\n"), 0o600)
	if err := os.Symlink(outside, filepath.Join(e.root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.other, filepath.Join(e.root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(e.root, "ok.txt")
	write(t, inside, []byte("ok\n"), 0o644)
	for name, p := range map[string]string{
		"outside":        outside,
		"sibling prefix": e.root + "2/secret.txt",
		"dotdot":         filepath.Join(e.root, "..", "data2", "secret.txt"),
		"file symlink":   filepath.Join(e.root, "link.txt"),
		"dir symlink":    filepath.Join(e.root, "dirlink", "secret.txt"),
		"relative":       "ok.txt",
		"empty":          "",
		"directory":      e.root,
		"missing":        filepath.Join(e.root, "nope.txt"),
	} {
		if code, _ := e.open(t, p); code != 404 {
			t.Errorf("GET %s: %d", name, code)
		}
		if code, _ := e.save(t, p, "owned\n", textVersion{Mtime: "1", Sha256: "a"}); code != 404 {
			t.Errorf("PUT %s: %d", name, code)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "private\n" {
		t.Error("a file outside the roots was written")
	}
	if _, err := os.Stat(filepath.Join(e.root, "nope.txt")); err == nil {
		t.Error("a save created a file")
	}
}

func TestEditorRefusesCredentialFolders(t *testing.T) {
	e := newEditor(t, true)
	if err := os.Mkdir(filepath.Join(e.root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(e.root, ".ssh", "authorized_keys")
	write(t, p, []byte("ssh-ed25519 AAAA\n"), 0o600)
	if code, _ := e.open(t, p); code != 404 {
		t.Errorf("GET %d", code)
	}
}

func TestEditorRefusesBinaryFitsOversizeAndUnknownKinds(t *testing.T) {
	e := newEditor(t, true)
	mk := func(name string, b []byte) string {
		p := filepath.Join(e.root, name)
		write(t, p, b, 0o644)
		return p
	}
	fitsCard := []byte("SIMPLE  =                    T / conforms to FITS standard")
	big := bytes.Repeat([]byte("a"), maxTextFile+1)
	for name, c := range map[string]struct {
		p    string
		want int
	}{
		"nul bytes":         {mk("bin.txt", []byte("abc\x00def")), 415},
		"invalid utf8":      {mk("bad.txt", []byte{0xff, 0xfe, 'a'}), 415},
		"fits extension":    {mk("cube.fits", []byte("not really")), 415},
		"fits fz":           {mk("cube.fz", []byte("x")), 415},
		"fits magic as txt": {mk("disguised.txt", fitsCard), 415},
		"fits magic no ext": {mk("disguised", fitsCard), 415},
		"unknown extension": {mk("a.png", []byte("x")), 415},
		"oversize":          {mk("big.txt", big), 413},
		"dotfile extension": {mk(".bashrc", []byte("x")), 415},
	} {
		if code, _ := e.open(t, c.p); code != c.want {
			t.Errorf("GET %s: %d, want %d", name, code, c.want)
		}
		if code, _ := e.save(t, c.p, "x\n", textVersion{Mtime: "1", Sha256: "a"}); code != c.want {
			t.Errorf("PUT %s: %d, want %d", name, code, c.want)
		}
	}
	// a symlink named like text that points at a FITS file inside the root is judged by the real file
	if err := os.Symlink(filepath.Join(e.root, "cube.fits"), filepath.Join(e.root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.open(t, filepath.Join(e.root, "notes.txt")); code != 415 {
		t.Errorf("link to FITS: %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "cube.fits")); string(b) != "not really" {
		t.Error("FITS file was touched")
	}
	// what is sent is checked as well
	ok := filepath.Join(e.root, "ok.md")
	write(t, ok, []byte("hi\n"), 0o644)
	_, f := e.open(t, ok)
	for name, content := range map[string]string{"nul": "a\x00b", "fits magic": string(fitsCard), "oversize": string(big)} {
		code, _ := e.save(t, ok, content, f.textVersion)
		if code != 415 && code != 413 {
			t.Errorf("PUT %s content: %d", name, code)
		}
	}
	if b, _ := os.ReadFile(ok); string(b) != "hi\n" {
		t.Errorf("file after refused saves: %q", b)
	}
}

func TestEditorStaleBaseIsA409WithTheCurrentContentAndNothingIsWritten(t *testing.T) {
	e := newEditor(t, true)
	p := filepath.Join(e.root, "job.sh")
	write(t, p, []byte("echo one\n"), 0o755)
	_, opened := e.open(t, p)
	write(t, p, []byte("echo two, by another program\n"), 0o755)
	code, b := e.save(t, p, "echo mine\n", opened.textVersion)
	if code != 409 {
		t.Fatalf("stale save: %d %s", code, b)
	}
	var c textConflict
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Current.Content != "echo two, by another program\n" || c.Current.Sha256 == opened.Sha256 || c.Error == "" {
		t.Errorf("conflict body: %+v", c)
	}
	if got, _ := os.ReadFile(p); string(got) != "echo two, by another program\n" {
		t.Errorf("a refused save wrote: %q", got)
	}
	// keep mine: resend with the base the conflict returned
	if code, b := e.save(t, p, "echo mine\n", c.Current.textVersion); code != 200 {
		t.Errorf("resend with the new base: %d %s", code, b)
	}
	if got, _ := os.ReadFile(p); string(got) != "echo mine\n" {
		t.Errorf("after keep mine: %q", got)
	}
	// the same size and bytes' hash differ, and so does a base without the hash
	if code, _ := e.save(t, p, "x\n", textVersion{Size: 10, Mtime: "1"}); code != 400 {
		t.Errorf("base without a hash: %d", code)
	}
	// the file removed after it was opened is not recreated
	os.Remove(p)
	if code, _ := e.save(t, p, "back\n", c.Current.textVersion); code != 404 {
		t.Errorf("save of a removed file: %d", code)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("a save recreated a removed file")
	}
}

func TestEditorSaveIsAtomicAndKeepsTheMode(t *testing.T) {
	e := newEditor(t, true)
	p := filepath.Join(e.root, "run.slurm")
	write(t, p, []byte("#!/bin/bash\n"), 0o750)
	_, f := e.open(t, p)
	if code, b := e.save(t, p, "#!/bin/bash\necho hi\n", f.textVersion); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Errorf("mode after save: %v", fi.Mode().Perm())
	}
	des, _ := os.ReadDir(e.root)
	for _, d := range des {
		if d.Name() != "run.slurm" {
			t.Errorf("a temporary file was left behind: %s", d.Name())
		}
	}
}

func TestEditorSaveIsAuditedWithHashesAndNeverTheContent(t *testing.T) {
	e := newEditor(t, true)
	p := filepath.Join(e.root, "notes.md")
	write(t, p, []byte("before secret-token\n"), 0o644)
	_, f := e.open(t, p)
	code, b := e.save(t, p, "after hunter2\n", f.textVersion)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var saved textFile
	_ = json.Unmarshal(b, &saved)
	es, err := e.au.List("file", 0, 10)
	if err != nil || len(es) != 1 {
		t.Fatalf("audit: %v %+v", err, es)
	}
	d := es[0].Detail
	for _, want := range []string{f.Sha256, saved.Sha256, p} {
		if !strings.Contains(d, want) {
			t.Errorf("audit detail lacks %q: %s", want, d)
		}
	}
	for _, leaked := range []string{"secret-token", "hunter2", "before", "after"} {
		if strings.Contains(d, leaked) {
			t.Errorf("audit detail contains content %q: %s", leaked, d)
		}
	}
	// a refused save is not a save
	e.save(t, p, "again\n", f.textVersion)
	if es, _ := e.au.List("file", 0, 10); len(es) != 1 {
		t.Errorf("a refused save was audited as a save: %d entries", len(es))
	}
}

func TestEditorRefusesCrossSiteAndForeignHostRequests(t *testing.T) {
	e := newEditor(t, true)
	p := filepath.Join(e.root, "a.toml")
	write(t, p, []byte("x = 1\n"), 0o644)
	_, f := e.open(t, p)
	body := textSave{Path: p, Content: "x = 2\n", Base: f.textVersion}
	for name, h := range map[string]map[string]string{
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"same-site":    {"Sec-Fetch-Site": "same-site"},
		"foreign host": {"Host": "evil.example"},
		"other origin": {"Origin": "https://evil.example"},
		"other port":   {"Origin": "http://127.0.0.1:1"},
	} {
		if code, _ := e.do(t, "PUT", e.ts.URL+"/api/files/text", body, h); code != 403 {
			t.Errorf("PUT %s: %d", name, code)
		}
		if _, originOnly := h["Origin"]; originOnly {
			continue // the Origin rule guards writes; a cross-origin read is stopped by Sec-Fetch-Site above
		}
		if code, _ := e.do(t, "GET", e.ts.URL+"/api/files/text?path="+p, nil, h); code != 403 {
			t.Errorf("GET %s: %d", name, code)
		}
	}
	// a form post cannot write: PUT must be JSON like every other write
	req, _ := http.NewRequest("PUT", e.ts.URL+"/api/files/text", strings.NewReader("path="+p))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("PUT as text/plain: %d", resp.StatusCode)
	}
	if b, _ := os.ReadFile(p); string(b) != "x = 1\n" {
		t.Errorf("a refused request wrote: %q", b)
	}
	// the same-origin request the app makes is accepted
	if code, b := e.do(t, "PUT", e.ts.URL+"/api/files/text", body, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": e.ts.URL}); code != 200 {
		t.Errorf("same-origin PUT: %d %s", code, b)
	}
}
