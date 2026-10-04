package readonlyguards

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// runGitCmd duplicates the helper of the same name in
// cmd/launcher/driver-exec/bundleout_cmd_test.go, which is in a different
// package and so cannot be imported here.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
}

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

// Message and RuntimeMessage carry deliberately different text so the test
// pins that the shim renders RuntimeMessage, not the prompt-validator-facing
// Message (issue #2509 Finding 2).
func TestInstall_CommandShimRejectsGuardedSubcommand(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-pr-create",
			Marker:         "gh pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr create'. Refusing to invoke the Driver.",
			RuntimeMessage: "read-only Box: PRs are opened via the PR-intent relay; do not run `gh pr create` -- this call has been blocked locally.",
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
	if !bytes.Contains([]byte(got), []byte(rows[0].RuntimeMessage)) {
		t.Fatalf("shim output = %q, want it to contain RuntimeMessage %q", got, rows[0].RuntimeMessage)
	}
	if bytes.Contains([]byte(got), []byte(rows[0].Message)) {
		t.Fatalf("shim output = %q, want it to NOT contain the prompt-validator Message %q", got, rows[0].Message)
	}
}

// requireExecutable skips the test when name is absent from PATH. Tests use it
// to get a real no-op binary that proves the exec-through path reaches it.
func requireExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
	return path
}

// A subcommand that no row names for this argv0 reaches the real binary
// unmodified, via the path the shim reads from the sibling .real-<argv0> file.
func TestInstall_CommandShimPassesThroughUnguardedSubcommand(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-pr-create",
			Marker:         "gh pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: gh pr create",
			RuntimeMessage: "blocked: gh pr create",
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

	realFile := filepath.Join(shimDir, ".real-gh")
	realBytes, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatalf("read %s: %v", realFile, err)
	}
	if string(realBytes) != realTrue {
		t.Fatalf(".real-gh contents = %q, want %q", realBytes, realTrue)
	}

	// "gh pr list" is unguarded, so it falls through to the real binary, here
	// `true`, which always exits 0.
	got, code := runShim(t, shimDir, "gh", "pr", "list")
	if code != 0 {
		t.Fatalf("shim exit code = %d, want 0 (passthrough); output=%q", code, got)
	}
}

// The "gh-api-mutation" kind must reject a mutating method in any case and in
// either flag spelling, so the cases cover -X and --method, joined and
// separated, upper, lower and mixed case.
func TestInstall_GhAPIMutationRejectsMutatingMethod(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-api-mutation",
			Marker:         "gh api",
			Kind:           "gh-api-mutation",
			Enforce:        "command-shim",
			Message:        "blocked: gh api mutation",
			RuntimeMessage: "blocked: gh api mutation",
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

// A git-hook row's Message must reach both the pre-push and pre-receive hooks
// verbatim, and the installed hook must exit non-zero when git runs it.
func TestInstall_GitHookRow(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "read-only Box: do not run `git push` -- this push has been blocked locally.",
			RuntimeMessage: "read-only Box: do not run `git push` -- this push has been blocked locally.",
		},
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")
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

// Issue #2509 Finding 1: box's read-only guards must
// install the guard at both the decoy repo (RepoDir, which catches a plain
// `git push` to origin, whose pushurl is repointed there) and $WORK_DIR itself
// (an ExtraRepoDirs entry, which catches a push to an explicit URL or a
// non-origin remote). Losing the second install regresses the fix in #2463.
func TestInstall_GitHookRow_ExtraRepoDirs(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "blocked: git push (message)",
			RuntimeMessage: "blocked: git push (runtime message)",
		},
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init", "--bare")

	extraDir := t.TempDir()
	runGitCmd(t, extraDir, "init")

	cfg := Config{RepoDir: repoDir, ExtraRepoDirs: []string{extraDir}}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
	}

	// repoDir is bare, so hooks land directly under repoDir/hooks.
	for _, name := range []string{"pre-push", "pre-receive"} {
		hookPath := filepath.Join(repoDir, "hooks", name)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read %s: %v", hookPath, err)
		}
		if !bytes.Contains(content, []byte(rows[0].RuntimeMessage)) {
			t.Fatalf("%s content = %q, want it to contain %q", name, content, rows[0].RuntimeMessage)
		}
	}

	// extraDir is a normal working copy, so hooks land under
	// extraDir/.git/hooks.
	for _, name := range []string{"pre-push", "pre-receive"} {
		hookPath := filepath.Join(extraDir, ".git", "hooks", name)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read %s: %v", hookPath, err)
		}
		if !bytes.Contains(content, []byte(rows[0].RuntimeMessage)) {
			t.Fatalf("%s content = %q, want it to contain %q", name, content, rows[0].RuntimeMessage)
		}

		cmd := exec.Command(hookPath)
		cmdOut, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%s exit code = 0, want non-zero; output=%q", name, cmdOut)
		}
	}
}

