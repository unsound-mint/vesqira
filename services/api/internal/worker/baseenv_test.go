package worker

import (
	"strings"
	"testing"
)

// The memory cap reaches every worker the API starts, and nothing else from the API's environment does.
func TestBaseEnvPassesTheWorkerCapAndNothingElse(t *testing.T) {
	t.Setenv("VESQIRA_WORKER_MEM_MB", "512")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("SOME_SECRET", "x")
	env := strings.Join(BaseEnv(), "\n")
	if !strings.Contains(env, "VESQIRA_WORKER_MEM_MB=512") {
		t.Fatalf("the cap is not passed: %q", env)
	}
	for _, leak := range []string{"SSH_AUTH_SOCK", "SOME_SECRET"} {
		if strings.Contains(env, leak) {
			t.Fatalf("%s leaked into the worker environment: %q", leak, env)
		}
	}
}
