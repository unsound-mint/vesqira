package discover

import (
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
)

const hogRecord = `gitcommit = "0123456789abcdef"
[hog]
version = "0.1-synthetic"
[config]
kernel = 3
[products]
orientation_file = "/scratch/x/hog_orientation.fits"
coherence_file = "/scratch/x/hog_coherence.fits"
mask_file = "/scratch/x/hog_mask.fits"
`

// A tool that is not in the shipped profiles is detected and its outputs get generic roles from the profile data alone.
func TestASyntheticProfileDetectsARunAndMapsItsOutputs(t *testing.T) {
	root := canonical(t)
	write(t, root, "hog/hog_run.toml", hogRecord)
	for _, f := range []string{"hog_orientation", "hog_coherence", "hog_mask"} {
		write(t, root, "hog/"+f+".fits", "SIMPLE")
	}
	r := scan(t, LocalFS{root}, Options{Profiles: runstest.WithSynthetic(t)})
	run := runByPath(r, "hog")
	if run == nil || run.Profile != "HOG-synthetic@1" || run.RecordKind != "hog-synthetic" || run.Record != "hog_run.toml" {
		t.Fatalf("run: %+v", run)
	}
	// keyed by output file name; the product is the tool's own name for it
	want := map[string][2]string{"hog_orientation.fits": {"orientation", "orientation_map"}, "hog_coherence.fits": {"reliability", "coherence"}, "hog_mask.fits": {"support", "mask"}}
	for file, w := range want {
		if run.OutputRoles[file] != w[0] || run.OutputProducts[file] != w[1] {
			t.Errorf("%s: role %q product %q, want %v", file, run.OutputRoles[file], run.OutputProducts[file], w)
		}
	}
	// the shipped profiles alone do not know the tool: the folder is not a run
	if r := scan(t, LocalFS{root}, Options{}); runByPath(r, "hog") != nil {
		t.Error("a tool without a profile was detected as a run")
	}
}

func TestARecordWithoutTheProfilesKeyIsNotThatToolsRun(t *testing.T) {
	root := canonical(t)
	write(t, root, "hog/hog_run.toml", "gitcommit = \"abc\"\n[config]\nkernel = 3\n")
	write(t, root, "hog/hog_orientation.fits", "SIMPLE")
	r := scan(t, LocalFS{root}, Options{Profiles: runstest.WithSynthetic(t)})
	if run := runByPath(r, "hog"); run != nil {
		t.Errorf("detected without the profile's key: %+v", run)
	}
}
