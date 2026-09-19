package freshness

import (
	"strings"
	"testing"
)

// A stale verdict at the same rev the prior launcher process exited stale on
// means a rebuild happened without the rev moving, so the divergence is
// structural rather than stale content.
func TestNonConverging_SameStaleRevAsPrior(t *testing.T) {
	if got := NonConverging("deadbeef", "deadbeef"); !got {
		t.Errorf("NonConverging(%q, %q) = false, want true", "deadbeef", "deadbeef")
	}
}

// The base tip moved since the last stale exit, so this can be ordinary
// content staleness that a rebuild resolves.
func TestNonConverging_DifferentPriorRev(t *testing.T) {
	if got := NonConverging("deadbeef", "priorrev"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "deadbeef", "priorrev")
	}
}

// An empty staleRev means no rev was fetched, for instance because the fetch
// failed, so there is nothing to compare against priorStaleRev.
func TestNonConverging_EmptyStaleRev(t *testing.T) {
	if got := NonConverging("", "deadbeef"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "deadbeef")
	}
	if got := NonConverging("", ""); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "")
	}
}

func TestHostTaintDiagnostic_ContainsRequiredSubstrings(t *testing.T) {
	got := HostTaintDiagnostic("oci", "main", "deadbeef", ".#packages.x86_64-linux.agent-image", "spindrift:aaaa", "spindrift:bbbb")

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

// Under KindBwrap the host is always Linux, since lib/mkHarness.nix gates the
// agent-closure package on isLinux && runtime == "bwrap", so darwin can never
// be the cause. The bwrap imageTag slot also holds a bare nix store path, not
// a repo:tag string.
func TestHostTaintDiagnostic_Bwrap_DoesNotBlameDarwin(t *testing.T) {
	got := HostTaintDiagnostic(KindBwrap, "main", "deadbeef", ".#packages.x86_64-linux.agent-closure", "/nix/store/aaa-agent-closure", "/nix/store/bbb-agent-closure")

	if strings.Contains(strings.ToLower(got), "darwin") {
		t.Errorf("HostTaintDiagnostic(bwrap, ...) blames darwin, which is impossible under bwrap (host is always Linux); got:\n%s", got)
	}
	if strings.Contains(got, "image tag") {
		t.Errorf("HostTaintDiagnostic(bwrap, ...) says \"image tag\", but bwrap's compared values are bare store paths, not repo:tag strings; got:\n%s", got)
	}
	for _, want := range []string{
		"nix derivation show -r",
		".#packages.x86_64-linux.agent-closure",
		"/nix/store/aaa-agent-closure",
		"/nix/store/bbb-agent-closure",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HostTaintDiagnostic(bwrap, ...) does not contain %q; got:\n%s", want, got)
		}
	}
}
