package server

import (
	"encoding/json"
	"testing"
)

// The bodies the sweep routes send and take are the ones packages/contracts describes (ADR 0032).
func TestSweepResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := newWatchEnv(t)
	check := func(method, specPath, url string, in map[string]any, status int) []byte {
		t.Helper()
		if op := doc.Paths.Find(specPath).GetOperation(method); in != nil && op != nil && op.RequestBody != nil {
			conforms(t, requestSchema(t, doc, specPath, method), method+" "+specPath+" request", mustJSON(t, in))
		}
		var b []byte
		var code int
		if method == "GET" {
			code, b = getBody(t, url)
		} else {
			r, body := postJSON(t, url, in)
			code, b = r.StatusCode, body
		}
		if code != status {
			t.Fatalf("%s %s: %d %s", method, url, code, b)
		}
		conforms(t, responseSchemaAt(t, doc, specPath, method, status, "application/json"), method+" "+specPath, b)
		return b
	}
	req := e.sweepRequest("spec", p("dn", "8", "16"), map[string]any{"key": "ker", "label": "κ", "values": []string{"2", "4"}})
	check("POST", "/api/sweeps/preview", e.api("/api/sweeps/preview"), req, 200)
	var sw sweepOut
	json.Unmarshal(check("POST", "/api/sweeps", e.api("/api/sweeps"), req, 201), &sw)
	id := itoa(int(sw.ID))
	check("GET", "/api/sweeps", e.api("/api/sweeps"), nil, 200)
	check("GET", "/api/sweeps/{id}", e.api("/api/sweeps/"+id), nil, 200)
	hash, _ := e.sweepReview(sw.ID)
	check("GET", "/api/sweeps/{id}/review", e.api("/api/sweeps/"+id+"/review"), nil, 200)
	check("POST", "/api/sweeps/{id}/submit", e.api("/api/sweeps/"+id+"/submit"), map[string]any{"sweepHash": hash, "concurrency": 1}, 200)
	e.srv.waitSweeps()
	e.srv.waitRecords()
	check("GET", "/api/sweeps/{id}/grid", e.api("/api/sweeps/"+id+"/grid"), nil, 200)
	check("POST", "/api/sweeps/{id}/board", e.api("/api/sweeps/"+id+"/board"), map[string]any{}, 201)
	check("GET", "/api/boards/{id}", e.api("/api/boards/1"), nil, 200)
	check("POST", "/api/sweeps/{id}/cancel", e.api("/api/sweeps/"+id+"/cancel"), map[string]any{"cancelRunning": true}, 200)
}
