package review

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// What a supervisor's browser receives is decoded from the real handler and validated against
// packages/contracts/openapi.yaml (ADR 0032), the same file the web client's types are generated from.

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

// exchange sends one request to the real handler and checks what the spec declares for it: the request body, the
// status, the media type, the body's schema and every response header with a fixed value.
func exchange(t *testing.T, doc *openapi3.T, ts *httptest.Server, method, specPath, url string, in any, wantStatus int, wantType string) []byte {
	t.Helper()
	op := doc.Paths.Find(specPath).GetOperation(method)
	if op == nil {
		t.Fatalf("the spec has no %s %s", method, specPath)
	}
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		rb := op.RequestBody
		if rb == nil || rb.Value.Content.Get("application/json") == nil {
			t.Fatalf("the spec describes no JSON request body for %s %s", method, specPath)
		}
		validate(t, rb.Value.Content.Get("application/json").Schema, method+" "+specPath+" request", raw)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, ts.URL+url, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: %d, want %d: %s", method, url, resp.StatusCode, wantStatus, out)
	}
	r := op.Responses.Status(wantStatus)
	if r == nil {
		r = op.Responses.Default()
	}
	if r == nil || r.Value == nil {
		t.Fatalf("the spec describes no %d response for %s %s", wantStatus, method, specPath)
	}
	if ct := strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]; ct != wantType {
		t.Fatalf("%s %s: content type %q, want %q", method, url, ct, wantType)
	}
	mt := r.Value.Content.Get(wantType)
	if mt == nil {
		t.Fatalf("the spec describes no %s response with status %d for %s %s", wantType, wantStatus, method, specPath)
	}
	if wantType == "application/json" {
		validate(t, mt.Schema, method+" "+specPath+" "+resp.Status, out)
	}
	for name, h := range r.Value.Headers {
		if want := h.Value.Schema.Value.Enum; len(want) == 1 && resp.Header.Get(name) != want[0] {
			t.Errorf("%s %s: header %s is %q, the spec says %q", method, url, name, resp.Header.Get(name), want[0])
		}
	}
	return out
}

func validate(t *testing.T, schema *openapi3.SchemaRef, label string, body []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", label, err, body)
	}
	if err := schema.Value.VisitJSON(v, openapi3.MultiErrors()); err != nil {
		t.Errorf("%s does not match the spec: %v\n%s", label, err, body)
	}
}

// provenance is the shape the owner API writes for a figure (FigureProvenance), here for a file with no unit.
const provenance = `{"source":{"name":"cube.fits","sizeBytes":1024,"modifiedAt":"2026-01-02T03:04:05Z","identity":"declared by the file name only",
"headerDeclaredByFile":{"axes":[{"axis":1,"length":4,"ctype":"RA---SIN","cunit":"deg","crval":null,"cdelt":null}],"bunit":null,"radesys":null,"specsys":null,"bitpix":-32}},
"view":{"kind":"plane","channel":3,"colormap":"viridis","stretch":"linear","range":"percentile"},"statements":["Preview image; not the data."]}`

func TestReviewerResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	dir := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	st, err := boards.Open(cat.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var pb bytes.Buffer
	png.Encode(&pb, image.NewGray(image.Rect(0, 0, 4, 4)))
	fig, err := st.CreateFigure(boards.NewFigure{DatasetID: 1, Title: "Peak", Caption: "cap", Provenance: json.RawMessage(provenance)}, pb.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	bd, _ := st.CreateBoard("B")
	st.AddItem(bd.ID, fig.ID)
	st.AddJobItem(bd.ID, 7)
	// v1 is replaced by v2, so the reviewer of v1 gets the superseded branch; it also carries a job panel
	v1, tok, err := st.PublishWith(bd.ID, boards.PublishOptions{Label: "v1", Question: "Is it real?", TTL: time.Hour, Jobs: map[int64]boards.JobSnapshot{7: {Name: "vgt-ker16", State: "running"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = v1
	_, tok2, err := st.PublishWith(bd.ID, boards.PublishOptions{Label: "v2", TTL: time.Hour, Supersedes: v1.ID, Reason: "rescaled"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Handler{Store: st}).Handler())
	t.Cleanup(ts.Close)

	const spec = "/review-api/{token}"
	review := func(token string, status int) []byte {
		return exchange(t, doc, ts, "GET", spec, "/review-api/"+token, nil, status, "application/json")
	}

	// nothing said yet: no comments, an empty outcome, and no superseded notice on the newest version
	fresh := review(tok2, 200)
	for _, want := range []string{`"comments":[]`, `"outcome":{"value":""}`} {
		if !strings.Contains(string(fresh), want) {
			t.Errorf("a fresh review should contain %s: %s", want, fresh)
		}
	}

	c := "/review-api/" + tok + "/comments"
	cspec := spec + "/comments"
	post := func(in any, status int) []byte {
		return exchange(t, doc, ts, "POST", cspec, c, in, status, "application/json")
	}
	var first boards.Comment
	json.Unmarshal(post(map[string]any{"figureRevision": fig.Revision, "author": "Sam", "body": "Is this a ring?",
		"anchor": map[string]any{"kind": "box", "x": 0.1, "y": 0.2, "x1": 0.5, "y1": 0.6}}, 201), &first)
	point := post(map[string]any{"figureRevision": fig.Revision, "body": "and here", "anchor": map[string]any{"kind": "point", "x": 0.3, "y": 0.4}}, 201)
	if !strings.Contains(string(point), `"anchor":{"kind":"point","x":0.3,"y":0.4}`) {
		t.Errorf("a point anchor should carry no box corner: %s", point)
	}
	post(map[string]any{"parentId": first.ID, "author": "Sam", "body": "reply"}, 201)
	post(map[string]any{"figureRevision": fig.Revision, "body": ""}, 422)
	post(map[string]any{"figureRevision": fig.Revision, "body": "x", "anchor": map[string]any{"kind": "box", "x": 0.9, "y": 0.9, "x1": 0.1, "y1": 0.1}}, 422)
	if _, err := st.OwnerReply(v1.ID, boards.CommentInput{ParentID: first.ID, Body: "No, an artefact"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResolved(first.ID, true); err != nil {
		t.Fatal(err)
	}

	o := "/review-api/" + tok + "/outcome"
	ospec := spec + "/outcome"
	exchange(t, doc, ts, "POST", ospec, o, map[string]any{"outcome": "changes_requested", "note": "rescale", "author": "Sam"}, 201, "application/json")
	exchange(t, doc, ts, "POST", ospec, o, map[string]any{"outcome": "reviewed"}, 201, "application/json")
	// an outcome outside the enum is refused by the server whatever the client sends, so the body skips request validation
	r422, err := http.Post(ts.URL+o, "application/json", strings.NewReader(`{"outcome":"approved"}`))
	if err != nil {
		t.Fatal(err)
	}
	b422, _ := io.ReadAll(r422.Body)
	r422.Body.Close()
	if r422.StatusCode != 422 {
		t.Fatalf("an unknown outcome: %d %s", r422.StatusCode, b422)
	}
	validate(t, doc.Components.Schemas["Error"], "422 outcome body", b422)

	full := review(tok, 200)
	for _, want := range []string{`"superseded"`, `"kind":"job"`, `"fromOwner":true`, `"resolvedAt"`, `"value":"reviewed"`, `"question":"Is it real?"`} {
		if !strings.Contains(string(full), want) {
			t.Errorf("the review should exercise %s: %s", want, full)
		}
	}

	exchange(t, doc, ts, "GET", spec+"/preview/{sha}", "/review-api/"+tok+"/preview/"+fig.PreviewSHA, nil, 200, "image/png")

	// every failure for a bad link is the same JSON 404; a bad body or media type is plain text, as the spec says
	bad := strings.Repeat("a", 43)
	review(bad, 404)
	exchange(t, doc, ts, "GET", spec+"/preview/{sha}", "/review-api/"+tok+"/preview/"+strings.Repeat("a", 64), nil, 404, "application/json")
	exchange(t, doc, ts, "POST", cspec, "/review-api/"+bad+"/comments", map[string]any{"figureRevision": "x", "body": "x"}, 404, "application/json")
	exchange(t, doc, ts, "POST", ospec, "/review-api/"+bad+"/outcome", map[string]any{"outcome": "reviewed"}, 404, "application/json")
	resp, err := http.Post(ts.URL+c, "text/plain", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("a non-JSON comment is a plain-text 415, got %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if op := doc.Paths.Find(cspec).GetOperation("POST"); op.Responses.Default().Value.Content.Get("text/plain") == nil {
		t.Errorf("the spec should say that 415 is plain text")
	}
}
