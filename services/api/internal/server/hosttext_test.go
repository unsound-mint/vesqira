package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// hostEd is the editor with a host behind it: a fake ssh that runs the host's commands on this machine, a host whose one
// declared root is a folder here, and a project opened on that host.
type hostEd struct {
	*edEnv
	root, outside string
	hostID        int64
	projID        int64
}

func newHostEditor(t *testing.T) *hostEd {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, "remote-home")
	root, outside, local := filepath.Join(base, "remote_root"), filepath.Join(base, "elsewhere"), filepath.Join(base, "local")
	for _, d := range []string{home, root, outside, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mgr := hosts.NewManager(fakeSSH(t, home), nil)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	ps, _ := projects.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rs, _ := sourceroot.New([]string{local})
	h, err := hs.Create(hosts.Host{Name: "Cluster login", Dest: "login1", Python: "python3", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := ps.Touch(h.ID, filepath.Join(root, "proj"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Roots: rs, Store: cat, Worker: &fakeWorker{}, Audit: au, EditorEnabled: true, HostDB: hs, Remote: mgr, Projects: ps}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hostEd{edEnv: &edEnv{ts: ts, root: local, other: outside, au: au}, root: root, outside: outside, hostID: h.ID, projID: pr.ID}
}

func (e *hostEd) openH(t *testing.T, path string) (int, textFile, []byte) {
	t.Helper()
	code, b := e.do(t, "GET", e.ts.URL+"/api/files/text?hostId="+strconv.FormatInt(e.hostID, 10)+"&path="+url.QueryEscape(path), nil, nil)
	var f textFile
	_ = json.Unmarshal(b, &f)
	return code, f, b
}

func (e *hostEd) saveH(t *testing.T, path, content string, base textVersion) (int, []byte) {
	t.Helper()
	return e.do(t, "PUT", e.ts.URL+"/api/files/text", textSave{Path: path, Content: content, Base: base, HostID: e.hostID}, nil)
}

// putText is write, making the folders first.
func putText(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, p, b, mode)
}

func TestHostEditorOpensAndSavesAndKeepsTheMode(t *testing.T) {
	e := newHostEditor(t)
	p := filepath.Join(e.root, "proj", "configs", "pipeline.toml")
	putText(t, p, []byte("ker = 8\n"), 0o664)
	code, f, b := e.openH(t, p)
	if code != 200 || f.Content != "ker = 8\n" || f.Path != p || f.Name != "pipeline.toml" || f.HostID != e.hostID || f.Host != "Cluster login" || f.Sha256 != sha256Hex([]byte("ker = 8\n")) || f.Size != 8 || f.Mtime == "" {
		t.Fatalf("%d %s", code, b)
	}
	code, b = e.saveH(t, p, "ker = 16\n", f.textVersion)
	var saved textFile
	_ = json.Unmarshal(b, &saved)
	if code != 200 || saved.Sha256 != sha256Hex([]byte("ker = 16\n")) || saved.Host != "Cluster login" {
		t.Fatalf("%d %s", code, b)
	}
	if got, _ := os.ReadFile(p); string(got) != "ker = 16\n" {
		t.Fatalf("%q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o664 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// the answer is the next base
	if code, b = e.saveH(t, p, "ker = 32\n", saved.textVersion); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
}

func TestHostEditorStaleShaIsA409ShowingTheCurrentFile(t *testing.T) {
	e := newHostEditor(t)
	p := filepath.Join(e.root, "job.sh")
	putText(t, p, []byte("one\n"), 0o755)
	_, f, _ := e.openH(t, p)
	putText(t, p, []byte("two on the host\n"), 0o755)
	code, b := e.saveH(t, p, "mine\n", f.textVersion)
	var c textConflict
	_ = json.Unmarshal(b, &c)
	if code != 409 || c.Current.Content != "two on the host\n" || c.Current.Sha256 != sha256Hex([]byte("two on the host\n")) || c.Current.Host != "Cluster login" || c.Error == "" {
		t.Fatalf("%d %s", code, b)
	}
	if got, _ := os.ReadFile(p); string(got) != "two on the host\n" {
		t.Fatal("a stale save wrote")
	}
	if es, _ := e.au.List("host", e.hostID, 10); len(es) != 1 { // the open only
		t.Fatalf("a conflict is not a save: %+v", es)
	}
	// keep mine: resend with the base the conflict returned
	if code, b = e.saveH(t, p, "mine\n", c.Current.textVersion); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
}

func TestHostEditorRefusesEscapesDotDotLinksAndOutsideTheRoots(t *testing.T) {
	e := newHostEditor(t)
	putText(t, filepath.Join(e.outside, "secret.toml"), []byte("s = 1\n"), 0o644)
	putText(t, filepath.Join(e.root, "ok.toml"), []byte("ok\n"), 0o644)
	os.Symlink(filepath.Join(e.outside, "secret.toml"), filepath.Join(e.root, "leak.toml"))
	os.Symlink(e.outside, filepath.Join(e.root, "door"))
	os.MkdirAll(filepath.Join(e.root, "adir.toml"), 0o755)
	for name, p := range map[string]string{
		"outside the root": filepath.Join(e.outside, "secret.toml"),
		"dot dot":          e.root + "/../elsewhere/secret.toml",
		"file link":        filepath.Join(e.root, "leak.toml"),
		"folder link":      filepath.Join(e.root, "door", "secret.toml"),
		"a folder":         filepath.Join(e.root, "adir.toml"),
		"the root":         e.root,
		"relative":         "ok.toml",
		"missing":          filepath.Join(e.root, "missing.toml"),
		"credentials":      filepath.Join(e.root, ".ssh", "config"),
	} {
		if code, _, b := e.openH(t, p); code != 404 {
			t.Errorf("open %s: %d %s", name, code, b)
		}
		if code, b := e.saveH(t, p, "pwned\n", textVersion{Size: 1, Mtime: "1", Sha256: sha256Hex([]byte("s = 1\n"))}); code != 404 {
			t.Errorf("save %s: %d %s", name, code, b)
		}
	}
	for _, bad := range []string{"a\\b.toml", "a\nb.toml", "a\rb.toml", "a\x00b.toml"} {
		if code, _, b := e.openH(t, filepath.Join(e.root, bad)); code != 400 {
			t.Errorf("open %q: %d %s", bad, code, b)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(e.outside, "secret.toml")); string(got) != "s = 1\n" {
		t.Fatal("a refused save wrote outside the root")
	}
	if code, _, b := e.openH(t, filepath.Join(e.root, "ok.toml")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
}

func TestHostEditorRefusesBinaryFitsOversizeAndUnknownKinds(t *testing.T) {
	e := newHostEditor(t)
	w := func(name string, b []byte) string {
		p := filepath.Join(e.root, name)
		putText(t, p, b, 0o644)
		return p
	}
	for name, c := range map[string]struct {
		path string
		want int
	}{
		"nul":        {w("a.txt", []byte("ab\x00cd")), 415},
		"utf8":       {w("b.txt", []byte{0xff, 0xfe, 'a'}), 415},
		"fits ext":   {w("c.fits", []byte("plain")), 415},
		"fits magic": {w("d", []byte("SIMPLE  =                    T / conforms\n")), 415},
		"extension":  {w("e.png", []byte("text")), 415},
		"too large":  {w("f.log", []byte(strings.Repeat("x", maxTextFile+1))), 413},
	} {
		if code, _, b := e.openH(t, c.path); code != c.want {
			t.Errorf("open %s: %d %s", name, code, b)
		}
		if code, b := e.saveH(t, c.path, "text", textVersion{Size: 1, Mtime: "1", Sha256: sha256Hex([]byte("x"))}); code != c.want && code != 409 {
			t.Errorf("save %s: %d %s", name, code, b)
		}
	}
	// what is sent is checked as well
	p := w("ok.txt", []byte("fine\n"))
	_, f, _ := e.openH(t, p)
	for name, body := range map[string]string{"nul": "a\x00b", "fits": "SIMPLE  =                    T", "big": strings.Repeat("y", maxTextFile+1)} {
		if code, b := e.saveH(t, p, body, f.textVersion); code != 415 && code != 413 {
			t.Errorf("save content %s: %d %s", name, code, b)
		}
	}
	if got, _ := os.ReadFile(p); string(got) != "fine\n" {
		t.Fatalf("%q", got)
	}
	// a file that became binary after it was opened is not shown as a conflict
	putText(t, p, []byte{0, 1, 2}, 0o644)
	if code, b := e.saveH(t, p, "mine\n", f.textVersion); code != 415 {
		t.Errorf("%d %s", code, b)
	}
}

func TestHostEditorRefusesRawAndExternalDataButEditsOtherRoles(t *testing.T) {
	e := newHostEditor(t)
	for _, rel := range []string{"proj/data/raw/notes.txt", "data/external/ref.md", "proj/Data/RAW/x.txt"} {
		putText(t, filepath.Join(e.root, rel), []byte("x\n"), 0o644)
		if code, _, b := e.openH(t, filepath.Join(e.root, rel)); code != 403 {
			t.Errorf("%s: %d %s", rel, code, b)
		}
		if code, b := e.saveH(t, filepath.Join(e.root, rel), "y\n", textVersion{Size: 2, Mtime: "1", Sha256: sha256Hex([]byte("x\n"))}); code != 403 {
			t.Errorf("save %s: %d %s", rel, code, b)
		}
		if got, _ := os.ReadFile(filepath.Join(e.root, rel)); string(got) != "x\n" {
			t.Errorf("%s was written", rel)
		}
	}
	putText(t, filepath.Join(e.root, "proj/data/processed/notes.txt"), []byte("x\n"), 0o644)
	if code, _, b := e.openH(t, filepath.Join(e.root, "proj/data/processed/notes.txt")); code != 200 {
		t.Errorf("%d %s", code, b)
	}
}

func TestHostEditorNeverCreatesAFile(t *testing.T) {
	e := newHostEditor(t)
	p := filepath.Join(e.root, "new", "file.toml")
	if code, b := e.saveH(t, p, "x\n", textVersion{Size: 1, Mtime: "1", Sha256: sha256Hex([]byte("x"))}); code != 404 {
		t.Fatalf("%d %s", code, b)
	}
	if ents, _ := os.ReadDir(e.root); len(ents) != 0 {
		t.Fatalf("something was created: %v", ents)
	}
	// a file removed after it was opened is not recreated
	q := filepath.Join(e.root, "gone.toml")
	putText(t, q, []byte("a\n"), 0o644)
	_, f, _ := e.openH(t, q)
	os.Remove(q)
	if code, b := e.saveH(t, q, "b\n", f.textVersion); code != 404 {
		t.Fatalf("%d %s", code, b)
	}
	if _, err := os.Stat(q); err == nil {
		t.Fatal("recreated")
	}
}

func TestHostEditorAuditsPathAndHashesNeverContent(t *testing.T) {
	e := newHostEditor(t)
	p := filepath.Join(e.root, "notes.md")
	putText(t, p, []byte("before secret-token\n"), 0o644)
	_, f, _ := e.openH(t, p)
	code, b := e.saveH(t, p, "after hunter2\n", f.textVersion)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var saved textFile
	_ = json.Unmarshal(b, &saved)
	es, err := e.au.List("host", e.hostID, 10)
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %+v", err, es)
	}
	var opened, savedE string
	for _, a := range es {
		switch a.Action {
		case "file opened for editing on host":
			opened = a.Detail
		case "file saved on host":
			savedE = a.Detail
		}
	}
	for what, want := range map[string][]string{opened: {f.Sha256, p, "Cluster login"}, savedE: {f.Sha256, saved.Sha256, p, "Cluster login"}} {
		for _, w := range want {
			if !strings.Contains(what, w) {
				t.Errorf("audit detail lacks %q: %q", w, what)
			}
		}
	}
	for _, d := range []string{opened, savedE} {
		for _, leaked := range []string{"secret-token", "hunter2", "before", "after"} {
			if strings.Contains(d, leaked) {
				t.Errorf("audit detail contains content %q: %s", leaked, d)
			}
		}
	}
	// a refused open or save is not recorded
	e.saveH(t, p, "again\n", f.textVersion)
	e.openH(t, filepath.Join(e.root, "missing.md"))
	if es, _ := e.au.List("host", e.hostID, 10); len(es) != 2 {
		t.Errorf("a refusal was audited: %d entries", len(es))
	}
}

func TestHostEditorHostileFileNames(t *testing.T) {
	e := newHostEditor(t)
	t.Chdir(filepath.Dir(e.root))
	name := "run $(touch PWNED) 'one' \"two\" `touch PWNED`;&|<>.json"
	p := filepath.Join(e.root, "a b", "it's $(touch PWNED)", name)
	putText(t, p, []byte("{}\n"), 0o644)
	code, f, b := e.openH(t, p)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := e.saveH(t, p, "{ }\n", f.textVersion); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.root), "PWNED")); err == nil {
		t.Fatal("a name was executed as shell syntax")
	}
}

func TestHostEditorNeedsAKnownHostAndTheRemoteFeature(t *testing.T) {
	e := newHostEditor(t)
	p := filepath.Join(e.root, "a.toml")
	putText(t, p, []byte("a\n"), 0o644)
	if code, b := e.do(t, "GET", e.ts.URL+"/api/files/text?hostId=999&path="+urlEscape(p), nil, nil); code != 404 {
		t.Errorf("unknown host: %d %s", code, b)
	}
	if code, b := e.do(t, "GET", e.ts.URL+"/api/files/text?hostId=x&path="+urlEscape(p), nil, nil); code != 400 {
		t.Errorf("bad host: %d %s", code, b)
	}
	plain := newEditor(t, true) // no hosts configured
	if code, b := plain.do(t, "GET", plain.ts.URL+"/api/files/text?hostId=1&path="+urlEscape(p), nil, nil); code != 501 {
		t.Errorf("no remote: %d %s", code, b)
	}
	// a path of this computer is not readable through a host and the other way round
	local := filepath.Join(e.edEnv.root, "l.toml")
	putText(t, local, []byte("l\n"), 0o644)
	if code, _, b := e.openH(t, local); code != 404 {
		t.Errorf("a local path through a host: %d %s", code, b)
	}
	// the same-origin guards cover the host variants: a cross-site save is refused
	if code, b := e.do(t, "PUT", e.ts.URL+"/api/files/text", textSave{Path: p, Content: "x", Base: textVersion{Sha256: "a"}, HostID: e.hostID}, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Errorf("cross-origin: %d %s", code, b)
	}
}
