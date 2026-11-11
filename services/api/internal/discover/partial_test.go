package discover

import (
	"strings"
	"testing"
)

// An unfinished transfer lives in .vesqira-partial next to its destination (ADR 0051). A half-copied cube is never data, and
// the folder is not reported as skipped either: it is Vesqira's own.
func TestAnUnfinishedTransferIsNeverListedAsData(t *testing.T) {
	root := canonical(t)
	write(t, root, "data/cube.fits", "x")
	write(t, root, "data/.vesqira-partial/cube2.fits", "half")
	write(t, root, ".vesqira-partial/other.fits", "half")
	r := scan(t, LocalFS{root}, Options{})
	eq(t, "data", paths(r.Data), []string{"data/cube.fits"})
	for _, d := range r.Skipped.Dirs {
		if strings.Contains(d, ".vesqira-partial") {
			t.Errorf("the partial folder is reported as a skipped folder: %v", r.Skipped.Dirs)
		}
	}
}
