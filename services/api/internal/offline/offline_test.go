package offline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	dir := filepath.Join(t.TempDir(), "offline")
	s, err := Open(cat.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

var k1 = Key{DatasetID: 7, HostID: 2, Size: 100, MTime: 5, Op: "plane", Channel: 3, Step: 4}

// A copy comes back byte for byte, lives in a private file, and a file that was altered on disk is never served.
func TestPutGetRoundTripAndTamper(t *testing.T) {
	s, dir := open(t)
	head, payload := []byte(`{"a":1}`), []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if _, err := s.Put(k1, "sha256:ab", head, payload, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	e, h, p, err := s.Get(k1)
	if err != nil || !bytes.Equal(h, head) || !bytes.Equal(p, payload) || e.Identity != "sha256:ab" || !e.TakenAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("%+v %q %v %v", e, h, p, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "7", k1.name())); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file is not private: %v %v", info, err)
	}
	if _, _, _, err := s.Get(Key{DatasetID: 7, HostID: 2, Size: 100, MTime: 5, Op: "plane", Channel: 4, Step: 4}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another channel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "7", k1.name()), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Get(k1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an altered copy was served: %v", err)
	}
	if s.Has(7) {
		t.Fatal("the damaged row stayed")
	}
}

// The size limit refuses a copy instead of evicting another; replacing a copy counts only the difference.
func TestCapRefusesAndNeverEvicts(t *testing.T) {
	s, _ := open(t)
	if err := s.SetCap(100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(k1, "", []byte("h"), make([]byte, 50), time.Now()); err != nil {
		t.Fatal(err)
	}
	k2 := k1
	k2.Channel = 9
	var oc *OverCap
	if _, err := s.Put(k2, "", []byte("h"), make([]byte, 60), time.Now()); !errors.As(err, &oc) {
		t.Fatalf("want OverCap, got %v", err)
	}
	if _, _, _, err := s.Get(k1); err != nil {
		t.Fatalf("the first copy was evicted: %v", err)
	}
	if _, err := s.Put(k1, "", []byte("h"), make([]byte, 80), time.Now()); err != nil {
		t.Fatalf("replacing a copy must count only the difference: %v", err)
	}
}

// A changed file makes its copies stale (kept, never fresh again until a live look agrees); a newer preparation supersedes them.
func TestStaleAndSupersede(t *testing.T) {
	s, dir := open(t)
	if _, err := s.Put(k1, "", []byte("h"), []byte("p"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.NoteSeen(7, 2, 100, 5)
	es, _ := s.Entries(7)
	if len(es) != 1 || es[0].Stale() {
		t.Fatalf("the same signature is not stale: %+v", es)
	}
	s.NoteSeen(7, 2, 101, 5)
	if es, _ = s.Entries(7); !es[0].Stale() {
		t.Fatal("a different size seen on the host must make the copy stale")
	}
	if err := s.Supersede(7, 2, 101, 5); err != nil {
		t.Fatal(err)
	}
	if s.Has(7) {
		t.Fatal("the superseded copy stayed")
	}
	if _, err := os.Stat(filepath.Join(dir, "7", k1.name())); !os.IsNotExist(err) {
		t.Fatalf("the superseded file stayed: %v", err)
	}
}

// A catalog restored without the store's directory must not describe copies that are not there.
func TestRowsWithoutFilesAreDroppedAtStart(t *testing.T) {
	s, dir := open(t)
	if _, err := s.Put(k1, "", []byte("h"), []byte("p"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Has(7) || s2.Used() != 0 {
		t.Fatal("a row with no file survived the start")
	}
}

func TestForgetAndOrphanSweep(t *testing.T) {
	s, dir := open(t)
	if _, err := s.Put(k1, "", []byte("h"), []byte("p"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, freed, err := s.Forget(7); err != nil || n != 1 || freed == 0 {
		t.Fatalf("%d %d %v", n, freed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "7")); !os.IsNotExist(err) {
		t.Fatal("the folder stayed after forgetting")
	}
	// a folder with no row (an interrupted delete) is removed at the next start
	if err := os.MkdirAll(filepath.Join(dir, "99"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.db, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "99")); !os.IsNotExist(err) {
		t.Fatal("an orphan folder stayed")
	}
}
