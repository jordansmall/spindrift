package landdelta

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitT runs `git <args...>` in dir with a fixed, hermetic committer/author
// identity (never the ambient user's git config) so these tests behave the
// same on any machine or CI box. commit.gpgsign is forced off so a
// developer's global signing config can't make a test commit hang on a
// passphrase prompt.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=landdelta test",
		"-c", "user.email=landdelta-test@example.com",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=landdelta test",
		"GIT_AUTHOR_EMAIL=landdelta-test@example.com",
		"GIT_COMMITTER_NAME=landdelta test",
		"GIT_COMMITTER_EMAIL=landdelta-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a disposable repo on branch "main" with one committed
// file, base.txt, and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init")
	runGitT(t, dir, "checkout", "-b", "main")
	writeFileT(t, dir, "base.txt", "line1\n")
	runGitT(t, dir, "add", "base.txt")
	runGitT(t, dir, "commit", "-m", "init")
	return dir
}

func TestCompute_ZeroDelta(t *testing.T) {
	dir := newRepo(t)
	anchor := runGitT(t, dir, "rev-parse", "HEAD")

	got := Compute(dir, anchor, "")

	want := Delta{Known: true}
	if got != want {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
	if got.Summary() != "post-approval land delta: none — landing did not alter the reviewed tree" {
		t.Fatalf("Summary() = %q", got.Summary())
	}
}

func TestCompute_AddedCommitDelta(t *testing.T) {
	dir := newRepo(t)
	anchor := runGitT(t, dir, "rev-parse", "HEAD")

	writeFileT(t, dir, "base.txt", "line1\nline2\n")
	runGitT(t, dir, "add", "base.txt")
	runGitT(t, dir, "commit", "-m", "land: add line2")
	writeFileT(t, dir, "new.txt", "new\n")
	runGitT(t, dir, "add", "new.txt")
	runGitT(t, dir, "commit", "-m", "land: add new.txt")

	got := Compute(dir, anchor, "")

	want := Delta{Known: true, Files: 2, Insertions: 2, Deletions: 0}
	if got != want {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
	wantSummary := "post-approval land delta: 2 files changed, 2 insertions(+), 0 deletions(-)"
	if got.Summary() != wantSummary {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), wantSummary)
	}
}

// rebasedRepo builds: main at A, then a feature branch off A with a reviewed
// commit F1 (anchor), then main moves forward with an unrelated commit B,
// then feature is rebased onto main (F1 -> F1', a new SHA on top of B). If
// withLandCommit, one more commit L is added on feature after the rebase.
// Returns (dir, anchor).
func rebasedRepo(t *testing.T, withLandCommit bool) (string, string) {
	t.Helper()
	dir := newRepo(t)

	runGitT(t, dir, "checkout", "-b", "feature")
	writeFileT(t, dir, "base.txt", "line1\nline2\n")
	runGitT(t, dir, "add", "base.txt")
	runGitT(t, dir, "commit", "-m", "feature: add line2")
	anchor := runGitT(t, dir, "rev-parse", "HEAD")

	runGitT(t, dir, "checkout", "main")
	writeFileT(t, dir, "other.txt", "on main\n")
	runGitT(t, dir, "add", "other.txt")
	runGitT(t, dir, "commit", "-m", "main: moved base")

	runGitT(t, dir, "checkout", "feature")
	runGitT(t, dir, "rebase", "main")

	if withLandCommit {
		writeFileT(t, dir, "land.txt", "landed\n")
		runGitT(t, dir, "add", "land.txt")
		runGitT(t, dir, "commit", "-m", "land: add land.txt")
	}

	return dir, anchor
}

func TestCompute_RebaseOntoMovedBaseWithLandCommit(t *testing.T) {
	dir, anchor := rebasedRepo(t, true)

	got := Compute(dir, anchor, "main")

	// The rebase replays the same F1 content (base.txt) on both the
	// reviewed and landed sides, so it must net to zero; only the extra
	// land commit (land.txt) should count. Base movement (other.txt) must
	// not appear at all.
	want := Delta{Known: true, Files: 1, Insertions: 1, Deletions: 0}
	if got != want {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
}

func TestCompute_RebaseNoBranchChange(t *testing.T) {
	dir, anchor := rebasedRepo(t, false)

	got := Compute(dir, anchor, "main")

	want := Delta{Known: true}
	if got != want {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
}

func TestCompute_MissingAnchor(t *testing.T) {
	dir := newRepo(t)

	got := Compute(dir, "", "")

	if got.Known {
		t.Fatalf("Compute() = %+v, want Known=false", got)
	}
	if got.Reason != "no reviewed-commit anchor" {
		t.Fatalf("Reason = %q", got.Reason)
	}
	wantSummary := "post-approval land delta: unknown (no reviewed-commit anchor)"
	if got.Summary() != wantSummary {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), wantSummary)
	}
}

func TestCompute_GarbageAnchor(t *testing.T) {
	dir := newRepo(t)

	got := Compute(dir, "not-a-real-sha!!", "")

	if got.Known {
		t.Fatalf("Compute() = %+v, want Known=false", got)
	}
	if got.Reason != "no reviewed-commit anchor" {
		t.Fatalf("Reason = %q", got.Reason)
	}
}

func TestCompute_UnreachableAnchor(t *testing.T) {
	dir := newRepo(t)

	got := Compute(dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "")

	if got.Known {
		t.Fatalf("Compute() = %+v, want Known=false", got)
	}
	if got.Reason != "reviewed-commit anchor not found in the repo" {
		t.Fatalf("Reason = %q", got.Reason)
	}
}

func TestCompute_UnresolvableBase(t *testing.T) {
	dir := newRepo(t)
	anchor := runGitT(t, dir, "rev-parse", "HEAD")

	// Amend HEAD so the anchor (the pre-amend commit) is no longer an
	// ancestor of HEAD, forcing the rebase path, then leave baseBranch
	// unresolvable: no "origin" remote exists, so origin/$base,
	// $base, and origin/HEAD all fail to resolve.
	writeFileT(t, dir, "base.txt", "line1\nchanged\n")
	runGitT(t, dir, "add", "base.txt")
	runGitT(t, dir, "commit", "--amend", "-m", "init (amended)")

	got := Compute(dir, anchor, "does-not-exist")

	if got.Known {
		t.Fatalf("Compute() = %+v, want Known=false", got)
	}
	if got.Reason != "branch was rebased and the base ref could not be resolved" {
		t.Fatalf("Reason = %q", got.Reason)
	}
}

func TestDeltaSummary(t *testing.T) {
	cases := []struct {
		name string
		d    Delta
		want string
	}{
		{
			name: "counted",
			d:    Delta{Known: true, Files: 2, Insertions: 41, Deletions: 3},
			want: "post-approval land delta: 2 files changed, 41 insertions(+), 3 deletions(-)",
		},
		{
			name: "zero",
			d:    Delta{Known: true},
			want: "post-approval land delta: none — landing did not alter the reviewed tree",
		},
		{
			name: "unknown",
			d:    Delta{Known: false, Reason: "no reviewed-commit anchor"},
			want: "post-approval land delta: unknown (no reviewed-commit anchor)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Summary(); got != c.want {
				t.Fatalf("Summary() = %q, want %q", got, c.want)
			}
		})
	}
}
