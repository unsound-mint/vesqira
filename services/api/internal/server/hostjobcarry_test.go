package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
)

// Masks carried through a cutout job on a host (ADR 0064 addendum, rules of ADR 0055): the script passes the carry options with the names
// the API planned, the job writes the masks next to its result, and registering the result records them as masks of that host, equal to
// the ones the same cutout of a local copy carries. A mask file that is not there is never recorded, and the person is told.
func TestJobCarriesMasksOnAHostLikeHere(t *testing.T) {
	e := newHostParity(t)
	loc, rem := e.pair("hi_cube.fits")
	mk := func(d catalog.Dataset) (sat hpMask) {
		if c, _, raw := e.mask(d.ID, map[string]any{"method": "missing"}); c != 201 {
			t.Fatalf("missing mask of %d: %d %s", d.ID, c, raw)
		}
		c, sat, raw := e.mask(d.ID, map[string]any{"method": "saturated", "threshold": 0.5, "source": "datasheet"})
		if c != 201 {
			t.Fatalf("saturated mask of %d: %d %s", d.ID, c, raw)
		}
		return sat
	}
	lsat, rsat := mk(loc), mk(rem)
	params := map[string]string{"x0": "3", "x1": "20", "y0": "2", "y1": "15"}
	carry := func(id int64) []map[string]any { return []map[string]any{{"id": id, "carry": true}} }

	code, lb := e.call("POST", "/api/analysis", map[string]any{"op": "cutout", "datasetId": loc.ID, "params": params, "carryMasks": carry(lsat.ID)})
	if code != 201 {
		t.Fatalf("local cutout: %d %s", code, lb)
	}
	var lres struct{ Dataset catalog.Dataset }
	json.Unmarshal(lb, &lres)

	draft := func(body map[string]any) (int, jobs.Job, []byte) {
		c, b := e.call("POST", "/api/analysis/job", body)
		var j jobs.Job
		json.Unmarshal(b, &j)
		return c, j, b
	}
	code, job, jb := draft(map[string]any{"op": "cutout", "datasetId": rem.ID, "params": params, "carryMasks": carry(rsat.ID)})
	if code != 201 {
		t.Fatalf("cutout job with a carried mask: %d %s", code, jb)
	}
	// the script names the planned files, in the job's own output folder, and nothing else
	if !strings.Contains(job.Script, " --carry-masks '[{") || !strings.Contains(job.Script, " --missing-out "+job.OutputDir+"/mask-missing-") ||
		!strings.Contains(job.Script, `"out":"`+job.OutputDir+`/mask-carried-`) {
		t.Fatalf("the script does not carry the planned options:\n%s", job.Script)
	}
	e.runJob(job, nil)
	code, regb := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", job.ID), map[string]any{})
	if code != 201 || strings.Contains(string(regb), "masksNote") {
		t.Fatalf("register: %d %s", code, regb)
	}
	var out catalog.Dataset
	json.Unmarshal(regb, &out)
	if out.HostID != e.h.ID {
		t.Fatalf("the result is not on the host: %+v", out)
	}
	list := func(id int64) map[string]hpMask {
		_, b := e.get(fmt.Sprintf("/api/datasets/%d/masks", id))
		var l struct{ Masks []hpMask }
		json.Unmarshal(b, &l)
		m := map[string]hpMask{}
		for _, x := range l.Masks {
			m[x.Type] = x
		}
		return m
	}
	lm, rm := list(lres.Dataset.ID), list(out.ID)
	if len(lm) != 2 || len(rm) != 2 {
		t.Fatalf("masks of the result here %v, on the host %v: want the carried saturated and the recomputed no-value mask", lm, rm)
	}
	for _, typ := range []string{masks.Saturated, masks.Missing} {
		a, b := lm[typ], rm[typ]
		if a.ID == 0 || b.ID == 0 {
			t.Fatalf("%s missing: here %+v, host %+v", typ, a, b)
		}
		if b.HostID != e.h.ID || a.HostID != 0 || b.Stale {
			t.Errorf("%s: host %d / %d, stale %v", typ, b.HostID, a.HostID, b.Stale)
		}
		if a.Flagged != b.Flagged || a.Total != b.Total || a.Width != b.Width || a.Height != b.Height || a.Channels != b.Channels ||
			a.Tier != b.Tier || a.Method != b.Method || a.Name != b.Name {
			t.Errorf("%s: the host's mask differs from the local one:\n here %+v\n host %+v", typ, a, b)
		}
		if c, _ := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=3", b.ID)); c != 200 {
			t.Errorf("%s hatch on the host: %d", typ, c)
		}
	}
	// the files are on the host in the job's folder; nothing was written here
	files, _ := filepath.Glob(filepath.Join(job.OutputDir, "mask-*.fits"))
	if len(files) != 2 {
		t.Errorf("mask files in the job's folder: %v", files)
	}
	_, drb := e.get(fmt.Sprintf("/api/datasets/%d/derived", out.ID))
	var rec derived.Record
	json.Unmarshal(drb, &rec)
	if !strings.Contains(string(rec.Params), `"maskCarry"`) || strings.Contains(string(rec.Params), e.remoteRoot) || strings.Contains(string(rec.Params), "vesqira_derived") {
		t.Errorf("the derived record: %s", rec.Params)
	}
	for _, f := range derivedFiles(t, e.derivedDir) {
		if strings.HasPrefix(f, "mask-carried-") && strings.Contains(f, fmt.Sprintf("-%d-", rem.ID)) {
			t.Errorf("the host's mask was written here: %s", f)
		}
	}
	// registering again records the same masks once and keeps the record
	if c, b := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", job.ID), map[string]any{}); c != 201 || len(list(out.ID)) != 2 {
		t.Errorf("a second registration: %d %s, masks %v", c, b, list(out.ID))
	}
	_, drb = e.get(fmt.Sprintf("/api/datasets/%d/derived", out.ID))
	if !strings.Contains(string(drb), `"maskCarry"`) {
		t.Errorf("a second registration lost the record of the carry: %s", drb)
	}

	// a mask file that was not written is never recorded; the result is, and the person is told
	code, job2, jb := draft(map[string]any{"op": "slab", "datasetId": rem.ID, "params": map[string]string{"chanStart": "5", "chanEnd": "30"}, "carryMasks": carry(rsat.ID)})
	if code != 201 {
		t.Fatalf("slab job: %d %s", code, jb)
	}
	e.runJob(job2, func(outDir string) {
		got, _ := filepath.Glob(filepath.Join(outDir, "mask-carried-*.fits"))
		if len(got) != 1 {
			t.Fatalf("the job wrote %v", got)
		}
		os.Remove(got[0]) // the test removes the file the way a cleaned scratch folder would; Vesqira itself never does
	})
	code, regb = e.call("POST", fmt.Sprintf("/api/jobs/%d/register", job2.ID), map[string]any{})
	if code != 201 || !strings.Contains(string(regb), `"masksNote":"The result is registered, but no mask was recorded for it`) || !strings.Contains(string(regb), "is not in the job's output folder") {
		t.Fatalf("a job whose mask file is gone: %d %s", code, regb)
	}
	var out2 catalog.Dataset
	json.Unmarshal(regb, &out2)
	if out2.ID == 0 || out2.HostID != e.h.ID {
		t.Fatalf("the result must still be registered: %s", regb)
	}
	if got := list(out2.ID); len(got) != 0 {
		t.Errorf("masks were recorded for files that are not there: %v", got)
	}

	// a mask the source does not have, and an undecided mask of an alignment-like op, stay refused with nothing drafted
	for name, body := range map[string]map[string]any{
		"a mask of another dataset": {"op": "cutout", "datasetId": rem.ID, "params": params, "carryMasks": carry(lsat.ID)},
		"the no-value mask":         {"op": "cutout", "datasetId": rem.ID, "params": params, "carryMasks": carry(maskOfType(t, e, rem.ID, masks.Missing))},
		"a moment job":              {"op": "moment", "datasetId": rem.ID, "params": map[string]string{"order": "0"}, "carryMasks": carry(rsat.ID)},
		"an undecided convolution":  {"op": "convolve", "datasetId": rem.ID, "params": map[string]string{"targetBmaj": "60", "targetBmin": "60", "targetBpa": "0"}},
	} {
		if c, _, b := draft(body); c != 422 {
			t.Errorf("%s: %d %s", name, c, b)
		}
	}
	if c, _, b := draft(map[string]any{"op": "cutout", "datasetId": rem.ID, "params": params, "carryMasks": carry(lsat.ID)}); !strings.Contains(string(b), "not one of this dataset") {
		t.Errorf("a mask of another dataset: %d %s", c, b)
	}
}

func maskOfType(t *testing.T, e *hpEnv, id int64, typ string) int64 {
	t.Helper()
	_, b := e.get(fmt.Sprintf("/api/datasets/%d/masks", id))
	var l struct{ Masks []hpMask }
	json.Unmarshal(b, &l)
	for _, m := range l.Masks {
		if m.Type == typ {
			return m.ID
		}
	}
	t.Fatalf("no %s mask on %d", typ, id)
	return 0
}
