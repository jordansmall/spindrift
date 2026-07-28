package freshness

import (
	"strings"
	"testing"
)

// TestNonConverging_SameStaleRevAsPrior verifies that a stale verdict at the
// same rev the prior launcher process already exited stale on is reported
// as non-converging — a rebuild happened (the rev didn't change) and the
// image is still stale, so the divergence is structural, not stale content.
func TestNonConverging_SameStaleRevAsPrior(t *testing.T) {
	if got := NonConverging("deadbeef", "deadbeef"); !got {
		t.Errorf("NonConverging(%q, %q) = false, want true", "deadbeef", "deadbeef")
	}
}

// TestNonConverging_DifferentPriorRev verifies that a stale verdict at a rev
// different from the prior stale rev is NOT non-converging — the base tip
// moved since the last stale exit, so this could simply be ordinary content
// staleness that a rebuild will resolve.
func TestNonConverging_DifferentPriorRev(t *testing.T) {
	if got := NonConverging("deadbeef", "priorrev"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "deadbeef", "priorrev")
	}
}

// TestNonConverging_EmptyStaleRev verifies that an empty staleRev (no rev
// was fetched — e.g. the fetch itself failed) is never reported as
// non-converging, regardless of priorStaleRev.
func TestNonConverging_EmptyStaleRev(t *testing.T) {
	if got := NonConverging("", "deadbeef"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "deadbeef")
	}
	if got := NonConverging("", ""); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "")
	}
}

// TestHostTaintDiagnostic_ContainsRequiredSubstrings verifies the returned
// diagnostic names the likely cause (a consumer flake's packages,
// extraClosures, or skills pulling in a darwin derivation), gives the
// locate command, and echoes back the two tags an operator needs to see —
// the ones that will never converge.
func TestHostTaintDiagnostic_ContainsRequiredSubstrings(t *testing.T) {
	got := HostTaintDiagnostic("main", "deadbeef", ".#packages.x86_64-linux.agent-image", "spindrift:aaaa", "spindrift:bbbb")

	for _, want := range []string{
		"consumer flake",
		"packages",
		"extraClosures",
		"skills",
		"nix derivation show -r",
		"darwin",
		".#packages.x86_64-linux.agent-image",
		"spindrift:aaaa",
		"spindrift:bbbb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HostTaintDiagnostic(...) does not contain %q; got:\n%s", want, got)
		}
	}
}
