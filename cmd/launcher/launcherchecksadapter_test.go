package main

import (
	"strings"
	"testing"
)

// TestLauncherCheckDeps_BackendBindsValidatorToConfig proves
// launcherCheckDeps's Backend closure really binds a row's own
// validateTracker to the caller's config, rather than dropping it or
// binding some other c: an ISSUE_TRACKER=forgejo row with no
// FORGEJO_BASE_URL/FORGEJO_TOKEN set must fail with forgejo's own
// validation error through the adapted zero-arg closure.
func TestLauncherCheckDeps_BackendBindsValidatorToConfig(t *testing.T) {
	c := minimalValidConfig()
	c.issueTracker = "forgejo"

	deps := launcherCheckDeps(c)
	b, ok := deps.Backend("forgejo")
	if !ok {
		t.Fatal("launcherCheckDeps(c).Backend(\"forgejo\") ok = false, want true")
	}
	if b.ValidateTracker == nil {
		t.Fatal("launcherCheckDeps(c).Backend(\"forgejo\").ValidateTracker = nil, want a bound closure")
	}
	err := b.ValidateTracker()
	if err == nil || !strings.Contains(err.Error(), "FORGEJO") {
		t.Errorf("ValidateTracker() = %v, want an error mentioning FORGEJO", err)
	}

	// Same failure must surface through the real row-building path, not
	// just the adapter in isolation.
	ch := checkByName(t, launcherCrossKnobChecks(c), "issue-tracker-config")
	if _, err := ch.Probe(); err == nil {
		t.Error("issue-tracker-config Probe() = nil, want the forgejo validation error")
	}
}

// TestLauncherCheckDeps_BackendNilValidatorsStayNil proves a backendRow
// whose validateTracker/validateCodeForge are nil (github: axis membership
// only, no extra validation) adapts to a Backend with nil funcs too — a
// non-nil no-op wrapper would silently change crossKnobCheck's "no extra
// validation" arm into "run a no-op that always succeeds", which happens to
// look identical here but breaks the moment a row's absence of a validator
// is asserted on directly.
func TestLauncherCheckDeps_BackendNilValidatorsStayNil(t *testing.T) {
	c := minimalValidConfig()
	deps := launcherCheckDeps(c)
	b, ok := deps.Backend("github")
	if !ok {
		t.Fatal("launcherCheckDeps(c).Backend(\"github\") ok = false, want true")
	}
	if b.ValidateTracker != nil {
		t.Error("launcherCheckDeps(c).Backend(\"github\").ValidateTracker != nil, want nil")
	}
	if b.ValidateCodeForge != nil {
		t.Error("launcherCheckDeps(c).Backend(\"github\").ValidateCodeForge != nil, want nil")
	}
}

// TestLauncherCrossKnobDeps_ExtraCrossKnobIsRegistryProxyRoutesRow proves
// launcherCrossKnobDeps wires its ExtraCrossKnob to exactly the
// registry-proxy-routes row cmd/launcher owns (registryProxyRoutesCheck),
// the one piece launcherCrossKnobChecks' three-row order
// (TestLauncherCrossKnobChecks_ReturnsThreeRows) depends on but doesn't
// itself pin to this adapter field.
func TestLauncherCrossKnobDeps_ExtraCrossKnobIsRegistryProxyRoutesRow(t *testing.T) {
	c := minimalValidConfig()
	deps := launcherCrossKnobDeps(c)
	if len(deps.ExtraCrossKnob) != 1 {
		t.Fatalf("launcherCrossKnobDeps(c).ExtraCrossKnob has %d rows, want 1", len(deps.ExtraCrossKnob))
	}
	if got := deps.ExtraCrossKnob[0].Name; got != registryProxyRoutesCheckName {
		t.Errorf("launcherCrossKnobDeps(c).ExtraCrossKnob[0].Name = %q, want %q", got, registryProxyRoutesCheckName)
	}
}

// TestLauncherCheckDeps_NoExtraCrossKnobRow pins the split: the deps the
// required-knob path uses carry no extra cross-knob row, so building those
// rows never constructs a registry-proxy-routes row it discards.
func TestLauncherCheckDeps_NoExtraCrossKnobRow(t *testing.T) {
	if got := launcherCheckDeps(minimalValidConfig()).ExtraCrossKnob; got != nil {
		t.Errorf("launcherCheckDeps(c).ExtraCrossKnob = %v, want nil", got)
	}
}
