package oci

import (
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// TestIssueTextReachesAssembledPrompt_OCI is the oci half of issue #3470; the
// bwrap half lives in the bwrap package.
func TestIssueTextReachesAssembledPrompt_OCI(t *testing.T) {
	runnertest.AssertIssueTextReachesPrompt(t, "oci", ociRunEnv)
}
