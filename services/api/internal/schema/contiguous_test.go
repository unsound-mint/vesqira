package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Branches developed in parallel each pick "the next" schema version and are renumbered when they merge. This pins what
// a renumbering must leave behind: migrations numbered 2..Version with no gap or repeat, and a snapshot for every version
// a migration produces.
func TestMigrationsAreContiguousAndEachHasASnapshot(t *testing.T) {
	for i, m := range migrations {
		if want := i + 2; m.To != want {
			t.Fatalf("migration %d (%q) is numbered %d, want %d: versions must run 2..Version with no gap or repeat", i, m.Name, m.To, want)
		}
	}
	if last := migrations[len(migrations)-1].To; last != Version {
		t.Fatalf("the last migration produces version %d but Version is %d", last, Version)
	}
	for v := 2; v <= Version; v++ {
		p := filepath.Join("..", "..", "testdata", fmt.Sprintf("schema_v%d.sql", v))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("no snapshot for schema %d (%s): record it with UPDATE_SCHEMA_SNAPSHOT=1", v, p)
		}
	}
}
