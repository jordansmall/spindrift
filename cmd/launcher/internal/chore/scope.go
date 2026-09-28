// Package chore holds the pure Chore computations (ADR 0056): the scan
// Scope and NextScope for one run (the range of new commits since the
// Chore's last sweep, plus the next slice of the tree at the cursor, so new
// code is checked promptly and old code eventually, and no run reads the
// whole repo), the due Check, class parsing, and the enabled-Chore helpers.
//
// It stays a leaf: it imports no forge, dispatch, settle, or daemon
// package itself. dispatch imports Scope, so any such import back would
// cycle.
package chore

import (
	"sort"

	"spindrift.dev/launcher/internal/ledger"
)

// DefaultSliceSize is the number of tracked paths a run steps through the
// tree by default, absent an override.
const DefaultSliceSize = 40

// Scope is one run's scan scope: DiffRange covers the commits new since the
// last sweep, and Slice covers the next stretch of the tree the cursor walk
// reaches.
type Scope struct {
	// Head is the commit this run sweeps up to; it becomes the done
	// commit's lastSwept.
	Head string
	// DiffRange is prev.LastSwept + ".." + head, or "" when there is
	// nothing new to diff (first run, or lastSwept already at head).
	DiffRange string
	// Slice is the tree-walk slice: up to sliceSize tracked paths, sorted,
	// starting strictly after prev.Cursor and wrapping to the top of the
	// tree when the cursor is at or past the last path.
	Slice []string
	// NextCursor is Slice's last path, carried forward for the next run's
	// cursor. It is "" when Slice reached the final path in sorted order,
	// so the next run starts over from the top of the tree.
	NextCursor string
}

// NextScope computes the Scope for a run that resumes from prev's saved
// LastSwept/Cursor and sweeps up to head. files is the tracked path list at
// head, in any order (NextScope sorts a copy rather than mutating it).
// sliceSize bounds how many paths the tree-walk slice carries; callers
// normally pass DefaultSliceSize.
func NextScope(prev ledger.State, head string, files []string, sliceSize int) Scope {
	scope := Scope{Head: head}
	if prev.LastSwept != "" && prev.LastSwept != head {
		scope.DiffRange = prev.LastSwept + ".." + head
	}

	if len(files) == 0 {
		return scope
	}

	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	// First path strictly after the cursor. An empty cursor (first run)
	// matches everything; a cursor at or past the last path finds nothing,
	// so start falls through to len(sorted) and wraps below.
	start := sort.Search(len(sorted), func(i int) bool { return sorted[i] > prev.Cursor })
	if start >= len(sorted) {
		start = 0
	}

	end := start + sliceSize
	if end > len(sorted) {
		end = len(sorted)
	}
	scope.Slice = sorted[start:end]

	if end < len(sorted) {
		scope.NextCursor = scope.Slice[len(scope.Slice)-1]
	}
	return scope
}
