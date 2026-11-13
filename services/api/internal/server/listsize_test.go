package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// bigCatalog registers n datasets whose headers are what the worker writes for a VGT product cube.
func bigCatalog(t *testing.T, n int) (*httptest.Server, *catalog.Store) {
	t.Helper()
	hdr, err := os.ReadFile("testdata/header_cube_vgt.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("run%04d_psi.fits", i)
		if _, err := st.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: "/data/" + name, Name: name, SizeBytes: 1 << 20, ModifiedAt: time.Now(), Header: json.RawMessage(hdr)}); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer((&Server{Store: st, Worker: &fakeWorker{}}).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// listBudgetBytes is the most a list of 1000 datasets with realistic headers may weigh. It measured 493 KB when the budget was set
// (3.4 MB when each entry carried its header); the explorer fetches it on load and after each change, and a scanned host project
// has several thousand datasets.
const listBudgetBytes = 540 << 10

func TestListEntriesAreSummaries(t *testing.T) {
	t.Parallel()
	ts, _ := bigCatalog(t, 3)
	_, b := get(t, ts.URL+"/api/datasets")
	var l struct{ Datasets []map[string]json.RawMessage }
	if err := json.Unmarshal(b, &l); err != nil || len(l.Datasets) != 3 {
		t.Fatalf("list: %v %s", err, b)
	}
	for _, heavy := range []string{"header", "locations", "orientationHeader"} {
		if _, ok := l.Datasets[0][heavy]; ok {
			t.Errorf("a list entry carries %q; only GET /api/datasets/{id} does", heavy)
		}
	}
	var facts struct {
		Naxis, Channels, Width, Height *int
		Bunit                          *string
		BunitStated                    bool
		NumericColumns                 []string
	}
	if err := json.Unmarshal(l.Datasets[0]["facts"], &facts); err != nil || facts.Naxis == nil || *facts.Naxis != 3 || facts.Channels == nil || *facts.Channels != 4 ||
		facts.Width == nil || *facts.Width != 6 || facts.Height == nil || *facts.Height != 5 || facts.Bunit == nil || *facts.Bunit != "K" || !facts.BunitStated || facts.NumericColumns == nil {
		t.Errorf("facts: %v %s", err, l.Datasets[0]["facts"])
	}
	var id int
	json.Unmarshal(l.Datasets[0]["id"], &id)
	_, one := get(t, ts.URL+"/api/datasets/"+itoa(id))
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(one, &detail); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"header", "locations", "orientationHeader", "facts", "interpretation"} {
		if _, ok := detail[field]; !ok {
			t.Errorf("the detail lacks %q", field)
		}
	}
	var h struct{ Axes []json.RawMessage }
	if json.Unmarshal(detail["header"], &h); len(h.Axes) != 3 {
		t.Errorf("the detail's header is not the stored one: %s", detail["header"])
	}
}

func TestListSizeBudget(t *testing.T) {
	t.Parallel()
	ts, _ := bigCatalog(t, 1000)
	_, b := get(t, ts.URL+"/api/datasets")
	if len(b) > listBudgetBytes {
		t.Errorf("the list of 1000 datasets is %d bytes, over the %d budget: a heavy field is back in the summary", len(b), listBudgetBytes)
	}
	t.Logf("list of 1000: %d bytes", len(b))

	req, _ := http.NewRequest("GET", ts.URL+"/api/datasets", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", resp.Header)
	}
	packed, _ := io.ReadAll(resp.Body)
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !bytes.Equal(plain, b) {
		t.Error("the gzipped list differs from the plain one")
	}
	t.Logf("gzipped: %d bytes", len(packed))
}

func TestAcceptsGzip(t *testing.T) {
	t.Parallel()
	for h, want := range map[string]bool{"": false, "gzip": true, "br, gzip;q=0.5": true, "gzip;q=0": false, "gzip; q=0.0": false, "identity": false, "deflate, GZIP": true} {
		if got := acceptsGzip(h); got != want {
			t.Errorf("%q: %v", h, got)
		}
	}
}
