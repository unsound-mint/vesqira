package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// A catalog overlay end to end with the real worker (ADR 0042, ADR 0044): the stored declaration is the authority (never the
// scene), a changed declaration is not answered from the cache, kinds and datasets must match both ways, and the frame and its
// header conform to the spec.
func TestCatalogOverlayEndToEndWithRealWorker(t *testing.T) {
	t.Parallel()
	url, root, repo := catalogServer(t)
	doc := loadSpec(t)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var hi catalog.Dataset
	if st := postJSONStatus(t, url+"/api/datasets", map[string]string{"path": filepath.Join(root, "hi_cube.fits")}, &hi); st != 201 {
		t.Fatalf("register cube: %d", st)
	}
	// hi_cube is TAN at (150, 2) with 0.01 degree pixels, reference pixel (15, 11) 0-based
	table := "ra,dec,pa,p,sp\n150.0,2.0,0,2.0,0.1\n150.0,2.05,90,1.0,0.5\n150.0,40.0,0,3.0,0.1\n"
	if err := os.WriteFile(filepath.Join(root, "stars.csv"), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := registerCatalogFile(t, url, filepath.Join(root, "stars.csv"))
	headSchema := doc.Components.Schemas["OverlayGeometryHeader"]
	reqSchema := requestSchema(t, doc, "/api/datasets/{id}/overlay-geometry", "POST")
	ask := func(overlay map[string]any) (int, map[string]any, []byte) {
		t.Helper()
		body := map[string]any{"overlay": overlay}
		raw, _ := json.Marshal(body)
		conforms(t, reqSchema, "catalog overlay request", raw)
		r, b := postJSON(t, url+"/api/datasets/"+itoa(int(hi.ID))+"/overlay-geometry", body)
		if r.StatusCode != 200 {
			var e map[string]any
			json.Unmarshal(b, &e)
			return r.StatusCode, e, nil
		}
		n := binary.LittleEndian.Uint32(b[:4])
		head := bytes.TrimRight(b[4:4+n], " ")
		conforms(t, headSchema, "catalog overlay header", head)
		var h map[string]any
		json.Unmarshal(head, &h)
		return 200, h, b[4+n:]
	}
	overlay := func(extra map[string]any) map[string]any {
		o := map[string]any{"id": "stars", "kind": "catalog", "datasetId": cat.ID, "visible": true}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}

	// no declaration yet: refused with the reason, not guessed
	if code, e, _ := ask(overlay(nil)); code != 409 || !strings.Contains(e["error"].(string), "no columns are declared") {
		t.Fatalf("undeclared: %d %v", code, e)
	}
	decl := func(frame string, quantity any) map[string]any {
		return map[string]any{"lon": "ra", "lat": "dec", "format": "degrees", "frame": frame,
			"angle": map[string]any{"column": "pa", "unit": "deg", "frame": "sky", "quantity": quantity, "sigmaColumn": "sp"}, "values": []string{"p"}}
	}
	put := func(d map[string]any) {
		t.Helper()
		if st, b := callJSON(t, "PUT", url+"/api/datasets/"+itoa(int(cat.ID))+"/catalog/declaration", d); st != 200 {
			t.Fatalf("declare: %d %s", st, b)
		}
	}
	put(decl("icrs", nil))
	// markers need no angle meaning; vectors are refused naming the quantity
	code, h, payload := ask(overlay(nil))
	if code != 200 || h["kind"] != "catalog" || h["markers"].(float64) != 2 {
		t.Fatalf("markers: %d %v", code, h)
	}
	if len(payload) != 24*2 {
		t.Fatalf("payload %d bytes", len(payload))
	}
	px := func(i, k int) float64 {
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[24*i+4*k:])))
	}
	if math.Abs(px(0, 0)-15) > 0.5 || math.Abs(px(0, 1)-11) > 0.5 || px(0, 5) != 1 || math.Abs(px(1, 1)-16) > 0.5 {
		t.Fatalf("positions %v %v / %v %v", px(0, 0), px(0, 1), px(1, 0), px(1, 1))
	}
	counts := h["counts"].(map[string]any)
	if counts["outside"].(float64) != 1 || counts["drawn"].(float64) != 2 || counts["rows"].(float64) != 3 {
		t.Fatalf("counts %v", counts)
	}
	if code, e, _ := ask(overlay(map[string]any{"vectors": true})); code != 409 || !strings.Contains(e["error"].(string), "does not declare what its angle column") {
		t.Fatalf("vectors without a quantity: %d %v", code, e)
	}
	// a declared quantity: vectors drawn; a redeclared quantity changes the geometry (the cache holds the declaration)
	vec := map[string]any{"vectors": true, "lengthColumn": "p", "filter": map[string]any{"column": "p", "sigmaColumn": "sp", "min": 5}}
	put(decl("icrs", "starlight_polarization"))
	code, h, payload = ask(overlay(vec))
	if code != 200 || h["markers"].(float64) != 1 || h["filter"].(map[string]any)["removed"].(float64) != 1 {
		t.Fatalf("vectors: %d %v", code, h)
	}
	a1 := px(0, 2)
	put(decl("icrs", "dust_polarization"))
	code, _, payload = ask(overlay(vec))
	a2 := px(0, 2)
	if code != 200 || math.Abs(math.Mod(a2-a1+180, 180)-90) > 0.1 {
		t.Fatalf("dust vs starlight: %v %v (code %d)", a1, a2, code)
	}
	// columns that do not exist, and the wrong kind of dataset
	if code, e, _ := ask(overlay(map[string]any{"colorColumn": "nope"})); code != 409 || !strings.Contains(e["error"].(string), "colour column") {
		t.Fatalf("missing column: %d %v", code, e)
	}
	if code, _, _ := ask(map[string]any{"id": "c", "kind": "contours", "datasetId": cat.ID, "visible": true, "channel": 0, "levels": map[string]any{"mode": "list", "values": []float64{1}}}); code != 409 {
		t.Fatalf("contours of a catalog: %d", code)
	}
	if code, _, _ := ask(map[string]any{"id": "s", "kind": "catalog", "datasetId": hi.ID, "visible": true}); code != 409 {
		t.Fatalf("catalog overlay of an image: %d", code)
	}
	// the file changed since it was declared: the declaration is stale and not applied
	if err := os.WriteFile(filepath.Join(root, "stars.csv"), []byte(table+"150.0,2.01,0,1,0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := ask(overlay(nil)); code != 409 {
		t.Fatalf("changed catalog file: %d", code)
	}
}

func postJSONStatus(t *testing.T, url string, body any, into any) int {
	t.Helper()
	r, b := postJSON(t, url, body)
	if into != nil && r.StatusCode < 300 {
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatal(err)
		}
	}
	return r.StatusCode
}
