package signalwire

import (
	"reflect"
	"strings"
	"testing"
)

// TestValidateUnifiedDiff pins ValidateUnifiedDiff's own rule set (ADR
// 0057): a parseable unified diff is accepted, and a non-diff, a diff whose
// hunk line counts disagree with its header, and a binary hunk are each
// rejected with a reason naming what is wrong.
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
			// hunkHeaderRE admits only digits here, so this can only fail
			// Atoi on overflow -- pinning that it is rejected as a malformed
			// header, not silently clamped and then rejected (or accepted)
			// as a count disagreement (review finding on issue #4072).
			"hunk header count overflows",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,99999999999999999999 +1,1 @@\n-a\n+b\n",
			true, "malformed",
		},
		{
			// git apply accepts a fully empty line inside a hunk body as an
			// empty context line (whitespace-stripping transports produce
			// this); it must count against both old and new, not reject as
			// a count disagreement (review finding on issue #4072).
			"empty line inside hunk body is a context line",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,3 @@\n a\n\n-b\n+c\n",
			false, "",
		},
		{
			// The patch's own trailing newline is not a line: a hunk one
			// context line short must not borrow the split artifact after
			// it as an empty context line (git apply rejects this as a
			// corrupt patch).
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
			// git apply also accepts the legacy "Files a/X and b/X differ"
			// form for a binary file (still produced by e.g. `diff`
			// itself) -- rejected exactly like the modern "Binary files
			// ... differ" spelling (review finding on issue #4075).
			"legacy Files ... differ binary marker",
			"diff --git a/foo.bin b/foo.bin\nindex 1234567..89abcde 100644\nFiles a/foo.bin and b/foo.bin differ\n",
			true, "binary",
		},
		{
			// git apply also accepts the legacy "rename old "/"rename new "
			// extended headers alongside "rename from "/"rename to " -- a
			// hunk-bearing companion file elsewhere in the patch clears the
			// "no hunk" check, so this pins that the legacy headers
			// themselves are recognised, not merely tolerated as
			// unrecognised junk (review finding on issue #4075).
			"legacy rename old/new headers recognised",
			"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename old old.go\nrename new new.go\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			false, "",
		},
		{
			// An unrecognised line in a "diff --git" section's own
			// extended-header zone (before its own "--- "/"+++ " pair, if
			// any) must fail closed rather than silently drop the section
			// -- otherwise a smuggled path could ride past every
			// downstream gate under an unknown header (review finding on
			// issue #4075).
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
			// A quoted path (git's core.quotePath escaping a non-ASCII
			// name) is a path-resolution problem, not a structural one:
			// the socket must not reject the whole issue intent over it
			// (review finding on issue #4075) -- ParseUnifiedDiff still
			// fails closed on it below.
			"quoted path is structurally fine",
			"--- \"a/foo bar.go\"\n+++ \"b/foo bar.go\"\n@@ -1 +1 @@\n-a\n+b\n",
			false, "",
		},
		{
			// A --no-prefix (or diff.noprefix) diff leaves both the
			// "diff --git" line and the "--- "/"+++ " pair without their
			// usual a/ b/ prefixes -- also a path-resolution problem the
			// socket must let through structurally (review finding on
			// issue #4075).
			"no-prefix diff is structurally fine",
			"diff --git CLAUDE.md CLAUDE.md\nindex 1234567..89abcde 100644\n--- CLAUDE.md\n+++ CLAUDE.md\n@@ -1 +1 @@\n-old\n+new\n",
			false, "",
		},
		{
			// A path error in one file must not mask a real structural
			// fault in another: ValidateUnifiedDiff still rejects the
			// whole patch once the second file's hunk counts disagree.
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

// TestParseUnifiedDiff pins ParseUnifiedDiff's per-file bookkeeping (issue
// #4075): the path and change kind of every file entry a patch touches,
// and its added/removed line counts, which the butler's patch gate uses to
// enforce file/line caps and a path allow-list.
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
			// A pure rename carries no hunk of its own, so it needs a real
			// hunk-bearing file elsewhere in the same patch to clear the
			// "no hunk" check -- same reasoning as the mode-change gotcha
			// case below.
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
			// The gotcha this slice exists for: a hunkless "diff --git"
			// section carrying only a mode change is a section `git apply`
			// would still apply, so it must show up as its own entry
			// alongside the real, hunk-bearing file in the same patch.
			"mode-only hunkless section alongside a modified file",
			"diff --git a/foo.sh b/foo.sh\nold mode 100644\nnew mode 100755\ndiff --git a/bar.go b/bar.go\nindex 1234567..89abcde 100644\n--- a/bar.go\n+++ b/bar.go\n@@ -1 +1 @@\n-a\n+b\n",
			[]DiffFile{
				{Path: "foo.sh", Change: "mode change", Added: 0, Removed: 0},
				{Path: "bar.go", Change: "", Added: 1, Removed: 1},
			},
			false, "",
		},
		{
			// A second "--- "/"+++ " pair under one "diff --git" line is a
			// smuggling trick: `git apply` treats it as its own traditional
			// patch and applies both files, so the parser must surface both
			// as separate entries rather than letting the second pair
			// silently replace the first.
			"second header pair under one diff --git section is its own entry",
			"diff --git a/CLAUDE.md b/CLAUDE.md\nindex 1234567..89abcde 100644\n--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1 +1 @@\n-a\n+b\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1,2 +1,2 @@\n-c\n-d\n+e\n+f\n",
			[]DiffFile{
				{Path: "CLAUDE.md", Change: "", Added: 1, Removed: 1},
				{Path: "docs/x.md", Change: "", Added: 2, Removed: 2},
			},
			false, "",
		},
		{
			// Once a "diff --git" section's own "--- "/"+++ " pair and hunks
			// are accounted for, a trailing unrecognised line stays lenient
			// (silently skipped) rather than fail closed -- the fail-closed
			// rule only guards the section's extended-header zone, before
			// that pair is seen.
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
			// Attack from review finding on issue #4075: the legacy "Files
			// ... differ" binary marker followed by an ordinary hunk in its
			// own section. Before the fix, the binary section's unrecognised
			// line dropped it entirely (rank 0) and ParseUnifiedDiff
			// returned only the ordinary docs/x.md entry -- every patch gate
			// downstream then cleared a diff that actually smuggled a
			// binary CLAUDE.md change.
			"legacy Files ... differ binary marker does not slip past a companion hunk",
			"diff --git a/CLAUDE.md b/CLAUDE.md\nindex 1234567..89abcde 100644\nFiles a/CLAUDE.md and b/CLAUDE.md differ\ndiff --git a/docs/x.md b/docs/x.md\nindex 1234567..89abcde 100644\n--- a/docs/x.md\n+++ b/docs/x.md\n@@ -1 +1 @@\n-c\n+d\n",
			nil, true, "binary",
		},
		{
			// Attack from review finding on issue #4075: the legacy "rename
			// old "/"rename new " headers were unrecognised, so the section
			// was dropped and only docs/x.md came back -- while git apply
			// renamed CLAUDE.md away.
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
			// The socket's structural check accepts this (see
			// TestValidateUnifiedDiff), but ParseUnifiedDiff still fails
			// closed on it: no path could be resolved from either the
			// "diff --git" line or the "--- "/"+++ " pair.
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
