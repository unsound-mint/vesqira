package reports

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// packages/contracts/fixtures/report-blocks.json pins how a body splits into blocks. The web editor splits the same
// way (apps/web/src/reports/blocks.test.ts reads the same file), so a comment anchored to a block on the reader's
// page is the block the author edits. Change both sides together, and regenerate the file from this implementation.
func TestBlocksMatchTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/contracts/fixtures/report-blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Body   string  `json:"body"`
		Blocks []Block `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := Blocks(c.Body)
		if got == nil {
			got = []Block{}
		}
		if !reflect.DeepEqual(got, c.Blocks) {
			t.Errorf("body %q:\n got %#v\nwant %#v", c.Body, got, c.Blocks)
		}
	}
}
