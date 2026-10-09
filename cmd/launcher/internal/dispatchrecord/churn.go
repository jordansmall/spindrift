package dispatchrecord

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// churnWithinWindow reports the share of the lines merge added that other
// commits had rewritten or removed by end, and whether the clone could answer
// at all (matured). churn is nil when the lines cannot be told: the merge added
// none (or only submodule pointers, binary files or renamed-unchanged content),
// its first parent is missing (a shallow clone's boundary), or it is not on the
// base branch's first-parent line as it stood at end (e.g. it arrived as a later
// merge's second parent, or nothing on that line predates end). The revert
// verdict does not need churn, and judgeMaturity has already placed merge on tip
// past end, so those answers are permanent and the merge still matures.
//
// Added lines are the net diff against the merge's first parent, so lines a
// multi-commit PR rewrote among its own commits never count, and a file the PR
// itself renamed counts only its changed lines. A line survives when git blame
// --first-parent at the base branch as it stood at end still credits it to the
// merge. A file renamed by a later commit inside the window counts as
// rewritten; for a rebase merge only the last rebased commit is the recorded
// merge, as for reverts.
func churnWithinWindow(clone, merge, tip string, end time.Time) (churn *float64, matured bool, err error) {
	hasParent, err := hasFirstParent(clone, merge)
	if err != nil {
		return nil, false, err
	}
	if !hasParent {
		return nil, true, nil
	}
	added, total, err := addedLines(clone, merge)
	if err != nil {
		return nil, false, err
	}
	if total == 0 {
		return nil, true, nil
	}

	out, err := runGit(clone, "", "rev-list", "-1", "--first-parent", "--until="+strconv.FormatInt(end.Unix(), 10), tip)
	if err != nil {
		return nil, false, err
	}
	state := strings.TrimSpace(out)
	if state == "" {
		return nil, true, nil
	}
	firstParentLine, err := runGit(clone, "", "rev-list", "--first-parent", state, "^"+merge+"^1")
	if err != nil {
		return nil, false, err
	}
	if !slices.Contains(strings.Fields(firstParentLine), merge) {
		return nil, true, nil
	}

	surviving := 0
	for path, n := range added {
		kept, err := survivingLines(clone, state, path, merge)
		if err != nil {
			return nil, false, err
		}
		// Blame's rename detection can disagree with numstat's, so a path never
		// keeps more lines than the merge added there.
		surviving += min(kept, n)
	}
	v := float64(total-surviving) / float64(total)
	return &v, true, nil
}

// addedLines sums the lines merge added over its first parent per path, and in
// total. Binary files have no line count and are skipped. Submodule pointers
// are skipped too: numstat counts a gitlink as one line, but its commit is not
// in the clone, so blame could never credit it. Renames are detected (-M, not
// the user's diff.renames) so a moved file's unchanged lines are not "added".
func addedLines(clone, merge string) (map[string]int, int, error) {
	out, err := runGit(clone, "", "diff", "--numstat", "--ignore-submodules", "-M", "-l0", "-z", merge+"^1", merge)
	if err != nil {
		return nil, 0, err
	}
	added := map[string]int{}
	total := 0
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		entry := tokens[i]
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) != 3 {
			return nil, 0, fmt.Errorf("dispatchrecord: numstat entry %q", entry)
		}
		path := parts[2]
		if path == "" {
			// A rename's path field is empty; the old and new paths follow as
			// their own NUL-separated tokens.
			if i+2 >= len(tokens) {
				return nil, 0, fmt.Errorf("dispatchrecord: numstat rename entry %q", entry)
			}
			path = tokens[i+2]
			i += 2
		}
		if parts[0] == "-" {
			continue
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, 0, fmt.Errorf("dispatchrecord: numstat entry %q: %w", entry, err)
		}
		if n > 0 {
			added[path] = n
			total += n
		}
	}
	return added, total, nil
}

// survivingLines counts the lines of path at state that first-parent blame
// credits to merge. A path absent at state, or no longer a file there, has none.
func survivingLines(clone, state, path, merge string) (int, error) {
	// cat-file -e exits 128 for a missing path, the same as for a corrupt store,
	// so an ls-tree that lists nothing is the only unambiguous "absent". A path
	// replaced by a directory or submodule is listed too, but blame cannot read it.
	listed, err := runGit(clone, "", "ls-tree", "-z", state, "--", path)
	if err != nil {
		return 0, err
	}
	isBlob := false
	for _, entry := range strings.Split(listed, "\x00") {
		// Each entry is "<mode> <type> <object>\t<path>".
		meta, entryPath, ok := strings.Cut(entry, "\t")
		if ok && entryPath == path {
			fields := strings.Fields(meta)
			isBlob = len(fields) == 3 && fields[1] == "blob"
		}
	}
	if !isBlob {
		return 0, nil
	}
	out, err := runGit(clone, "", "blame", "--first-parent", "--porcelain", state, "--", path)
	if err != nil {
		return 0, err
	}
	// Porcelain gives each line a "<sha> <orig> <final>" header and then the
	// line itself behind a tab, so a line is counted against the latest header.
	n := 0
	var sha string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "\t") {
			if sha == merge {
				n++
			}
		} else if f := firstField(line); fullSHA.MatchString(f) {
			sha = f
		}
	}
	return n, nil
}

func firstField(line string) string {
	f, _, _ := strings.Cut(line, " ")
	return f
}

// hasFirstParent reports whether merge has a first parent in the clone; a
// shallow clone's boundary commit looks parentless.
func hasFirstParent(clone, merge string) (bool, error) {
	return gitTest(clone, "rev-parse", "--verify", "--quiet", merge+"^1^{commit}")
}
