package readonlyguards

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// runShim execs the installed shim at shimDir/argv0 with args, returning its
// stdout+stderr combined and exit code.
func runShim(t *testing.T, shimDir, argv0 string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(shimDir, argv0), args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("exec shim: %v", err)
		}
	}
	return string(out), code
}

// TestInstall_CommandShimRejectsGuardedSubcommand covers the base case: a
// single command-shim row installs a shim binary that, invoked with its
// guarded subcommand, rejects with the row's exact message and a non-zero
// exit code -- never reaching the real binary.
func TestInstall_CommandShimRejectsGuardedSubcommand(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-gh-pr-create",
			Marker:  "gh pr create",
			Kind:    "substring",
			Enforce: "command-shim",
			Message: "read-only Box: PRs are opened via the PR-intent relay; do not run `gh pr create` -- this call has been blocked locally.",
		},
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return "/nonexistent/real-" + argv0, nil
		},
	}

	var out bytes.Buffer
	if _, err := Install(rows, cfg, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, code := runShim(t, shimDir, "gh", "pr", "create")
	if code == 0 {
		t.Fatalf("shim exit code = 0, want non-zero; output=%q", got)
	}
	if !bytes.Contains([]byte(got), []byte(rows[0].Message)) {
		t.Fatalf("shim output = %q, want it to contain %q", got, rows[0].Message)
	}
}

// requireExecutable skips the test if fname isn't found on PATH -- used by
// tests that need a real, no-op stand-in binary to prove the exec-through
// path actually reaches it.
func requireExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
	return path
}

// TestInstall_CommandShimPassesThroughUnguardedSubcommand proves the
// exec-through indirection: a subcommand not named by any row for this
// argv0 reaches the real binary via the .real-<argv0> file, unmodified.
func TestInstall_CommandShimPassesThroughUnguardedSubcommand(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-gh-pr-create",
			Marker:  "gh pr create",
			Kind:    "substring",
			Enforce: "command-shim",
			Message: "blocked: gh pr create",
		},
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	if _, err := Install(rows, cfg, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// The exec-through indirection: the shim reads the real binary's path
	// from the sibling .real-gh file rather than hardcoding it.
	realFile := filepath.Join(shimDir, ".real-gh")
	realBytes, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatalf("read %s: %v", realFile, err)
	}
	if string(realBytes) != realTrue {
		t.Fatalf(".real-gh contents = %q, want %q", realBytes, realTrue)
	}

	// "gh pr list" is not a guarded subcommand -- it must fall through to
	// the real binary (here, `true`, which always exits 0).
	got, code := runShim(t, shimDir, "gh", "pr", "list")
	if code != 0 {
		t.Fatalf("shim exit code = %d, want 0 (passthrough); output=%q", code, got)
	}
}

// TestInstall_GhAPIMutationRejectsMutatingMethod covers the
// "gh-api-mutation" kind: -X/--method POST/PATCH/PUT/DELETE (any case,
// either flag spelling) is rejected with the row's message; a plain read
// (no method flag, or an explicit GET) passes through to the real binary.
func TestInstall_GhAPIMutationRejectsMutatingMethod(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-gh-api-mutation",
			Marker:  "gh api",
			Kind:    "gh-api-mutation",
			Enforce: "command-shim",
			Message: "blocked: gh api mutation",
		},
	}

	shimDir := t.TempDir()
	cfg := Config{
		RepoDir: t.TempDir(),
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	if _, err := Install(rows, cfg, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}

	rejectCases := [][]string{
		{"api", "repos/foo/bar/issues/1", "-X", "POST"},
		{"api", "-X", "post", "repos/foo/bar/issues/1"},
		{"api", "--method=PATCH", "repos/foo/bar/issues/1"},
		{"api", "--method", "put", "repos/foo/bar/issues/1"},
		{"api", "-X", "DeLeTe", "repos/foo/bar/issues/1"},
	}
	for _, args := range rejectCases {
		got, code := runShim(t, shimDir, "gh", args...)
		if code == 0 {
			t.Errorf("gh %v: exit code = 0, want non-zero; output=%q", args, got)
		}
		if !bytes.Contains([]byte(got), []byte(rows[0].Message)) {
			t.Errorf("gh %v: output = %q, want it to contain %q", args, got, rows[0].Message)
		}
	}

	allowCases := [][]string{
		{"api", "repos/foo/bar/issues/1"},
		{"api", "-X", "GET", "repos/foo/bar/issues/1"},
		{"api", "--method=get", "repos/foo/bar/issues/1"},
	}
	for _, args := range allowCases {
		got, code := runShim(t, shimDir, "gh", args...)
		if code != 0 {
			t.Errorf("gh %v: exit code = %d, want 0 (passthrough); output=%q", args, code, got)
		}
	}
}

