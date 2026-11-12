package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// The reflection test of ADR 0011 and ADR 0036, extended to the four pushed types: every field name a pushed document can carry
// (the frozen figure with its provenance, the run line, a board snapshot, a report revision, and every type inside them, derived
// results, controls and cross-run plots among them) passes the scan both sides run, so none is named like a location or a
// credential. A new field named like one fails here before it is refused on the wire.
func TestEveryPushedFieldNamePassesTheScan(t *testing.T) {
	seen := map[reflect.Type]bool{}
	n := 0
	var walk func(rt reflect.Type, trail string)
	walk = func(rt reflect.Type, trail string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] || rt.PkgPath() == "time" {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			n++
			if err := pushwire.Scan(json.RawMessage(`{"` + name + `":1}`)); err != nil {
				t.Errorf("%s.%s: %v", trail, name, err)
			}
			walk(f.Type, trail+"."+name)
		}
	}
	for _, v := range []any{pushedFigure{}, Provenance{}, runs.Summary{}, pushedBoard{}, publish.Revision{}, pushwire.Item{}} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Name())
	}
	if n < 60 {
		t.Fatalf("only %d field names walked", n)
	}
}
