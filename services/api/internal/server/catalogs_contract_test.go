package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// The wire format of the catalog routes is the one packages/contracts/openapi.yaml describes (ADR 0032): real handler answers,
// the real worker, validated against the spec the web client's types are made from.
func TestCatalogResponsesMatchTheSpec(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	url, root, _ := catalogServer(t)
	f := filepath.Join(root, "stars.dat")
	os.WriteFile(f, []byte(starlight), 0o644)
	st, b := callJSON(t, "POST", url+"/api/catalogs", map[string]string{"path": f})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/catalogs", "POST", 201, "application/json"), "POST /api/catalogs", b)
	var d catalog.Dataset
	json.Unmarshal(b, &d)
	conforms(t, requestSchema(t, doc, "/api/datasets/{id}/catalog/declaration", "PUT"), "the declaration request", mustJSON(t, goodDeclaration()))
	base := fmt.Sprintf("%s/api/datasets/%d", url, d.ID)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}", "GET", "application/json"), "the dataset before a declaration", mustGet(t, base))
	_, b = callJSON(t, "PUT", base+"/catalog/declaration", goodDeclaration())
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/catalog/declaration", "PUT", "application/json"), "PUT declaration", b)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}", "GET", "application/json"), "the dataset with a declaration", mustGet(t, base))
	for path, route := range map[string]string{
		"/catalog/report": "/api/datasets/{id}/catalog/report", "/catalog/rows?limit=2": "/api/datasets/{id}/catalog/rows",
		"/catalog/positions": "/api/datasets/{id}/catalog/positions", "/catalog/positions?frame=galactic": "/api/datasets/{id}/catalog/positions",
	} {
		_, b := callJSON(t, "GET", base+path, nil)
		conforms(t, responseSchema(t, doc, route, "GET", "application/json"), "GET "+path, b)
	}
	_, b = callJSON(t, "DELETE", base+"/catalog/declaration", nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/catalog/declaration", "DELETE", "application/json"), "DELETE declaration", b)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustGet(t *testing.T, url string) []byte {
	t.Helper()
	st, b := callJSON(t, "GET", url, nil)
	if st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	return b
}
