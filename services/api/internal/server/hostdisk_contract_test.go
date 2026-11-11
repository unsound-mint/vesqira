package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostDiskResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := newWatchEnv(t)
	base := "/api/hosts/" + itoa(int(e.host.ID))
	expire := func() {
		e.srv.disk.mu.Lock()
		e.srv.disk.look[e.host.ID].at = time.Now().Add(-time.Hour)
		e.srv.disk.mu.Unlock()
	}
	// no tool answers and nothing was ever measured: the nullable fields are real nulls
	os.WriteFile(filepath.Join(e.bin, "quota"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	_, b := get(t, e.api(base+"/storage"))
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/storage", "GET", "application/json"), "GET storage without a quota tool", b)
	os.WriteFile(filepath.Join(e.bin, "beegfs-ctl"), []byte("#!/bin/sh\necho '  spectre|  1000||  395.20 GiB|  400.00 GiB||  1234567|unlimited'\n"), 0o755)
	expire()
	_, b = postJSON(t, e.api(base+"/storage/refresh"), map[string]any{})
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/storage/refresh", "POST", "application/json"), "POST storage/refresh with a quota", b)
	if !strings.Contains(string(b), `"tool":"beegfs-ctl"`) {
		t.Errorf("%s", b)
	}
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	expire()
	_, b = get(t, e.api(base+"/storage"))
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/storage", "GET", "application/json"), "GET storage of an unreachable host", b)
	os.Remove(filepath.Join(e.home, "DOWN"))

	j := e.submit(e.draft("run-a", nil), nil)
	os.MkdirAll(j.OutputDir, 0o755)
	os.WriteFile(filepath.Join(j.OutputDir, "o"), make([]byte, 1<<20), 0o644)
	_, b = get(t, e.api(base+"/folders"))
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/folders", "GET", "application/json"), "GET folders unmeasured", b)
	r, b := postJSON(t, e.api(base+"/folders/size"), map[string]any{"path": j.OutputDir})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/folders/size", "POST", "application/json"), "POST folders/size", b)
	_, b = get(t, e.api(base+"/folders"))
	conforms(t, responseSchema(t, doc, "/api/hosts/{id}/folders", "GET", "application/json"), "GET folders measured", b)
	op := doc.Paths.Find("/api/hosts/{id}/folders/size").GetOperation("POST")
	var v any
	_ = json.Unmarshal([]byte(`{"path":"/x"}`), &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the request body does not match the spec: %v", err)
	}
}
