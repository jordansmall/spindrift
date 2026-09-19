package landdelta

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

	want := Delta{
		Known: true, Files: 2, Insertions: 2, Deletions: 0, Paths: []string{"base.txt", "new.txt"},
		Ranges: map[string][]Range{
			"base.txt": {{Start: 1, Count: 0}},
			"new.txt":  {{Start: 0, Count: 0}},
		},
	}
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
	want := Delta{
		Known: true, Files: 1, Insertions: 1, Deletions: 0, Paths: []string{"land.txt"},
		Ranges: map[string][]Range{"land.txt": {{Start: 0, Count: 0}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
	if len(got.Paths) != got.Files {
		t.Fatalf("len(Paths) = %d, want Files = %d", len(got.Paths), got.Files)
	}
	wantSummary := "post-approval land delta: 1 files changed, 1 insertions(+), 0 deletions(-)"
	if got.Summary() != wantSummary {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), wantSummary)
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
	if got.Ranges != nil {
		t.Fatalf("Ranges = %v, want nil", got.Ranges)
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

func TestParsePreImageRanges(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want map[string][]Range
	}{
		{
			name: "single hunk modification",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -3,2 +3,2 @@\n" +
				"-old1\n" +
				"-old2\n" +
				"+new1\n" +
				"+new2\n",
			want: map[string][]Range{
				"foo.txt": {{Start: 3, Count: 2}},
			},
		},
		{
			name: "pure insertion",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -7,0 +8,3 @@\n" +
				"+new1\n" +
				"+new2\n" +
				"+new3\n",
			want: map[string][]Range{
				"foo.txt": {{Start: 7, Count: 0}},
			},
		},
		{
			name: "pure deletion",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -4,3 +3,0 @@\n" +
				"-old1\n" +
				"-old2\n" +
				"-old3\n",
			want: map[string][]Range{
				"foo.txt": {{Start: 4, Count: 3}},
			},
		},
		{
			name: "single line hunk comma omitted both sides",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -12 +12 @@\n" +
				"-old\n" +
				"+new\n",
			want: map[string][]Range{
				"foo.txt": {{Start: 12, Count: 1}},
			},
		},
		{
			name: "multi hunk file",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -3,1 +3,1 @@\n" +
				"-old1\n" +
				"+new1\n" +
				"@@ -10,2 +10,2 @@\n" +
				"-old2\n" +
				"-old3\n" +
				"+new2\n" +
				"+new3\n" +
				"@@ -20,0 +22,1 @@\n" +
				"+new4\n",
			want: map[string][]Range{
				"foo.txt": {
					{Start: 3, Count: 1},
					{Start: 10, Count: 2},
					{Start: 20, Count: 0},
				},
			},
		},
		{
			name: "multi file diff",
			diff: "diff --git a/foo.txt b/foo.txt\n" +
				"--- a/foo.txt\n" +
				"+++ b/foo.txt\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-old\n" +
				"+new\n" +
				"diff --git a/bar.txt b/bar.txt\n" +
				"--- a/bar.txt\n" +
				"+++ b/bar.txt\n" +
				"@@ -5,2 +5,2 @@\n" +
				"-old1\n" +
				"-old2\n" +
				"+new1\n" +
				"+new2\n",
			want: map[string][]Range{
				"foo.txt": {{Start: 1, Count: 1}},
				"bar.txt": {{Start: 5, Count: 2}},
			},
		},
		{
			name: "deleted file dev null",
			diff: "diff --git a/gone.txt b/gone.txt\n" +
				"deleted file mode 100644\n" +
				"--- a/gone.txt\n" +
				"+++ /dev/null\n" +
				"@@ -1,3 +0,0 @@\n" +
				"-old1\n" +
				"-old2\n" +
				"-old3\n",
			want: map[string][]Range{
				"gone.txt": {{Start: 1, Count: 3}},
			},
		},
		{
			name: "hunk content mimics new-path header",
			diff: "diff --git a/f.md b/f.md\n" +
				"--- a/f.md\n" +
				"+++ b/f.md\n" +
				"@@ -1,0 +2,1 @@\n" +
				"+++ b/evil.go\n" +
				"@@ -5,1 +6,1 @@\n" +
				"-old5\n" +
				"+new5\n",
			want: map[string][]Range{
				"f.md": {
					{Start: 1, Count: 0},
					{Start: 5, Count: 1},
				},
			},
		},
		{
			name: "hunk content mimics old-path header",
			diff: "diff --git a/f.md b/f.md\n" +
				"--- a/f.md\n" +
				"+++ b/f.md\n" +
				"@@ -1,1 +0,0 @@\n" +
				"--- a/evil.go\n" +
				"@@ -5,1 +5,1 @@\n" +
				"-old5\n" +
				"+new5\n",
			want: map[string][]Range{
				"f.md": {
					{Start: 1, Count: 1},
					{Start: 5, Count: 1},
				},
			},
		},
		{
			name: "empty input",
			diff: "",
			want: nil,
		},
		{
			// core.quotePath (on by default) renders a non-ASCII path
			// quoted with backslash-escaped octal, a shape neither
			// oldPathHeaderRe nor newPathHeaderRe matches, so the path
			// never gets ranges (issue #3503) — a documented, safe
			// degradation rather than a bug.
			name: "quoted non-ASCII path yields no ranges",
			diff: "diff --git \"a/\\303\\251.txt\" \"b/\\303\\251.txt\"\n" +
				"--- \"a/\\303\\251.txt\"\n" +
				"+++ \"b/\\303\\251.txt\"\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-old\n" +
				"+new\n",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePreImageRanges(c.diff)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parsePreImageRanges() = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDeltaRangesJSONTag(t *testing.T) {
	withRanges := Delta{
		Known:  true,
		Files:  1,
		Ranges: map[string][]Range{"foo.txt": {{Start: 3, Count: 2}}},
	}
	b, err := json.Marshal(withRanges)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"ranges":{"foo.txt":[{"start":3,"count":2}]}`) {
		t.Fatalf("json = %s, want ranges key with start/count", b)
	}

	withoutRanges := Delta{Known: true}
	b, err = json.Marshal(withoutRanges)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), `"ranges"`) {
		t.Fatalf("json = %s, want no ranges key", b)
	}
}

// TestCompute_Ranges pins Delta.Ranges (issue #3503) across both Compute
// paths: direct-diff and rebase-invariant. Each subtest builds its own
// disposable repo since the setups differ (single-line change, rebase,
// insertion, deletion, multi-hunk).
func TestCompute_Ranges(t *testing.T) {
	// The direct-diff path twice over one fixture: once in a pristine repo,
	// once in one whose ambient config rewrites the diff preImageRanges
	// parses. Unpinned, each of diff.noprefix, diff.srcPrefix/diff.dstPrefix,
	// color.ui and diff.external empties the ranges on its own, so the two
	// runs share a `want` rather than drifting apart by hand.
	for _, c := range []struct {
		name string
		// config holds repo-local `git config <key> <value>` pairs.
		config [][2]string
	}{
		{name: "direct diff"},
		{name: "direct diff under hostile ambient diff config", config: [][2]string{
			{"diff.noprefix", "true"},
			{"diff.mnemonicPrefix", "true"},
			{"diff.srcPrefix", "SRC/"},
			{"diff.dstPrefix", "DST/"},
			{"color.ui", "always"},
			{"diff.external", "echo external-diff"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := newRepo(t)
			for _, kv := range c.config {
				runGitT(t, dir, "config", kv[0], kv[1])
			}
			anchor := runGitT(t, dir, "rev-parse", "HEAD")

			writeFileT(t, dir, "base.txt", "line1changed\n")
			runGitT(t, dir, "add", "base.txt")
			runGitT(t, dir, "commit", "-m", "land: change line1")

			got := Compute(dir, anchor, "")

			want := map[string][]Range{"base.txt": {{Start: 1, Count: 1}}}
			if !reflect.DeepEqual(got.Ranges, want) {
				t.Fatalf("Ranges = %+v, want %+v", got.Ranges, want)
			}
		})
	}

	t.Run("rebased branch", func(t *testing.T) {
		dir, anchor := rebasedRepo(t, true)

		got := Compute(dir, anchor, "main")

		if got.Ranges == nil {
			t.Fatal("Ranges = nil, want non-nil")
		}
		if _, ok := got.Ranges["land.txt"]; !ok {
			t.Fatalf("Ranges = %+v, want a \"land.txt\" key", got.Ranges)
		}
		// other.txt only changed because main (the base) moved during
		// review; the land pass itself never touched it, so it must not
		// appear in Ranges (AC3).
		if _, ok := got.Ranges["other.txt"]; ok {
			t.Fatalf("Ranges = %+v, want no \"other.txt\" key (base-movement-only path)", got.Ranges)
		}
	})

	t.Run("mixed provenance", func(t *testing.T) {
		// shared.txt is edited by all three of: the anchor commit (line 3),
		// the base's own movement (line 35), and the land commit (line 20).
		// anchor..HEAD's hunks for shared.txt therefore mix the land pass's
		// own edit with the base's, which cannot be told apart in anchor
		// coordinates — shared.txt must still count as a touched path, but
		// it must never get a Ranges entry.
		base := make([]string, 40)
		for i := range base {
			base[i] = "line" + strconv.Itoa(i+1)
		}
		dir := newRepo(t)
		writeFileT(t, dir, "shared.txt", strings.Join(base, "\n")+"\n")
		runGitT(t, dir, "add", "shared.txt")
		runGitT(t, dir, "commit", "-m", "add shared.txt")

		runGitT(t, dir, "checkout", "-b", "feature")
		featureLines := append([]string(nil), base...)
		featureLines[2] = "line3mod"
		writeFileT(t, dir, "shared.txt", strings.Join(featureLines, "\n")+"\n")
		runGitT(t, dir, "add", "shared.txt")
		runGitT(t, dir, "commit", "-m", "feature: change line3")
		anchor := runGitT(t, dir, "rev-parse", "HEAD")

		runGitT(t, dir, "checkout", "main")
		mainLines := append([]string(nil), base...)
		mainLines[34] = "line35mod"
		writeFileT(t, dir, "shared.txt", strings.Join(mainLines, "\n")+"\n")
		runGitT(t, dir, "add", "shared.txt")
		runGitT(t, dir, "commit", "-m", "main: change line35")

		runGitT(t, dir, "checkout", "feature")
		runGitT(t, dir, "rebase", "main")

		landLines := append([]string(nil), base...)
		landLines[2] = "line3mod"
		landLines[34] = "line35mod"
		landLines[19] = "line20mod"
		writeFileT(t, dir, "shared.txt", strings.Join(landLines, "\n")+"\n")
		runGitT(t, dir, "add", "shared.txt")
		runGitT(t, dir, "commit", "-m", "land: change line20")

		got := Compute(dir, anchor, "main")

		found := false
		for _, p := range got.Paths {
			if p == "shared.txt" {
				found = true
			}
		}
		if !found {
			t.Fatalf("Paths = %v, want \"shared.txt\"", got.Paths)
		}
		if _, ok := got.Ranges["shared.txt"]; ok {
			t.Fatalf("Ranges = %+v, want no \"shared.txt\" key (mixed provenance)", got.Ranges)
		}
	})

	t.Run("header mimicry hazard end-to-end", func(t *testing.T) {
		// Drives the same header-mimicry hazard as TestParsePreImageRanges'
		// "hunk content mimics new-path header" case through real git, to
		// pin the assumption about git's actual -U0 output rather than
		// just the parser's unit table.
		var lines []string
		for i := 1; i <= 10; i++ {
			lines = append(lines, "line"+strconv.Itoa(i))
		}
		dir := newRepo(t)
		writeFileT(t, dir, "notes.md", strings.Join(lines, "\n")+"\n")
		runGitT(t, dir, "add", "notes.md")
		runGitT(t, dir, "commit", "-m", "add notes.md")
		anchor := runGitT(t, dir, "rev-parse", "HEAD")

		// Insert a line whose literal text is "++ b/evil.go" — git's diff
		// output renders the added line as "+++ b/evil.go", a new-path
		// header's exact shape — plus a second, distant changed region.
		landed := append([]string{}, lines[0:2]...)
		landed = append(landed, "++ b/evil.go")
		landed = append(landed, lines[2:]...)
		landed[9] = "line9mod"
		writeFileT(t, dir, "notes.md", strings.Join(landed, "\n")+"\n")
		runGitT(t, dir, "add", "notes.md")
		runGitT(t, dir, "commit", "-m", "land: add evil-mimicking line and modify line9")

		got := Compute(dir, anchor, "")

		want := map[string][]Range{"notes.md": {{Start: 2, Count: 0}, {Start: 9, Count: 1}}}
		if !reflect.DeepEqual(got.Ranges, want) {
			t.Fatalf("Ranges = %+v, want %+v", got.Ranges, want)
		}
	})

	t.Run("pure insertion", func(t *testing.T) {
		dir := newRepo(t)
		anchor := runGitT(t, dir, "rev-parse", "HEAD")

		writeFileT(t, dir, "base.txt", "line1\nline2\n")
		runGitT(t, dir, "add", "base.txt")
		runGitT(t, dir, "commit", "-m", "land: append line2")

		got := Compute(dir, anchor, "")

		want := map[string][]Range{"base.txt": {{Start: 1, Count: 0}}}
		if !reflect.DeepEqual(got.Ranges, want) {
			t.Fatalf("Ranges = %+v, want %+v", got.Ranges, want)
		}
	})

	t.Run("pure deletion", func(t *testing.T) {
		dir := newRepo(t)
		writeFileT(t, dir, "base.txt", "line1\nline2\nline3\nline4\n")
		runGitT(t, dir, "add", "base.txt")
		runGitT(t, dir, "commit", "-m", "expand")
		anchor := runGitT(t, dir, "rev-parse", "HEAD")

		writeFileT(t, dir, "base.txt", "line1\nline4\n")
		runGitT(t, dir, "add", "base.txt")
		runGitT(t, dir, "commit", "-m", "land: delete line2 and line3")

		got := Compute(dir, anchor, "")

		want := map[string][]Range{"base.txt": {{Start: 2, Count: 2}}}
		if !reflect.DeepEqual(got.Ranges, want) {
			t.Fatalf("Ranges = %+v, want %+v", got.Ranges, want)
		}
	})

	t.Run("multi-hunk file", func(t *testing.T) {
		dir := newRepo(t)
		var lines []string
		for i := 1; i <= 10; i++ {
			lines = append(lines, "line"+strconv.Itoa(i))
		}
		writeFileT(t, dir, "base.txt", strings.Join(lines, "\n")+"\n")
		runGitT(t, dir, "add", "base.txt")
		runGitT(t, dir, "commit", "-m", "expand")
		anchor := runGitT(t, dir, "rev-parse", "HEAD")

		lines[1] = "line2mod"
		lines[8] = "line9mod"
		writeFileT(t, dir, "base.txt", strings.Join(lines, "\n")+"\n")
		runGitT(t, dir, "add", "base.txt")
		runGitT(t, dir, "commit", "-m", "land: change two distant lines")

		got := Compute(dir, anchor, "")

		want := map[string][]Range{"base.txt": {{Start: 2, Count: 1}, {Start: 9, Count: 1}}}
		if !reflect.DeepEqual(got.Ranges, want) {
			t.Fatalf("Ranges = %+v, want %+v", got.Ranges, want)
		}
	})

	t.Run("unknown delta", func(t *testing.T) {
		dir := newRepo(t)

		got := Compute(dir, "", "")

		if got.Known {
			t.Fatalf("Known = true, want false")
		}
		if got.Ranges != nil {
			t.Fatalf("Ranges = %+v, want nil", got.Ranges)
		}
	})
}