// A bare repo has no .git subdirectory, so hooks must land under
// repoDir/hooks. Installing them at repoDir/.git/hooks, a path git never
// consults, leaves the guard silently absent while Result.HookInstalled still
// reports true.
func TestInstall_GitHookRow_BareRepo(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "read-only Box: do not run `git push` -- this push has been blocked locally.",
			RuntimeMessage: "read-only Box: do not run `git push` -- this push has been blocked locally.",
		},
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init", "--bare")
	cfg := Config{RepoDir: repoDir}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
	}

	wrongPath := filepath.Join(repoDir, ".git", "hooks", "pre-receive")
	if _, err := os.Stat(wrongPath); err == nil {
		t.Fatalf("hook installed at %s, want it absent (bare repo has no .git dir)", wrongPath)
	}

	for _, name := range []string{"pre-push", "pre-receive"} {
		hookPath := filepath.Join(repoDir, "hooks", name)
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
		if !bytes.Contains(out, []byte(rows[0].Message)) {
			t.Fatalf("%s output = %q, want it to contain %q", name, out, rows[0].Message)
		}
	}
}

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

// The synthetic "widget" argv0 alongside "gh" pins that grouping comes from the
// marker's own first word rather than a hardcoded "gh".
func TestInstall_GroupsByArgv0Generically(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-pr-create",
			Marker:         "gh pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: gh pr create",
			RuntimeMessage: "blocked: gh pr create",
		},
		{
			ID:             "forbidden-widget-launch",
			Marker:         "widget launch",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: widget launch",
			RuntimeMessage: "blocked: widget launch",
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

	if _, code := runShim(t, shimDir, "widget", "status"); code != 0 {
		t.Errorf("widget status: exit code = %d, want 0 (passthrough)", code)
	}
	if _, code := runShim(t, shimDir, "gh", "pr", "list"); code != 0 {
		t.Errorf("gh pr list: exit code = %d, want 0 (passthrough)", code)
	}
}

