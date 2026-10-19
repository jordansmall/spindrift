package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestLauncherCheckDeps_BackendBindsValidatorToConfig proves the Backend
// closure binds a row's own validateTracker to the caller's config rather
// than dropping it or binding a different config: a forgejo row with no
// FORGEJO_BASE_URL/FORGEJO_TOKEN set must fail with forgejo's own validation
// error through the adapted zero-arg closure.
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

	// The real row-building path must fail the same way, not just the
	// adapter in isolation.
	ch := checkByName(t, launcherCrossKnobChecks(c), "issue-tracker-config")
	if _, err := ch.Probe(); err == nil {
		t.Error("issue-tracker-config Probe() = nil, want the forgejo validation error")
	}
}

// TestLauncherCheckDeps_BackendNilValidatorsStayNil proves a backendRow with
// nil validateTracker/validateCodeForge (github has no code-forge validator,
// local no tracker validator) adapts to a Backend with a nil func. A non-nil
// no-op wrapper would turn crossKnobCheck's "no extra validation" arm into a
// no-op that always succeeds, breaking any test that asserts a row has no
// validator.
func TestLauncherCheckDeps_BackendNilValidatorsStayNil(t *testing.T) {
	c := minimalValidConfig()
	deps := launcherCheckDeps(c)
	b, ok := deps.Backend("github")
	if !ok {
		t.Fatal("launcherCheckDeps(c).Backend(\"github\") ok = false, want true")
	}
	if b.ValidateCodeForge != nil {
		t.Error("launcherCheckDeps(c).Backend(\"github\").ValidateCodeForge != nil, want nil")
	}
	b, ok = deps.Backend("local")
	if !ok {
		t.Fatal("launcherCheckDeps(c).Backend(\"local\") ok = false, want true")
	}
	if b.ValidateTracker != nil {
		t.Error("launcherCheckDeps(c).Backend(\"local\").ValidateTracker != nil, want nil")
	}
}

// TestLauncherCrossKnobDeps_ExtraCrossKnobIsRegistryProxyRoutesRow pins
// ExtraCrossKnob to the registry-proxy-routes row cmd/launcher owns.
// TestLauncherCrossKnobChecks_ReturnsThreeRows relies on that row for its
// three-row order but never ties it to this adapter field.
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

// TestLauncherCredentialsCoverDriverRules fails when a Driver's credential
// rule names an env var the adapter never fills into Credentials, or one with
// no secretKnobs row for the --secret-cmd fallback to fetch.
func TestLauncherCredentialsCoverDriverRules(t *testing.T) {
	creds := launcherCredentials(config{})
	knobs := map[string]bool{}
	for _, k := range secretKnobs {
		knobs[k.env] = true
	}
	for _, name := range driver.Names() {
		rules, _ := driver.CredentialRules(name)
		for _, r := range rules {
			for _, env := range r.Env {
				if _, ok := creds[env]; !ok {
					t.Errorf("driver %s: rule var %s has no launcherCredentials entry", name, env)
				}
				if !knobs[env] {
					t.Errorf("driver %s: rule var %s has no secretKnobs row", name, env)
				}
			}
		}
	}
}
