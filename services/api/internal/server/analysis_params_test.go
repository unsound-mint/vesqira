package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// An analysis job whose stored record cannot be read is refused, never read as "no corrections asked for": an empty record would skip the
// check that the job's program applied the researcher's corrections (ADR 0064). A job with no record at all still registers and reads.
func TestAnalysisJobWithAnUnreadableRecordIsRefused(t *testing.T) {
	t.Parallel()
	e := newHostParity(t)
	cube, err := os.ReadFile(filepath.Join(e.remoteRoot, "hi_cube.fits"))
	if err != nil {
		t.Fatal(err)
	}
	// a finished job: its folder holds result.json and result.fits, as a host's program leaves them
	finished := func(name, params string) jobs.Job {
		out := filepath.Join(e.remoteRoot, name)
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(out, "result.json"), []byte(`{"ok":true,"summary":{}}`), 0o644)
		os.WriteFile(filepath.Join(out, "result.fits"), cube, 0o644)
		j, err := e.jobs.CreateDraft(jobs.Job{HostID: e.h.ID, Name: name, Template: "analysis", Script: "#!/bin/bash\n", OutputDir: out, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	bad := finished("bad-record", "{")
	if code, b := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", bad.ID), map[string]any{}); code != 500 || !strings.Contains(string(b), "cannot be read") {
		t.Errorf("registering a job with an unreadable record: %d %s", code, b)
	}
	if code, b := e.get(fmt.Sprintf("/api/jobs/%d/result", bad.ID)); code != 500 || !strings.Contains(string(b), "cannot be read") {
		t.Errorf("the result of a job with an unreadable record: %d %s", code, b)
	}

	empty := finished("no-record", "")
	if code, b := e.get(fmt.Sprintf("/api/jobs/%d/result", empty.ID)); code != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Errorf("the result of a job with no record: %d %s", code, b)
	}
	if code, b := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", empty.ID), map[string]any{}); code != 201 {
		t.Errorf("registering a job with no record: %d %s", code, b)
	}
}