// Not every Box's image bakes every registry-named binary: fj is baked only for
// a forgejo-backend Consumer. A group whose argv0 has no resolvable real binary
// is therefore skipped instead of failing Install, so a github-backend Box
// still gets the "gh" shim and the git-hook guard (issue #2509).
func TestInstall_CommandShimSkipsMissingBinary(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-pr-create",
			Marker:         "gh pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: gh pr create",
			RuntimeMessage: "blocked: gh pr create",
		},
		{
			ID:             "forbidden-fj-pr-create",
			Marker:         "fj pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: fj pr create",
			RuntimeMessage: "blocked: fj pr create",
		},
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "blocked: git push",
			RuntimeMessage: "blocked: git push",
		},
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")
	cfg := Config{
		RepoDir: repoDir,
		ShimDir: shimDir,
		RealBinary: func(argv0 string) (string, error) {
			if argv0 == "fj" {
				return "", fmt.Errorf("exec: %q: executable file not found in $PATH", argv0)
			}
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v, want nil (a missing binary must not be fatal)", err)
	}

	wantShims := []string{"gh"}
	if len(result.Shims) != len(wantShims) || result.Shims[0] != wantShims[0] {
		t.Fatalf("result.Shims = %v, want %v (fj skipped, gh still installed)", result.Shims, wantShims)
	}
	if !result.HookInstalled {
		t.Error("result.HookInstalled = false, want true (the git-hook guard must still install)")
	}

	if _, err := os.Stat(filepath.Join(shimDir, "gh")); err != nil {
		t.Errorf("gh shim not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shimDir, "fj")); err == nil {
		t.Error("fj shim installed, want it skipped (no real fj binary)")
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git", "hooks", "pre-push")); err != nil {
		t.Errorf("pre-push hook not installed: %v", err)
	}

	if !bytes.Contains(out.Bytes(), []byte(`skipping "fj"`)) {
		t.Errorf("Install log = %q, want a note that the fj shim was skipped", out.String())
	}
}

// This installs the real forbiddenMarkers fixture end to end rather than a
// hand-built row set, so a registry change that drops a shim or a message shows
// up here. Every fj and gh row enforces as a command shim (issue #2509).
func TestInstall_FullRegistry(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows, err := promptassembly.LoadForbiddenMarkersFile(repopath.ForbiddenMarkersJSON())
	if err != nil {
		t.Fatalf("LoadForbiddenMarkersFile: %v", err)
	}

	shimDir := t.TempDir()
	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")
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

	if want := []string{"fj", "gh"}; len(result.Shims) != len(want) || result.Shims[0] != want[0] || result.Shims[1] != want[1] {
		t.Fatalf("result.Shims = %v, want %v", result.Shims, want)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
	}

	for _, tc := range []struct {
		argv0 string
		args  []string
		want  string
	}{
		{"gh", []string{"pr", "create"}, "gh pr create"},
		{"gh", []string{"pr", "ready"}, "gh pr ready"},
		{"gh", []string{"pr", "merge"}, "gh pr merge"},
		{"gh", []string{"issue", "comment"}, "gh issue comment"},
		{"gh", []string{"issue", "create"}, "gh issue create"},
		{"gh", []string{"api", "-X", "POST", "repos/foo/bar"}, "gh api"},
		{"fj", []string{"pr", "create"}, "fj pr create"},
		{"fj", []string{"pr", "ready"}, "fj pr ready"},
		{"fj", []string{"pr", "merge"}, "fj pr merge"},
		{"fj", []string{"issue", "comment"}, "fj issue comment"},
		{"fj", []string{"issue", "create"}, "fj issue create"},
	} {
		got, code := runShim(t, shimDir, tc.argv0, tc.args...)
		if code == 0 {
			t.Errorf("%s %v: exit code = 0, want non-zero; output=%q", tc.argv0, tc.args, got)
		}
		if !bytes.Contains([]byte(got), []byte(tc.want)) {
			t.Errorf("%s %v: output = %q, want it to mention %q", tc.argv0, tc.args, got, tc.want)
		}
	}

	if _, code := runShim(t, shimDir, "gh", "pr", "list"); code != 0 {
		t.Errorf("gh pr list: exit code != 0, want passthrough")
	}
	if _, code := runShim(t, shimDir, "gh", "api", "repos/foo/bar"); code != 0 {
		t.Errorf("gh api (read): exit code != 0, want passthrough")
	}
	if _, code := runShim(t, shimDir, "fj", "pr", "list"); code != 0 {
		t.Errorf("fj pr list: exit code != 0, want passthrough")
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read %s: %v", hookPath, err)
	}
	if !bytes.Contains(content, []byte("outbox")) {
		t.Fatalf("pre-push hook content = %q, want it to mention 'outbox' (the row's runtimeMessage)", content)
	}
}

// A git-hook row with an empty cfg.RepoDir must return an error rather than
// panic or silently do nothing.
func TestInstall_GitHookRowMissingRepoDir(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "blocked: git push",
			RuntimeMessage: "blocked: git push",
		},
	}

	cfg := Config{ShimDir: t.TempDir()} // RepoDir deliberately left empty

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err == nil {
		t.Fatalf("Install: got nil error, want non-nil (RepoDir is empty)")
	}
	if result.HookInstalled {
		t.Errorf("result.HookInstalled = true, want false on error")
	}
}

