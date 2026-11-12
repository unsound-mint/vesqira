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

func linksOf(t *testing.T, s *Server, dir string) map[string]dirEntry {
	t.Helper()
	w := treeOf(s, dir)
	if w.Code != 200 {
		t.Fatalf("tree: %d %s", w.Code, w.Body)
	}
	var out struct{ Entries []dirEntry }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]dirEntry{}
	for _, e := range out.Entries {
		m[e.Name] = e
	}
	return m
}

func TestTreeShowsALinkOutOfTheRootsAsALinkThatPointsOutside(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "secret.fits"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(root, "escape"))
	os.Symlink(filepath.Join(outside, "secret.fits"), filepath.Join(root, "file-escape.fits"))
	os.Symlink(filepath.Join(base, "nothing"), filepath.Join(root, "dangling"))
	rs, _ := sourceroot.New([]string{root})
	s := &Server{Roots: rs}
	got := linksOf(t, s, root)
	for name, want := range map[string]struct{ target, points string }{
		"escape":           {outside, "outside"},
		"file-escape.fits": {filepath.Join(outside, "secret.fits"), "outside"},
		"dangling":         {filepath.Join(base, "nothing"), "broken"},
	} {
		e := got[name]
		if e.Kind != "link" || e.Target != want.target || e.Points != want.points || e.Size != 0 {
			t.Errorf("%s = %+v, want a link to %s pointing %s", name, e, want.target, want.points)
		}
	}
	if treeOf(s, filepath.Join(root, "escape")).Code != 404 {
		t.Fatal("a listing must not descend through a link to outside the roots")
	}
}

func TestTreeShowsALinkInsideTheRootsAsALinkAndStaysUsable(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "root")
	os.MkdirAll(filepath.Join(root, "real"), 0o755)
	os.WriteFile(filepath.Join(root, "real", "cube.fits"), []byte("x"), 0o644)
	os.Symlink("real", filepath.Join(root, "scratch")) // relative, as a link made by hand usually is
	rs, _ := sourceroot.New([]string{root})
	s := &Server{Roots: rs}
	e := linksOf(t, s, root)["scratch"]
	if e.Kind != "link" || e.Target != "real" || e.Points != "inside" {
		t.Fatalf("scratch = %+v", e)
	}
	if got := linksOf(t, s, filepath.Join(root, "scratch")); got["cube.fits"].Kind != "file" {
		t.Fatalf("a link that stays inside the roots is still a way into the folder: %+v", got)
	}
}

func TestCompletionMarksALinkAndNamesItsTarget(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(base, "real"), 0o755)
	os.Symlink("real", filepath.Join(base, "alias"))
	out, err := listNames(base, "", completeQuery{kind: "any"})
	if err != nil {
		t.Fatal(err)
	}
	var link, plain completeEntry
	for _, e := range out.Entries {
		if e.Name == "alias" {
			link = e
		} else if e.Name == "real" {
			plain = e
		}
	}
	if !link.Link || link.Target != "real" || !link.Dir || plain.Link || plain.Target != "" {
		t.Fatalf("link = %+v, plain = %+v", link, plain)
	}
}
