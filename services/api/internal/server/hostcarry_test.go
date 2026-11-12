package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
)

// Masks carried into a subset written on a host (ADR 0064, with the rules of ADR 0055): the masks of the source the researcher chose to
// carry, and the no-value mask computed again, are written on the host with the file, registered as masks of that host, and equal the
// ones the same subset of a local copy carries. The files appear together or not at all.
func TestSubsetCarriesMasksOnAHostLikeHere(t *testing.T) {
	e := newHostParity(t)
	loc, rem := e.pair("hi_cube.fits")
	hostDerived := filepath.Join(e.remoteRoot, "vesqira_derived")
	def := map[string]any{"name": "Core", "reason": "carrying", "box": map[string]any{"x0": 3, "x1": 20, "y0": 2, "y1": 15}, "step": 1, "channels": map[string]any{"c0": 10, "c1": 40}}
	type side struct {
		src                  catalog.Dataset
		sat, missing         hpMask
		sub                  subsetView
		subPath, carriedBody string
	}
	setup := func(d catalog.Dataset) *side {
		s := &side{src: d}
		var code int
		var raw string
		if code, s.missing, raw = e.mask(d.ID, map[string]any{"method": "missing"}); code != 201 {
			t.Fatalf("missing mask of %d: %d %s", d.ID, code, raw)
		}
		if code, s.sat, raw = e.mask(d.ID, map[string]any{"method": "saturated", "threshold": 0.5, "source": "datasheet"}); code != 201 {
			t.Fatalf("saturated mask of %d: %d %s", d.ID, code, raw)
		}
		code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/subsets", d.ID), def)
		if code != 201 {
			t.Fatalf("subset of %d: %d %s", d.ID, code, b)
		}
		var doc subDoc
		json.Unmarshal(b, &doc)
		s.sub, s.subPath = doc.Subset, fmt.Sprintf("/api/subsets/%d", doc.Subset.ID)
		return s
	}
	ls, rs := setup(loc), setup(rem)
	body := func(s *side) map[string]any {
		return map[string]any{"carryMasks": []map[string]any{{"id": s.sat.ID, "carry": true}}}
	}
	lc, lb := e.call("POST", ls.subPath+"/materialise", body(ls))
	rc, rb := e.call("POST", rs.subPath+"/materialise", body(rs))
	if lc != 201 || rc != 201 {
		t.Fatalf("materialise with a carried mask: here %d %s, host %d %s", lc, lb, rc, rb)
	}
	var ld, rd subDoc
	json.Unmarshal(lb, &ld)
	json.Unmarshal(rb, &rd)
	if rd.Dataset.HostID != e.h.ID {
		t.Fatalf("the subset file is not on the host: %+v", rd.Dataset)
	}

	maskList := func(id int64) []hpMask {
		_, b := e.get(fmt.Sprintf("/api/datasets/%d/masks", id))
		var l struct{ Masks []hpMask }
		json.Unmarshal(b, &l)
		return l.Masks
	}
	byType := func(l []hpMask) map[string]hpMask {
		m := map[string]hpMask{}
		for _, x := range l {
			m[x.Type] = x
		}
		return m
	}
	lm, rm := byType(maskList(ld.Dataset.ID)), byType(maskList(rd.Dataset.ID))
	if len(lm) != 2 || len(rm) != 2 {
		t.Fatalf("the new file has masks here %v, on the host %v: want the carried saturated and the recomputed no-value mask", lm, rm)
	}
	for _, typ := range []string{masks.Saturated, masks.Missing} {
		a, b := lm[typ], rm[typ]
		if a.ID == 0 || b.ID == 0 {
			t.Fatalf("%s missing: here %+v, host %+v", typ, a, b)
		}
		if b.HostID != e.h.ID || a.HostID != 0 {
			t.Errorf("%s: masks live on host %d / %d, want %d / 0", typ, b.HostID, a.HostID, e.h.ID)
		}
		if a.Flagged != b.Flagged || a.Total != b.Total || a.Width != b.Width || a.Height != b.Height || a.Channels != b.Channels ||
			a.Tier != b.Tier || a.Method != b.Method || a.Name != b.Name || a.Meaning != b.Meaning {
			t.Errorf("%s: the host's mask differs from the local one:\n here %+v\n host %+v", typ, a, b)
		}
		if b.Stale {
			t.Errorf("%s carried onto the host is stale at once", typ)
		}
		if strings.Contains(string(b.Rule), e.remoteRoot) {
			t.Errorf("%s: the rule names a host path: %s", typ, b.Rule)
		}
		// its hatch, read by the host's worker, is the plane the local copy's mask gives
		c1, pa := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=5", a.ID))
		c2, pb := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=5", b.ID))
		if c1 != 200 || c2 != 200 {
			t.Fatalf("%s hatch: %d %d %s", typ, c1, c2, pb)
		}
		_, xa := subFrame(t, pa)
		_, xb := subFrame(t, pb)
		if !bytes.Equal(xa, xb) {
			t.Errorf("%s: the carried mask's hatch on the host differs from the local one", typ)
		}
	}
	if rm[masks.Saturated].Method != masks.MethodCarried || rm[masks.Missing].Method != masks.MethodComputed {
		t.Errorf("methods: %s, %s", rm[masks.Saturated].Method, rm[masks.Missing].Method)
	}

	// the files are on the host, beside the subset; nothing of them was written here and no temporary is left
	hostFiles := derivedFiles(t, hostDerived)
	var carried, missingFile int
	for _, f := range hostFiles {
		switch {
		case strings.HasPrefix(f, ".part-"):
			t.Errorf("a temporary file was left on the host: %s", f)
		case strings.HasPrefix(f, fmt.Sprintf("mask-carried-%d-", rem.ID)):
			carried++
		case strings.HasPrefix(f, fmt.Sprintf("mask-missing-%d-", rem.ID)):
			missingFile++
		}
	}
	if carried != 1 || missingFile != 1 {
		t.Errorf("host derived folder: %v", hostFiles)
	}
	for _, f := range derivedFiles(t, e.derivedDir) {
		if strings.HasPrefix(f, fmt.Sprintf("mask-carried-%d-", rem.ID)) || strings.HasPrefix(f, fmt.Sprintf("mask-missing-%d-", rem.ID)) || strings.HasPrefix(f, fmt.Sprintf("subset-%d-", rs.sub.ID)) {
			t.Errorf("the host's file was written here: %s", f)
		}
	}

	// the identity recorded for the carried mask is the file's on the host
	_, drb := e.get(fmt.Sprintf("/api/datasets/%d/derived", rd.Dataset.ID))
	var rec derived.Record
	json.Unmarshal(drb, &rec)
	var pm struct {
		MaskCarry struct {
			Operation string `json:"operation"`
			Carried   []struct {
				Name               string `json:"name"`
				CarriedFingerprint string `json:"carriedFingerprint"`
			} `json:"carried"`
			LeftBehind []any    `json:"leftBehind"`
			Recomputed []string `json:"recomputed"`
		} `json:"maskCarry"`
	}
	json.Unmarshal(rec.Params, &pm)
	if pm.MaskCarry.Operation != "subset" || len(pm.MaskCarry.Carried) != 1 || len(pm.MaskCarry.LeftBehind) != 0 || fmt.Sprint(pm.MaskCarry.Recomputed) != "[missing]" {
		t.Fatalf("the record of the carry: %s", rec.Params)
	}
	var carriedFile string
	for _, f := range hostFiles {
		if strings.HasPrefix(f, fmt.Sprintf("mask-carried-%d-", rem.ID)) {
			carriedFile = filepath.Join(hostDerived, f)
		}
	}
	raw, err := os.ReadFile(carriedFile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if pm.MaskCarry.Carried[0].CarriedFingerprint != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("the carried mask's fingerprint %s is not the host file's", pm.MaskCarry.Carried[0].CarriedFingerprint)
	}
	var lrec derived.Record
	_, ldb := e.get(fmt.Sprintf("/api/datasets/%d/derived", ld.Dataset.ID))
	json.Unmarshal(ldb, &lrec)
	var lpm map[string]map[string]any
	var rpm map[string]map[string]any
	json.Unmarshal(lrec.Params, &lpm)
	json.Unmarshal(rec.Params, &rpm)
	for _, k := range []string{"operation", "by", "flagsFollow"} {
		if fmt.Sprint(lpm["maskCarry"][k]) != fmt.Sprint(rpm["maskCarry"][k]) {
			t.Errorf("maskCarry.%s: here %v, host %v", k, lpm["maskCarry"][k], rpm["maskCarry"][k])
		}
	}
	if bytes.Contains(rec.Params, []byte(e.remoteRoot)) || bytes.Contains(rec.Params, []byte("vesqira_derived")) {
		t.Errorf("the derived record names a host path: %s", rec.Params)
	}

	// the same request reuses the file; the choice to carry nothing is another file
	if c, b := e.call("POST", rs.subPath+"/materialise", body(rs)); c != 200 || !strings.Contains(string(b), `"reused":true`) {
		t.Errorf("reuse with the same carry: %d %s", c, b)
	}
	if c, b := e.call("POST", rs.subPath+"/materialise", map[string]any{}); c != 201 {
		t.Errorf("a subset that carries nothing is a different file: %d %s", c, b)
	}

	// a source mask that changed on the host is refused before anything is written
	before := derivedFiles(t, hostDerived)
	srcFile := hostMaskFile(t, hostDerived, masks.Mask{DatasetID: rem.ID, Type: masks.Saturated})
	if err := os.WriteFile(srcFile, append(mustRead(t, srcFile), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	c, b := e.call("POST", rs.subPath+"/materialise", map[string]any{"carryMasks": []map[string]any{{"id": rs.sat.ID, "carry": true}}, "expectVersion": 0})
	if c != 409 {
		t.Errorf("a changed source mask: %d %s", c, b)
	}
	if after := derivedFiles(t, hostDerived); len(after) != len(before) {
		t.Errorf("files appeared on the host for a refused request: %v -> %v", before, after)
	}
}
