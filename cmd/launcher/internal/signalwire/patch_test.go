package signalwire

import (
	"reflect"
	"strings"
	"testing"
)

// TestValidateUnifiedDiff pins ValidateUnifiedDiff's rule set (ADR 0057).
func TestValidateUnifiedDiff(t *testing.T) {
	const validDiff = `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,2 @@
 package foo
-func Old() {}
+func New() {}
`
	const validDiffWithGitHeader = `diff --git a/foo.go b/foo.go
index 1234567..89abcde 100644
--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,2 @@
-old line
+new line
 unchanged
`
	const validDiffTwoHunks = `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,2 @@
-a
+b
 c
@@ -10,1 +10,1 @@
-d
+e
`

	cases := []struct {
		name       string
		patch      string
		wantErr    bool
		wantSubstr string // substring the error must contain, when wantErr
	}{
		{"valid single-hunk diff", validDiff, false, ""},
		{"valid diff with git extended header", validDiffWithGitHeader, false, ""},
		{"valid diff with two hunks", validDiffTwoHunks, false, ""},
		{
			// The second file's "--- " header also starts with '-': only the
			// first hunk's counts say it is not a removed line.
			"valid two-file diff with no git header",
			"--- a/x.md\n+++ b/x.md\n@@ -1 +1 @@\n-a\n+b\n--- a/y.md\n+++ b/y.md\n@@ -1 +1 @@\n-c\n+d\n",
			false, "",
		},
		{"valid diff removing a line that reads -- x", "--- a/x.md\n+++ b/x.md\n@@ -1,2 +1 @@\n--- x\n a\n", false, ""},
		{"non-diff plain text", "just some ordinary text\nwith a second line\n", true, "no hunk"},
		{"empty string", "", true, "no hunk"},
		{
			"hunk counts disagree with header",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,3 @@\n package foo\n-func Old() {}\n",
			true, "disagree",
		},
		{
			// Atoi overflow must read as a malformed header, not be clamped
			// into a count disagreement (issue #4072).
			"hunk header count overflows",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,99999999999999999999 +1,1 @@\n-a\n+b\n",
			true, "malformed",
		},
		{
			// git apply takes an empty line in a hunk body as empty context
			// (whitespace-stripping transports produce it); it counts against
			// both sides (issue #4072).
			"empty line inside hunk body is a context line",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,3 @@\n a\n\n-b\n+c\n",
			false, "",
		},
		{
			// A hunk one context line short must not borrow the trailing
			// newline's split artifact as empty context (git apply calls it
			// corrupt).
			"hunk truncated by one trailing context line",
			"--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n a\n-b\n+c\n",
			true, "disagree",
		},
		{
			"binary files differ",
			"diff --git a/foo.bin b/foo.bin\nindex 1234567..89abcde 100644\nBinary files a/foo.bin and b/foo.bin differ\n",
			true, "binary",
		},
		{
			"git binary patch",
			"diff --git a/foo.bin b/foo.bin\nGIT binary patch\nliteral 10\nabcdefghij\n",
			true, "binary",
		},
		{
			// The legacy "Files ... differ" binary marker, which `diff` still
			// emits and git apply accepts, is rejected too (issue #4075).
			"legacy Files ... differ binary marker",
			"diff --git a/foo.bin b/foo.bin\nindex 1234567..89abcde 100644\nFiles a/foo.bin and b/foo.bin differ\n",
			true, "binary",
		},
		{
			// The legacy "rename old "/"rename new " headers must be
			// recognised, not tolerated as junk; the companion hunk clears
			// "no hunk" (issue #4075).
			"legacy rename old/new headers recognised",
			"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename old old.go\nrename new new.go\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			false, "",
		},
		{
			// An unknown line in the extended-header zone fails closed, or a
			// smuggled path could ride past every gate under it (issue #4075).
			"unrecognised extended header line fails closed",
			"diff --git a/CLAUDE.md b/docs/evil.md\nbogus header line\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			true, "unrecognised",
		},
		{
			"dash header with no plus header",
			"--- a/foo.go\nnot a plus header\n@@ -1,1 +1,1 @@\n-a\n+b\n",
			true, "unified diff",
		},
		{
			// A quoted path (core.quotePath) is a path problem, not a
			// structural one: the socket must not reject the intent over it
			// (issue #4075). ParseUnifiedDiff still fails closed on it below.
			"quoted path is structurally fine",
			"--- \"a/foo bar.go\"\n+++ \"b/foo bar.go\"\n@@ -1 +1 @@\n-a\n+b\n",
			false, "",
		},
		{
			// A --no-prefix diff is likewise a path problem the socket lets
			// through (issue #4075).
			"no-prefix diff is structurally fine",
			"diff --git CLAUDE.md CLAUDE.md\nindex 1234567..89abcde 100644\n--- CLAUDE.md\n+++ CLAUDE.md\n@@ -1 +1 @@\n-old\n+new\n",
			false, "",
		},
		{
			// A path error in one file must not mask a structural fault in
			// another.
			"structural error after a path error still surfaces",
			"--- \"a/foo bar.go\"\n+++ \"b/foo bar.go\"\n@@ -1 +1 @@\n-a\n+b\n--- a/second.go\n+++ b/second.go\n@@ -1,3 +1,3 @@\n package foo\n-func Old() {}\n",
			true, "disagree",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUnifiedDiff(tc.patch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateUnifiedDiff(%q) = nil, want an error containing %q", tc.patch, tc.wantSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Fatalf("ValidateUnifiedDiff(%q) error = %q, want it to contain %q", tc.patch, err.Error(), tc.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateUnifiedDiff(%q) = %v, want accept", tc.patch, err)
			}
		})
	}
}

