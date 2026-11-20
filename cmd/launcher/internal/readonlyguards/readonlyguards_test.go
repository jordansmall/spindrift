package readonlyguards

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// runGitCmd duplicates the helper of the same name in
// cmd/launcher/driver-exec/helpers_test.go (see there for why gc is off),
// which is in a different package and so cannot be imported here.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
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

	repoDir := t.TempDir()
	cfg := Config{RepoDir: repoDir}

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.HookInstalled {
		t.Fatalf("result.HookInstalled = true, want false")
	}

	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if _, err := os.Stat(hooksDir); err == nil {
		t.Fatalf("%s exists, want no hooks dir for a prompt-only-only row set", hooksDir)
	}
}

// This installs the real forbiddenMarkers fixture end to end rather than a
// hand-built row set, so a registry change that drops the hook or its message
// shows up here.
func TestInstall_FullRegistry(t *testing.T) {
	rows, err := promptassembly.LoadForbiddenMarkersFile(repopath.ForbiddenMarkersJSON())
	if err != nil {
		t.Fatalf("LoadForbiddenMarkersFile: %v", err)
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")

	var out bytes.Buffer
	result, err := Install(rows, Config{RepoDir: repoDir}, &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.HookInstalled {
		t.Fatalf("result.HookInstalled = false, want true")
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

	cfg := Config{} // RepoDir deliberately left empty

	var out bytes.Buffer
	result, err := Install(rows, cfg, &out)
	if err == nil {
		t.Fatalf("Install: got nil error, want non-nil (RepoDir is empty)")
	}
	if result.HookInstalled {
		t.Errorf("result.HookInstalled = true, want false on error")
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
