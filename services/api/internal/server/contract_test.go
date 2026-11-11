package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// These tests decode real handler responses and validate them against packages/contracts/openapi.yaml (ADR 0032), so the Go
// handlers and the spec the web client's types are generated from cannot drift apart. They use the real worker on the
// generated fixtures: a stub would only prove that the stub matches the spec.

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "..", "..", "packages", "contracts", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatalf("the spec is not valid OpenAPI: %v", err)
	}
	return doc
}

// conforms checks a JSON document against a named schema of the spec and fails with the path of the first mismatch.
func conforms(t *testing.T, schema *openapi3.SchemaRef, label string, body []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", label, err, body)
	}
	if err := schema.Value.VisitJSON(v, openapi3.MultiErrors()); err != nil {
		t.Errorf("%s does not match the spec: %v\n%s", label, err, body)
	}
}

// responseSchema is the schema the spec declares for the 200 response of an operation in one content type.
func responseSchema(t *testing.T, doc *openapi3.T, path, method, contentType string) *openapi3.SchemaRef {
	t.Helper()
	return responseSchemaAt(t, doc, path, method, 200, contentType)
}

func responseSchemaAt(t *testing.T, doc *openapi3.T, path, method string, status int, contentType string) *openapi3.SchemaRef {
	t.Helper()
	op := doc.Paths.Find(path).GetOperation(method)
	if op == nil {
		t.Fatalf("the spec has no %s %s", method, path)
	}
	r := op.Responses.Status(status)
	if r == nil || r.Value == nil || r.Value.Content.Get(contentType) == nil {
		t.Fatalf("the spec describes no %s response with status %d for %s %s", contentType, status, method, path)
	}
	return r.Value.Content.Get(contentType).Schema
}

// requestSchema is the JSON body schema the spec declares for an operation.
func requestSchema(t *testing.T, doc *openapi3.T, path, method string) *openapi3.SchemaRef {
	t.Helper()
	op := doc.Paths.Find(path).GetOperation(method)
	if op == nil || op.RequestBody == nil || op.RequestBody.Value.Content.Get("application/json") == nil {
		t.Fatalf("the spec describes no JSON request body for %s %s", method, path)
	}
	return op.RequestBody.Value.Content.Get("application/json").Schema
}

func TestResponsesMatchTheSpecForTheViewerRoutes(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	doc := loadSpec(t)
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts, root, _ := newPlaneServerWith(t, pw, pw)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	// each fixture exercises a different branch of the shapes: celestial or pixel-only sky, a resolved or missing spectral
	// axis, declared or absent flags, a scaled integer file, a reduced preview
	files := []string{"hi_cube.fits", "hi_cube_no_spectral_ref.fits", "hi_cube_no_celestial_wcs.fits", "cube_int16_scaled.fits", "cube_stokes_freq.fits", "vgt_psi_deg.fits"}
	for _, name := range files {
		resp := post(t, ts, filepath.Join(root, name), nil)
		if resp.StatusCode != 201 {
			t.Fatalf("%s: register %d", name, resp.StatusCode)
		}
	}
	_, list := get(t, ts.URL+"/api/datasets")
	conforms(t, responseSchema(t, doc, "/api/datasets", "GET", "application/json"), "GET /api/datasets", list)
	var l struct{ Datasets []struct{ ID int } }
	json.Unmarshal(list, &l)
	if len(l.Datasets) != len(files) {
		t.Fatalf("want %d datasets, got %d", len(files), len(l.Datasets))
	}

	one := responseSchema(t, doc, "/api/datasets/{id}", "GET", "application/json")
	view := responseSchema(t, doc, "/api/datasets/{id}/view", "GET", "application/json")
	plane := doc.Paths.Find("/api/datasets/{id}/plane").GetOperation("GET").Responses.Status(200).Value.Content.Get("application/vnd.vesqira.plane")
	if plane == nil || plane.Extensions["x-frame-header"] == nil {
		t.Fatal("the spec must describe the plane content type and its JSON header (x-frame-header)")
	}
	headerSchema := doc.Components.Schemas["PlaneHeader"]
	for _, d := range l.Datasets {
		api := ts.URL + "/api/datasets/" + itoa(d.ID)
		_, b := get(t, api)
		conforms(t, one, "GET /api/datasets/{id} "+itoa(d.ID), b)
		_, b = get(t, api+"/view")
		conforms(t, view, "GET /api/datasets/{id}/view "+itoa(d.ID), b)
		var v struct{ Channels int }
		json.Unmarshal(b, &v)
		for _, q := range []string{"?channel=0", "?channel=0&step=2"} {
			if r, b := get(t, api+"/plane"+q); r.StatusCode == 200 {
				if ct := r.Header.Get("Content-Type"); ct != "application/vnd.vesqira.plane" {
					t.Fatalf("plane content type %q", ct)
				}
				n := binary.LittleEndian.Uint32(b[:4])
				conforms(t, headerSchema, "plane header "+itoa(d.ID)+q, bytes.TrimRight(b[4:4+n], " "))
			} else if q == "?channel=0" {
				t.Fatalf("plane %d: %d %s", d.ID, r.StatusCode, b) // a preview may be refused (orientation map), the full plane may not
			}
		}
	}

	// the spec names the Error shape for failures; check the real one
	r, b := get(t, ts.URL+"/api/datasets/999")
	if r.StatusCode != 404 {
		t.Fatalf("want 404, got %d", r.StatusCode)
	}
	conforms(t, doc.Components.Schemas["Error"], "404 body", b)
}
