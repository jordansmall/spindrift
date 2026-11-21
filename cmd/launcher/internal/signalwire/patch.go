package signalwire

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// diffGitHeaderRE needs the default a/ b/ prefixes; a --no-prefix diff fails
// to match and becomes a path fault, not a structural one. When it matches, it
// is the path source for a git section with no "--- "/"+++ " pair (a pure rename or mode change).
var diffGitHeaderRE = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)

const devNull = "/dev/null"

// isBinaryContentLine reports whether line is one of the binary-file markers
// `git apply` accepts in place of a hunk ("Binary files ... differ", "GIT
// binary patch", or the legacy "Files ... differ" that `diff` emits). Patch
// never carries a binary blob, so any of them aborts the parse rather than
// being skipped as unrecognised content.
func isBinaryContentLine(line string) bool {
	if strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ") {
		return true
	}
	if strings.HasPrefix(line, "Files ") && strings.HasSuffix(line, " differ") {
		return true
	}
	return line == "GIT binary patch"
}

// Ranks for setChange: when several header lines (or resolvePathAndChange's
// +/- path comparison) name a change kind for one entry, the most disruptive
// wins.
const (
	rankModeChange     = 1
	rankAddedOrDeleted = 2
	rankRenamed        = 3
	rankCopied         = 4
)

func setChange(change *string, rank *int, c string, r int) {
	if r > *rank {
		*change, *rank = c, r
	}
}

// DiffFile is one file entry parsed out of a unified diff by
// ParseUnifiedDiff. A hunkless git section (pure rename, mode change) is
// still an entry, with zero counts, because git apply still applies it.
type DiffFile struct {
	Path    string
	Change  string // "", "added", "deleted", "renamed", "copied", or "mode change"
	Added   int
	Removed int
}

// ParseUnifiedDiff parses patch into its file entries under ValidateUnifiedDiff's
// structural rules, and also fails on the first path it cannot resolve (a
// quoted path, or nothing to strip at -p1), since its caller keys a gate on
// real paths.
func ParseUnifiedDiff(patch string) ([]DiffFile, error) {
	files, structErr, pathErr := parseUnifiedDiff(patch)
	if structErr != nil {
		return nil, structErr
	}
	if pathErr != nil {
		return nil, pathErr
	}
	return files, nil
}

// ValidateUnifiedDiff reports whether patch is structurally a unified diff:
// each `--- `/`+++ ` pair is followed by at least one hunk whose body counts
// match its `@@` header. A hunkless "diff --git" section of extended headers
// only (mode, rename, copy) passes, as `git apply` takes it, provided some
// other section has a hunk. Binary content is rejected: Patch never carries a
// blob. An unrecognised line inside a git section's extended-header zone fails
// closed, so an unknown header cannot drop the section; elsewhere (preamble,
// junk between files) it is skipped, matching `git apply`'s leniency. Paths
// are not resolved here: a quoted path (default core.quotePath) or a
// --no-prefix diff passes, and ParseUnifiedDiff's caller fails closed on it.
// The error is a signalwire.Reject Reason verbatim: one lower-case line naming
// no path or token.
func ValidateUnifiedDiff(patch string) error {
	_, structErr, _ := parseUnifiedDiff(patch)
	return structErr
}

