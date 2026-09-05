package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestNewGatedContext_CleanConfig_SucceedsAndPopulatesFields verifies
// newGatedContext succeeds against a clean, fully-local config (mirroring
// TestNewReadContext_FullyLocal_ConstructsClean in readcontext_test.go) and
// that the returned gatedContext carries the same populated config/
// issueTracker/codeForge trio newReadContext alone would give — the
// validate+gate prologue this issue adds must not change what a clean run
// gets back, only reject an unclean one.
func TestNewGatedContext_CleanConfig_SucceedsAndPopulatesFields(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("RUNTIME", "echo")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err != nil {
		t.Fatalf("newGatedContext() = %v, want nil error for a clean config", err)
	}
	if gc.config.issueTracker != "local" {
		t.Errorf("gc.config.issueTracker = %q, want %q", gc.config.issueTracker, "local")
	}
	if gc.issueTracker == nil {
		t.Error("gc.issueTracker = nil, want a non-nil IssueTracker")
	}
	if gc.codeForge == nil {
		t.Error("gc.codeForge = nil, want a non-nil CodeForge")
	}
}

// TestNewGatedContext_ResearchKind_AppliesKindAndLabel verifies newGatedContext
// threads its kind param through to newReadContext's applyDispatchKind call
// (issue #2944 slice 1) the same way bootstrap() applies it today: a
// dispatchKindResearch call against an otherwise-clean config must come back
// with gc.config.dispatchKind set to the research kind and gc.config.label
// swapped to the fixed research family's Dispatchable label (forge.
// ResearchDispatchLabels), not whatever GH_LABEL_DISPATCHABLE the ambient
// config would otherwise carry for work. See
// TestNewGatedContext_SelfContainedResearch_SetsSelfContainedField below for
// the selfContained param's own coverage.
func TestNewGatedContext_ResearchKind_AppliesKindAndLabel(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("RUNTIME", "echo")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindResearch, false)

	if err != nil {
		t.Fatalf("newGatedContext() = %v, want nil error for a clean config", err)
	}
	if gc.config.dispatchKind != dispatchKindResearch {
		t.Errorf("gc.config.dispatchKind = %q, want %q", gc.config.dispatchKind, dispatchKindResearch)
	}
	if want := forge.ResearchDispatchLabels().Dispatchable; gc.config.label != want {
		t.Errorf("gc.config.label = %q, want %q (forge.ResearchDispatchLabels().Dispatchable)", gc.config.label, want)
	}
}

// TestNewGatedContext_SelfContainedResearch_SetsSelfContainedField verifies
// newGatedContext threads its selfContained param through to newReadContext
// (issue #2944 slice 1) the same way bootstrap() applies it today for the
// research kind's no-repo sub-mode (issue #2202): a selfContained=true call
// must come back with gc.config.selfContained set, and — since
// repoRequirementExempt (internal/launcherchecks) exempts this exact
// combination (dispatchKindResearch + selfContained + ISSUE_TRACKER=local's
// InBoxUnreachableTracker) from the REPO_SLUG/GH_TOKEN requirement — must
// succeed without either of those set. This only proves the field
// assignment; see TestNewGatedContext_SelfContainedWorkKind_RejectedByValidate
// below for proof the param actually reaches validate(c).
func TestNewGatedContext_SelfContainedResearch_SetsSelfContainedField(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("RUNTIME", "echo")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindResearch, true)

	if err != nil {
		t.Fatalf("newGatedContext() = %v, want nil error for a clean self-contained research config", err)
	}
	if !gc.config.selfContained {
		t.Error("gc.config.selfContained = false, want true")
	}
}

