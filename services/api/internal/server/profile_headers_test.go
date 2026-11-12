package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type profileWorker struct{}

func (profileWorker) Header(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"vgt":{"psiframe":"PIXEL","psiangle":"BFIELD","vgtprod":"psi","params":{}},` +
		`"orientation":{"label":"profile:VGT.jl@1","frame":"pixel","sky_frame":null,"zero_direction":"+axis 1","sense":"counter-clockwise","quantity":"b_field","unit":"rad","from":{},"unresolved":[],"notes":[]}}`), nil
}

func TestRefreshProfileHeadersReadsOnlyUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := json.RawMessage(`{"vgt":{"psiframe":"PIXEL","psiangle":"BFIELD","params":{}}}`)
	reg := func(name string, size int) catalog.Dataset {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		d, err := st.Upsert(workspace.Local, "t", catalog.Dataset{Path: p, Name: name, SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: old})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	same, changed := reg("same.fits", 10), reg("changed.fits", 10)
	if err := os.WriteFile(changed.Path, make([]byte, 20), 0o644); err != nil { // the file moved on since it was registered
		t.Fatal(err)
	}
	s := &Server{Store: st, Worker: profileWorker{}}
	if n := s.RefreshProfileHeaders(context.Background()); n != 1 {
		t.Fatalf("replaced %d, want 1", n)
	}
	got, _ := st.Get(workspace.Local, same.ID)
	if v := catalog.VGTOf(got.Header); v == nil || v.Orientation == nil || v.Orientation.Profile != "profile:VGT.jl@1" {
		t.Fatalf("the unchanged file's header is read through the profile now: %+v", v)
	}
	got, _ = st.Get(workspace.Local, changed.ID)
	if v := catalog.VGTOf(got.Header); v == nil || v.Orientation != nil || len(v.Unresolved) != 3 {
		t.Fatalf("a changed file keeps its stored header, unresolved: %+v", v)
	}
	if n := s.RefreshProfileHeaders(context.Background()); n != 0 {
		t.Fatalf("nothing is left for the unchanged file, replaced %d", n)
	}
}
