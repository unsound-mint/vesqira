package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
)

// The refusals ADR 0064 keeps, each with what to do instead: a mask that lives on another machine than the file being read (a dataset with
// a local and a host location reads one of them), and relative orientation of a map on a host.
func TestHostParityRefusalsSayWhatToDo(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ms, err := masks.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(100, 0)
	d, err := st.Upsert(catalog.Dataset{Path: "/data/a.fits", Name: "a.fits", SizeBytes: 10, ModifiedAt: mtime, Header: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ms.Add(masks.Mask{DatasetID: d.ID, HostID: 3, Type: masks.Saturated, Name: "Bright", Path: "/cluster/data/vesqira_derived/mask-1-saturated-0a.fits", Width: 4, Height: 3,
		Tier: catalog.TierDeclared, Method: masks.MethodComputed, Rule: json.RawMessage(`{}`), Author: "user", SourceSize: 10, SourceMtime: mtime.UnixNano(), FileSize: 1, FileMtime: 1, SHA256: "ab"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Masks: ms}
	if _, code, msg := s.usableMask(m.ID, d); code != 409 || !strings.Contains(msg, "make it again where the file is read") {
		t.Errorf("a host mask applied to the file read here: %d %s", code, msg)
	}

	frame, quantity := "sky", "b_field"
	onHost := catalog.Dataset{ID: 9, HostID: 3, Name: "psi.fits", Header: []byte(`{}`), Interpretation: &catalog.Interpretation{Kind: "axial_orientation", Unit: "deg", Frame: &frame, Quantity: &quantity}}
	if _, code, msg := s.orientationSide(httptest.NewRequest("POST", "/", nil), onHost, "", "the map"); code != 409 || !strings.Contains(msg, "download the map with Transfers") {
		t.Errorf("relative orientation of a map on a host: %d %s", code, msg)
	}
}
