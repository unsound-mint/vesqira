package server

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// A registered file whose only role is being the file of an imported mask is marked in the dataset list with the dataset it serves,
// from the mask's row. Any other use keeps it plain, forgetting the mask releases it, and nothing is deleted.
func TestMaskSourceIsMarkedInTheListUntilUsedElsewhere(t *testing.T) {
	e := maskServer(t)
	d := e.reg("zeros_and_holes.fits")
	mk := exec.Command("python3", "-c", `
import sys, numpy as np
from astropy.io import fits
m = np.zeros((6, 8), dtype='<i2'); m[0, 0:3] = 1
fits.PrimaryHDU(m).writeto(sys.argv[1])
`, filepath.Join(e.root, "their_mask.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	theirs := e.reg("their_mask.fits")
	marks := func() map[int64][]string {
		_, b := get(t, e.ts.URL+"/api/datasets")
		var out struct {
			Datasets []struct {
				ID            int64
				MaskSourceFor []struct {
					ID   int64
					Name string
				}
			}
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		got := map[int64][]string{}
		for _, r := range out.Datasets {
			for _, m := range r.MaskSourceFor {
				got[r.ID] = append(got[r.ID], m.Name)
			}
		}
		return got
	}
	if got := marks(); len(got) != 0 {
		t.Fatalf("before any mask: %v", got)
	}
	st, m, b := e.make(t, d.ID, map[string]any{"method": "import", "type": "invalid", "importDatasetId": theirs.ID, "nonzero": "flagged"})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	if got := marks(); len(got) != 1 || len(got[theirs.ID]) != 1 || got[theirs.ID][0] != d.Name {
		t.Fatalf("the mask file serves %s: %v", d.Name, got)
	}
	_, lb := get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/masks")
	if !strings.Contains(string(lb), `"sourceDataset":{"id":`+itoa(int(theirs.ID))+`,"name":"their_mask.fits"}`) {
		t.Fatalf("the target's mask names its file: %s", lb)
	}

	db := e.cat.DB()
	// a mask made before the id was recorded does not say which file: that file stays an ordinary dataset
	if _, err := db.Exec(`UPDATE masks SET rule=json_remove(rule,'$.sourceDatasetId') WHERE id=?`, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := marks(); len(got) != 0 {
		t.Fatalf("a row that does not say which file: %v", got)
	}
	if _, err := db.Exec(`UPDATE masks SET rule=json_set(rule,'$.sourceDatasetId',?) WHERE id=?`, theirs.ID, m.ID); err != nil {
		t.Fatal(err)
	}

	// every other use keeps the file visible, and ending that use makes it a mask source again
	if _, err := boards.Open(db, t.TempDir()); err != nil { // creates the figures table the board items hang off
		t.Fatal(err)
	}
	if _, err := db.Exec(schema.ExplorationStepsTable); err != nil {
		t.Fatal(err)
	}
	for _, use := range []struct{ name, table, insert string }{
		{"opened in the viewer", "exploration_steps", `INSERT INTO exploration_steps(project_id,dataset_id,kind,label,scene,at) VALUES(0,?,'open','x','{}',0)`},
		{"saved region", "saved_regions", `INSERT INTO saved_regions(dataset_id,name,shape,created_at,updated_at) VALUES(?,'r','{}',0,0)`},
		{"figure", "figures", `INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance) VALUES(?,'t','',0,'rev1','',1,1,'{}')`},
		{"analysis input", "derived", `INSERT INTO derived(dataset_id,op,params,sources,summary,software,created_at) VALUES(999,'op','{}','[' || ? || ']','{}','{}',0)`},
	} {
		if _, err := db.Exec(use.insert, theirs.ID); err != nil {
			t.Fatalf("%s: %v", use.name, err)
		}
		if got := marks(); len(got) != 0 {
			t.Errorf("%s: the file is used for something else and must stay visible: %v", use.name, got)
		}
		if _, err := db.Exec(`DELETE FROM ` + use.table); err != nil {
			t.Fatal(err)
		}
		if got := marks(); len(got) != 1 {
			t.Errorf("%s ended: the file is a mask source again: %v", use.name, got)
		}
	}

	// forgetting the mask releases the file, which stays registered
	if st, b := callJSON(t, "POST", e.ts.URL+"/api/masks/"+itoa(int(m.ID))+"/delete", map[string]any{}); st != 200 {
		t.Fatalf("delete: %d %s", st, b)
	}
	if got := marks(); len(got) != 0 {
		t.Fatalf("mask forgotten: %v", got)
	}
	if _, err := e.srv.Store.Get(theirs.ID); err != nil {
		t.Fatalf("nothing is unregistered: %v", err)
	}
}
