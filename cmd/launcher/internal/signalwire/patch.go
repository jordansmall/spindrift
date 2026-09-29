package signalwire

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// hunkHeaderRE matches a unified-diff hunk header: "@@ -a[,b] +c[,d] @@",
// with an optional trailing context (e.g. a function name) this parser
// ignores.
var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// extendedHeaderPrefixes is every `diff --git` extended-header line this
// parser skips over on its way to a `--- `/`+++ ` file header pair. Patch
// (ADR 0057) only ever carries modification hunks -- the host decides
// whether to apply it, never the Box -- so these lines are accepted as
// harmless preamble rather than parsed for their own content.
var extendedHeaderPrefixes = []string{
	"diff --git ",
	"index ",
	"old mode ",
	"new mode ",
	"deleted file mode ",
	"new file mode ",
	"copy from ",
	"copy to ",
	"rename from ",
	"rename to ",
	"similarity index ",
	"dissimilarity index ",
}

// ValidateUnifiedDiff reports whether patch parses as a unified diff:
// zero or more `diff --git`/extended header lines, then at least one
// `--- `/`+++ ` file header pair, each followed by at least one `@@ ... @@`
// hunk whose body lines' ' '/'+'/'-'/'\' counts match its header. A binary
// hunk (`Binary files ... differ` or `GIT binary patch`) is rejected outright:
// Patch only ever carries a modification, never a binary blob. Any other
// unrecognised line -- prose preamble, junk between files, extra lines past
// a hunk's counts -- is silently skipped rather than rejected, matching
// `git apply`'s own leniency. The error returned is a signalwire.Reject
// Reason verbatim -- one lower-case line naming no path or token, only what
// is wrong.
func ValidateUnifiedDiff(patch string) error {
	// The patch's own trailing newline terminates its last line; it is not
	// an empty line of its own, so Split's trailing "" artifact is dropped
	// before a hunk could count it as context.
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	filesFound := 0

	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case line == "":
			i++
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			return errors.New("patch carries binary content")
		case line == "GIT binary patch":
			return errors.New("patch carries binary content")
		case isExtendedHeaderLine(line):
			i++
		case strings.HasPrefix(line, "--- "):
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return errors.New("patch is not a unified diff: --- file header has no matching +++ header")
			}
			i += 2
			hunks := 0
			for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
				next, err := parseHunk(lines, i)
				if err != nil {
					return err
				}
				i = next
				hunks++
			}
			if hunks == 0 {
				return errors.New("patch is not a unified diff: no hunk")
			}
			filesFound++
		default:
			i++
		}
	}

	if filesFound == 0 {
		return errors.New("patch is not a unified diff: no hunk")
	}
	return nil
}

func isExtendedHeaderLine(line string) bool {
	for _, p := range extendedHeaderPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// parseHunk parses the hunk starting at lines[i] (a "@@ " header) and
// returns the index of the line after its body. The body is consumed by the
// header's own counts, not by line shape: a removed line reading "-- x" and
// the next file's "--- " header both start with '-', so only the counts say
// where a hunk ends.
func parseHunk(lines []string, i int) (int, error) {
	m := hunkHeaderRE.FindStringSubmatch(lines[i])
	if m == nil {
		return 0, errors.New("patch is not a unified diff: malformed @@ hunk header")
	}
	oldLeft, oldErr := countOrDefault(m[2])
	newLeft, newErr := countOrDefault(m[4])
	if oldErr != nil || newErr != nil {
		return 0, errors.New("patch is not a unified diff: malformed @@ hunk header")
	}

	for i++; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, `\`) {
			// A "\ No newline at end of file" marker: not counted either way.
			continue
		}
		if oldLeft == 0 && newLeft == 0 {
			return i, nil
		}
		if line == "" {
			// A fully empty line inside a hunk body is an empty context
			// line (git apply accepts this too; a whitespace-stripping
			// transport can produce one), not a malformed line. The patch's
			// trailing newline never reaches here as one: the caller drops
			// Split's artifact.
			oldLeft--
			newLeft--
			if oldLeft < 0 || newLeft < 0 {
				return 0, errHunkCounts
			}
			continue
		}
		switch line[0] {
		case ' ':
			oldLeft--
			newLeft--
		case '+':
			newLeft--
		case '-':
			oldLeft--
		default:
			return 0, errHunkCounts
		}
		if oldLeft < 0 || newLeft < 0 {
			return 0, errHunkCounts
		}
	}
	if oldLeft != 0 || newLeft != 0 {
		return 0, errHunkCounts
	}
	return i, nil
}

var errHunkCounts = errors.New("patch hunk line counts disagree with its @@ header")

// countOrDefault parses s (a hunk header's count field) as the default 1
// when absent, or its Atoi value. hunkHeaderRE admits only digits here, so
// Atoi can fail only on overflow -- propagated rather than discarded, since
// a clamped MaxInt would otherwise masquerade as a legitimate count and get
// rejected downstream as a count disagreement instead of a malformed header.
func countOrDefault(s string) (int, error) {
	if s == "" {
		return 1, nil
	}
	return strconv.Atoi(s)
}