// parseUnifiedDiff backs ParseUnifiedDiff and ValidateUnifiedDiff, splitting
// its failures so each picks the one it needs: structErr aborts the parse; pathErr is the first unresolvable path, recorded but not
// fatal, since the diff is still well-formed.
func parseUnifiedDiff(patch string) (files []DiffFile, structErr, pathErr error) {
	// Drop Split's trailing "" so a hunk cannot count it as an empty context line.
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	sawRealHunk := false

	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case line == "":
			i++
		case isBinaryContentLine(line):
			return nil, errors.New("patch carries binary content"), nil
		case strings.HasPrefix(line, "diff --git "):
			file, ok, hadHunk, next, err, perr := parseGitSection(lines, i)
			if err != nil {
				return nil, err, nil
			}
			if perr != nil && pathErr == nil {
				pathErr = perr
			}
			i = next
			if ok {
				files = append(files, file)
			}
			if hadHunk {
				sawRealHunk = true
			}
		case strings.HasPrefix(line, "--- "):
			file, next, err, perr := parsePlainFileSection(lines, i)
			if err != nil {
				return nil, err, nil
			}
			if perr != nil && pathErr == nil {
				pathErr = perr
			}
			i = next
			files = append(files, file)
			sawRealHunk = true
		default:
			i++
		}
	}

	if !sawRealHunk {
		return nil, errors.New("patch is not a unified diff: no hunk"), nil
	}
	return files, nil, pathErr
}

// parseGitSection parses one "diff --git" section starting at lines[i]. ok is
// false when it carries no applicable content (e.g. a bare "index" line).
// A hunkless section with extended headers (pure rename, mode change) is
// recorded as its own DiffFile so a gate keyed on hunks cannot miss it, but
// hadHunk stays false, so a patch of only such sections still fails "no hunk".
func parseGitSection(lines []string, i int) (file DiffFile, ok, hadHunk bool, next int, err, pathErr error) {
	gitOldPath, gitNewPath := resolveGitHeaderPaths(lines[i], &pathErr)
	i++

	change := ""
	rank := 0

	var dashPath, plusPath string
	haveHeaderPair := false
	added, removed := 0, 0
	sawHunk := false

loop:
	for i < len(lines) {
		line := lines[i]
		switch {
		case line == "":
			i++
		case strings.HasPrefix(line, "diff --git "):
			break loop
		case isBinaryContentLine(line):
			return DiffFile{}, false, false, 0, errors.New("patch carries binary content"), pathErr
		case strings.HasPrefix(line, "new file mode "):
			setChange(&change, &rank, "added", rankAddedOrDeleted)
			i++
		case strings.HasPrefix(line, "deleted file mode "):
			setChange(&change, &rank, "deleted", rankAddedOrDeleted)
			i++
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "rename old "), strings.HasPrefix(line, "rename new "):
			// "rename old "/"rename new " is the legacy form `git apply` still accepts.
			setChange(&change, &rank, "renamed", rankRenamed)
			i++
		case strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "):
			setChange(&change, &rank, "copied", rankCopied)
			i++
		case strings.HasPrefix(line, "similarity index "):
			setChange(&change, &rank, "renamed", rankRenamed)
			i++
		case strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "):
			setChange(&change, &rank, "mode change", rankModeChange)
			i++
		case strings.HasPrefix(line, "index "), strings.HasPrefix(line, "dissimilarity index "):
			i++
		case strings.HasPrefix(line, "--- "):
			// A second "--- "/"+++ " pair is a separate patch to `git apply`:
			// end the section so the outer loop parses it as its own entry.
			if haveHeaderPair {
				break loop
			}
			var a, r, hunks, next2 int
			var herr error
			dashPath, plusPath, a, r, hunks, next2, herr = parseHeaderPairAndHunks(lines, i, &pathErr)
			if herr != nil {
				return DiffFile{}, false, false, 0, herr, pathErr
			}
			haveHeaderPair = true
			// Runs at most once per section (see above), so assign, don't accumulate.
			added, removed, sawHunk = a, r, hunks > 0
			i = next2
		default:
			if !haveHeaderPair {
				// Fail closed in the extended-header zone: breaking here would
				// silently drop the rest of the section.
				return DiffFile{}, false, false, 0, errors.New("patch is not a unified diff: unrecognised line in diff --git header"), pathErr
			}
			break loop
		}
	}

	switch {
	case haveHeaderPair:
		if !sawHunk {
			return DiffFile{}, false, false, 0, errors.New("patch is not a unified diff: no hunk"), pathErr
		}
		path, finalChange := resolvePathAndChange(change, rank, dashPath, plusPath)
		return DiffFile{Path: path, Change: finalChange, Added: added, Removed: removed}, true, true, i, nil, pathErr
	case rank > 0:
		path := gitNewPath
		if change == "deleted" {
			path = gitOldPath
		}
		return DiffFile{Path: path, Change: change}, true, false, i, nil, pathErr
	default:
		return DiffFile{}, false, false, i, nil, pathErr
	}
}

