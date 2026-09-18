package landdelta

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runGitT runs git in dir with a fixed committer identity rather than the
// ambient git config, so these tests behave the same on any machine, and with
// commit.gpgsign off so a developer's global signing config cannot hang a
// commit on a passphrase prompt.
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

// newRepo creates a disposable repo on branch "main" with one commit adding
// base.txt.
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
	if !reflect.DeepEqual(got, want) {
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

	want := Delta{Known: true, Files: 2, Insertions: 2, Deletions: 0, Paths: []string{"base.txt", "new.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
	if len(got.Paths) != got.Files {
		t.Fatalf("len(Paths) = %d, want Files = %d", len(got.Paths), got.Files)
	}
	wantSummary := "post-approval land delta: 2 files changed, 2 insertions(+), 0 deletions(-)"
	if got.Summary() != wantSummary {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), wantSummary)
	}
}

// rebasedRepo builds the history the rebase path needs: main at A, a feature
// branch off A with the reviewed commit F1 (the anchor), main moving forward
// with an unrelated commit B, then feature rebased onto main so F1 gets a new
// SHA on top of B. withLandCommit adds one more commit on feature afterwards.
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

	// The rebase replays the same F1 content (base.txt) on both the reviewed
	// and landed sides, so it nets to zero. Only the extra land commit
	// (land.txt) counts, and the base movement (other.txt) must not appear.
	want := Delta{Known: true, Files: 1, Insertions: 1, Deletions: 0, Paths: []string{"land.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
	if len(got.Paths) != got.Files {
		t.Fatalf("len(Paths) = %d, want Files = %d", len(got.Paths), got.Files)
	}
}

func TestCompute_RebaseNoBranchChange(t *testing.T) {
	dir, anchor := rebasedRepo(t, false)

	got := Compute(dir, anchor, "main")

	want := Delta{Known: true}
	if !reflect.DeepEqual(got, want) {
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
	if got.Paths != nil {
		t.Fatalf("Paths = %v, want nil", got.Paths)
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

	// Amending HEAD leaves the anchor off HEAD's ancestry, which forces the
	// rebase path. The repo has no "origin" remote, so origin/$base, $base,
	// and origin/HEAD all fail to resolve.
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
