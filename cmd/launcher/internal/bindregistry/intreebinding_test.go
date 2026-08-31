package bindregistry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInTreeBindingTableHasCargoRow(t *testing.T) {
	var got *InTreeBinding
	for i := range inTreeBindings {
		if inTreeBindings[i].Ecosystem == "cargo" {
			got = &inTreeBindings[i]
		}
	}
	if got == nil {
		t.Fatalf("inTreeBindings has no cargo row: %+v", inTreeBindings)
	}
	if got.ConfigPath != ".cargo/config.toml" {
		t.Errorf("cargo row ConfigPath = %q, want %q", got.ConfigPath, ".cargo/config.toml")
	}
}

// runGit is a small helper mirroring the established hermetic-git-test
// pattern (forgetest.GitRepoFixture) -- a single local repo dir, no
// bare/clone/push needed since isTracked is purely local.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "initial")

	untracked := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestIsTrackedReportsTrueForTrackedFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "tracked.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if !tracked {
		t.Error("isTracked(tracked.txt) = false, want true")
	}
}

func TestIsTrackedReportsFalseForUntrackedFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "untracked.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if tracked {
		t.Error("isTracked(untracked.txt) = true, want false")
	}
}

func TestIsTrackedReportsFalseForMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "does-not-exist.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if tracked {
		t.Error("isTracked(does-not-exist.txt) = true, want false")
	}
}

// writeCargoConfig writes relPath (relative to dir) with content, tracking
// it in git (add + commit) when tracked is true, leaving it untouched on
// disk only otherwise.
func writeCargoConfig(t *testing.T, dir, relPath, content string, tracked bool) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if tracked {
		runGit(t, dir, "add", relPath)
		runGit(t, dir, "commit", "-m", "add "+relPath)
	}
}

// skipWorktreeSet reports whether relPath's skip-worktree bit is set,
// mirroring the "S" prefix `git ls-files -v` reports for that bit.
func skipWorktreeSet(t *testing.T, dir, relPath string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "ls-files", "-v", "--", relPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files -v: %v: %s", err, out)
	}
	return strings.HasPrefix(string(out), "S ")
}

var cargoBinding = InTreeBinding{Ecosystem: "cargo", ConfigPath: ".cargo/config.toml"}

func TestApplyInTreeBindingRewritesTrackedFileBothSchemes(t *testing.T) {
	dir := newTestRepo(t)
	content := "[source.crates-io]\nreplace-with = \"proxy\"\n\n[source.proxy]\nregistry = \"sparse+https://upstream.example/index/\"\n\n[registries.proxy]\nindex = \"http://upstream.example/other/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, content, true)

	applied, untracked, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if !applied {
		t.Error("applied = false, want true")
	}
	if untracked {
		t.Error("untracked = true, want false")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:27182/index/") {
		t.Errorf("rewritten content missing expected sparse https rewrite: %s", got)
	}
	if !strings.Contains(string(got), "http://127.0.0.1:27182/other/") {
		t.Errorf("rewritten content missing expected http rewrite: %s", got)
	}
	if !skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit not set after apply")
	}
}

