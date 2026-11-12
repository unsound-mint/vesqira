package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
)

// The history routes answer as the spec says, including a step with no parent, one with a parent, and the single-step read with its scene.
func TestExplorationHistoryResponsesMatchTheSpec(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := history.Open(cat.DB())
	d, err := cat.Upsert(catalog.Dataset{Path: "/data/cube.fits", Name: "cube.fits", SizeBytes: 1, ModifiedAt: time.Now(), Header: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: cat, History: hs}).Handler())
	defer ts.Close()
	check := func(path, method string, status int, label string, body []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, path, method, status, "application/json"), label, body)
	}
	_, b := get(t, ts.URL+"/api/history")
	check("/api/history", "GET", 200, "GET empty history", b)
	r, b := postJSON(t, ts.URL+"/api/history", map[string]any{"projectId": 0, "kind": "opened", "label": "Opened cube.fits", "scene": sceneFor(d.ID, 0)})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/history", "POST", 201, "POST first step", b)
	var first struct{ ID int64 }
	_ = json.Unmarshal(b, &first)
	r, b = postJSON(t, ts.URL+"/api/history", map[string]any{"projectId": 0, "parentId": first.ID, "kind": "channel", "label": "Channel 4", "scene": sceneFor(d.ID, 3)})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/history", "POST", 201, "POST child step", b)
	_, b = get(t, ts.URL+"/api/history?project=0")
	check("/api/history", "GET", 200, "GET history", b)
	_, b = get(t, ts.URL+"/api/history/"+itoa(int(first.ID)))
	check("/api/history/{id}", "GET", 200, "GET one step", b)
	// the request body in the spec accepts what the browser sends, and refuses an unknown field
	op := doc.Paths.Find("/api/history").GetOperation("POST")
	var v any
	_ = json.Unmarshal([]byte(`{"projectId":0,"parentId":null,"kind":"link","label":"x","scene":`+string(mustJSON(t, sceneFor(1, 0)))+`}`), &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the request body does not match the spec: %v", err)
	}
	v.(map[string]any)["extra"] = 1
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err == nil {
		t.Error("the spec accepts an unknown field")
	}
}
