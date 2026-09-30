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

// diffGitHeaderRE matches a "diff --git a/X b/Y" line with the default
// a/ b/ prefixes -p1 expects -- a --no-prefix (or diff.noprefix) diff
// drops both prefixes and so does not match here; that line's paths fold
// into pathErr via resolveGitHeaderPaths rather than aborting the
// structural parse (see ValidateUnifiedDiff's doc comment). When it does
// match, it is also the fallback path source for a git section with no
// "--- "/"+++ " pair of its own (a pure rename or a permission-only mode
// change).
var diffGitHeaderRE = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)

const devNull = "/dev/null"

// isBinaryContentLine reports whether line is one of the three markers
// `git apply` emits in place of a hunk for a binary file: the usual
// "Binary files a/X and b/X differ", its "GIT binary patch" literal-diff
// counterpart, or the legacy "Files a/X and b/X differ" form (still
// produced by e.g. `diff` itself, and accepted by `git apply` the same
// way). Patch never carries a binary blob, so any of the three aborts the
// parse outright rather than being silently dropped as unrecognised
// content.
func isBinaryContentLine(line string) bool {
	if strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ") {
		return true
	}
	if strings.HasPrefix(line, "Files ") && strings.HasSuffix(line, " differ") {
		return true
	}
	return line == "GIT binary patch"
}

// Ranks for setChange: when several extended-header lines (or the +/-
// path comparison in resolvePathAndChange) each name a change kind for
// the same entry, the most disruptive one wins -- copied outranks
// renamed outranks added/deleted outranks a bare mode change.
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
// ParseUnifiedDiff: the path a downstream gate should key on, what kind of
// change it is (empty for a plain modification), and its total +/- line
// counts across all of its hunks (both zero for a hunkless git section,
// e.g. a pure rename or a permission-only mode change; git apply still
// applies these, so they are recorded as entries even though they have no
// hunk).
type DiffFile struct {
	Path    string
	Change  string // "", "added", "deleted", "renamed", "copied", or "mode change"
	Added   int
	Removed int
}

// ParseUnifiedDiff parses patch into its file entries, applying the same
// structural rule set ValidateUnifiedDiff documents, with the same
// structural errors. It additionally resolves each entry's path (the -p1
// leading component stripped from a "--- "/"+++ " header, or a git
// header's own a/ b/ pair) and fails on the first one it can't resolve --
// a quoted path, or a header with no leading component to strip -- even
// on a patch ValidateUnifiedDiff itself accepts, since a caller here
// needs a real path to key its gate on.
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

// ValidateUnifiedDiff reports whether patch is structurally a unified
// diff: zero or more `diff --git`/extended header lines, then at least
// one `--- `/`+++ ` file header pair, each followed by at least one
// `@@ ... @@` hunk whose body lines' ' '/'+'/'-'/'\' counts match its
// header -- except a hunkless "diff --git" section carrying only extended
// headers (new or deleted file mode, rename, copy, or a mode change),
// which `git apply` still applies without a hunk of its own, as long as
// some other section in the patch has one. A binary hunk (`Binary files ... differ`, the legacy
// `Files ... differ`, or `GIT binary patch`) is rejected outright: Patch
// only ever carries a modification, never a binary blob. Within a "diff
// --git" section's own extended-header zone (before its "--- "/"+++ "
// pair, if any), an unrecognised line is a structural error too -- fail
// closed, since an unknown extended header must not silently drop the
// section and its content out of the parse. Elsewhere -- prose preamble,
// junk between files, extra lines past a hunk's counts -- an unrecognised
// line is still silently skipped rather than rejected, matching `git
// apply`'s own leniency. This check does not resolve any path: a quoted
// path (e.g. a Box's `git diff` under the default core.quotePath) or a
// --no-prefix diff both still pass here, structurally sound diffs that
// ParseUnifiedDiff's caller separately fails closed on. The error
// returned is a signalwire.Reject Reason verbatim -- one lower-case line
// naming no path or token, only what is wrong.
func ValidateUnifiedDiff(patch string) error {
	_, structErr, _ := parseUnifiedDiff(patch)
	return structErr
}