// TestInstall_GitHookRow proves a git-hook row's Message ends up, verbatim,
// in both the pre-push and pre-receive hooks installed under
// RepoDir/.git/hooks, and that the installed hook actually rejects (exits
// non-zero) when invoked.
func TestInstall_GitHookRow(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-git-push",
			Marker:  "git push",
			Kind:    "substring",
			Enforce: "git-hook",
			Message: "read-only Box: do not run `git push` -- this push has been blocked locally.",
		},
	}

	repoDir := t.TempDir()
	cfg := Config{RepoDir: repoDir}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
	}

	for _, name := range []string{"pre-push", "pre-receive"} {
		hookPath := filepath.Join(repoDir, ".git", "hooks", name)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read %s: %v", hookPath, err)
		}
		if !bytes.Contains(content, []byte(rows[0].Message)) {
			t.Fatalf("%s content = %q, want it to contain %q", name, content, rows[0].Message)
		}

		cmd := exec.Command(hookPath)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%s exit code = 0, want non-zero; output=%q", name, out)
		}
	}
}

// TestInstall_PromptOnlyRowProducesNoArtifact proves a prompt-only row is
// skipped entirely: no shim file, no .real-<argv0> file, no hook content
// mentioning it, and Install reports nothing installed.
func TestInstall_PromptOnlyRowProducesNoArtifact(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-git-bundle-create",
			Marker:  "git bundle create",
			Kind:    "substring",
			Enforce: "prompt-only",
			Message: "blocked: git bundle create",
		},
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	cfg := Config{RepoDir: repoDir, ShimDir: shimDir}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.HookInstalled {
		t.Fatalf("result.HookInstalled = true, want false")
	}
	if len(result.Shims) != 0 {
		t.Fatalf("result.Shims = %v, want empty", result.Shims)
	}

	entries, err := os.ReadDir(shimDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", shimDir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("shimDir entries = %v, want none", entries)
	}

	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if _, err := os.Stat(hooksDir); err == nil {
		t.Fatalf("%s exists, want no hooks dir for a prompt-only-only row set", hooksDir)
	}
}

