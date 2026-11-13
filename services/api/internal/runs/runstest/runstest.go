// Package runstest builds the run-profile registries tests use. Only tests import it.
package runstest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// WithSynthetic is the shipped registry plus the synthetic HOG-style profile of runs/testdata. That profile is never shipped: it
// exists to show that a tool other than VGT.jl and ROHSA is detected and mapped to generic roles from data alone.
func WithSynthetic(t testing.TB) *runs.Registry {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "testdata", "synthetic_hog_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := runs.Shipped().With(data)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