// TestNewGatedContext_SelfContainedWorkKind_RejectedByValidate verifies the
// selfContained param actually reaches validate(c), not just the config
// struct: validate rejects selfContained=true paired with dispatchKindWork
// (main.go's "--self-contained is only valid for the research dispatch
// kind" check) outright, before any gate runs, so a selfContained=true call
// with dispatchKindWork must fail with that exact message.
func TestNewGatedContext_SelfContainedWorkKind_RejectedByValidate(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("RUNTIME", "echo")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, true)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want validate()'s self-contained rejection")
	}
	if !strings.Contains(err.Error(), "--self-contained is only valid for the research dispatch kind") {
		t.Errorf("newGatedContext() error = %q, want it to name the self-contained/dispatch-kind mismatch", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on validate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_InvalidConfig_SurfacesValidateError verifies
// newGatedContext returns validate(c)'s own error for a config with a
// broken required knob (bogus MERGE_MODE, same technique doctor_test.go's
// TestDoctorReport_ConfigInvalid_NamesEveryBrokenKnob uses) and never
// reaches the gate registry at all — a config validate() rejects has no
// business being gate-checked.
func TestNewGatedContext_InvalidConfig_SurfacesValidateError(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "bogus")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("RUNTIME", "echo")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want validate()'s MERGE_MODE rejection")
	}
	if !strings.Contains(err.Error(), "MERGE_MODE") {
		t.Errorf("newGatedContext() error = %q, want it to name MERGE_MODE", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on validate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_FailingRegistryGate_SurfacesGateError verifies
// newGatedContext surfaces a gateRegistry failure: CODE_FORGE=git is not
// RelayCapable (internal/backend/registry_gen.go), so pairing it with
// BOX_FORGE_AND_ISSUE_ACCESS=read-only trips checkReadOnlyCapabilityGate,
// the registry's first entry, on an otherwise-valid config -- proving
// newGatedContext actually walks gateRegistry after validate() passes,
// rather than only ever exercising the validate() short-circuit above.
func TestNewGatedContext_FailingRegistryGate_SurfacesGateError(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "git")
	t.Setenv("CODE_FORGE_REMOTE_URL", "https://example.com/repo.git")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "echo")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want checkReadOnlyCapabilityGate to reject CODE_FORGE=git under read-only access")
	}
	if !strings.Contains(err.Error(), "bundle-relay") {
		t.Errorf("newGatedContext() error = %q, want the read-only-capability gate's bundle-relay message", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_BwrapPastaGateRunsBeforeTokenGates verifies
// newGatedContext enforces bootstrap.go's exact interleaved gate order
// (capability, network-mode, bwrap-pasta, bwrap-overlay, gh-token,
// forgejo-token) rather than gateRegistry's order followed by the bwrap
// gates: with BOTH the bwrap-pasta gate and the read-only-token-github gate
// primed to fail, the returned error must be bwrap-pasta's -- proving
// newGatedContext reaches and stops at bwrap-pasta before it ever reaches
// (and makes the live network call inside) the token gate.
func TestNewGatedContext_BwrapPastaGateRunsBeforeTokenGates(t *testing.T) {
	// A PATH containing only a fake "true" -- RUNTIME's own required-knob
	// check needs *something* resolvable -- but no "pasta", forces
	// runner.ValidatePasta() to fail deterministically (same technique as
	// withFakePasta in bwrap_pasta_gate_test.go), without depending on this
	// host's real pasta installation.
	withFixtureBinary(t, "true")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "true")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("NETWORK_MODE", "open") // not host/none, so checkBwrapPastaGate actually calls runner.ValidatePasta()
	// BOX_GH_TOKEN intentionally unset: the read-only-token-github gate
	// would also fail here (see readonly_token_gate.go), if the walk ever
	// reached it.

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want checkBwrapPastaGate's pasta-missing rejection")
	}
	if !strings.Contains(err.Error(), "pasta") {
		t.Errorf("newGatedContext() error = %q, want it to mention pasta (checkBwrapPastaGate), not a token gate", err.Error())
	}
	if strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Errorf("newGatedContext() error = %q, want it to stop at bwrap-pasta before ever reaching the token gate", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_BwrapOverlayGateRunsBeforeTokenGates is
// BwrapPastaGateRunsBeforeTokenGates' pair for the second bwrap gate:
// checkBwrapOverlayGate's own runner.ValidateOverlay() has no injectable
// exec seam from this package (internal/runner's execCommand is
// package-private), and its outcome is a real kernel probe -- a devbox or
// CI runner with unprivileged-userns-overlayfs support would make it pass
// regardless of what's misconfigured elsewhere, so forcing a genuine
// failure needs bwrap itself absent from PATH (ValidateOverlay's own
// runner.execCommand("bwrap", ...) call then fails outright, before it ever
// gets to probe overlay support) rather than depending on kernel behavior.
// NETWORK_MODE=host makes checkBwrapPastaGate a documented no-op so only
// the overlay gate's failure is in play.
func TestNewGatedContext_BwrapOverlayGateRunsBeforeTokenGates(t *testing.T) {
	// A PATH containing only a fake "true" -- RUNTIME's own required-knob
	// check needs *something* resolvable -- but no "bwrap", forces
	// runner.ValidateOverlay()'s exec("bwrap", ...) to fail deterministically.
	withFixtureBinary(t, "true")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "true")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("NETWORK_MODE", "host") // checkBwrapPastaGate no-ops (issue #2666), isolating the overlay gate's failure
	t.Setenv("NIX_STORE_WRITABLE", "true")
	t.Setenv("NIX_CONFIG_FILE", "/nix/store/somehash-nix.conf")
	// BOX_GH_TOKEN intentionally unset: the read-only-token-github gate
	// would also fail here, if the walk ever reached it.

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want checkBwrapOverlayGate's overlay-probe rejection")
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("newGatedContext() error = %q, want it to mention overlay (checkBwrapOverlayGate), not a token gate", err.Error())
	}
	if strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Errorf("newGatedContext() error = %q, want it to stop at bwrap-overlay before ever reaching the token gate", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_BwrapGatesRunAfterCapabilityAndNetworkModeGates is the
// other half of bootstrap.go's order: capability and network-mode run
// BEFORE the bwrap gates, not after. With CODE_FORGE=git (not RelayCapable,
// same fixture as TestNewGatedContext_FailingRegistryGate_SurfacesGateError)
// tripping checkReadOnlyCapabilityGate, AND the bwrap-pasta gate also primed
// to fail (pasta absent from PATH), the capability gate's error must win --
// proving newGatedContext never reaches the bwrap gates once an earlier
// registry gate has already failed.
func TestNewGatedContext_BwrapGatesRunAfterCapabilityAndNetworkModeGates(t *testing.T) {
	// pasta absent from PATH would fail checkBwrapPastaGate if the walk
	// ever reached it.
	withFixtureBinary(t, "true")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "git") // not RelayCapable (internal/backend/registry_gen.go)
	t.Setenv("CODE_FORGE_REMOTE_URL", "https://example.com/repo.git")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "true")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("NETWORK_MODE", "open") // not host/none, so checkBwrapPastaGate would call runner.ValidatePasta() if reached

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want checkReadOnlyCapabilityGate to reject CODE_FORGE=git under read-only access")
	}
	if !strings.Contains(err.Error(), "bundle-relay") {
		t.Errorf("newGatedContext() error = %q, want the read-only-capability gate's bundle-relay message, not the bwrap-pasta gate's", err.Error())
	}
	if strings.Contains(err.Error(), "pasta") {
		t.Errorf("newGatedContext() error = %q, want it to stop at read-only-capability before ever reaching the bwrap gates", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
}

// TestNewGatedContext_FailingNetworkModeRuntimeGate_SurfacesGateError
// verifies newGatedContext surfaces a checkNetworkModeRuntimeGate failure --
// the registry's second entry, network-mode-runtime -- specifically:
// NETWORK_MODE=no-host-loopback paired with RUNNER_KIND=bwrap has no
// rendering distinct from the isolated-by-default NETWORK_MODE=open
// (main.go's checkNetworkModeRuntimeGate doc comment), a combination this
// gate exists to reject at runtime since mkHarness's eval assert only ever
// sees what a Consumer flake bakes, never a runtime override. This is the
// gate no existing test tripped through newGatedContext: deleting the
// network-mode-runtime entry from gateRegistry left every other test in this
// file and launchgates_test.go green, so this test is the pin against a
// silent regression there. BOX_FORGE_AND_ISSUE_ACCESS is set explicitly to
// "read-write" so the read-only-capability gate -- the registry's first
// entry -- stays a no-op and never trips first. It's set explicitly, not
// merely left unset, because a dogfood Box's own ambient environment
// carries BOX_FORGE_AND_ISSUE_ACCESS=read-only
// (spindrift dogfoods itself, ADR 0018/issue #470) -- an unset t.Setenv would
// silently inherit that, making the read-only-token-github gate applicable
// and fail on its own unset BOX_GH_TOKEN before ever reaching this test's
// intended failure.
func TestNewGatedContext_FailingNetworkModeRuntimeGate_SurfacesGateError(t *testing.T) {
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "echo")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("NETWORK_MODE", "no-host-loopback")

	var w bytes.Buffer
	gc, err := newGatedContext(&w, dispatchKindWork, false)

	if err == nil {
		t.Fatal("newGatedContext() = nil error, want checkNetworkModeRuntimeGate to reject NETWORK_MODE=no-host-loopback under RUNNER_KIND=bwrap")
	}
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Errorf("newGatedContext() error = %v, want it to wrap errLaunchGateConfigInvalid", err)
	}
	if !strings.Contains(err.Error(), "no-host-loopback") {
		t.Errorf("newGatedContext() error = %q, want it to name NETWORK_MODE=no-host-loopback (checkNetworkModeRuntimeGate)", err.Error())
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
}

// withFixtureBinary points PATH at a fresh temp dir containing a single
// executable named name that immediately exits 0 -- enough for RUNTIME's own
// required-knob check (doctor.RuntimeCheck) to find something on PATH to
// resolve, while leaving every other binary (pasta, bwrap, ...) absent so a
// gate that shells out to one fails deterministically regardless of what
// happens to be installed on the host running the test.
func withFixtureBinary(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, name)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fixture binary %s: %v", name, err)
	}
	t.Setenv("PATH", dir)
}