// TestInstall_GroupsByArgv0Generically proves grouping is derived from the
// marker's own first word rather than hardcoded to "gh": a synthetic
// "widget" argv0, alongside "gh", produces two independent shims, each only
// guarding its own subcommands.
func TestInstall_GroupsByArgv0Generically(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:      "forbidden-gh-pr-create",
			Marker:  "gh pr create",
			Kind:    "substring",
			Enforce: "command-shim",
			Message: "blocked: gh pr create",
		},
		{
			ID:      "forbidden-widget-launch",
			Marker:  "widget launch",
			Kind:    "substring",
			Enforce: "command-shim",
			Message: "blocked: widget launch",
		},
	}

	shimDir := t.TempDir()
	cfg := Config{
		RepoDir: t.TempDir(),
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	wantShims := []string{"gh", "widget"}
	if len(result.Shims) != len(wantShims) {
		t.Fatalf("result.Shims = %v, want %v", result.Shims, wantShims)
	}
	for i, name := range wantShims {
		if result.Shims[i] != name {
			t.Fatalf("result.Shims = %v, want %v", result.Shims, wantShims)
		}
	}

	for _, name := range wantShims {
		if _, err := os.Stat(filepath.Join(shimDir, name)); err != nil {
			t.Fatalf("stat shim %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(shimDir, ".real-"+name)); err != nil {
			t.Fatalf("stat .real-%s: %v", name, err)
		}
	}

	// The "widget" shim must guard "widget launch" but not know about "gh
	// pr create" at all -- and vice versa.
	got, code := runShim(t, shimDir, "widget", "launch")
	if code == 0 {
		t.Errorf("widget launch: exit code = 0, want non-zero; output=%q", got)
	}
	if !bytes.Contains([]byte(got), []byte("blocked: widget launch")) {
		t.Errorf("widget launch: output = %q, want it to contain the widget message", got)
	}

	got, code = runShim(t, shimDir, "gh", "pr", "create")
	if code == 0 {
		t.Errorf("gh pr create: exit code = 0, want non-zero; output=%q", got)
	}
	if !bytes.Contains([]byte(got), []byte("blocked: gh pr create")) {
		t.Errorf("gh pr create: output = %q, want it to contain the gh message", got)
	}

	// Unguarded subcommands on each still pass through to the real binary.
	if _, code := runShim(t, shimDir, "widget", "status"); code != 0 {
		t.Errorf("widget status: exit code = %d, want 0 (passthrough)", code)
	}
	if _, code := runShim(t, shimDir, "gh", "pr", "list"); code != 0 {
		t.Errorf("gh pr list: exit code = %d, want 0 (passthrough)", code)
	}
}

// TestInstall_FullRegistry loads the real thirteen-row forbiddenMarkers
// fixture (promptassembly/testdata/forbidden-markers.json) and installs it
// end to end: exactly one command-shim (argv0 "gh" -- every "fj ..." row is
// still enforce=="prompt-only" as of issue #2509, per this slice's scope
// note) and the one git-hook guard, with every "gh" subcommand row's
// message reachable and no "fj" shim installed.
func TestInstall_FullRegistry(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows, err := promptassembly.LoadForbiddenMarkersFile("../promptassembly/testdata/forbidden-markers.json")
	if err != nil {
		t.Fatalf("LoadForbiddenMarkersFile: %v", err)
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if want := []string{"gh"}; len(result.Shims) != len(want) || result.Shims[0] != want[0] {
		t.Fatalf("result.Shims = %v, want %v (all fj rows are still prompt-only)", result.Shims, want)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
	}
	if _, err := os.Stat(filepath.Join(shimDir, "fj")); err == nil {
		t.Fatalf("fj shim exists, want none installed (every fj row is prompt-only)")
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"pr", "create"}, "gh pr create"},
		{[]string{"pr", "ready"}, "gh pr ready"},
		{[]string{"pr", "merge"}, "gh pr merge"},
		{[]string{"issue", "comment"}, "gh issue comment"},
		{[]string{"issue", "create"}, "gh issue create"},
		{[]string{"api", "-X", "POST", "repos/foo/bar"}, "gh api"},
	} {
		got, code := runShim(t, shimDir, "gh", tc.args...)
		if code == 0 {
			t.Errorf("gh %v: exit code = 0, want non-zero; output=%q", tc.args, got)
		}
		if !bytes.Contains([]byte(got), []byte(tc.want)) {
			t.Errorf("gh %v: output = %q, want it to mention %q", tc.args, got, tc.want)
		}
	}

	if _, code := runShim(t, shimDir, "gh", "pr", "list"); code != 0 {
		t.Errorf("gh pr list: exit code != 0, want passthrough")
	}
	if _, code := runShim(t, shimDir, "gh", "api", "repos/foo/bar"); code != 0 {
		t.Errorf("gh api (read): exit code != 0, want passthrough")
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read %s: %v", hookPath, err)
	}
	if !bytes.Contains(content, []byte("git push")) {
		t.Fatalf("pre-push hook content = %q, want it to mention 'git push'", content)
	}
}
