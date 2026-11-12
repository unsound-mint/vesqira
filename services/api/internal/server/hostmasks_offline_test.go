package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Offline copies are unmasked replies of a file (ADR 0059). Since masks are applied to files on a host (ADR 0064), a masked region request
// and a mask's hatch plane reach the host's worker too: when that worker fails, neither may be answered from a copy, and preparing a copy
// never keeps the hatch of a mask as the dataset's.
func TestMaskedHostRepliesAreNeverOfflineCopies(t *testing.T) {
	t.Parallel()
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
	d, err := st.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: "/remote/a.fits", Name: "a.fits", SizeBytes: 10, ModifiedAt: mtime, Header: []byte(`{}`), HostID: 3})
	if err != nil {
		t.Fatal(err)
	}
	q := "rect,0,0,7,5"
	if _, err := off.Put(offline.Key{DatasetID: d.ID, HostID: 3, Size: 10, MTime: mtime.UnixNano(), Op: "spectrum", Q: q}, "", []byte(`{"ok":true}`), []byte{1, 2, 3, 4}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Offline: off}
	ask := func(k cacheKey) bool {
		_, ok := s.afterFailure(httptest.NewRecorder(), localRequest("GET", "/"), k)
		return ok
	}
	k := cacheKey{id: d.ID, host: 3, size: 10, mtime: mtime.UnixNano(), op: "spectrum", q: q}
	if !ask(k) {
		t.Fatal("the control failed: the unmasked spectrum's copy is served when the host went away")
	}
	k.q = q + `|masks=[{"fingerprint":"sha256:ab","policy":"exclude"}]`
	if ask(k) {
		t.Fatal("a masked spectrum was answered with the unmasked copy")
	}
	// the hatch of a mask is keyed by the mask (a negative id), never by a dataset that has a copy
	if ask(cacheKey{id: -d.ID, host: 3, size: 5, mtime: 1, op: "mask_plane", channel: 0}) {
		t.Fatal("a mask's plane was answered from an offline copy")
	}
	c := &offlineCapture{s: s, d: d, at: time.Now(), budget: 1 << 20}
	ctx := context.WithValue(context.Background(), captureKey{}, c)
	s.keepOffline(ctx, cacheKey{id: -d.ID, host: 3, size: 5, mtime: 1, op: "mask_plane"}, worker.Reply{Head: []byte(`{}`), Payload: []byte{1}})
	if c.copies != 0 {
		t.Fatal("preparing an offline copy kept a mask's plane")
	}
}
