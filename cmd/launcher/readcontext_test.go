package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// ISSUE_TRACKER=local is the only tracker reconcile's LivenessProbe check
// reaches (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_LocalTracker_ReturnsNonNil(t *testing.T) {
	c := baseConfig()
	c.IssueTracker = "local"
	rc := readContext{config: c, capabilities: forge.Capabilities{TrackerDescriptor: backend.Local}}

	lp, err := rc.reconcileLivenessProbe(t.TempDir())
	if err != nil {
		t.Fatalf("reconcileLivenessProbe: %v", err)
	}

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}

func TestReadContext_ReconcileLivenessProbe_UnknownRunnerKind_ReturnsError(t *testing.T) {
	c := baseConfig()
	c.IssueTracker = "local"
	c.runnerKind = "nope"
	rc := readContext{config: c, capabilities: forge.Capabilities{TrackerDescriptor: backend.Local}}

	_, err := rc.reconcileLivenessProbe(t.TempDir())

	if err == nil {
		t.Fatal("reconcileLivenessProbe: nil error, want one for an unknown RUNNER_KIND")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not name the unknown kind %q", err, "nope")
	}
}

// A non-local tracker skips the runner build entirely, which is the common
// github/jira "nothing to do" refusal (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_GithubTracker_ReturnsNil(t *testing.T) {
	c := baseConfig()
	c.IssueTracker = "github"
	rc := readContext{config: c}

	lp, err := rc.reconcileLivenessProbe(t.TempDir())
	if err != nil {
		t.Fatalf("reconcileLivenessProbe: %v", err)
	}

	if lp != nil {
		t.Errorf("reconcileLivenessProbe(pwd) = %v, want nil for ISSUE_TRACKER=github", lp)
	}
}

