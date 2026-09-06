package main

import (
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// TestReadContext_ReconcileLivenessProbe_LocalTracker_ReturnsNonNil verifies
// reconcileLivenessProbe builds a real probe for ISSUE_TRACKER=local, the
// only tracker reconcile's LivenessProbe check reaches (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_LocalTracker_ReturnsNonNil(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	rc := readContext{config: c, capabilities: forge.Capabilities{TrackerDescriptor: backend.Local}}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}

// TestReadContext_ReconcileLivenessProbe_GithubTracker_ReturnsNil verifies
// reconcileLivenessProbe returns nil for a non-local tracker, skipping the
// runner build entirely for the common github/jira "nothing to do" refusal
// (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_GithubTracker_ReturnsNil(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	rc := readContext{config: c}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp != nil {
		t.Errorf("reconcileLivenessProbe(pwd) = %v, want nil for ISSUE_TRACKER=github", lp)
	}
}

// TestNewReadContext_FullyLocal_ConstructsClean verifies newReadContext
// wires the IssueTracker/CodeForge for a fully-local run (ISSUE_TRACKER=
// local, CODE_FORGE=local) — the read prefix doctor.go's cmdDoctor and
// reconcile_cmd.go's cmdReconcile now share instead of each building its own
// copy inline (issue #2941).
func TestNewReadContext_FullyLocal_ConstructsClean(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")

	rc := newReadContext(dispatchKindWork, false)

	if rc.config.issueTracker != "local" {
		t.Errorf("rc.config.issueTracker = %q, want %q", rc.config.issueTracker, "local")
	}
	if rc.issueTracker == nil {
		t.Error("rc.issueTracker = nil, want a non-nil IssueTracker")
	}
	if rc.codeForge == nil {
		t.Error("rc.codeForge = nil, want a non-nil CodeForge")
	}
}

// TestNewReadContext_FullyLocal_ResolvesCapabilities verifies newReadContext
// resolves capabilities once via forge.ResolveCapabilities for a
// fully-local run (ISSUE_TRACKER=local, CODE_FORGE=local), so a later
// consumer reads rc.capabilities instead of re-asserting interfaces itself
// (issue #2945 slice 4).
func TestNewReadContext_FullyLocal_ResolvesCapabilities(t *testing.T) {
	setFullyLocalEnv(t)

	rc := newReadContext(dispatchKindWork, false)

	if rc.capabilities.LandingRecorder == nil {
		t.Error("rc.capabilities.LandingRecorder = nil, want non-nil for a local IssueTracker")
	}
	if rc.capabilities.BundleRelay == nil {
		t.Error("rc.capabilities.BundleRelay = nil, want non-nil for a local CodeForge")
	}
	if rc.capabilities.PRForge != nil {
		t.Errorf("rc.capabilities.PRForge = %v, want nil, local CodeForge doesn't implement PRForge", rc.capabilities.PRForge)
	}
	if rc.capabilities.ForgeDescriptor.Name != "local" {
		t.Errorf("rc.capabilities.ForgeDescriptor.Name = %q, want %q", rc.capabilities.ForgeDescriptor.Name, "local")
	}
	if rc.capabilities.TrackerDescriptor.Name != "local" {
		t.Errorf("rc.capabilities.TrackerDescriptor.Name = %q, want %q", rc.capabilities.TrackerDescriptor.Name, "local")
	}
}

// TestReadContext_ConstructibleAgainstForgeFake verifies readContext can be
// built directly from forge.NewFake(), the fake-based half of #2920's
// Testing Decisions ("each tier constructible against the forge fake and
// env fixtures") that newReadContext's env-fixture test above doesn't cover
// (issue #2941 AC4).
func TestReadContext_ConstructibleAgainstForgeFake(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	fake := forge.NewFake()
	rc := readContext{config: c, issueTracker: fake, codeForge: fake, capabilities: forge.Capabilities{TrackerDescriptor: backend.Local}}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}