// TestApplyInTreeBindingEscaping covers hosts containing characters that
// were sed metacharacters under the old bash phase (the mechanism this
// engine replaced needed per-host escaping; strings.ReplaceAll never does).
func TestApplyInTreeBindingEscaping(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		content string
		want    string
	}{
		{
			name:    "sed_special_character_host",
			host:    "registry.corp#1.example",
			content: "registry = \"https://registry.corp#1.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "asterisk_host",
			host:    "registry*.example",
			content: "registry = \"https://registry*.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "bracket_character_class_host",
			host:    "registry[1].example",
			content: "registry = \"https://registry[1].example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "caret_dollar_anchor_host",
			host:    "registry^end$.example",
			content: "registry = \"https://registry^end$.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "backslash_host",
			host:    "registry\\.example",
			content: "registry = \"https://registry\\.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		// dot_does_not_match_arbitrary_character asserts the host's "."s are
		// literal, not sed's/regexp's "any character": if strings.ReplaceAll
		// (or a would-be regex-based rewrite) treated "." as a wildcard, the
		// decoy line below -- same length, "." positions replaced with
		// different letters -- would false-positive-match too. Only the exact
		// reg.stry.example line may be rewritten.
		{
			name:    "dot_does_not_match_arbitrary_character",
			host:    "reg.stry.example",
			content: "registry = \"https://reg.stry.example/index/\"\nother = \"https://regXstryYexample/decoy/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\nother = \"https://regXstryYexample/decoy/\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTestRepo(t)
			writeCargoConfig(t, dir, cargoBinding.ConfigPath, tc.content, true)

			applied, untracked, err := ApplyInTreeBinding(dir, cargoBinding, tc.host, "http://127.0.0.1:27182")
			if err != nil {
				t.Fatalf("ApplyInTreeBinding: %v", err)
			}
			if !applied {
				t.Error("applied = false, want true")
			}
			if untracked {
				t.Error("untracked = true, want false")
			}

			got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("rewritten content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInTreeBindingUntrackedFileTolerance covers both engine entry points
// against the same untracked-file fixture: neither ApplyInTreeBinding nor
// RevertInTreeBinding may run `git update-index --skip-worktree` (or
// `checkout`) against a path git doesn't track -- git would reject the
// former and the latter's meaning is undefined for an untracked path.
func TestInTreeBindingUntrackedFileTolerance(t *testing.T) {
	content := "registry = \"https://upstream.example/index/\"\n"

	cases := []struct {
		name string
		run  func(t *testing.T, dir string) (actedOn bool)
	}{
		{
			name: "apply",
			run: func(t *testing.T, dir string) bool {
				applied, untracked, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
				if err != nil {
					t.Fatalf("ApplyInTreeBinding: %v", err)
				}
				if !untracked {
					t.Error("untracked = false, want true")
				}
				return applied
			},
		},
		{
			name: "revert",
			run: func(t *testing.T, dir string) bool {
				reverted, err := RevertInTreeBinding(dir, cargoBinding)
				if err != nil {
					t.Fatalf("RevertInTreeBinding: %v", err)
				}
				return reverted
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTestRepo(t)
			writeCargoConfig(t, dir, cargoBinding.ConfigPath, content, false)

			if acted := tc.run(t, dir); acted {
				t.Errorf("%s: acted on untracked file, want no-op", tc.name)
			}

			got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Errorf("%s: untracked file was modified: got %q, want %q", tc.name, got, content)
			}
		})
	}
}

func TestApplyInTreeBindingNoopOnMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	applied, untracked, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if applied {
		t.Error("applied = true, want false")
	}
	if untracked {
		t.Error("untracked = true, want false")
	}
}

func TestApplyInTreeBindingNoopWhenHostAbsent(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://some-other-host.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, content, true)

	applied, untracked, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if applied {
		t.Error("applied = true, want false")
	}
	if untracked {
		t.Error("untracked = true, want false")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed unexpectedly: got %q, want %q", got, content)
	}
	if skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit set, want unset")
	}
}

func TestApplyInTreeBindingIdempotentOnSecondCall(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, content, true)

	applied1, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("first ApplyInTreeBinding: %v", err)
	}
	if !applied1 {
		t.Fatal("first call: applied = false, want true")
	}

	afterFirst, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	applied2, untracked2, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("second ApplyInTreeBinding: %v", err)
	}
	if applied2 {
		t.Error("second call: applied = true, want false (idempotent no-op)")
	}
	if untracked2 {
		t.Error("second call: untracked = true, want false")
	}

	afterSecond, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("content changed on second call: before %q, after %q", afterFirst, afterSecond)
	}
	if !skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit not set after second (no-op) call, want still set")
	}
}

