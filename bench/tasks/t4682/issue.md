---
title: "glob.Overlap misses overlap between two wildcard segments, so the touch-overlap gate dispatches colliding issues"
state: ready-for-agent
---
## Problem

`segmentOverlap` (cmd/launcher/internal/glob/glob.go:83-98) decides whether two single path segments can match a common name by running `path.Match(a, b)` and `path.Match(b, a)`, i.e. by treating one pattern as a literal name. When *both* segments contain wildcards and neither pattern literally matches the other's text, it returns false even though a concrete filename matches both.

Verified with a throwaway test against the current tree:

```
Overlap("cmd/*.go", "cmd/main*")   // false, but cmd/main.go matches both
Overlap("src/foo*", "src/*bar")    // false, but src/foobar matches both
Match("cmd/*.go", "cmd/main.go") && Match("cmd/main*", "cmd/main.go") // true
```

## Impact

`glob.Overlap` is the sole primitive behind the wave overlap gate (`touchSetsOverlap` in cmd/launcher/internal/waves/touches.go:88-97, used when `OverlapGate == "defer"`). Two issues whose `## Touches` sections declare e.g. `cmd/launcher/internal/forge/*.go` and `cmd/launcher/internal/forge/verdict*` are judged disjoint, so the gate dispatches the second while the first is still in progress — exactly the concurrent edit to one file the gate exists to defer. The gate fails open silently; nothing logs the miss.

## Suggested fix

Replace the "match one pattern against the other as a literal" heuristic with a real intersection test for two single-segment `*`/`?`/character-class patterns (e.g. a small DP over the two patterns' token sequences, analogous to the existing `segmentsOverlap` table one level up). It must be exact: two wildcard segments with no common match (say `*.go` and `*.md`) stay disjoint, so the gate does not defer unrelated work. Add the two cases above to glob_test.go.

`## Touches` lines are untrusted input, so the intersection must stay polynomial in time and linear in memory in the segments' length (no full len(a)×len(b) table). A malformed pattern, or one that is not valid UTF-8, fails closed (reports overlap): a false overlap only defers a dispatch.


Filed by the butler's `bugs` Chore. Class: `correctness`. Files: `cmd/launcher/internal/glob/glob.go`.

<!-- spindrift-dedup: cmd:launcher:internal:glob:glob:go:segmentoverlap -->
