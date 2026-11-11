package server

import (
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

func vf(k, v string) runs.EnvFact { return runs.EnvFact{Key: k, Status: runs.EnvVerified, Value: v} }

func fpBase(fp string) baseline {
	env := []runs.EnvFact{vf("code.commit", strings.Repeat("a", 40)), vf("code.dirty", "no"), vf("input.size_bytes", "100"), vf("input.mtime_epoch", "1000")}
	if fp != "" {
		env = append(env, vf("input.sampled_fp", fp))
	}
	return baseline{Env: env}
}

func nowFacts(mtime string, fp *runs.EnvFact) []runs.EnvFact {
	now := []runs.EnvFact{vf("code.commit", strings.Repeat("a", 40)), vf("code.dirty", "no"), vf("input.size_bytes", "100"), vf("input.mtime_epoch", mtime)}
	if fp != nil {
		now = append(now, *fp)
	}
	return now
}

// The input is compared by its fingerprint when both sides have one: a touched file with the same sampled bytes does not
// block a strict rerun, a different fingerprint does even when size and time agree, and without one the size and time decide.
func TestStrictInputIsComparedByFingerprintWhenBothSidesHaveOne(t *testing.T) {
	a, b := strings.Repeat("1", 64), strings.Repeat("2", 64)
	fa, fb := vf("input.sampled_fp", a), vf("input.sampled_fp", b)

	if _, _, blocking := compareToNow(fpBase(a), nowFacts("2000", &fa), true); blocking {
		t.Error("a touched file with an equal sampled fingerprint blocked a strict rerun")
	}
	if _, _, blocking := compareToNow(fpBase(a), nowFacts("1000", &fb), true); !blocking {
		t.Error("a different fingerprint with equal size and time did not block a strict rerun")
	}
	if _, _, blocking := compareToNow(fpBase(""), nowFacts("2000", nil), true); !blocking {
		t.Error("without fingerprints a changed modification time must still block (the earlier behaviour)")
	}
	// the parent has one, the host could not measure now: size and time decide, and the message says so
	unres := runs.EnvFact{Key: "input.sampled_fp", Status: runs.EnvUnresolved, Reason: "the host's worker did not answer"}
	_, msgs, blocking := compareToNow(fpBase(a), nowFacts("1000", &unres), true)
	if blocking || !strings.Contains(strings.Join(msgs, " "), "could not be measured now") {
		t.Errorf("unmeasurable fingerprint: blocking=%v msgs=%v", blocking, msgs)
	}
}
