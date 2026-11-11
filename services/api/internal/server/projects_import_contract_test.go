package server

import (
	"fmt"
	"testing"
)

// The import routes answer what the spec says they answer, in every state a client meets: no rules, a draft with a conflict and a
// broken rule, saved rules, an apply with registered, declared, duplicate and skipped files, and a refusal.
func TestImportResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := importEnv(t)
	root := e.project(t, "owner")
	ownerProject(t, root)
	tinyFITS(t, root, "dup/copy_kms.fits", 3, "") // the same bytes as data/exp_raw/cube_a.fits
	s := e.open(t, root)
	base := fmt.Sprintf("/api/projects/%d/import", s.ID)
	check := func(path, method string, status int, label string, body []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, path, method, status, "application/json"), label, body)
	}
	call := func(method, p string, body any, want int) []byte {
		t.Helper()
		var code int
		var b []byte
		if method == "PUT" {
			code, b = e.putJSON(t, p, body)
		} else {
			code, b = e.call(t, method, p, body)
		}
		if code != want {
			t.Fatalf("%s %s: %d %s", method, p, code, b)
		}
		return b
	}
	const rules = "/api/projects/{id}/import/rules"
	check(rules, "GET", 200, "no rules", call("GET", base+"/rules", nil, 200))
	check("/api/projects/{id}/import/preview", "POST", 200, "preview with no rules", call("POST", base+"/preview", map[string]any{}, 200))
	broad := map[string]any{"name": "all of vda", "kind": "regex", "pattern": `^vda/`, "role": "ignore"}
	broken := map[string]any{"name": "broken", "kind": "regex", "pattern": "(", "role": "ignore"}
	check("/api/projects/{id}/import/preview", "POST", 200, "draft with a conflict and a broken rule",
		call("POST", base+"/preview", map[string]any{"rules": []any{psiRule, kmsRule, broad, broken}, "expand": "orientation|.fits|psi maps"}, 200))
	check("/api/projects/{id}/import/preview", "POST", 422, "a folder outside the project", call("POST", base+"/preview", map[string]any{"folder": "../x"}, 422))
	check(rules, "PUT", 422, "invalid rules", call("PUT", base+"/rules", map[string]any{"rules": []any{map[string]any{"name": "o", "kind": "glob", "pattern": "*.fits", "role": "orientation"}}}, 422))
	check(rules, "PUT", 200, "saved", call("PUT", base+"/rules", map[string]any{"rules": []any{psiRule, kmsRule}}, 200))
	check(rules, "GET", 200, "saved rules", call("GET", base+"/rules", nil, 200))
	check("/api/projects/{id}/import/apply", "POST", 400, "not confirmed", call("POST", base+"/apply", map[string]any{}, 400))
	check("/api/projects/{id}/import/apply", "POST", 200, "applied", call("POST", base+"/apply", map[string]any{"confirm": true}, 200))
	check("/api/projects/{id}/import/preview", "POST", 200, "preview after applying", call("POST", base+"/preview", map[string]any{}, 200))
	check("/api/projects/{id}/import/rules/write", "POST", 400, "not confirmed", call("POST", base+"/rules/write", map[string]any{}, 400))
	check("/api/projects/{id}/import/rules/write", "POST", 200, "written", call("POST", base+"/rules/write", map[string]any{"write": true}, 200))
	check(rules, "GET", 200, "rules in the file", call("GET", base+"/rules", nil, 200))
}
