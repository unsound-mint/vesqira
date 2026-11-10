package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func openAt(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"path": path})
	req := httptest.NewRequest("POST", "/api/projects/open", strings.NewReader(string(body)))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(w, req)
	return w
}

func treeOf(s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/files/tree?path="+url.QueryEscape(path), nil)
	req.Host = "127.0.0.1:8787"
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestOpenedFolderBecomesReadableAndIsRemembered(t *testing.T) {
	base := t.TempDir()
	start := filepath.Join(base, "start")
	proj := filepath.Join(base, "proj")
	for _, d := range []string{start, filepath.Join(proj, "data"), filepath.Join(proj, ".ssh")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(proj, "cube.fits"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("x"), 0o644)
	rs, _ := sourceroot.New([]string{start})
	remembered := filepath.Join(base, "roots")
	s := &Server{Roots: rs, RootsFile: remembered}

	if treeOf(s, proj).Code != 404 {
		t.Fatal("a folder outside the roots must not be listed")
	}
	if w := openAt(t, s, proj); w.Code != 200 {
		t.Fatalf("open: %d %s", w.Code, w.Body)
	}
	openAt(t, s, proj) // again: no duplicate line
	w := treeOf(s, proj)
	if w.Code != 200 {
		t.Fatalf("tree: %d %s", w.Code, w.Body)
	}
	var out struct {
		Entries []treeEntry `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	var names []string
	for _, e := range out.Entries {
		names = append(names, e.Kind+":"+e.Name)
	}
	if got := strings.Join(names, ","); got != "dir:data,file:a.txt,file:cube.fits" { // folders first, credentials folder hidden
		t.Fatalf("entries = %s", got)
	}
	b, _ := os.ReadFile(remembered)
	if strings.Count(string(b), "\n") != 1 {
		t.Fatalf("roots file = %q", b)
	}
}

func TestOpenRefusesBroadAndCredentialFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o755)
	os.MkdirAll(filepath.Join(home, "proj"), 0o755)
	s := &Server{}
	for _, p := range []string{"/", home, filepath.Dir(home), filepath.Join(home, ".ssh"), "relative", filepath.Join(home, "missing")} {
		if w := openAt(t, s, p); w.Code != http.StatusBadRequest {
			t.Errorf("open %q: %d, want 400", p, w.Code)
		}
	}
	if w := openAt(t, s, filepath.Join(home, "proj")); w.Code != 200 {
		t.Errorf("a project inside home: %d", w.Code)
	}
}

func TestTreeDoesNotFollowLinksOutOfRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(outside, 0o755)
	os.Symlink(outside, filepath.Join(root, "escape"))
	rs, _ := sourceroot.New([]string{root})
	s := &Server{Roots: rs}
	if treeOf(s, filepath.Join(root, "escape")).Code != 404 {
		t.Fatal("a link that leaves the roots must be refused")
	}
	var out struct{ Entries []treeEntry }
	json.Unmarshal(treeOf(s, root).Body.Bytes(), &out)
	if len(out.Entries) != 1 || out.Entries[0].Kind != "link" {
		t.Fatalf("entries = %+v", out.Entries)
	}
}
