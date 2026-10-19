package bwrap

import (
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// TestIssueTextReachesAssembledPrompt_Bwrap is the bwrap half of issue #3470;
// the oci half lives in the oci package.
func TestIssueTextReachesAssembledPrompt_Bwrap(t *testing.T) {
	runnertest.AssertIssueTextReachesPrompt(t, "bwrap", resolvedRunEnv)
}
