package jobs

import (
	"strings"
	"testing"
)

func TestBatchStatusSplitsOneAnswerPerJobWithTheSingleJobRules(t *testing.T) {
	if _, err := BatchStatusCommand([]string{"12", "13; id"}); err == nil {
		t.Fatal("an id that is not a number reached a command")
	}
	cmd, err := BatchStatusCommand([]string{"12", "13", "14", "15"})
	if err != nil || strings.Count(cmd, "squeue") != 1 || strings.Count(cmd, "sacct") != 1 || !strings.Contains(cmd, "12,13,14,15") {
		t.Fatalf("one squeue and one sacct for all jobs: %q %v", cmd, err)
	}
	out := "12|RUNNING\n13|PENDING\n---\n14|TIMEOUT|0:15|01:00:00|s|e\n15|COMPLETED|0:0|00:00:09|s|e\n12|RUNNING|0:0|00:00:01|s|Unknown\n"
	got := ParseBatchStatus(out, []string{"12", "13", "14", "15", "16"})
	for id, want := range map[string]struct{ state, slurm, exit string }{
		"12": {StateRunning, "RUNNING", "0:0"}, "13": {StateSubmitted, "PENDING", ""}, "14": {StateFailed, "TIMEOUT", "0:15"}, "15": {StateCompleted, "COMPLETED", "0:0"}, "16": {"", "", ""},
	} {
		g := got[id]
		if g.State != want.state || g.Slurm != want.slurm || g.ExitCode != want.exit {
			t.Errorf("job %s: %+v, want %+v", id, g, want)
		}
	}
	// the same text through the single-job parser gives the same answer, so both paths agree
	one := "\n---\nTIMEOUT|0:15|01:00:00|s|e\n"
	if s, d := ParseStatus(one); s != StateFailed || !strings.Contains(d, "exit 0:15") || ParseStatusFull(one).Detail != d {
		t.Errorf("single job: %q %q", s, d)
	}
	if g := got["16"]; g.Detail == "" {
		t.Errorf("a job Slurm does not list must say so, not be silent")
	}
}
