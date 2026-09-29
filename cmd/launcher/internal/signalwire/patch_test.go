package signalwire

import (
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
			"dash header with no plus header",
			"--- a/foo.go\nnot a plus header\n@@ -1,1 +1,1 @@\n-a\n+b\n",
			true, "unified diff",
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