// TestNewReadContext_InvalidConfig_ConstructsCleanly verifies newReadContext
// never validates at construction (issue #2992, invariant with
// reconcileLivenessProbe's own lazy-build precedent): a config
// validateConfigChecks would reject (GIT_USER_NAME unset, failing the
// "git-user-name" row) still comes back as a usable readContext rather than
// newReadContext itself failing or panicking on the broken knob. Only a
// later rc.validation() call surfaces that the config is invalid.
func TestNewReadContext_InvalidConfig_ConstructsCleanly(t *testing.T) {
	setFullyLocalEnv(t)
	t.Setenv("GIT_USER_NAME", "")

	rc := newReadContext(dispatchKindWork, false)

	if rc.issueTracker == nil {
		t.Error("rc.issueTracker = nil, want a non-nil IssueTracker even though rc.config fails validation")
	}
	if rc.codeForge == nil {
		t.Error("rc.codeForge = nil, want a non-nil CodeForge even though rc.config fails validation")
	}
}

// TestReadContext_Validation_InvalidConfig_ReturnsConfigErrNamingBrokenKnob
// verifies validation() runs cmdDoctor's full-report classification
// (validateConfigChecks) against rc.config and surfaces a broken Required
// row's remedy, naming the knob to fix (issue #2992).
func TestReadContext_Validation_InvalidConfig_ReturnsConfigErrNamingBrokenKnob(t *testing.T) {
	c := minimalValidConfig()
	c.gitUserName = ""
	rc := readContext{config: c}

	v := rc.validation()

	if v.configErr == nil {
		t.Fatal("validation().configErr = nil, want a non-nil error for GIT_USER_NAME unset")
	}
	if !strings.Contains(v.configErr.Error(), "GIT_USER_NAME") {
		t.Errorf("validation().configErr = %q, want it to name the broken GIT_USER_NAME knob", v.configErr)
	}
}

// TestReadContext_Validation_ValidConfig_ReturnsNilConfigErr verifies
// validation() returns a nil configErr for a config that already passes
// cmdDoctor's full-report classification (issue #2992).
func TestReadContext_Validation_ValidConfig_ReturnsNilConfigErr(t *testing.T) {
	rc := readContext{config: minimalValidConfig()}

	v := rc.validation()

	if v.configErr != nil {
		t.Errorf("validation().configErr = %v, want nil for minimalValidConfig()", v.configErr)
	}
}

// TestReadContext_Validation_ReportSharesClassifyMemoizedProbe verifies
// validation() calls doctorCheckSets(c) exactly once and hands back the
// matching report half, so a route-credential Probe shared between
// validation()'s own classify pass and the returned reportChecks Peeks its
// credential at most once (issue #3144's guarantee, exercised through the
// read tier added by issue #2992). Two separate doctorCheckSets(c) calls --
// one inside validation(), a second to build reportChecks -- would instead
// give reportChecks its own unmemoized Probe, which would re-Peek and
// notice the env var this test unsets between the two steps.
func TestReadContext_Validation_ReportSharesClassifyMemoizedProbe(t *testing.T) {
	const envName = "SPINDRIFT_TEST_READCONTEXT_VALIDATION_SHARED_PROBE"
	t.Setenv(envName, "resolvable")
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "`+envName+`" }
`)
	rc := readContext{config: c}

	v := rc.validation()
	if v.configErr != nil {
		t.Fatalf("validation().configErr = %v, want nil while %s is set", v.configErr, envName)
	}

	// Unsetting the env var now would make a fresh Peek fail; a memoized
	// report Probe must instead keep returning the cached success from the
	// classify pass above.
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("os.Unsetenv(%q) failed: %v", envName, err)
	}
	report := checkByName(t, v.reportChecks, "registry-route-credential[registry.example.com]")

	_, err := report.Probe()

	if err != nil {
		t.Errorf("report row Probe() = %v after %s was unset, want the classify pass's cached nil error (shared memoized Probe, issue #3144)", err, envName)
	}
}
