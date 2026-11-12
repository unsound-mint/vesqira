package sourceroot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// each runs a test on the openat2 path and on the component walk, which is what a kernel or sandbox without openat2 gets.
func each(t *testing.T, fn func(t *testing.T)) {
	for _, mode := range []struct {
		name string
		on   bool
	}{{"openat2", true}, {"walk", false}} {
		t.Run(mode.name, func(t *testing.T) {
			useOpenat2 = mode.on
			t.Cleanup(func() { useOpenat2, afterResolve = true, nil })
			fn(t)
		})
	}
}

func contents(t *testing.T, p string) string {
	t.Helper()
	f, err := OpenFile(p)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", p, err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func twoTrees(t *testing.T) (root, outside string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, s := range map[string]string{filepath.Join(root, "sub", "cube.fits"): "inside", filepath.Join(outside, "cube.fits"): "OUTSIDE"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestOpenFileReadsARegularFile(t *testing.T) {
	each(t, func(t *testing.T) {
		root, _ := twoTrees(t)
		if got := contents(t, filepath.Join(root, "sub", "cube.fits")); got != "inside" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestOpenFileHonestLinkStillWorks(t *testing.T) {
	each(t, func(t *testing.T) {
		root, _ := twoTrees(t)
		link := filepath.Join(root, "scratch")
		if err := os.Symlink(filepath.Join(root, "sub"), link); err != nil {
			t.Fatal(err)
		}
		if got := contents(t, filepath.Join(link, "cube.fits")); got != "inside" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestOpenFileRefusesAParentSwappedForALinkAfterTheResolve(t *testing.T) {
	each(t, func(t *testing.T) {
		root, outside := twoTrees(t)
		afterResolve = func(string) {
			_ = os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "sub.moved"))
			_ = os.Symlink(outside, filepath.Join(root, "sub"))
		}
		f, err := OpenFile(filepath.Join(root, "sub", "cube.fits"))
		if f != nil {
			b, _ := io.ReadAll(f)
			t.Fatalf("opened and read %q", b)
		}
		if !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenFileRefusesTheFileSwappedForALinkAfterTheResolve(t *testing.T) {
	each(t, func(t *testing.T) {
		root, outside := twoTrees(t)
		file := filepath.Join(root, "sub", "cube.fits")
		afterResolve = func(string) {
			_ = os.Remove(file)
			_ = os.Symlink(filepath.Join(outside, "cube.fits"), file)
		}
		if f, err := OpenFile(file); f != nil || !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("f=%v err=%v", f, err)
		}
	})
}

func TestOpenFileRefusesAFifoWithoutHanging(t *testing.T) {
	each(t, func(t *testing.T) {
		root, _ := twoTrees(t)
		fifo := filepath.Join(root, "pipe")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Fatal(err)
		}
		if f, err := OpenFile(fifo); f != nil || !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("f=%v err=%v", f, err)
		}
		if f, err := OpenFile("/dev/null"); f != nil || !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("device: f=%v err=%v", f, err)
		}
	})
}

func TestOpenFileRefusesADirectoryAndBadPaths(t *testing.T) {
	each(t, func(t *testing.T) {
		root, _ := twoTrees(t)
		for _, p := range []string{filepath.Join(root, "sub"), "", "relative/cube.fits", filepath.Join(root, "missing.fits"), "/"} {
			if f, err := OpenFile(p); f != nil || !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("%q: f=%v err=%v", p, f, err)
			}
		}
	})
}

func TestOpenDirOpensAFolderAndRefusesALinkSwappedAfterTheCheck(t *testing.T) {
	each(t, func(t *testing.T) {
		root, outside := twoTrees(t)
		f, err := OpenDir(filepath.Join(root, "sub"))
		if err != nil {
			t.Fatal(err)
		}
		names, _ := f.Readdirnames(-1)
		f.Close()
		if len(names) != 1 || names[0] != "cube.fits" {
			t.Fatalf("names = %v", names)
		}
		if _, err := OpenDir(filepath.Join(root, "sub", "cube.fits")); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("a file is not a folder: %v", err)
		}
		afterResolve = func(string) {
			os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "sub.moved"))
			os.Symlink(outside, filepath.Join(root, "sub"))
		}
		if _, err := OpenDir(filepath.Join(root, "sub")); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("a folder swapped for a link after the check: %v", err)
		}
	})
}

func TestIDOfSeesAnotherFileUnderTheSameNameEvenWithEqualSizeAndTime(t *testing.T) {
	root, _ := twoTrees(t)
	p := filepath.Join(root, "sub", "cube.fits")
	fi, _ := os.Stat(p)
	before, ok := IDOf(fi)
	if !ok || !before.Recorded() {
		t.Fatalf("no id: %+v %v", before, ok)
	}
	os.WriteFile(p+".new", []byte("INSIDE"), 0o644) // same size as "inside"
	os.Chtimes(p+".new", fi.ModTime(), fi.ModTime())
	os.Rename(p+".new", p)
	fj, _ := os.Stat(p)
	after, _ := IDOf(fj)
	if fj.Size() != fi.Size() || !fj.ModTime().Equal(fi.ModTime()) || after == before {
		t.Fatalf("size %d/%d, time %v/%v, id %+v/%+v", fi.Size(), fj.Size(), fi.ModTime(), fj.ModTime(), before, after)
	}
}
