package server

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestEditorResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := newEditor(t, true)
	p := filepath.Join(e.root, "a.toml")
	write(t, p, []byte("x = 1\n"), 0o644)
	_, b := e.do(t, "GET", e.ts.URL+"/api/files/text?path="+p, nil, nil)
	conforms(t, responseSchema(t, doc, "/api/files/text", "GET", "application/json"), "GET /api/files/text", b)
	var f textFile
	_ = json.Unmarshal(b, &f)
	write(t, p, []byte("x = 3\n"), 0o644) // changed behind the editor's back
	code, b := e.save(t, p, "x = 2\n", f.textVersion)
	if code != 409 {
		t.Fatalf("%d", code)
	}
	op := doc.Paths.Find("/api/files/text").GetOperation("PUT")
	conforms(t, op.Responses.Status(409).Value.Content.Get("application/json").Schema, "PUT 409", b)
	var c textConflict
	_ = json.Unmarshal(b, &c)
	_, b = e.save(t, p, "x = 2\n", c.Current.textVersion)
	conforms(t, responseSchema(t, doc, "/api/files/text", "PUT", "application/json"), "PUT 200", b)
	body, _ := json.Marshal(textSave{Path: p, Content: "x", Base: f.textVersion})
	var v any
	_ = json.Unmarshal(body, &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the request body does not match the spec: %v", err)
	}
}

func TestHostEditorResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := newHostEditor(t)
	p := filepath.Join(e.root, "a.toml")
	write(t, p, []byte("x = 1\n"), 0o644)
	_, f, b := e.openH(t, p)
	conforms(t, responseSchema(t, doc, "/api/files/text", "GET", "application/json"), "GET /api/files/text?hostId", b)
	write(t, p, []byte("x = 3\n"), 0o644)
	code, b := e.saveH(t, p, "x = 2\n", f.textVersion)
	if code != 409 {
		t.Fatalf("%d", code)
	}
	op := doc.Paths.Find("/api/files/text").GetOperation("PUT")
	conforms(t, op.Responses.Status(409).Value.Content.Get("application/json").Schema, "PUT 409 (host)", b)
	var c textConflict
	_ = json.Unmarshal(b, &c)
	_, b = e.saveH(t, p, "x = 2\n", c.Current.textVersion)
	conforms(t, responseSchema(t, doc, "/api/files/text", "PUT", "application/json"), "PUT 200 (host)", b)
	body, _ := json.Marshal(textSave{Path: p, Content: "x", Base: f.textVersion, HostID: e.hostID})
	var v any
	_ = json.Unmarshal(body, &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the request body does not match the spec: %v", err)
	}
	get := doc.Paths.Find("/api/files/text").GetOperation("GET")
	for _, status := range []int{404, 413, 415, 501, 502} {
		if op.Responses.Status(status) == nil || get.Responses.Status(status) == nil {
			t.Errorf("status %d is not described", status)
		}
	}
}