// parseUnifiedDiff does the actual parse behind ParseUnifiedDiff and
// ValidateUnifiedDiff, splitting its two kinds of failure so each caller
// can pick the one it wants: structErr is a genuine structural fault (a
// malformed header, mismatched hunk counts, binary content, ...) and
// aborts the parse outright; pathErr is the first path a section
// couldn't resolve (quoted, or with no leading component to strip),
// recorded but not fatal to the structural walk, since the diff itself
// is still well-formed even when a downstream gate can't key on one of
// its paths.
func parseUnifiedDiff(patch string) (files []DiffFile, structErr, pathErr error) {
	// The patch's own trailing newline terminates its last line; it is not
	// an empty line of its own, so Split's trailing "" artifact is dropped
	// before a hunk could count it as context.
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

// parseGitSection parses one "diff --git" section starting at lines[i] and
// returns the DiffFile it describes. ok is false when the section carries
// no applicable content at all (e.g. a bare "index" line with nothing
// else) -- it should not become a DiffFile entry. pathErr is the first
// unresolvable path this section found, if any -- see parseUnifiedDiff.
//
// A section with extended-header lines (new/deleted file mode, rename,
// copy, mode change) but no "--- "/"+++ " pair and no hunk -- a pure
// rename or a permission-only mode change -- still carries real,
// applicable content: `git apply` WOULD apply it. It is recorded here as
// its own hunkless DiffFile so a gate keyed only on hunks can't miss it;
// hadHunk stays false for it, so a patch made of nothing but such sections
// still fails the "no hunk" check, same as today.
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
			// "rename old "/"rename new " is the legacy form of the same
			// header pair, still accepted by `git apply` alongside
			// "rename from "/"rename to ".
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
			// A second "--- "/"+++ " pair under one "diff --git" line is
			// `git apply`'s own separate patch, not part of this section --
			// end the section here so the outer loop hands it to
			// parsePlainFileSection as its own entry.
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
			// A second "--- " above already ends the loop before reaching
			// here, so this branch runs at most once per section -- assign,
			// don't accumulate.
			added, removed, sawHunk = a, r, hunks > 0
			i = next2
		default:
			if !haveHeaderPair {
				// An unrecognised line in the extended-header zone (before
				// this section's own "--- "/"+++ " pair, if any) must not
				// silently drop the rest of the section -- fail closed
				// instead of the lenient break below, which only applies
				// once this section's own content is already accounted for.
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

// parsePlainFileSection parses one "--- "/"+++ " file section (no
// preceding "diff --git" line) starting at lines[i]. pathErr is the
// first unresolvable path found, if any -- see parseUnifiedDiff.
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
		// parseHunk already matched and count-parsed this header at
		// lines[i+2], so m is non-nil and the counts parse.
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

// parseHeaderPairAndHunks parses one "--- "/"+++ " header pair starting at
// lines[i] and the "@@ " hunks that immediately follow it, shared by
// parseGitSection (a header pair nested under a "diff --git" line) and
// parsePlainFileSection (a bare one). A missing "+++ " partner or a
// malformed hunk is a structural fault and aborts; a path either header
// line can't resolve (quoted, or with no leading component to strip)
// instead folds into *pathErr (first one only) and parsing continues with
// that side's path treated as unresolved -- see parseUnifiedDiff.
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

// resolvePathAndChange folds the +/- file-header paths into change
// (already primed from any extended-header lines seen, at rank) and picks
// the path a downstream gate should key on: the new-side path, except a
// deletion reports its old-side path since the new side is /dev/null.
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

// resolveGitHeaderPaths resolves a "diff --git a/X b/Y" line's two paths,
// folding a failure -- a quoted path, or a line --no-prefix (or
// diff.noprefix) left without its literal a/ b/ prefixes -- into *pathErr
// (first one only) instead of the structural parse. See parseUnifiedDiff.
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

// resolveDiffPathHeader resolves one "--- "/"+++ " header's path the same
// way -- see resolveGitHeaderPaths and parseUnifiedDiff.
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

// parseGitHeaderPaths splits a "diff --git a/X b/Y" line's two paths. The
// regex's own literal "a/"/"b/" already strips the leading component --
// unlike the "--- "/"+++ " pair, this line's prefix is fixed, not -p-level
// dependent, so there is no separate stripP1 step here. A line that
// doesn't match (a --no-prefix diff, or git's quoted "a/…" form, which
// never reaches here as literal a/ either) is a path fault, not a
// structural one -- see resolveGitHeaderPaths and parseUnifiedDiff.
func parseGitHeaderPaths(line string) (oldPath, newPath string, err error) {
	m := diffGitHeaderRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", errors.New("patch diff --git header paths cannot be resolved")
	}
	return m[1], m[2], nil
}

// parseDiffPathHeader extracts a usable path from a "--- "/"+++ " header's
// text (the prefix already trimmed by the caller). "/dev/null" is returned
// verbatim -- it has no path component to strip. A trailing tab introduces
// a real differ's mtime metadata, not part of the path.
func parseDiffPathHeader(raw string) (string, error) {
	if idx := strings.IndexByte(raw, '\t'); idx >= 0 {
		raw = raw[:idx]
	}
	if raw == devNull {
		return devNull, nil
	}
	return stripP1(raw)
}

// stripP1 strips a path's leading component the way `git apply`'s default
// -p1 does. A C-quoted name (git's escaping for a path with whitespace or
// control characters) is rejected rather than unquoted, and a name with no
// component to strip is rejected too: both are conservative refusals so a
// patch gate built on this parser fails closed on a path it can't resolve.
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

// parseHunk parses the hunk starting at lines[i] (a "@@ " header) and
// returns the index of the line after its body, plus its added ('+') and
// removed ('-') body line counts. The body is consumed by the header's own
// counts, not by line shape: a removed line reading "-- x" and the next
// file's "--- " header both start with '-', so only the counts say where a
// hunk ends.
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
			// A fully empty line inside a hunk body is an empty context
			// line (git apply accepts this too; a whitespace-stripping
			// transport can produce one), not a malformed line. The patch's
			// trailing newline never reaches here as one: the caller drops
			// Split's artifact.
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