// TestParseUnifiedDiff pins the per-file path, change kind and line counts
// the butler's patch gate enforces its caps and allow-list on (issue #4075).
func TestParseUnifiedDiff(t *testing.T) {
	cases := []struct {
		name       string
		patch      string
		want       []DiffFile
		wantErr    bool
		wantSubstr string
	}{
		{
			"plain modification",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,2 +1,2 @@\n package foo\n-func Old() {}\n+func New() {}\n",
			[]DiffFile{{Path: "foo.go", Change: "", Added: 1, Removed: 1}},
			false, "",
		},
		{
			"two files",
			"--- a/x.md\n+++ b/x.md\n@@ -1 +1 @@\n-a\n+b\n--- a/y.md\n+++ b/y.md\n@@ -1 +1 @@\n-c\n+d\n",
			[]DiffFile{
				{Path: "x.md", Change: "", Added: 1, Removed: 1},
				{Path: "y.md", Change: "", Added: 1, Removed: 1},
			},
			false, "",
		},
		{
			"plain --- /dev/null add",
			"--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+line1\n+line2\n",
			[]DiffFile{{Path: "new.go", Change: "added", Added: 2, Removed: 0}},
			false, "",
		},
		{
			"plain +++ /dev/null delete",
			"--- a/old.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-line1\n-line2\n",
			[]DiffFile{{Path: "old.go", Change: "deleted", Added: 0, Removed: 2}},
			false, "",
		},
		{
			// A /dev/null header decides create/delete outright; the
			// hunk-shape guess never overrides it (issue #4116).
			"plain --- /dev/null add with a delete-shaped hunk",
			"--- /dev/null\n+++ b/x.go\n@@ -1 +0,0 @@\n-line\n",
			[]DiffFile{{Path: "x.go", Change: "added", Added: 0, Removed: 1}},
			false, "",
		},
		{
			"git new file mode",
			"diff --git a/new.go b/new.go\nnew file mode 100644\nindex 0000000..1234567\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+line1\n+line2\n",
			[]DiffFile{{Path: "new.go", Change: "added", Added: 2, Removed: 0}},
			false, "",
		},
		{
			"git deleted file mode",
			"diff --git a/old.go b/old.go\ndeleted file mode 100644\nindex 1234567..0000000\n--- a/old.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-line1\n-line2\n",
			[]DiffFile{{Path: "old.go", Change: "deleted", Added: 0, Removed: 2}},
			false, "",
		},
		{
			// The companion hunk-bearing file clears the "no hunk" check.
			"rename via git headers, hunkless",
			"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{
				{Path: "new.go", Change: "renamed", Added: 0, Removed: 0},
				{Path: "bar.go", Change: "", Added: 1, Removed: 1},
			},
			false, "",
		},
		{
			"rename via ---/+++ name mismatch",
			"--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{{Path: "new.go", Change: "renamed", Added: 1, Removed: 1}},
			false, "",
		},
		{
			// Differing ---/+++ paths outrank the hunk-shape delete guess
			// (issue #4116).
			"rename outranks a delete-shaped hunk",
			"--- a/old.txt\n+++ b/new.txt\n@@ -1 +0,0 @@\n-x\n",
			[]DiffFile{{Path: "new.txt", Change: "renamed", Added: 0, Removed: 1}},
			false, "",
		},
		{
			"copy via git headers, hunkless",
			"diff --git a/orig.go b/copy.go\nsimilarity index 100%\ncopy from orig.go\ncopy to copy.go\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{
				{Path: "copy.go", Change: "copied", Added: 0, Removed: 0},
				{Path: "bar.go", Change: "", Added: 1, Removed: 1},
			},
			false, "",
		},
		{
			"mode change with hunks",
			"diff --git a/foo.sh b/foo.sh\nold mode 100644\nnew mode 100755\n--- a/foo.sh\n+++ b/foo.sh\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{{Path: "foo.sh", Change: "mode change", Added: 1, Removed: 1}},
			false, "",
		},
		{
			// A hunkless mode-change section is still applied by `git apply`,
			// so it must be its own entry.
			"mode-only hunkless section alongside a modified file",
			"diff --git a/foo.sh b/foo.sh\nold mode 100644\nnew mode 100755\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{
				{Path: "foo.sh", Change: "mode change", Added: 0, Removed: 0},
				{Path: "bar.go", Change: "", Added: 1, Removed: 1},
			},
			false, "",
		},
		{
			// Smuggling: `git apply` applies a second "--- "/"+++ " pair under
			// one "diff --git" as its own patch, so both must surface.
			"second header pair under one diff --git section is its own entry",
			"diff --git a/CLAUDE.md b/CLAUDE.md\nindex 1234567..89abcde 100644\n--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1 +1 @@\n-a\n+b\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1,2 +1,2 @@\n-c\n-d\n+e\n+f\n",
			[]DiffFile{
				{Path: "CLAUDE.md", Change: "", Added: 1, Removed: 1},
				{Path: "docs/x.md", Change: "", Added: 2, Removed: 2},
			},
			false, "",
		},
		{
			// Fail-closed only guards the extended-header zone; junk after
			// the section's hunks is skipped.
			"trailing junk after a section's header pair and hunk stays lenient",
			"diff --git a/foo.go b/foo.go\nindex 1234567..89abcde 100644\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n+b\nsome trailing junk line\n",
			[]DiffFile{{Path: "foo.go", Change: "", Added: 1, Removed: 1}},
			false, "",
		},
		{
			"binary content rejected",
			"diff --git a/foo.bin b/foo.bin\nindex 1234567..89abcde 100644\nBinary files a/foo.bin and b/foo.bin differ\n",
			nil, true, "binary",
		},
		{
			// Attack (issue #4075): before the fix, an unrecognised legacy binary
			// marker dropped its section, so gates cleared a diff smuggling a binary
			// CLAUDE.md change behind docs/x.md.
			"legacy Files ... differ binary marker does not slip past a companion hunk",
			"diff --git a/CLAUDE.md b/CLAUDE.md\nindex 1234567..89abcde 100644\nFiles a/CLAUDE.md and b/CLAUDE.md differ\ndiff --git a/docs/x.md b/docs/x.md\nindex 1234567..89abcde 100644\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1 +1 @@\n-c\n+d\n",
			nil, true, "binary",
		},
		{
			// Attack (issue #4075): before the fix, unrecognised legacy rename
			// headers dropped the section while git apply renamed CLAUDE.md away.
			"legacy rename old/new headers surface as a renamed entry",
			"diff --git a/CLAUDE.md b/docs/evil.md\nrename old CLAUDE.md\nrename new docs/evil.md\ndiff --git a/docs/x.md b/docs/x.md\nindex 1234567..89abcde 100644\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1 +1 @@\n-c\n+d\n",
			[]DiffFile{{Path: "docs/evil.md", Change: "renamed"}, {Path: "docs/x.md", Added: 1, Removed: 1}},
			false, "",
		},
		{
			"quoted path rejected",
			"--- \"a/foo bar.go\"\n+++ \"b/foo bar.go\"\n@@ -1 +1 @@\n-a\n+b\n",
			nil, true, "quoted",
		},
		{
			// Structurally valid (TestValidateUnifiedDiff), but no path
			// resolves from either header.
			"no-prefix diff path unresolvable",
			"diff --git CLAUDE.md CLAUDE.md\nindex 1234567..89abcde 100644\n--- CLAUDE.md\n+++ CLAUDE.md\n@@ -1 +1 @@\n-old\n+new\n",
			nil, true, "cannot be resolved",
		},
		{
			"counts summed across multiple hunks",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n+b\n@@ -10 +10 @@\n-d\n+e\n",
			[]DiffFile{{Path: "foo.go", Change: "", Added: 2, Removed: 2}},
			false, "",
		},
		{
			"no newline at end of file marker not counted",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n",
			[]DiffFile{{Path: "foo.go", Change: "", Added: 1, Removed: 1}},
			false, "",
		},
		{
			"all-hunkless git sections still rejected as no hunk",
			"diff --git a/foo.sh b/foo.sh\nold mode 100644\nnew mode 100755\n",
			nil, true, "no hunk",
		},
		{
			// git apply --summary reports " delete f.txt" here (issue #4116).
			"plain single-hunk delete inferred without /dev/null header",
			"--- a/f.txt\n+++ b/f.txt\n@@ -1 +0,0 @@\n-a\n",
			[]DiffFile{{Path: "f.txt", Change: "deleted", Added: 0, Removed: 1}},
			false, "",
		},
		{
			"plain single-hunk create inferred without /dev/null header",
			"--- a/g.txt\n+++ b/g.txt\n@@ -0,0 +1 @@\n+b\n",
			[]DiffFile{{Path: "g.txt", Change: "added", Added: 1, Removed: 0}},
			false, "",
		},
		{
			// git reports no summary line for more than one hunk.
			"plain multi-hunk all-removed stays a modification",
			"--- a/h.txt\n+++ b/h.txt\n@@ -1 +0,0 @@\n-a\n@@ -3 +2,0 @@\n-c\n",
			[]DiffFile{{Path: "h.txt", Change: "", Added: 0, Removed: 2}},
			false, "",
		},
		{
			// git pins create/delete to "no" for a git-format section.
			"diff --git section with same hunk shape and no extended header stays a modification",
			"diff --git a/j.txt b/j.txt\n--- a/j.txt\n+++ b/j.txt\n@@ -1 +0,0 @@\n-a\n",
			[]DiffFile{{Path: "j.txt", Change: "", Added: 0, Removed: 1}},
			false, "",
		},
		{
			// A nested second pair is a plain section to git too.
			"nested second header pair under one diff --git section infers its own delete",
			"diff --git a/CLAUDE.md b/CLAUDE.md\nindex 1234567..89abcde 100644\n--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1 +1 @@\n-a\n+b\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1 +0,0 @@\n-c\n",
			[]DiffFile{
				{Path: "CLAUDE.md", Change: "", Added: 1, Removed: 1},
				{Path: "docs/x.md", Change: "deleted", Added: 0, Removed: 1},
			},
			false, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUnifiedDiff(tc.patch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseUnifiedDiff(%q) = %#v, nil, want an error containing %q", tc.patch, got, tc.wantSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Fatalf("ParseUnifiedDiff(%q) error = %q, want it to contain %q", tc.patch, err.Error(), tc.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUnifiedDiff(%q) = %v, want accept", tc.patch, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseUnifiedDiff(%q) = %#v, want %#v", tc.patch, got, tc.want)
			}
		})
	}
}
