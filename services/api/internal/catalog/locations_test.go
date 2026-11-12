package catalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("file:" + filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var t0 = time.Unix(1_700_000_000, 123)

func reg(t *testing.T, s *Store, host int64, path string, size int64, mod time.Time) Dataset {
	t.Helper()
	d, err := s.Upsert(workspace.Local, "local-owner", Dataset{HostID: host, Path: path, Name: filepath.Base(path), SizeBytes: size, ModifiedAt: mod, Header: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEveryDatasetHasItsRegisteredFileAsThePrimaryLocation(t *testing.T) {
	s := openStore(t)
	local := reg(t, s, 0, "/data/a.fits", 100, t0)
	remote := reg(t, s, 3, "/scratch/b.fits", 200, t0)
	if len(local.Locations) != 1 || !local.Locations[0].Primary || local.Locations[0].Path != "/data/a.fits" || local.Locations[0].HostID != 0 || local.Locations[0].Link != LinkRegistered {
		t.Fatalf("local: %+v", local.Locations)
	}
	if l := remote.Locations[0]; l.HostID != 3 || l.Path != "/scratch/b.fits" || l.SizeBytes != 200 || !l.ModifiedAt.Equal(t0.UTC()) {
		t.Fatalf("a file on a host keeps its plain path in the location: %+v", l)
	}
	ds, _ := s.List(workspace.Local)
	for _, d := range ds {
		if len(d.Locations) != 1 {
			t.Fatalf("list must carry locations: %+v", d)
		}
	}
	if none := reg(t, s, 0, "/data/c.fits", 1, t0); none.Locations == nil {
		t.Fatal("locations is always a list")
	}
}

func TestAFingerprintNeverStaysOnBytesItWasNotComputedFrom(t *testing.T) {
	s := openStore(t)
	d := reg(t, s, 0, "/data/a.fits", 100, t0)
	loc := d.Locations[0]
	// a value computed for other bytes than the recorded ones is refused
	if err := s.SetFingerprint(workspace.Local, loc.ID, "sampled", "abc", "api", 101, t0.UnixNano()); !errors.Is(err, ErrStale) {
		t.Fatalf("stale size: %v", err)
	}
	if err := s.SetFingerprint(workspace.Local, loc.ID, "sampled", "abc", "api", 100, t0.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFingerprint(workspace.Local, loc.ID, "full", "def", "api", 100, t0.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFingerprint(workspace.Local, loc.ID, "bogus", "x", "api", 100, t0.UnixNano()); err == nil {
		t.Fatal("unknown kind")
	}
	// registering the same file again, unchanged, keeps them
	d = reg(t, s, 0, "/data/a.fits", 100, t0)
	if d.Locations[0].Sampled == nil || d.Locations[0].Full == nil || d.Locations[0].Sampled.By != "api" {
		t.Fatalf("unchanged: %+v", d.Locations[0])
	}
	// the file changed (size or time): both are dropped
	for _, c := range []struct {
		size int64
		mod  time.Time
	}{{101, t0}, {100, t0.Add(time.Second)}} {
		reg(t, s, 0, "/data/a.fits", 100, t0)
		s.SetFingerprint(workspace.Local, loc.ID, "sampled", "abc", "api", 100, t0.UnixNano())
		s.SetFingerprint(workspace.Local, loc.ID, "full", "def", "api", 100, t0.UnixNano())
		d = reg(t, s, 0, "/data/a.fits", c.size, c.mod)
		if got := d.Locations[0]; got.Sampled != nil || got.Full != nil {
			t.Fatalf("after a change to size %d / time %v: %+v %+v", c.size, c.mod, got.Sampled, got.Full)
		}
	}
}

func TestAddingAndRemovingLocations(t *testing.T) {
	s := openStore(t)
	a := reg(t, s, 0, "/data/a.fits", 100, t0)
	b := reg(t, s, 0, "/data/b.fits", 100, t0)
	loc, err := s.AddLocation(workspace.Local, a.ID, Location{Path: "/backup/a.fits", Kind: LocCopy, Link: LinkVerified, SizeBytes: 100, ModifiedAt: t0, Full: &Stamp{Value: "f", By: "api"}, Sampled: &Stamp{Value: "s", By: "api"}})
	if err != nil || loc.Primary || loc.Link != LinkVerified || loc.Full == nil || loc.Full.Value != "f" || loc.Sampled == nil {
		t.Fatalf("add: %+v %v", loc, err)
	}
	if _, err := s.AddLocation(workspace.Local, b.ID, Location{Path: "/backup/a.fits", Link: LinkDeclared, SizeBytes: 1, ModifiedAt: t0}); !errors.Is(err, ErrLocationExists) {
		t.Fatalf("one file is a location of one dataset only: %v", err)
	}
	if _, err := s.AddLocation(workspace.Local, a.ID, Location{Path: "/data/b.fits", Link: LinkDeclared, SizeBytes: 1, ModifiedAt: t0}); !errors.Is(err, ErrLocationExists) {
		t.Fatalf("another dataset's registered file: %v", err)
	}
	if _, err := s.AddLocation(workspace.Local, a.ID, Location{Path: "/x", Link: "registered", SizeBytes: 1, ModifiedAt: t0}); err == nil {
		t.Fatal("a non-primary location must say why it belongs: verified, probable or declared")
	}
	if owner, ok, _ := s.LocationOwner(workspace.Local, 0, "/backup/a.fits"); !ok || owner != a.ID {
		t.Fatalf("owner: %d %v", owner, ok)
	}
	got, _ := s.Get(workspace.Local, a.ID)
	if len(got.Locations) != 2 || !got.Locations[0].Primary || got.Locations[1].ID != loc.ID {
		t.Fatalf("the registered file first: %+v", got.Locations)
	}
	if err := s.RemoveLocation(workspace.Local, a.ID, got.Locations[0].ID); !errors.Is(err, ErrPrimary) {
		t.Fatalf("primary: %v", err)
	}
	if err := s.RemoveLocation(workspace.Local, b.ID, loc.ID); !errors.Is(err, ErrNoLocation) {
		t.Fatalf("a location of another dataset: %v", err)
	}
	if err := s.RemoveLocation(workspace.Local, a.ID, loc.ID); err != nil {
		t.Fatal(err)
	}
	// locations go with their dataset
	s.AddLocation(workspace.Local, a.ID, Location{Path: "/backup/a.fits", Link: LinkDeclared, SizeBytes: 1, ModifiedAt: t0})
	if _, err := s.db.Exec(`DELETE FROM datasets WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM dataset_locations WHERE dataset_id=?`, a.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d locations left behind", n)
	}
}

func TestFindSameReturnsVerifiedBeforeProbableAndNeverADifferentFile(t *testing.T) {
	s := openStore(t)
	mk := func(path string, size int64, sampled, full string) Dataset {
		d := reg(t, s, 0, path, size, t0)
		l := d.Locations[0]
		if sampled != "" {
			s.SetFingerprint(workspace.Local, l.ID, "sampled", sampled, "api", size, t0.UnixNano())
		}
		if full != "" {
			s.SetFingerprint(workspace.Local, l.ID, "full", full, "api", size, t0.UnixNano())
		}
		return d
	}
	probable := mk("/data/p.fits", 50, "S", "")
	verified := mk("/data/v.fits", 50, "S", "F")
	mk("/data/other-size.fits", 51, "S", "F")
	mk("/data/other-sampled.fits", 50, "T", "")
	mk("/data/other-full.fits", 50, "S", "G")
	got, err := s.FindSame(workspace.Local, identity.Evidence{Size: 50, Sampled: "S", Full: "F"}, 0)
	if err != nil || len(got) != 2 || got[0].DatasetID != verified.ID || got[0].Relation != identity.Verified || got[1].DatasetID != probable.ID || got[1].Relation != identity.Probable {
		t.Fatalf("%+v %v", got, err)
	}
	got, _ = s.FindSame(workspace.Local, identity.Evidence{Size: 50, Sampled: "S"}, verified.ID)
	if len(got) != 2 || got[0].Relation != identity.Probable || got[1].Relation != identity.Probable {
		t.Fatalf("without a full hash on this side nothing is verified: %+v", got)
	}
	if got, _ := s.FindSame(workspace.Local, identity.Evidence{Size: 50}, 0); len(got) != 0 {
		t.Fatalf("no evidence, no match: %+v", got)
	}
}

func TestOtherLocationsAreNotTrustedAfterTheRegisteredFileChanges(t *testing.T) {
	s := openStore(t)
	d := reg(t, s, 0, "/data/a.fits", 100, t0)
	add := func(path, link string) Location {
		l, err := s.AddLocation(workspace.Local, d.ID, Location{Path: path, Link: link, SizeBytes: 100, ModifiedAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	v, p, dec := add("/b/v.fits", LinkVerified), add("/b/p.fits", LinkProbable), add("/b/d.fits", LinkDeclared)
	reg(t, s, 0, "/data/a.fits", 100, t0) // read again, unchanged: nothing is demoted
	got, _ := s.Locations(workspace.Local, d.ID)
	if got[1].Link != LinkVerified || got[2].Link != LinkProbable || got[3].Link != LinkDeclared {
		t.Fatalf("unchanged: %+v", got)
	}
	reg(t, s, 0, "/data/a.fits", 101, t0) // the registered file changed
	got, _ = s.Locations(workspace.Local, d.ID)
	if got[1].ID != v.ID || got[1].Link != LinkStale || got[2].ID != p.ID || got[2].Link != LinkStale || got[3].ID != dec.ID || got[3].Link != LinkDeclared {
		t.Fatalf("verified and probable go stale, a declaration stays what it was: %+v", got)
	}
	if order := ReadOrder(got); len(order) != 1 || !order[0].Primary {
		t.Fatalf("a stale location is not read from: %+v", order)
	}
	if err := s.SetLink(workspace.Local, v.ID, LinkVerified); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLink(workspace.Local, v.ID, "registered"); err == nil {
		t.Fatal("not a link for another file")
	}
}

func TestOnlyTheRegisteredFileAndVerifiedLocationsMakeADatasetAMatch(t *testing.T) {
	s := openStore(t)
	d := reg(t, s, 0, "/data/a.fits", 50, t0)
	for path, link := range map[string]string{"/b/declared.fits": LinkDeclared, "/b/probable.fits": LinkProbable} {
		l, err := s.AddLocation(workspace.Local, d.ID, Location{Path: path, Link: link, SizeBytes: 50, ModifiedAt: t0, Sampled: &Stamp{Value: "S"}, Full: &Stamp{Value: "F"}})
		if err != nil {
			t.Fatal(err)
		}
		_ = l
	}
	// the registered file has no fingerprint; only locations that prove nothing do
	got, err := s.FindSame(workspace.Local, identity.Evidence{Size: 50, Sampled: "S", Full: "F"}, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("a declared or probable location is not what the dataset's bytes are: %+v %v", got, err)
	}
	if _, err := s.AddLocation(workspace.Local, d.ID, Location{Path: "/b/verified.fits", Link: LinkVerified, SizeBytes: 50, ModifiedAt: t0, Full: &Stamp{Value: "F"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindSame(workspace.Local, identity.Evidence{Size: 50, Full: "F"}, 0); len(got) != 1 || got[0].Relation != identity.Verified {
		t.Fatalf("a verified location does: %+v", got)
	}
}

func TestReadOrderPrefersLocalThenTheRegisteredFileAndOnlyEverVerifiedOnes(t *testing.T) {
	loc := func(id, host int64, primary bool, link string) Location {
		return Location{ID: id, HostID: host, Primary: primary, Link: link}
	}
	ids := func(ls []Location) []int64 {
		var out []int64
		for _, l := range ls {
			out = append(out, l.ID)
		}
		return out
	}
	// a registered file on a host with a verified copy on this computer: the copy is read first
	got := ReadOrder([]Location{loc(1, 5, true, LinkRegistered), loc(2, 0, false, LinkVerified), loc(3, 0, false, LinkProbable), loc(4, 0, false, LinkDeclared)})
	if want := []int64{2, 1}; !equal(ids(got), want) {
		t.Fatalf("%v, want %v (probable and declared locations are never read from)", ids(got), want)
	}
	// both local: the registered file first; two verified copies in the order they were added
	got = ReadOrder([]Location{loc(1, 0, true, LinkRegistered), loc(2, 0, false, LinkVerified), loc(3, 0, false, LinkVerified)})
	if want := []int64{1, 2, 3}; !equal(ids(got), want) {
		t.Fatalf("%v, want %v", ids(got), want)
	}
	// a registered file on this computer is still read before a verified copy on a host
	got = ReadOrder([]Location{loc(2, 7, false, LinkVerified), loc(1, 0, true, LinkRegistered)})
	if want := []int64{1, 2}; !equal(ids(got), want) {
		t.Fatalf("%v, want %v", ids(got), want)
	}
	if len(ReadOrder(nil)) != 0 {
		t.Fatal("nothing to read")
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
