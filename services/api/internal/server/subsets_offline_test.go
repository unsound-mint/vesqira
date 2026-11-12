package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
)

// An offline copy is a copy of the file, keyed without a window. When the worker on a host fails, a request for a subset must not be
// answered with the copy of the source's plane at the same channel number.
func TestAWindowIsNeverAnsweredFromAnOfflineCopy(t *testing.T) {
	base := t.TempDir()
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	off, err := offline.Open(st.DB(), filepath.Join(base, "offline"))
	if err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(100, 0)
	d, err := st.Upsert(catalog.Dataset{Path: "/remote/a.fits", Name: "a.fits", SizeBytes: 10, ModifiedAt: mtime, Header: []byte(`{}`), HostID: 3})
	if err != nil {
		t.Fatal(err)
	}
	key := offline.Key{DatasetID: d.ID, HostID: 3, Size: 10, MTime: mtime.UnixNano(), Op: "plane", Channel: 2}
	if _, err := off.Put(key, "", []byte(`{"ok":true,"channel":2}`), []byte{1, 2, 3, 4}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Offline: off}
	k := cacheKey{id: d.ID, host: 3, size: 10, mtime: mtime.UnixNano(), op: "plane", channel: 2}
	if _, ok := s.afterFailure(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), k); !ok {
		t.Fatal("the control failed: a copy of the file is served when its host went away")
	}
	k.win = `{"x0":3,"x1":9,"y0":0,"y1":5}`
	if rep, ok := s.afterFailure(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), k); ok {
		t.Fatalf("a window was answered with the source's offline copy: %s", rep.Head)
	}
}
