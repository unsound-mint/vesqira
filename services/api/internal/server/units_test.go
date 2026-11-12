package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// A unit the person declares for a file that has none is an overlay: every head the viewer reads says it, labelled as the
// person's and never as the file's; withdrawing it brings back what the file said (nothing); a file that states its own
// BUNIT cannot be given another; the file's bytes are never touched; and the worker's cache cannot serve a stale unit.
func TestADeclaredUnitReachesTheViewerHeadsAsTheUsersAndNeverChangesTheFile(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts, root, _ := newPlaneServerWith(t, pw, pw)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// a scalar cube without BUNIT: the hi_cube fixture with the card removed (the vgt_* maps are angles and cannot carry one)
	strip := exec.Command("python3", "-I", "-c", "import sys\nfrom astropy.io import fits\nwith fits.open(sys.argv[1]) as h:\n    del h[0].header['BUNIT']\n    h.writeto(sys.argv[2])", filepath.Join(root, "hi_cube.fits"), filepath.Join(root, "hi_cube_nounit.fits"))
	if out, err := strip.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	register := func(name string) catalog.Dataset {
		resp := post(t, ts, filepath.Join(root, name), nil)
		if resp.StatusCode != 201 {
			t.Fatalf("%s: register %d", name, resp.StatusCode)
		}
		var d catalog.Dataset
		json.NewDecoder(resp.Body).Decode(&d)
		return d
	}
	noUnit, withUnit := register("hi_cube_nounit.fits"), register("hi_cube.fits")
	if !strings.Contains(string(noUnit.Header), `"bunit_status":"unresolved"`) || !strings.Contains(string(withUnit.Header), `"bunit_status":"declared"`) {
		t.Fatalf("the fixtures changed: %s | %s", noUnit.Header, withUnit.Header)
	}
	before := sha256.Sum256(mustRead(t, filepath.Join(root, "hi_cube_nounit.fits")))

	call := func(method, path string, body any) (int, []byte) { return callJSON(t, method, ts.URL+path, body) }
	head := func(id int64) (unit any, status string) {
		st, b := call("GET", fmt.Sprintf("/api/datasets/%d/view", id), nil)
		if st != 200 {
			t.Fatalf("view: %d %s", st, b)
		}
		var v struct {
			Bunit       any    `json:"bunit"`
			BunitStatus string `json:"bunit_status"`
		}
		json.Unmarshal(b, &v)
		return v.Bunit, v.BunitStatus
	}

	// every kind of head the viewer reads: view and coordinate readout are JSON, plane and spectrum are frames (a 4-byte length,
	// the JSON head, the payload)
	allHeads := func(id int64) map[string][2]any {
		out := map[string][2]any{}
		for name, path := range map[string]string{
			"view": "/view", "plane": "/plane?channel=3", "coord": "/coord?x=2&y=2&channel=3", "spectrum": "/spectrum?shape=circle&cx=20&cy=9&r=3",
		} {
			st, b := call("GET", fmt.Sprintf("/api/datasets/%d%s", id, path), nil)
			if st != 200 {
				t.Fatalf("%s: %d %s", name, st, b)
			}
			if len(b) > 0 && b[0] != '{' {
				b = b[4 : 4+binary.LittleEndian.Uint32(b[:4])]
			}
			var h struct {
				Bunit       any    `json:"bunit"`
				BunitStatus string `json:"bunit_status"`
			}
			if err := json.Unmarshal(b, &h); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			out[name] = [2]any{h.Bunit, h.BunitStatus}
		}
		return out
	}
	for name, h := range allHeads(noUnit.ID) {
		if h[0] != nil || h[1] != "unresolved" {
			t.Fatalf("before: %s says %v / %v", name, h[0], h[1]) // also puts these heads in the plane cache
		}
	}
	st, b := call("POST", fmt.Sprintf("/api/datasets/%d/unit", noUnit.ID), map[string]any{"unit": "Jy/beam"})
	var d catalog.Dataset
	json.Unmarshal(b, &d)
	if st != 200 || d.UnitDeclaration == nil || d.UnitDeclaration.Unit != "Jy/beam" || d.UnitDeclaration.DeclaredBy != "user" {
		t.Fatalf("declare: %d %s", st, b)
	}
	if string(d.Header) != string(noUnit.Header) {
		t.Fatal("the header facts the file gave were changed")
	}
	for name, h := range allHeads(noUnit.ID) {
		if h[0] != "Jy/beam" || h[1] != "user" {
			t.Fatalf("after declaring %s says %v / %v (a cached head would be stale)", name, h[0], h[1])
		}
	}
	// declared again with another unit, and then withdrawn: each state is served as it is
	call("POST", fmt.Sprintf("/api/datasets/%d/unit", noUnit.ID), map[string]any{"unit": "mJy/beam"})
	if u, _ := head(noUnit.ID); u != "mJy/beam" {
		t.Fatalf("%v", u)
	}
	if st, b := call("POST", fmt.Sprintf("/api/datasets/%d/unit", noUnit.ID), map[string]any{"clear": true}); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	for name, h := range allHeads(noUnit.ID) {
		if h[0] != nil || h[1] != "unresolved" {
			t.Fatalf("after withdrawing %s says %v / %v", name, h[0], h[1])
		}
	}

	// refusals: a file that states its unit, text that is not a unit, and a dataset that does not exist
	if st, b := call("POST", fmt.Sprintf("/api/datasets/%d/unit", withUnit.ID), map[string]any{"unit": "Jy/beam"}); st != 409 {
		t.Fatalf("the file's own BUNIT must not be overridden: %d %s", st, b)
	}
	if u, s := head(withUnit.ID); u != "K" || s != "declared" {
		t.Fatalf("%v %s", u, s)
	}
	for _, bad := range []string{"", " K", "K\nm", strings.Repeat("x", 41)} {
		if st, _ := call("POST", fmt.Sprintf("/api/datasets/%d/unit", noUnit.ID), map[string]any{"unit": bad}); st != 400 {
			t.Fatalf("%q was accepted: %d", bad, st)
		}
	}
	if st, _ := call("POST", "/api/datasets/9999/unit", map[string]any{"unit": "K"}); st != 404 {
		t.Fatalf("%d", st)
	}
	// an orientation map (a VGT angle map) has no brightness: refused, with a reason
	psi := register("vgt_psi_deg.fits")
	if st, b := call("POST", fmt.Sprintf("/api/datasets/%d/interpretation", psi.ID), map[string]any{"kind": "axial_orientation", "unit": "deg"}); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	if st, b := call("POST", fmt.Sprintf("/api/datasets/%d/unit", psi.ID), map[string]any{"unit": "Jy/beam"}); st != 409 || !strings.Contains(string(b), "orientation map") {
		t.Fatalf("an orientation map was given a brightness unit: %d %s", st, b)
	}
	// a request that is not the JSON content type is refused by the existing guard (a form post from another site)
	req, _ := http.NewRequest("POST", ts.URL+fmt.Sprintf("/api/datasets/%d/unit", noUnit.ID), strings.NewReader(`unit=K`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode < 400 {
		t.Fatalf("a form post was accepted: %v", err)
	}
	if after := sha256.Sum256(mustRead(t, filepath.Join(root, "hi_cube_nounit.fits"))); after != before {
		t.Fatal("the file was changed")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// echoPlanes answers every request with a head that says which unit it was given, so a test sees what a client would be told.
type echoPlanes struct{ calls []map[string]any }

func (e *echoPlanes) Do(_ context.Context, req map[string]any) (worker.Reply, error) {
	e.calls = append(e.calls, req)
	head, _ := json.Marshal(map[string]any{"ok": true, "nbytes": 0, "bunit": req["declared_bunit"]})
	return worker.Reply{Head: head}, nil
}

// The unit goes to the worker only when it is current, is part of the cache key, and is dropped when the declaration no longer
// describes the file: each state is answered as it is, never from another state's cache entry. (Isolated from the real worker:
// it looks at what is asked of it, how often, and what the client is told.)
func TestTheDeclaredUnitIsSentOnlyWhenCurrentAndKeysTheCache(t *testing.T) {
	t.Parallel()
	ep := &echoPlanes{}
	ts, root, _ := newPlaneServer(t, ep)
	file := filepath.Join(root, "a.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, ts, file, nil).Body).Decode(&d)
	// what the client is told, and whether the worker was asked
	plane := func() (told any, asked bool) {
		before := len(ep.calls)
		r, b := get(t, ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/view")
		if r.StatusCode != 200 {
			t.Fatalf("view: %d %s", r.StatusCode, b)
		}
		var h struct {
			Bunit any `json:"bunit"`
		}
		json.Unmarshal(b, &h)
		return h.Bunit, len(ep.calls) > before
	}
	unit := func(u any) int {
		st, _ := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/unit", u)
		return st
	}

	if told, asked := plane(); told != nil || !asked {
		t.Fatalf("no declaration: %v %v", told, asked)
	}
	if told, asked := plane(); told != nil || asked {
		t.Fatalf("the same request twice must be one worker call: %v %v", told, asked)
	}
	if unit(map[string]any{"unit": "Jy/beam"}) != 200 {
		t.Fatal("declare")
	}
	if told, asked := plane(); told != "Jy/beam" || !asked {
		t.Fatalf("the unit must reach the worker, and the no-unit entry must not be served: %v %v", told, asked)
	}
	if told, asked := plane(); told != "Jy/beam" || asked {
		t.Fatalf("a repeat with the same unit is cached: %v %v", told, asked)
	}
	unit(map[string]any{"unit": "mJy/beam"})
	if told, asked := plane(); told != "mJy/beam" || !asked {
		t.Fatalf("a changed unit must not be served from the old unit's entry: %v %v", told, asked)
	}
	unit(map[string]any{"clear": true})
	if told, _ := plane(); told != nil {
		t.Fatalf("a withdrawn unit was still served: %v", told)
	}
	// declared, then the file is rewritten and read again: the declaration describes other bytes, so it is not applied
	unit(map[string]any{"unit": "Jy/beam"})
	os.WriteFile(file, []byte("xx"), 0o644)
	json.NewDecoder(post(t, ts, file, nil).Body).Decode(&d)
	if d.UnitDeclaration == nil || !d.UnitDeclaration.Stale {
		t.Fatalf("a rewritten file must mark the declaration stale: %+v", d.UnitDeclaration)
	}
	if told, _ := plane(); told != nil {
		t.Fatalf("a stale declaration was applied: %v", told)
	}
	// declaring again confirms it for the file as it is now
	unit(map[string]any{"unit": "Jy/beam"})
	if told, _ := plane(); told != "Jy/beam" {
		t.Fatalf("%v", told)
	}
}