// newReadContext is the read prefix cmdDoctor and cmdReconcile share (issue
// #2941).
func TestNewReadContext_FullyLocal_ConstructsClean(t *testing.T) {
	setKnob(t, "ISSUE_TRACKER", "local")
	setKnob(t, "CODE_FORGE", "local")
	setKnob(t, "LOCAL_ISSUES_DIR", t.TempDir())
	setKnob(t, "MERGE_MODE", "immediate")
	setKnob(t, "CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	setKnob(t, "GIT_USER_NAME", "bot")
	setKnob(t, "GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")

	rc, err := newReadContext(dispatchkind.Work, false)
	if err != nil {
		t.Fatalf("newReadContext: %v", err)
	}

	if rc.config.IssueTracker != "local" {
		t.Errorf("rc.config.issueTracker = %q, want %q", rc.config.IssueTracker, "local")
	}
	if rc.issueTracker == nil {
		t.Error("rc.issueTracker = nil, want a non-nil IssueTracker")
	}
	if rc.codeForge == nil {
		t.Error("rc.codeForge = nil, want a non-nil CodeForge")
	}
}

// newReadContext resolves capabilities once via forge.ResolveCapabilities so a
// later consumer reads rc.capabilities instead of re-asserting interfaces
// itself (issue #2945 slice 4).
func TestNewReadContext_FullyLocal_ResolvesCapabilities(t *testing.T) {
	setFullyLocalEnv(t)

	rc, err := newReadContext(dispatchkind.Work, false)
	if err != nil {
		t.Fatalf("newReadContext: %v", err)
	}

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

// readContext also builds straight from forge.NewFake(), the fake-based half of
// #2920's Testing Decisions ("each tier constructible against the forge fake
// and env fixtures") that newReadContext's env-fixture test above doesn't
// cover (issue #2941 AC4).
func TestReadContext_ConstructibleAgainstForgeFake(t *testing.T) {
	c := baseConfig()
	c.IssueTracker = "local"
	fake := forge.NewFake()
	rc := readContext{config: c, issueTracker: fake, codeForge: fake, capabilities: forge.Capabilities{TrackerDescriptor: backend.Local}}

	lp, err := rc.reconcileLivenessProbe(t.TempDir())
	if err != nil {
		t.Fatalf("reconcileLivenessProbe: %v", err)
	}

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}

// newReadContext never validates at construction (issue #2992): a config
// validateConfigChecks would reject still comes back as a usable readContext
// rather than failing or panicking on the broken knob. Only a later
// rc.validation() call reports that the config is invalid.
func TestNewReadContext_InvalidConfig_ConstructsCleanly(t *testing.T) {
	setFullyLocalEnv(t)
	setKnob(t, "GIT_USER_NAME", "")

	rc, err := newReadContext(dispatchkind.Work, false)
	if err != nil {
		t.Fatalf("newReadContext: %v", err)
	}

	if rc.issueTracker == nil {
		t.Error("rc.issueTracker = nil, want a non-nil IssueTracker even though rc.config fails validation")
	}
	if rc.codeForge == nil {
		t.Error("rc.codeForge = nil, want a non-nil CodeForge even though rc.config fails validation")
	}
}

// validation() runs cmdDoctor's full-report classification
// (validateConfigChecks) against rc.config and reports a broken Required row's
// remedy, naming the knob to fix (issue #2992).
func TestReadContext_Validation_InvalidConfig_ReturnsConfigErrNamingBrokenKnob(t *testing.T) {
	c := minimalValidConfig()
	c.GitUserName = ""
	rc := readContext{config: c}

	v := rc.validation()

	if v.configErr == nil {
		t.Fatal("validation().configErr = nil, want a non-nil error for an unset commit user name")
	}
	if !strings.Contains(v.configErr.Error(), "--user-name") {
		t.Errorf("validation().configErr = %q, want it to name the missing commit user name", v.configErr)
	}
}

// A config that already passes cmdDoctor's full-report classification gets a
// nil configErr (issue #2992).
func TestReadContext_Validation_ValidConfig_ReturnsNilConfigErr(t *testing.T) {
	rc := readContext{config: minimalValidConfig()}

	v := rc.validation()

	if v.configErr != nil {
		t.Errorf("validation().configErr = %v, want nil for minimalValidConfig()", v.configErr)
	}
}

// validation() calls doctorCheckSets(c) exactly once, so a route-credential
// Probe shared with reportChecks Peeks at most once (issues #3144, #2992). A
// second call would give reportChecks its own unmemoized Probe, which would
// re-Peek and notice the unset env var.
func TestReadContext_Validation_ReportSharesClassifyMemoizedProbe(t *testing.T) {
	const envName = "SPINDRIFT_TEST_READCONTEXT_VALIDATION_SHARED_PROBE"
	t.Setenv(envName, "resolvable")
	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "`+envName+`" }
`)
	rc := readContext{config: c}

	v := rc.validation()
	if v.configErr != nil {
		t.Fatalf("validation().configErr = %v, want nil while %s is set", v.configErr, envName)
	}

	// A fresh Peek fails once the env var is gone; a memoized report Probe
	// keeps returning the cached success from the classify pass above.
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("os.Unsetenv(%q) failed: %v", envName, err)
	}
	report := checkByName(t, v.reportChecks, "registry-route-credential[registry.example.com]")

	_, err := report.Probe()

	if err != nil {
		t.Errorf("report row Probe() = %v after %s was unset, want the classify pass's cached nil error (shared memoized Probe, issue #3144)", err, envName)
	}
}

// A forgejo row whose Connector cannot load fails newReadContext with
// the load error rather than panicking: every read-only command (doctor,
// reconcile, research) and daemon child builds its Code Forge through here.
func TestNewReadContext_ForgejoLoadFailure_ReturnsError(t *testing.T) {
	setFullyLocalEnv(t)
	setKnob(t, "CODE_FORGE", "forgejo")
	t.Setenv("FORGEJO_TOKEN", "tok")
	t.Cleanup(backendrows.Snapshot())
	doc := credentialRowDoc("forgejo", "")
	if err := backendrows.Use([]byte(doc), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	rc, err := newReadContext(dispatchkind.Work, false)

	if err == nil || !strings.Contains(err.Error(), "read module") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("newReadContext err = %v, want the Connector's module read error", err)
	}
	if rc.codeForge != nil {
		t.Errorf("rc.codeForge = %T, want nil on a failed build", rc.codeForge)
	}
}