func TestApplyInTreeBindingConvergesAfterCrashBetweenPhases(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, original, true)

	// Simulate Apply's rewrite step landing but the process dying before
	// the skip-worktree bit got set -- content is already rewritten and
	// dirty vs HEAD, bit is clear.
	rewritten := "registry = \"http://127.0.0.1:27182/index/\"\n"
	if err := os.WriteFile(filepath.Join(dir, cargoBinding.ConfigPath), []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}

	if !skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit not set after converge, want set")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != rewritten {
		t.Errorf("content = %q, want unchanged %q", got, rewritten)
	}
}

// gitOutput runs git and returns stdout, failing the test on a nonzero exit
// -- unlike runGit it doesn't print combined output, since callers here only
// want a value (e.g. a branch name) back.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// newUnmergedTestRepo builds a repo where relPath is genuinely unmerged --
// two branches each rewrite it while still mentioning upstream.example, then
// merging lands mid-conflict (UU), the same state a pre-work-rebase can leave
// a config file in (issue #2932). Unlike newTestRepo's plain init-plus-commit,
// this needs a real three-commit history for `git merge` to actually
// conflict rather than fast-forward.
func newUnmergedTestRepo(t *testing.T, relPath string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("registry = \"https://upstream.example/index/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "base")
	base := strings.TrimSpace(gitOutput(t, dir, "symbolic-ref", "--short", "HEAD"))

	runGit(t, dir, "checkout", "-b", "feature")
	write("registry = \"https://upstream.example/other/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "feature")

	runGit(t, dir, "checkout", base)
	write("registry = \"https://upstream.example/index2/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "base2")

	// A conflicting merge is the point of this fixture -- unlike runGit's
	// other calls, a nonzero exit here is the expected/desired outcome, not
	// a setup failure.
	if err := exec.Command("git", "-C", dir, "merge", "feature").Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	// Sanity precondition: confirm this fixture actually reproduces an
	// unmerged path, the same state `git update-index --skip-worktree`
	// rejects with exit 128 (issue #2932) -- not some other kind of dirty
	// working tree.
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", relPath).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", relPath, status, err)
	}

	return dir
}

// TestApplyInTreeBindingDoesNotRewriteContentWhenSkipWorktreeFails covers
// issue #2932: on an unmerged config path, `git update-index
// --skip-worktree` fails (exit 128), and the old write-then-tag order had
// already rewritten the file's content by that point -- landing the
// local-registry-proxy URL in a tracked, unmerged file that
// RevertInTreeBinding can't clean up either (`git checkout --` refuses an
// unmerged path). ApplyInTreeBinding must fail without ever touching content.
func TestApplyInTreeBindingDoesNotRewriteContentWhenSkipWorktreeFails(t *testing.T) {
	dir := newUnmergedTestRepo(t, cargoBinding.ConfigPath)

	before, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (skip-worktree must fail on an unmerged path)")
	}

	after, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("content rewritten despite update-index failure: before %q, after %q", before, after)
	}
}

// TestApplyInTreeBindingUnsetsBitWhenWriteFileFails covers the other half of
// issue #2932's tag-then-write ordering: `update-index --skip-worktree`
// succeeds first (a git subprocess that only flips an index bit), then the
// content os.WriteFile fails -- forced here by making the config file itself
// read-only. Chmodding only the parent directory (as one might expect) does
// NOT reproduce the failure: os.WriteFile opens an *existing* file with
// O_TRUNC, and overwriting an existing file's content needs write permission
// on the file itself, not on the directory that contains it (directory
// permissions gate creating/renaming/removing directory entries, not
// truncating an existing one) -- confirmed empirically before writing this
// test. `update-index --skip-worktree` still succeeds either way, since it
// only touches .git/index, never the working-tree file. ApplyInTreeBinding's
// compensating `--no-skip-worktree` call must undo the bit so a later Apply
// doesn't mistake "bit set" for "already applied" against never-rewritten
// content.
func TestApplyInTreeBindingUnsetsBitWhenWriteFileFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permission bits don't block writes, so the WriteFile failure can't be simulated")
	}

	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, original, true)

	configFile := filepath.Join(dir, cargoBinding.ConfigPath)
	if err := os.Chmod(configFile, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configFile, 0o644)
	})

	applied, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (WriteFile must fail against a read-only config file)")
	}
	if applied {
		t.Error("applied = true, want false")
	}

	if skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit still set after WriteFile failure, want unset (compensating --no-skip-worktree should have run)")
	}

	if err := os.Chmod(configFile, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("content = %q, want unchanged %q", got, original)
	}
}