// parsePlainFileSection parses one "--- "/"+++ " file section with no
// preceding "diff --git" line.
func parsePlainFileSection(lines []string, i int) (file DiffFile, next int, err, pathErr error) {
	dashPath, plusPath, added, removed, hunks, next, err := parseHeaderPairAndHunks(lines, i, &pathErr)
	if err != nil {
		return DiffFile{}, 0, err, pathErr
	}
	if hunks == 0 {
		return DiffFile{}, 0, errors.New("patch is not a unified diff: no hunk"), pathErr
	}
	// git apply's parse_single_patch infers a create/delete for a plain
	// section from one hunk with an empty side; context lines count toward
	// a side, so the header's counts decide, not added/removed. Like git,
	// only when no /dev/null header already decides it. A degenerate
	// -0,0 +0,0 hunk lands on "deleted"; git refuses it as corrupt anyway.
	change, rank := "", 0
	if hunks == 1 && dashPath != devNull && plusPath != devNull {
		// parseHunk already validated this header, so m is non-nil and the
		// counts parse.
		m := hunkHeaderRE.FindStringSubmatch(lines[i+2])
		oldLen, _ := countOrDefault(m[2])
		newLen, _ := countOrDefault(m[4])
		switch {
		case newLen == 0:
			setChange(&change, &rank, "deleted", rankAddedOrDeleted)
		case oldLen == 0:
			setChange(&change, &rank, "added", rankAddedOrDeleted)
		}
	}
	path, finalChange := resolvePathAndChange(change, rank, dashPath, plusPath)
	return DiffFile{Path: path, Change: finalChange, Added: added, Removed: removed}, next, nil, pathErr
}

// parseHeaderPairAndHunks parses one "--- "/"+++ " header pair and the hunks
// that follow it. An unresolvable path folds into *pathErr (first only) and
// parsing continues.
func parseHeaderPairAndHunks(lines []string, i int, pathErr *error) (dashPath, plusPath string, added, removed, hunks, next int, err error) {
	if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
		return "", "", 0, 0, 0, i, errors.New("patch is not a unified diff: --- file header has no matching +++ header")
	}
	dashPath = resolveDiffPathHeader(strings.TrimPrefix(lines[i], "--- "), pathErr)
	plusPath = resolveDiffPathHeader(strings.TrimPrefix(lines[i+1], "+++ "), pathErr)
	i += 2

	for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
		var a, r int
		i, a, r, err = parseHunk(lines, i)
		if err != nil {
			return "", "", 0, 0, 0, i, err
		}
		added += a
		removed += r
		hunks++
	}
	return dashPath, plusPath, added, removed, hunks, i, nil
}

// resolvePathAndChange folds the +/- header paths into change and picks the
// path to key on: the new side, or the old side for a deletion.
func resolvePathAndChange(change string, rank int, dashPath, plusPath string) (path, finalChange string) {
	switch {
	case dashPath == devNull:
		setChange(&change, &rank, "added", rankAddedOrDeleted)
	case plusPath == devNull:
		setChange(&change, &rank, "deleted", rankAddedOrDeleted)
	case dashPath != plusPath:
		setChange(&change, &rank, "renamed", rankRenamed)
	}
	path = plusPath
	if plusPath == devNull {
		path = dashPath
	}
	return path, change
}

// resolveGitHeaderPaths resolves a "diff --git" line's two paths, folding a
// failure into *pathErr (first only) rather than the structural parse.
func resolveGitHeaderPaths(line string, pathErr *error) (oldPath, newPath string) {
	oldPath, newPath, err := parseGitHeaderPaths(line)
	if err != nil {
		if *pathErr == nil {
			*pathErr = err
		}
		return "", ""
	}
	return oldPath, newPath
}

