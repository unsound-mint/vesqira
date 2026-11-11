package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fitsLike writes a file that starts like FITS (one 2880-byte header block holding END) followed by data.
func fitsLike(t *testing.T, path string, data []byte) int {
	t.Helper()
	var hdr []byte
	for _, c := range []string{"SIMPLE  =                    T", "BITPIX  =                    8", "END"} {
		hdr = append(hdr, []byte(c+strings.Repeat(" ", 80-len(c)))...)
	}
	hdr = append(hdr, bytes.Repeat([]byte(" "), fitsBlock-len(hdr))...)
	if err := os.WriteFile(path, append(hdr, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return len(hdr)
}

func patch(t *testing.T, path string, off int, b byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[off] ^= b
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampled(t *testing.T, path string) string {
	t.Helper()
	v, err := Sampled(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newCube(t *testing.T) (string, int) {
	t.Helper()
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(i * 7 % 251)
	}
	p := filepath.Join(t.TempDir(), "a.fits")
	return p, fitsLike(t, p, data)
}

func TestSampledIsStableForAnUnchangedFileAndChangesWithTheHeaderOrASampledBlock(t *testing.T) {
	p, hlen := newCube(t)
	base := sampled(t, p)
	if base != sampled(t, p) || len(base) != 64 {
		t.Fatalf("not stable: %s", base)
	}
	for name, off := range map[string]int{
		"a header card":    100,
		"the first block":  hlen,
		"the last byte":    -1, // resolved below
		"a middle block":   hlen + 205346 + 10,
		"the last block's": -2,
	} {
		data, _ := os.ReadFile(p)
		o := off
		switch off {
		case -1:
			o = len(data) - 1
		case -2:
			o = len(data) - 65536 + 5
		}
		patch(t, p, o, 1)
		if got := sampled(t, p); got == base {
			t.Errorf("a change in %s (offset %d) must change the sampled fingerprint", name, o)
		}
		patch(t, p, o, 1) // restore
		if got := sampled(t, p); got != base {
			t.Errorf("restoring %s must restore the fingerprint", name)
		}
	}
}

// The documented blind spot: a change between the sampled blocks is not seen by the sampled fingerprint, and is by the full hash.
func TestAChangeBetweenSampledBlocksIsNotDetectedBySampledButIsByFull(t *testing.T) {
	p, hlen := newCube(t)
	sBefore := sampled(t, p)
	fBefore, _, err := Full(context.Background(), p, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	patch(t, p, hlen+100_000, 1) // data offset 100000 lies between block 0 (to 65536) and block 1 (from 205346)
	if sampled(t, p) != sBefore {
		t.Fatal("this offset was meant to lie outside every sampled block")
	}
	fAfter, _, err := Full(context.Background(), p, 0, nil)
	if err != nil || fAfter == fBefore {
		t.Fatalf("the full hash must see it: %v", err)
	}
}

func TestFullIsTheSHA256OfEveryByteBoundedAndCancellable(t *testing.T) {
	p, _ := newCube(t)
	data, _ := os.ReadFile(p)
	want := sha256.Sum256(data)
	got, n, err := Full(context.Background(), p, 0, nil)
	if err != nil || got != hex.EncodeToString(want[:]) || n != int64(len(data)) {
		t.Fatalf("full: %s %d %v", got, n, err)
	}
	// above the cap: refused before anything is read
	if _, n, err := Full(context.Background(), p, 1<<20, func(int64) { t.Fatal("must not read") }); !errors.Is(err, ErrTooLarge) || n != 0 {
		t.Fatalf("cap: %d %v", n, err)
	}
	// cancelled after the first MiB: stops, returns the context's error, never a value
	ctx, cancel := context.WithCancel(context.Background())
	v, n, err := Full(ctx, p, 0, func(done int64) {
		if done >= 1<<20 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || v != "" || n >= int64(len(data)) {
		t.Fatalf("cancel: %q read %d of %d: %v", v, n, len(data), err)
	}
	if _, _, err := Full(context.Background(), filepath.Dir(p), 0, nil); err == nil {
		t.Fatal("a directory is not a file")
	}
}

func TestSmallAndNonFITSFilesHaveAFingerprint(t *testing.T) {
	d := t.TempDir()
	for name, content := range map[string][]byte{"empty": {}, "tiny": []byte("a,b\n1,2\n"), "no-end": []byte("SIMPLE  =                    T")} {
		p := filepath.Join(d, name)
		os.WriteFile(p, content, 0o644)
		if v := sampled(t, p); len(v) != 64 {
			t.Errorf("%s: %q", name, v)
		}
	}
	a, b := filepath.Join(d, "x"), filepath.Join(d, "y")
	os.WriteFile(a, []byte("one"), 0o644)
	os.WriteFile(b, []byte("two"), 0o644)
	if sampled(t, a) == sampled(t, b) {
		t.Fatal("different small files must differ")
	}
}

func TestRelateSaysOnlyWhatTheEvidenceProves(t *testing.T) {
	ev := func(size int64, s, f string) Evidence { return Evidence{Size: size, Sampled: s, Full: f} }
	for _, c := range []struct {
		name string
		a, b Evidence
		want string
	}{
		{"equal full hashes are verified", ev(10, "s1", "f"), ev(10, "s2-unused", "f"), Verified},
		{"different full hashes differ even if sampled agree", ev(10, "s", "f1"), ev(10, "s", "f2"), Different},
		{"equal sampled without full is only probable", ev(10, "s", ""), ev(10, "s", ""), Probable},
		{"equal sampled, one full: still probable", ev(10, "s", "f"), ev(10, "s", ""), Probable},
		{"different sizes differ", ev(10, "s", ""), ev(11, "s", ""), Different},
		{"different sampled differ", ev(10, "s1", ""), ev(10, "s2", ""), Different},
		{"nothing computed on one side is unknown", ev(10, "s", ""), ev(10, "", ""), Unknown},
	} {
		if got := Relate(c.a, c.b); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// The scientific worker computes the same sampled fingerprint for files on a host: one definition, two implementations.
func TestTheWorkersSampledFingerprintEqualsThisOne(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	cube, _ := newCube(t)
	small := filepath.Join(dir, "small.csv")
	os.WriteFile(small, []byte("a,b\n1,2\n"), 0o644)
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o644)
	headerOnly := filepath.Join(dir, "h.fits")
	fitsLike(t, headerOnly, nil)
	oddSize := filepath.Join(dir, "odd.fits")
	fitsLike(t, oddSize, bytes.Repeat([]byte{9, 8, 7}, 100_001))
	worker, _ := filepath.Abs(filepath.Join("..", "..", "..", "scientific-worker"))
	for _, p := range []string{cube, small, empty, headerOnly, oddSize} {
		out, err := exec.Command(py, "-I", "-c", "import sys; sys.path.insert(0, sys.argv[1]); import fingerprint; print(fingerprint.sampled(sys.argv[2]))", worker, p).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got, want := strings.TrimSpace(string(out)), sampled(t, p); got != want {
			t.Errorf("%s: worker %s, api %s", filepath.Base(p), got, want)
		}
	}
}
