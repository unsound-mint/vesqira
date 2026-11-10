package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func treeOf(s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/files/tree?path="+url.QueryEscape(path), nil)
	req.Host = "127.0.0.1:8787"
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestTreeListsNamesFoldersFirstAndHidesCredentialFolders(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	other := filepath.Join(base, "other")
	for _, d := range []string{filepath.Join(proj, "data"), filepath.Join(proj, ".ssh"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(proj, "cube.fits"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("x"), 0o644)
	rs, _ := sourceroot.New([]string{proj})
	s := &Server{Roots: rs}

	if treeOf(s, other).Code != 404 {
		t.Fatal("a folder outside the roots must not be listed")
	}
	if treeOf(s, filepath.Join(proj, ".ssh")).Code != 404 {
		t.Fatal("a credentials folder must not be listed")
	}
	w := treeOf(s, proj)
	if w.Code != 200 {
		t.Fatalf("tree: %d %s", w.Code, w.Body)
	}
	var out struct {
		Entries []dirEntry `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	var names []string
	for _, e := range out.Entries {
		names = append(names, e.Kind+":"+e.Name)
	}
	if got := strings.Join(names, ","); got != "dir:data,file:a.txt,file:cube.fits" { // folders first, credentials folder hidden
		t.Fatalf("entries = %s", got)
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
	var out struct{ Entries []dirEntry }
	json.Unmarshal(treeOf(s, root).Body.Bytes(), &out)
	if len(out.Entries) != 1 || out.Entries[0].Kind != "link" {
		t.Fatalf("entries = %+v", out.Entries)
	}
}