// cfg.SkipGitHook makes Install treat git-hook rows as absent, so an empty
// RepoDir is not an error. The outbox-incapable forgejo caller that sets it
// still wants its command-shim guard installed (issue #2509).
func TestInstall_SkipGitHookIgnoresGitHookRows(t *testing.T) {
	realTrue := requireExecutable(t, "true")

	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "blocked: git push",
			RuntimeMessage: "blocked: git push",
		},
		{
			ID:             "forbidden-fj-pr-create",
			Marker:         "fj pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: fj pr create",
			RuntimeMessage: "blocked: fj pr create",
		},
	}

	shimDir := t.TempDir()
	cfg := Config{
		SkipGitHook: true, // RepoDir deliberately left empty
		ShimDir:     shimDir,
		RealBinary: func(argv0 string) (string, error) {
			return realTrue, nil
		},
	}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v, want nil error (SkipGitHook is true)", err)
	}
	if result.HookInstalled {
		t.Errorf("result.HookInstalled = true, want false (SkipGitHook is true)")
	}
	if want := []string{"fj"}; len(result.Shims) != len(want) || result.Shims[0] != want[0] {
		t.Fatalf("result.Shims = %v, want %v", result.Shims, want)
	}
}

// A command-shim row with an empty cfg.ShimDir must return an error rather than
// panic or silently do nothing.
func TestInstall_CommandShimRowMissingShimDir(t *testing.T) {
	rows := []promptassembly.ForbiddenMarkerRow{
		{
			ID:             "forbidden-gh-pr-create",
			Marker:         "gh pr create",
			Kind:           "substring",
			Enforce:        "command-shim",
			Message:        "blocked: gh pr create",
			RuntimeMessage: "blocked: gh pr create",
		},
	}

	cfg := Config{RepoDir: t.TempDir()} // ShimDir deliberately left empty

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err == nil {
		t.Fatalf("Install: got nil error, want non-nil (ShimDir is empty)")
	}
	if len(result.Shims) != 0 {
		t.Errorf("result.Shims = %v, want empty on error", result.Shims)
	}
}

func TestHookPaths_BareAndWorkingRepo(t *testing.T) {
	work := t.TempDir()
	runGitCmd(t, work, "init", "-q")
	bare := t.TempDir()
	runGitCmd(t, bare, "init", "--bare", "-q")
	for dir, hooks := range map[string]string{work: filepath.Join(work, ".git", "hooks"), bare: filepath.Join(bare, "hooks")} {
		want := []string{filepath.Join(hooks, "pre-push"), filepath.Join(hooks, "pre-receive")}
		got := HookPaths(dir)
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("HookPaths(%s) = %v, want %v", dir, got, want)
		}
	}
}

