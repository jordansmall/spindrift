package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGhExecOutsideForge walks all non-test Go source files in cmd/launcher,
// excluding internal/forge, and fails if any contain exec.Command("gh" —
// keeping all gh API calls behind the forge seam.
func TestNoGhExecOutsideForge(t *testing.T) {
	// Tests run with CWD = the package directory (cmd/launcher).
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the forge package itself — that's where gh calls are allowed.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/forge") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), `exec.Command("gh"`) {
			t.Errorf("%s: contains exec.Command(\"gh\") — all gh calls must go through the forge seam (IssueTracker/CodeForge)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoRunnerExecOutsidePackage walks all non-test Go source files in
// cmd/launcher, excluding internal/runner, and fails if any contain an
// exec.Command literal for the container CLI or system tools — keeping all
// sandbox life-cycle calls behind the runner seam.
func TestNoRunnerExecOutsidePackage(t *testing.T) {
	forbidden := []string{
		`exec.Command("bwrap"`,
		`exec.Command("nix"`,
		`exec.Command("podman"`,
		`exec.Command("docker"`,
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/runner") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — all sandbox exec calls must go through runner.Runner", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoOutcomeParsingOutsidePackage walks all non-test Go source files in
// cmd/launcher, excluding internal/outcome, and fails if any contain the
// SPINDRIFT_OUTCOME prefix literal — keeping all outcome parsing behind the
// outcome seam.
func TestNoOutcomeParsingOutsidePackage(t *testing.T) {
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the outcome package — that's where the parsing lives.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/outcome") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Check both Go string-quoting styles; backtick literals bypass a
		// double-quote-only check.
		content := string(data)
		if strings.Contains(content, `"SPINDRIFT_OUTCOME "`) ||
			strings.Contains(content, "`SPINDRIFT_OUTCOME `") {
			t.Errorf("%s: contains SPINDRIFT_OUTCOME parsing — all outcome parsing must go through internal/outcome", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoBoxConstructionOutsideDispatchPackage walks all non-test Go source
// files in cmd/launcher, excluding internal/dispatch, and fails if any
// construct a runner.Box, open an issue log file for writing, or classify a
// Driver exit directly — the per-issue execution seam established by issue
// #441.
func TestNoBoxConstructionOutsideDispatchPackage(t *testing.T) {
	forbidden := []string{
		`runner.Box{`,
		`os.Create(`,
		`.ClassifyTransient(`,
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/dispatch") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — all Box construction, issue-log creation, and Driver classification must go through internal/dispatch", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestRunnerUnpackingWrappersRemoved asserts newRunner and newBuildRunner —
// the positional-unpacking wrappers around runner.NewOCI/NewBwrap/
// NewBwrapBuild — have been deleted from main.go; call sites build a
// runner.Config via runnerConfig(c) and select the adapter directly
// (issue #445).
func TestRunnerUnpackingWrappersRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	content := string(data)
	for _, needle := range []string{"func newRunner(", "func newBuildRunner("} {
		if strings.Contains(content, needle) {
			t.Errorf("main.go still defines %s; delete it — adapters take runner.Config directly", needle)
		}
	}
}

// TestPrintOutcomeReportRemoved asserts the deprecated printOutcomeReport
// helper (five ignored parameters collapsed to a single Println, issue #443)
// has been deleted from main.go.
func TestPrintOutcomeReportRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(data), "func printOutcomeReport(") {
		t.Error("main.go still defines printOutcomeReport; delete it")
	}
}

// TestOsExitOnlyInMain pins issue #443's thin-main acceptance criterion:
// os.Exit skips deferred cleanup, so every subcommand must return a plain
// exit code and let main be the only frame that calls it — that's the only
// way driver-cache cleanup can run via defer on every exit path.
func TestOsExitOnlyInMain(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	got := strings.Count(string(data), "os.Exit(")
	if got != 1 {
		t.Errorf("os.Exit( appears %d times in main.go, want 1 (main only) — subcommand functions must return exit codes instead", got)
	}
}

// TestBootstrapPrologueAppearsOnce pins issue #443's core acceptance
// criterion: run, the selective `dispatch <nums>` path, and recover used to
// each carry their own copy of the six-step prologue (config load+validate,
// runner construction, ready-check, forge client, dispatch factory, settle).
// newDispatchFactory(c, pwd, r) is a distinctive literal that appears only
// inside that prologue -- build, preview, and doctor never construct a
// dispatch.Factory -- so pinning its count to 1 across the whole package
// (excluding tests) proves the prologue now lives in bootstrap only.
func TestBootstrapPrologueAppearsOnce(t *testing.T) {
	const needle = "newDispatchFactory(c, pwd, r)"
	total := 0
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += strings.Count(string(data), needle)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if total != 1 {
		t.Errorf("%q appears %d times, want 1 (bootstrap only) — the six-step prologue must not be duplicated across run/dispatch<nums>/recover", needle, total)
	}
}

// TestNoPRIssueStateLiteralOutsideForge walks all non-test Go source files in
// cmd/launcher, excluding internal/forge, and fails if any contain a raw
// "OPEN"/"MERGED"/"CLOSED" state literal — every PR/issue state must flow
// through the typed forge.PRState/forge.IssueState constants, translated at
// each adapter's own edge (issue #444).
func TestNoPRIssueStateLiteralOutsideForge(t *testing.T) {
	forbidden := []string{`"OPEN"`, `"MERGED"`, `"CLOSED"`}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/forge") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %s — PR/issue state must use forge.PRState/forge.IssueState constants, not a raw literal", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoBranchPrefixConcatOutsideForge walks all non-test Go source files in
// cmd/launcher, excluding internal/forge, and fails if any concatenate
// branchPrefix directly — the agent branch name must be computed by
// CodeForge's AgentBranch(num), its single owner (issue #444).
func TestNoBranchPrefixConcatOutsideForge(t *testing.T) {
	forbidden := []string{"branchPrefix + ", "BranchPrefix + "}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/forge") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — the agent branch name must be computed by CodeForge.AgentBranch(num), not concatenated here", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoMergeGateOutsideSettlePackage walks all non-test Go source files in
// cmd/launcher, excluding internal/settle, and fails if any of the merge-gate
// functions absorbed by issue #442 have crept back into package main —
// gateToGreen, selfHeal, applyMergeMode, mergeImmediate, landPushOnly,
// verifyMerged, adoptAndGate, gateIssue, and the merge-guard check must live
// in internal/settle only.
func TestNoMergeGateOutsideSettlePackage(t *testing.T) {
	forbidden := []string{
		"func gateToGreen(",
		"func selfHeal(",
		"func applyMergeMode(",
		"func mergeImmediate(",
		"func landPushOnly(",
		"func verifyMerged(",
		"func adoptAndGate(",
		"func gateIssue(",
		"func mergeGuardHit(",
		"func postUsageComment(",
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/settle") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — the merge gate must live in internal/settle only (issue #442)", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