func resolveDiffPathHeader(raw string, pathErr *error) string {
	path, err := parseDiffPathHeader(raw)
	if err != nil {
		if *pathErr == nil {
			*pathErr = err
		}
		return ""
	}
	return path
}

// parseGitHeaderPaths splits a "diff --git a/X b/Y" line's two paths; the
// regex's literal a/ b/ does the -p1 strip. A --no-prefix or quoted line does
// not match and is a path fault, not a structural one.
func parseGitHeaderPaths(line string) (oldPath, newPath string, err error) {
	m := diffGitHeaderRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", errors.New("patch diff --git header paths cannot be resolved")
	}
	return m[1], m[2], nil
}

// parseDiffPathHeader extracts the path from a "--- "/"+++ " header. A tab
// starts a differ's mtime metadata, not part of the path.
func parseDiffPathHeader(raw string) (string, error) {
	if idx := strings.IndexByte(raw, '\t'); idx >= 0 {
		raw = raw[:idx]
	}
	if raw == devNull {
		return devNull, nil
	}
	return stripP1(raw)
}

// stripP1 strips a path's leading component as `git apply`'s default -p1
// does. A C-quoted name is rejected rather than unquoted, as is a name with
// nothing to strip, so a gate built on this fails closed.
func stripP1(path string) (string, error) {
	if strings.HasPrefix(path, `"`) {
		return "", errors.New("patch path is quoted")
	}
	idx := strings.IndexByte(path, '/')
	if idx < 0 {
		return "", errors.New("patch path has no leading component to strip")
	}
	return path[idx+1:], nil
}

// parseHunk parses the hunk at lines[i] and returns the index past its body.
// The body ends by the header's counts, not line shape: a removed "-- x" line
// and the next file's "--- " header both start with '-'.
func parseHunk(lines []string, i int) (next, added, removed int, err error) {
	m := hunkHeaderRE.FindStringSubmatch(lines[i])
	if m == nil {
		return 0, 0, 0, errors.New("patch is not a unified diff: malformed @@ hunk header")
	}
	oldLeft, oldErr := countOrDefault(m[2])
	newLeft, newErr := countOrDefault(m[4])
	if oldErr != nil || newErr != nil {
		return 0, 0, 0, errors.New("patch is not a unified diff: malformed @@ hunk header")
	}

	for i++; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, `\`) {
			// A "\ No newline at end of file" marker: not counted either way.
			continue
		}
		if oldLeft == 0 && newLeft == 0 {
			return i, added, removed, nil
		}
		if line == "" {
			// An empty context line, as a whitespace-stripping transport
			// produces; git apply accepts it too.
			oldLeft--
			newLeft--
			if oldLeft < 0 || newLeft < 0 {
				return 0, 0, 0, errHunkCounts
			}
			continue
		}
		switch line[0] {
		case ' ':
			oldLeft--
			newLeft--
		case '+':
			newLeft--
			added++
		case '-':
			oldLeft--
			removed++
		default:
			return 0, 0, 0, errHunkCounts
		}
		if oldLeft < 0 || newLeft < 0 {
			return 0, 0, 0, errHunkCounts
		}
	}
	if oldLeft != 0 || newLeft != 0 {
		return 0, 0, 0, errHunkCounts
	}
	return i, added, removed, nil
}

var errHunkCounts = errors.New("patch hunk line counts disagree with its @@ header")

// countOrDefault parses a hunk header's count field, 1 when absent.
// hunkHeaderRE admits only digits, so Atoi can fail only on overflow; the error
// is propagated so the hunk reads as a malformed header, not a clamped count
// disagreement.
func countOrDefault(s string) (int, error) {
	if s == "" {
		return 1, nil
	}
	return strconv.Atoi(s)
}