func TestRevertInTreeBindingRestoresAfterApply(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, original, true)

	applied, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if !applied {
		t.Fatal("ApplyInTreeBinding: applied = false, want true")
	}

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if !reverted {
		t.Error("reverted = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("reverted content = %q, want %q", got, original)
	}
	if skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit still set after revert")
	}
}

func TestRevertInTreeBindingSecondCallIsNoop(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, original, true)

	if _, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182"); err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}

	reverted1, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("first RevertInTreeBinding: %v", err)
	}
	if !reverted1 {
		t.Fatal("first call: reverted = false, want true")
	}

	afterFirst, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	reverted2, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("second RevertInTreeBinding: %v", err)
	}
	if reverted2 {
		t.Error("second call: reverted = true, want false (idempotent no-op)")
	}

	afterSecond, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("content changed on second revert: before %q, after %q", afterFirst, afterSecond)
	}
	if skipWorktreeSet(t, dir, cargoBinding.ConfigPath) {
		t.Error("skip-worktree bit set after second revert, want unset")
	}
}

func TestRevertInTreeBindingNoopOnNeverApplied(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, content, true)

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if reverted {
		t.Error("reverted = true, want false")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed unexpectedly: got %q, want %q", got, content)
	}
}

func TestRevertInTreeBindingRestoresAfterCrashBetweenPhases(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeCargoConfig(t, dir, cargoBinding.ConfigPath, original, true)

	// Simulate Apply's rewrite step landing but the process dying before
	// the skip-worktree bit got set -- content is dirty, bit is clear.
	rewritten := "registry = \"http://127.0.0.1:27182/index/\"\n"
	if err := os.WriteFile(filepath.Join(dir, cargoBinding.ConfigPath), []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if !reverted {
		t.Error("reverted = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("reverted content = %q, want %q", got, original)
	}
}

// TestApplyInTreeBindingRefusesSymlinkedConfigPath covers issue #2932's
// symlink-escape hazard: os.Stat/os.ReadFile/os.WriteFile all follow
// symlinks, so a tracked .cargo/config.toml that is itself a symlink
// (git tracks symlinks as blob mode 120000, a legitimate tracked state --
// see isTracked's doc) would otherwise cause ApplyInTreeBinding to read and
// rewrite whatever file the symlink resolves to, even one entirely outside
// repoDir. ApplyInTreeBinding must refuse before ever calling
// os.ReadFile/os.WriteFile on the resolved target.
func TestApplyInTreeBindingRefusesSymlinkedConfigPath(t *testing.T) {
	dir := newTestRepo(t)

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside-config.toml")
	sentinel := "registry = \"https://upstream.example/index/\"\n"
	if err := os.WriteFile(outsideFile, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, cargoBinding.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", cargoBinding.ConfigPath)
	runGit(t, dir, "commit", "-m", "add symlinked config")

	_, _, err := ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (symlinked config path must be refused)")
	}

	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Errorf("outside file was modified through the symlink: got %q, want %q", got, sentinel)
	}
}

func TestRevertInTreeBindingNoopOnMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if reverted {
		t.Error("reverted = true, want false")
	}
}
