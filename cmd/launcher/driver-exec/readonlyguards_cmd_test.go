package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// forbiddenMarkersRegistryPathForReadonlyGuardsTest reuses promptassembly's
// own testdata forbiddenMarkers registry fixture (the real 13-row registry,
// issue #2464) rather than duplicating it by hand, mirroring
// assembleprompt_cmd_test.go's own forbiddenMarkersRegistryPathForTest.
const forbiddenMarkersRegistryPathForReadonlyGuardsTest = "../internal/promptassembly/testdata/forbidden-markers.json"

// stubBinOnPath creates a fake, no-op executable named name under a fresh
// temp dir and prepends that dir to PATH for the duration of the test, so
// exec.LookPath(name) -- the default readonlyguards.Config.RealBinary --
// resolves without depending on the host/sandbox actually having name
// installed (the nix build sandbox, e.g., has no "gh" on PATH).
func stubBinOnPath(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRunReadonlyGuards_FullRegistryInstallsShimAndHook verifies the
// readonly-guards subcommand's flag parsing reaches
// promptassembly.LoadForbiddenMarkersFile and readonlyguards.Install with the
// right Config: the real 13-row forbiddenMarkers registry fixture installs
// both the "gh" and "fj" command shims and the pre-push/pre-receive git hook
// under -repo-dir, exit code 0 (issue #2509).
func TestRunReadonlyGuards_FullRegistryInstallsShimAndHook(t *testing.T) {
	stubBinOnPath(t, "gh")
	stubBinOnPath(t, "fj")
	repoDir := t.TempDir()
	shimDir := t.TempDir()

	var stdout bytes.Buffer
	rc := runReadonlyGuards([]string{
		"--forbidden-markers-registry", forbiddenMarkersRegistryPathForReadonlyGuardsTest,
		"--repo-dir", repoDir,
		"--shim-dir", shimDir,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runReadonlyGuards exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if _, err := os.Stat(filepath.Join(shimDir, "gh")); err != nil {
		t.Errorf("gh shim not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shimDir, ".real-gh")); err != nil {
		t.Errorf(".real-gh not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shimDir, "fj")); err != nil {
		t.Errorf("fj shim not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shimDir, ".real-fj")); err != nil {
		t.Errorf(".real-fj not installed: %v", err)
	}

	for _, name := range []string{"pre-push", "pre-receive"} {
		hookPath := filepath.Join(repoDir, ".git", "hooks", name)
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("%s hook not installed: %v", name, err)
		}
	}

	out := stdout.String()
	if !bytes.Contains([]byte(out), []byte("gh")) {
		t.Errorf("stdout = %q, want it to mention the installed gh shim", out)
	}

	// Exercise the installed shim end-to-end: a guarded subcommand rejects,
	// naming the relay it replaces.
	cmd := exec.Command(filepath.Join(shimDir, "gh"), "pr", "create")
	shimOut, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gh pr create via installed shim exit = 0, want non-zero; output=%q", shimOut)
	}
	if !bytes.Contains(shimOut, []byte("gh pr create")) {
		t.Errorf("installed shim output = %q, want it to mention 'gh pr create'", shimOut)
	}

	fjCmd := exec.Command(filepath.Join(shimDir, "fj"), "pr", "create")
	fjOut, err := fjCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("fj pr create via installed shim exit = 0, want non-zero; output=%q", fjOut)
	}
	if !bytes.Contains(fjOut, []byte("fj pr create")) {
		t.Errorf("installed shim output = %q, want it to mention 'fj pr create'", fjOut)
	}
}

// TestRunReadonlyGuards_MissingRegistryFlagReturnsNonZero verifies a missing
// -forbidden-markers-registry flag fails loudly (exit 1) instead of running
// Install against a zero-value rows slice.
func TestRunReadonlyGuards_MissingRegistryFlagReturnsNonZero(t *testing.T) {
	var stdout bytes.Buffer
	rc := runReadonlyGuards([]string{
		"--repo-dir", t.TempDir(),
		"--shim-dir", t.TempDir(),
	}, &stdout)
	if rc == 0 {
		t.Fatal("runReadonlyGuards exit = 0, want non-zero for a missing -forbidden-markers-registry")
	}
	if !bytes.Contains(stdout.Bytes(), []byte("forbidden-markers-registry")) {
		t.Errorf("stdout = %q, want it to mention forbidden-markers-registry", stdout.String())
	}
}

// TestRunReadonlyGuards_BadRegistryPathReturnsNonZero verifies a
// -forbidden-markers-registry path pointing at a nonexistent file fails
// loudly (exit 1) with a useful stderr message, instead of panicking or
// silently installing nothing.
func TestRunReadonlyGuards_BadRegistryPathReturnsNonZero(t *testing.T) {
	var stdout bytes.Buffer
	rc := runReadonlyGuards([]string{
		"--forbidden-markers-registry", filepath.Join(t.TempDir(), "does-not-exist.json"),
		"--repo-dir", t.TempDir(),
		"--shim-dir", t.TempDir(),
	}, &stdout)
	if rc == 0 {
		t.Fatal("runReadonlyGuards exit = 0, want non-zero for a nonexistent registry path")
	}
	if stdout.Len() == 0 {
		t.Error("stdout is empty, want a useful error message")
	}
}

// TestIsReadonlyGuardsInvocation verifies the readonly-guards subcommand's
// dispatch guard: a bare "readonly-guards" first arg selects it, while every
// other invocation shape falls through to the default Driver-invocation path
// (or, for the other subcommands, to those).
func TestIsReadonlyGuardsInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"readonly-guards first arg", []string{"readonly-guards", "--forbidden-markers-registry", "x"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"bundle-out", []string{"bundle-out"}, false},
		{"outcome-backstop", []string{"outcome-backstop"}, false},
		{"assemble-prompt", []string{"assemble-prompt"}, false},
	}
	for _, c := range cases {
		if got := isReadonlyGuardsInvocation(c.args); got != c.want {
			t.Errorf("%s: isReadonlyGuardsInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// TestNewReadonlyGuardsFlagSet_DefaultsAreEmpty verifies every
// readonly-guards flag defaults to the empty string when omitted, mirroring
// outcome-backstop's own default-inspection test -- runReadonlyGuards'
// required-flag check depends on that default staying "".
func TestNewReadonlyGuardsFlagSet_DefaultsAreEmpty(t *testing.T) {
	fs, _ := newReadonlyGuardsFlagSet()

	for _, name := range []string{"forbidden-markers-registry", "repo-dir", "shim-dir"} {
		got := fs.Lookup(name)
		if got == nil {
			t.Fatalf("%s flag not registered", name)
		}
		if got.DefValue != "" {
			t.Errorf("%s default = %q, want empty", name, got.DefValue)
		}
	}
}
