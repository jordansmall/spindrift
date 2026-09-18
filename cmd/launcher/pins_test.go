package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every gh API call must stay behind the forge seam, so no file outside
// internal/forge may shell out to gh directly.
func TestNoGhExecOutsideForge(t *testing.T) {
	// Go runs each test with the working directory set to the package
	// directory, cmd/launcher, so this walk starts there.
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// The forge package is where gh calls are allowed.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/forge") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Quickstart runs before nix develop, so no forge client exists yet.
		// Its Environment seam shells to gh directly to read token scopes and
		// the gh CLI fallback (issue #1047), the same carve-out ADR 0027 makes
		// for the pre-CLI wizard.
		if strings.HasPrefix(filepath.ToSlash(path), "quickstart") {
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

// Every sandbox life-cycle call must stay behind the runner seam, so no file
// outside internal/runner may exec a container CLI or system tool directly.
func TestNoRunnerExecOutsidePackage(t *testing.T) {
	forbidden := []string{
		`exec.Command("bwrap"`,
		`exec.Command("nix"`,
		`exec.Command("podman"`,
		`exec.Command("docker"`,
		`exec.Command("nerdctl"`,
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
		// driver-exec is a standalone in-box binary (issue #626) that spawns the
		// Driver from inside the disposable container. That is a different seam
		// from the host-side runner.Runner this guard polices, which launches
		// the Box itself.
		if strings.HasPrefix(filepath.ToSlash(path), "driver-exec") {
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

// All outcome parsing must stay behind the outcome seam, so no file outside
// internal/outcome may carry the SPINDRIFT_OUTCOME prefix literal.
func TestNoOutcomeParsingOutsidePackage(t *testing.T) {
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// The outcome package is where the parsing lives.
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
		// Check both Go string-quoting styles. A backtick literal slips past a
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

// Issue #441 made internal/dispatch the per-issue execution seam: no file
// outside it may construct a runner.Box, open an issue log for writing, or
// classify a Driver exit.
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
		// driver-exec's own os.Create writes its teed stream log (issue #626), a
		// different file in a different process from the launcher's per-issue
		// Box log this guard polices.
		if strings.HasPrefix(filepath.ToSlash(path), "driver-exec") {
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

// Issue #445 deleted newRunner and newBuildRunner, the positional-unpacking
// wrappers around runner.NewOCI, NewBwrap and NewBwrapBuild. Call sites now
// build a runner.Config via runnerConfig(c) and select the adapter directly.
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

// Issue #443 deleted printOutcomeReport, a helper that ignored five of its
// parameters and collapsed to a single Println.
func TestPrintOutcomeReportRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(data), "func printOutcomeReport(") {
		t.Error("main.go still defines printOutcomeReport; delete it")
	}
}

// Issue #443's thin-main criterion: os.Exit skips deferred cleanup, so every
// subcommand returns a plain exit code and only main calls os.Exit. That is the
// only way driver-cache cleanup runs via defer on every exit path.
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

// Issue #444: every PR and issue state flows through the typed forge.PRState
// and forge.IssueState constants, translated at each adapter's own edge, so no
// file outside internal/forge may carry a raw state literal.
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

// Issue #444 made CodeForge.AgentBranch(num) the single owner of the agent
// branch name, so no file outside internal/forge may concatenate branchPrefix
// itself.
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

// Issue #442 absorbed the merge gate into internal/settle. This guard fails if
// any of those functions crept back into package main.
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