// recordingBinary stands in for the real gh or fj: it appends its argv, one
// call per line, to the returned log so a passthrough test can prove the shim
// execs through with the arguments untouched.
func recordingBinary(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "real")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >>%q\n", log)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// Each rejection names the relay that replaces the blocked command, so the
// Driver knows where to go instead; a bare "blocked" would leave it guessing.
// The shim dir is a caller-chosen path, so installing under $HOME rather than
// the work dir's parent is box's concern (box TestReadonlyGuards_*).
func TestInstall_FullRegistryRejectionNamesTheRelay(t *testing.T) {
	bin, _ := recordingBinary(t)
	rows, err := promptassembly.LoadForbiddenMarkersFile(repopath.ForbiddenMarkersJSON())
	if err != nil {
		t.Fatalf("LoadForbiddenMarkersFile: %v", err)
	}
	shimDir := t.TempDir()
	cfg := Config{
		RepoDir:    t.TempDir(),
		ShimDir:    shimDir,
		RealBinary: func(string) (string, error) { return bin, nil },
	}
	cfg.SkipGitHook = true
	if _, err := Install(rows, cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	for _, tc := range []struct {
		argv0 string
		args  []string
		want  []string
	}{
		{"gh", []string{"pr", "create", "--title", "x", "--body", "y"}, []string{"gh pr create", "SPINDRIFT_PR_INTENT"}},
		{"gh", []string{"pr", "ready", "1"}, []string{"gh pr ready", "launcher"}},
		{"gh", []string{"pr", "merge", "1"}, []string{"gh pr merge", "launcher"}},
		{"gh", []string{"issue", "comment", "1", "--body", "hi"}, []string{"gh issue comment", "note="}},
		{"gh", []string{"issue", "create", "--title", "x", "--body", "y"}, []string{"gh issue create", "SPINDRIFT_ISSUE_INTENT"}},
		{"gh", []string{"api", "-X", "POST", "repos/o/r/issues/1/comments"}, []string{"gh api"}},
		{"gh", []string{"api", "-X", "post", "repos/o/r/issues/1/comments"}, []string{"gh api"}},
		{"gh", []string{"api", "--method", "PATCH", "repos/o/r/issues/1"}, []string{"gh api"}},
		{"fj", []string{"pr", "create", "--title", "x", "--body", "y"}, []string{"fj pr create"}},
		{"fj", []string{"pr", "ready", "1"}, []string{"fj pr ready"}},
		{"fj", []string{"pr", "merge", "1"}, []string{"fj pr merge"}},
		{"fj", []string{"issue", "comment", "1", "--body", "hi"}, []string{"fj issue comment"}},
		{"fj", []string{"issue", "create", "--title", "x", "--body", "y"}, []string{"fj issue create"}},
	} {
		got, code := runShim(t, shimDir, tc.argv0, tc.args...)
		if code == 0 {
			t.Errorf("%s %v: exit code = 0, want non-zero; output=%q", tc.argv0, tc.args, got)
		}
		for _, w := range tc.want {
			if !bytes.Contains([]byte(got), []byte(w)) {
				t.Errorf("%s %v: output = %q, want it to contain %q", tc.argv0, tc.args, got, w)
			}
		}
	}
}

// A read reaches the real binary with its argv intact; the shim only ever
// stands in front of the write subcommands.
func TestInstall_FullRegistryReadsReachTheRealBinary(t *testing.T) {
	bin, log := recordingBinary(t)
	rows, err := promptassembly.LoadForbiddenMarkersFile(repopath.ForbiddenMarkersJSON())
	if err != nil {
		t.Fatalf("LoadForbiddenMarkersFile: %v", err)
	}
	shimDir := t.TempDir()
	cfg := Config{
		ShimDir:     shimDir,
		SkipGitHook: true,
		RealBinary:  func(string) (string, error) { return bin, nil },
	}
	if _, err := Install(rows, cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	reads := []struct {
		argv0 string
		args  []string
	}{
		{"gh", []string{"api", "repos/owner/repo/issues"}},
		{"gh", []string{"issue", "view", "1"}},
		{"gh", []string{"pr", "view", "1"}},
		{"gh", []string{"run", "view", "1"}},
		{"gh", []string{"run", "list"}},
		{"fj", []string{"pr", "list"}},
		{"fj", []string{"issue", "view", "1"}},
	}
	var want string
	for _, tc := range reads {
		got, code := runShim(t, shimDir, tc.argv0, tc.args...)
		if code != 0 {
			t.Errorf("%s %v: exit code = %d, want 0 (passthrough); output=%q", tc.argv0, tc.args, code, got)
		}
		want += strings.Join(tc.args, " ") + "\n"
	}
	if got, err := os.ReadFile(log); err != nil || string(got) != want {
		t.Errorf("real binary saw %q (err=%v), want %q", got, err, want)
	}
}
