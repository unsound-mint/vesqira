package sourceroot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (root, sibling string, r Roots) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "data")
	sibling = filepath.Join(base, "data2") // shares a string prefix with root
	for _, d := range []string{root, sibling, filepath.Join(root, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func write(t *testing.T, p string) string {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveInsideRoot(t *testing.T) {
	root, _, r := setup(t)
	f := write(t, filepath.Join(root, "sub", "a.fits"))
	got, err := r.Resolve(f)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(f)
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestRejects(t *testing.T) {
	root, sibling, r := setup(t)
	outside := write(t, filepath.Join(sibling, "b.fits"))
	inside := write(t, filepath.Join(root, "a.fits"))

	if err := os.Symlink(outside, filepath.Join(root, "escape.fits")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"sibling with shared prefix": outside,
		"symlink escaping the root":  filepath.Join(root, "escape.fits"),
		"file through a dir symlink": filepath.Join(root, "dirlink", "b.fits"),
		"dot-dot traversal":          filepath.Join(root, "..", "data2", "b.fits"),
		"relative path":              "a.fits",
		"empty":                      "",
		"nul byte":                   inside + "\x00",
		"missing file":               filepath.Join(root, "nope.fits"),
		"directory":                  filepath.Join(root, "sub"),
	}
	for name, p := range cases {
		if _, err := r.Resolve(p); err == nil {
			t.Errorf("%s: %q was authorized", name, p)
		}
	}
	if _, err := r.Resolve(outside); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("sibling should be ErrOutsideRoots, got %v", err)
	}
}

func TestNewRejectsMissingRoot(t *testing.T) {
	if _, err := New([]string{filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("expected error")
	}
}
