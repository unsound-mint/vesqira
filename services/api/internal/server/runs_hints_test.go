package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type countingHints struct {
	s *catalog.Store
	n int
}

func (c *countingHints) HintIndex() (map[catalog.HintKey]int64, error) {
	c.n++
	return c.s.HintIndex()
}

// The runs list suggests an input for every run from one read of the catalog, however many runs and files there are
// (148 runs and 1 350 files is the scale it has to serve). The first file by name and id still wins a tie.
func TestRunsListReadsTheCatalogOnceForInputHints(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, _ := runs.Open(cat.DB())
	rs, _ := sourceroot.New([]string{base})

	ids := map[string]int64{}
	for i := 0; i < 1500; i++ {
		name := fmt.Sprintf("cube%04d.fits", i%700) // names repeat: two sizes per name for the tie-break
		d, err := cat.Upsert(catalog.Dataset{Path: fmt.Sprintf("/d/%d/%s", i, name), Name: name, SizeBytes: int64(1000 + i/700), ModifiedAt: time.Unix(1, 0), Header: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%s/%d", name, d.SizeBytes)
		if _, ok := ids[key]; !ok {
			ids[key] = d.ID
		}
	}
	for i := 0; i < 150; i++ {
		name := fmt.Sprintf("cube%04d.fits", i*4%700)
		if _, err := rn.Save(fmt.Sprintf("/runs/%d", i), runs.Parsed{Run: runs.Run{Name: fmt.Sprint("r", i), Kind: "vgt", InputName: name, InputSizeBytes: 1000}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	counter := &countingHints{s: cat}
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Runs: rn, Hints: counter}).Handler())
	defer ts.Close()

	start := time.Now()
	resp, err := ts.Client().Get(ts.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Runs []struct {
			Name      string `json:"name"`
			InputName string `json:"inputName"`
			InputHint *struct {
				DatasetID int64 `json:"datasetId"`
			} `json:"inputHint"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if counter.n != 1 {
		t.Fatalf("the catalog was read %d times for the hints, want 1", counter.n)
	}
	if len(out.Runs) != 150 {
		t.Fatalf("%d runs", len(out.Runs))
	}
	for _, r := range out.Runs {
		want := ids[r.InputName+"/1000"]
		if r.InputHint == nil || r.InputHint.DatasetID != want {
			t.Fatalf("%s: hint %+v, want dataset %d", r.Name, r.InputHint, want)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the runs list took %v at this scale", d)
	}
}
