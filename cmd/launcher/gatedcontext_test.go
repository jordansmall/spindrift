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

// The validate+gate prologue must not change what a clean run gets back, only
// reject an unclean one: a clean fully-local config still yields the populated
// config/issueTracker/codeForge trio newReadContext alone would give.
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

// newGatedContext must thread its kind param through to newReadContext's
// applyDispatchKind call (issue #2944 slice 1), so the label comes from the
// fixed research family, not from the ambient GH_LABEL_DISPATCHABLE for work.
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

// newGatedContext must thread selfContained through for research's no-repo
// sub-mode (issues #2944 slice 1, #2202). REPO_SLUG and GH_TOKEN stay unset on
// purpose: repoRequirementExempt (internal/launcherchecks) exempts exactly this
// combination, so setting them would hide a regression in that exemption.
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

// This pins that selfContained reaches validate(c), not just the config struct:
// validate rejects selfContained paired with dispatchKindWork before any gate
// runs.
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

// A config validate() rejects must never be gate-checked: the broken
// MERGE_MODE has to come back as validate's own error.
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

// CODE_FORGE=git is not RelayCapable (internal/backend/registry_gen.go), so
// pairing it with read-only access trips checkReadOnlyCapabilityGate on an
// otherwise-valid config. That proves newGatedContext walks gateRegistry at
// all, rather than only ever exercising the validate() short-circuit above.
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

// newGatedContext must keep bootstrap.go's interleaved gate order (capability,
// network-mode, bwrap-pasta, bwrap-overlay, gh-token, forgejo-token), not
// gateRegistry's order followed by the bwrap gates. Both bwrap-pasta and the
// token gate are primed to fail, so bwrap-pasta's error winning proves the walk
// stops before the token gate's live network call.
func TestNewGatedContext_BwrapPastaGateRunsBeforeTokenGates(t *testing.T) {
	// A PATH holding only a fake "true" satisfies RUNTIME's required-knob check
	// while leaving pasta absent, so runner.ValidatePasta() fails whatever this
	// host has installed.
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
	// BOX_GH_TOKEN stays unset so the read-only-token-github gate
	// (readonly_token_gate.go) would also fail if the walk reached it.

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

// The pair of TestNewGatedContext_BwrapPastaGateRunsBeforeTokenGates, for the
// second bwrap gate. ValidateOverlay() has no injectable exec seam from this
// package and ends in a real kernel probe that passes on any host with
// unprivileged-userns-overlayfs, so the only deterministic failure is bwrap
// absent from PATH. NETWORK_MODE=host makes checkBwrapPastaGate a no-op.
func TestNewGatedContext_BwrapOverlayGateRunsBeforeTokenGates(t *testing.T) {
	// A PATH holding only a fake "true" satisfies RUNTIME's required-knob check
	// while leaving bwrap absent, so ValidateOverlay()'s exec("bwrap", ...)
	// fails before it can probe overlay support.
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
	// BOX_GH_TOKEN stays unset so the read-only-token-github gate would also
	// fail if the walk reached it.

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

// The other half of bootstrap.go's order: capability and network-mode run
// before the bwrap gates. Both the capability gate and bwrap-pasta are primed
// to fail, so the capability gate's error winning proves the walk stops at the
// earlier registry gate.
func TestNewGatedContext_BwrapGatesRunAfterCapabilityAndNetworkModeGates(t *testing.T) {
	// pasta absent from PATH would fail checkBwrapPastaGate if the walk reached
	// it.
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

// This is the only test that trips the registry's network-mode-runtime entry:
// deleting that entry from gateRegistry left every other test here and in
// launchgates_test.go green. BOX_FORGE_AND_ISSUE_ACCESS is set explicitly to
// read-write, not left unset, because a dogfood Box's ambient environment
// carries read-only (ADR 0018, issue #470) and would trip an earlier gate.
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

// withFixtureBinary points PATH at a temp dir holding one executable that exits
// 0. That is enough for RUNTIME's required-knob check (doctor.RuntimeCheck) to
// resolve something, while every other binary (pasta, bwrap, and the rest) is
// absent, so a gate that shells out to one fails whatever the host has
// installed.
func withFixtureBinary(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, name)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fixture binary %s: %v", name, err)
	}
	t.Setenv("PATH", dir)
}
