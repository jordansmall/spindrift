package main

import (
	"path"
	"strings"
)

// scheduleSlices partitions slices (already in manifest/declaration order)
// into an ordered list of batches, where each batch is a set of slices safe
// to dispatch concurrently, and batches themselves run strictly in order --
// batch N+1 never starts until every slice in batch N has finished (issue
// #2060). This is a pure planning function: it makes no decision about how
// a batch is actually dispatched or joined -- that's LaunchWorkers'
// (workers.go) job, wired up by a later slice of #2060.
//
// Two rules keep a batch's concurrency provably safe:
//
//   - Lease disjointness: a slice with a non-empty FileLeases may join an
//     existing batch only if none of its leases (normalized and compared
//     for equality, path-prefix overlap, or one of a handful of
//     provably-can't-tell shapes -- see leasesOverlap) overlap any lease
//     already claimed by a slice already placed in that batch, and that
//     batch doesn't already hold a slice with empty/undeclared FileLeases.
//   - Undeclared leases are conservative: a slice with empty FileLeases is
//     always sequenced into its own batch alone, both because it can't
//     itself join a batch that has other members (its own touched-files
//     surface is unknown, so it's treated as touching everything) and
//     because no later slice may join a batch already holding one.
//
// DependsOn edges (names of other slices in the same manifest) add a floor
// on top of the lease rules: a slice's earliest eligible batch index is one
// past the batch index of each of its dependencies already placed by this
// function, i.e. max(batchIndexOf[dep]+1) across every DependsOn entry that
// names a slice present in this manifest. A DependsOn entry naming a slice
// absent from slices is simply ignored -- cross-manifest/dangling edges are
// out of scope here (ParseManifestLine, manifest.go, doesn't validate
// DependsOn targets either).
//
// Placement first computes a dependency-respecting processing order over
// slices (see dependencyOrder) so that a DependsOn edge is honored
// regardless of whether the dependency was declared earlier or later than
// the dependent in the manifest, then does a single pass over that order:
// for each slice, compute its earliest eligible batch index from DependsOn
// (0 if none declared or none present in this manifest). If that exact
// batch already exists and the slice can join it under the lease rules
// above, append it there. Otherwise open a brand-new batch at the end of
// the batches built so far and place the slice there alone. This never
// backfills an earlier batch once skipped, and never searches batches past
// the earliest-eligible index -- deliberately conservative in exchange for
// simple, deterministic behavior (it need not find every possible
// parallelization opportunity, only ones that are safe and simple to
// reason about).
//
// A slice named by dependencyOrder's forceSolo (placed via its
// cycle-fallback branch, so its DependsOn floor can't be trusted) skips the
// DependsOn-floor/canJoin logic entirely: it always opens a brand-new batch
// containing only itself, and that batch is marked as holding an
// undeclared-lease slice regardless of the slice's actual FileLeases, so
// nothing else may ever join it either.
func scheduleSlices(slices []ManifestSlice) [][]ManifestSlice {
	if len(slices) == 0 {
		return nil
	}

	ordered, forceSolo := dependencyOrder(slices)

	var batches [][]ManifestSlice
	batchIndexOf := make(map[string]int, len(ordered))
	// batchLeases[i] is the set of normalized FileLeases claimed by slices
	// already placed in batches[i]; batchHasUndeclared[i] tracks whether
	// batches[i] already holds a slice with empty/undeclared FileLeases,
	// which makes that batch solo (see doc comment above).
	batchLeases := make([][]string, 0, len(ordered))
	batchHasUndeclared := make([]bool, 0, len(ordered))

	for _, s := range ordered {
		if forceSolo[s.Name] {
			idx := len(batches)
			batches = append(batches, []ManifestSlice{s})
			batchLeases = append(batchLeases, claimLeases(s, nil))
			batchHasUndeclared = append(batchHasUndeclared, true)
			batchIndexOf[s.Name] = idx
			continue
		}

		earliest := 0
		for _, dep := range s.DependsOn {
			if depBatch, ok := batchIndexOf[dep]; ok && depBatch+1 > earliest {
				earliest = depBatch + 1
			}
		}

		placed := false
		if earliest < len(batches) && canJoin(s, batchLeases[earliest], batchHasUndeclared[earliest]) {
			batches[earliest] = append(batches[earliest], s)
			batchLeases[earliest] = claimLeases(s, batchLeases[earliest])
			if len(s.FileLeases) == 0 {
				batchHasUndeclared[earliest] = true
			}
			placed = true
		}

		if !placed {
			idx := len(batches)
			batches = append(batches, []ManifestSlice{s})
			batchLeases = append(batchLeases, claimLeases(s, nil))
			batchHasUndeclared = append(batchHasUndeclared, len(s.FileLeases) == 0)
			earliest = idx
		}

		batchIndexOf[s.Name] = earliest
	}

	return batches
}

// dependencyOrder returns slices reordered so that every slice appears
// after each of its DependsOn dependencies that is present in slices,
// regardless of the raw declaration order in the manifest -- a forward
// reference (a dependency declared later than its dependent) is honored
// rather than silently ignored. Slices with no ordering constraint between
// them keep their relative declaration order (a stable tie-break), which is
// what makes TestScheduleSlices_ManifestOrderPreservedWithinBatch hold.
//
// It repeatedly scans slices in original order, appending every slice
// that's "ready" (every DependsOn entry naming a slice present in this
// manifest has already been placed in the output order) in that scan, until
// either everything is placed or a full scan places nothing new. The latter
// means a dependency cycle -- manifest.go's ParseManifestLine doesn't
// validate against cycles either, so this defensively falls back to
// appending whatever remains in original order rather than looping forever
// (out of scope for #2060 beyond not hanging).
//
// The second return value, forceSolo, names every slice appended by that
// fallback branch -- its DependsOn floor can't be trusted (the dependency
// it names may not have been placed, or even processed, yet), so
// scheduleSlices must never let it join a batch by lease rules alone, and
// must never let anything else join its batch either.
func dependencyOrder(slices []ManifestSlice) (ordered []ManifestSlice, forceSolo map[string]bool) {
	present := make(map[string]bool, len(slices))
	for _, s := range slices {
		present[s.Name] = true
	}

	placed := make(map[string]bool, len(slices))
	ordered = make([]ManifestSlice, 0, len(slices))
	forceSolo = make(map[string]bool)

	for len(ordered) < len(slices) {
		progressed := false
		for _, s := range slices {
			if placed[s.Name] {
				continue
			}
			ready := true
			for _, dep := range s.DependsOn {
				if present[dep] && !placed[dep] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			ordered = append(ordered, s)
			placed[s.Name] = true
			progressed = true
		}
		if !progressed {
			// Dependency cycle among the remaining slices -- append what's
			// left in original order and stop rather than looping forever.
			// Every slice placed here is unsafe to co-batch by DependsOn
			// floor alone, so mark it forceSolo.
			for _, s := range slices {
				if !placed[s.Name] {
					ordered = append(ordered, s)
					placed[s.Name] = true
					forceSolo[s.Name] = true
				}
			}
			break
		}
	}

	return ordered, forceSolo
}

// canJoin reports whether slice s may join a batch already holding
// normalized leases (claimed by slices already placed in it) and
// hasUndeclared (whether that batch already contains a slice with
// empty/undeclared FileLeases).
func canJoin(s ManifestSlice, leases []string, hasUndeclared bool) bool {
	if hasUndeclared {
		// The batch already has an undeclared-lease slice, which makes it
		// solo -- nothing else may join.
		return false
	}
	if len(s.FileLeases) == 0 {
		// s itself has undeclared leases -- it can never share a batch,
		// including one that's otherwise empty of leases.
		return false
	}
	for _, lease := range s.FileLeases {
		norm := normalizeLease(lease)
		for _, claimed := range leases {
			if leasesOverlap(norm, claimed) {
				return false
			}
		}
	}
	return true
}

// claimLeases returns leases with every one of s's FileLeases appended,
// normalized.
func claimLeases(s ManifestSlice, leases []string) []string {
	for _, lease := range s.FileLeases {
		leases = append(leases, normalizeLease(lease))
	}
	return leases
}

// normalizeLease cleans a repo-relative, POSIX-style lease path so that
// equivalent spellings (e.g. "./a.go" and "a.go") compare equal. It
// deliberately uses the path package, not path/filepath, to keep behavior
// OS-independent and deterministic regardless of the host running tests or
// the orchestrator. It does not resolve the lease against a repo root --
// this package has no access to one -- so the result may still be absolute,
// "." (whole tree), or escape the root via ".."; leasesOverlap is what
// turns those shapes into a conservative "can't prove disjoint" verdict.
func normalizeLease(lease string) string {
	return path.Clean(lease)
}

// leasesOverlap reports whether two normalized lease paths are not provably
// disjoint. Beyond the straightforward cases -- equal, or one a path-prefix
// of the other at a "/" boundary (e.g. "cmd/x" overlaps
// "cmd/x/dispatch.go", since the latter names a file inside the directory
// the former names) -- four shapes are treated as unprovable and therefore
// overlapping, always erring toward sequencing rather than risking an
// unsafe parallel batch:
//
//   - Either lease is "." or "/" (path.Clean("") is also "."), i.e. it
//     names the whole repo root -- it can't be disjoint from anything.
//   - Either lease still starts with ".." after cleaning (it's exactly
//     ".." or starts with "../") -- it escapes the repo root, so there's
//     no shared base to compare against.
//   - One lease is absolute (starts with "/") and the other is relative --
//     this package has no repoRoot to resolve the relative one against, so
//     there's no reliable way to tell whether they name the same file.
//   - Either lease contains a glob metacharacter ("*", "?", "[") -- this
//     function only ever compares leases as plain strings (equality or
//     path-prefix), never as an actual glob match against candidate paths,
//     so a glob lease can never be proven disjoint from anything by this
//     logic.
func leasesOverlap(a, b string) bool {
	if isWholeTreeLease(a) || isWholeTreeLease(b) {
		return true
	}
	if escapesRoot(a) || escapesRoot(b) {
		return true
	}
	if containsGlobMeta(a) || containsGlobMeta(b) {
		return true
	}
	aAbs, bAbs := isAbsoluteLease(a), isAbsoluteLease(b)
	if aAbs != bAbs {
		return true
	}
	if a == b {
		return true
	}
	if len(a) < len(b) && b[:len(a)] == a && b[len(a)] == '/' {
		return true
	}
	if len(b) < len(a) && a[:len(b)] == b && a[len(b)] == '/' {
		return true
	}
	return false
}

// isWholeTreeLease reports whether a normalized lease names the whole repo
// root ("." or "/").
func isWholeTreeLease(lease string) bool {
	return lease == "." || lease == "/"
}

// escapesRoot reports whether a normalized lease escapes the repo root,
// i.e. it's exactly ".." or starts with "../".
func escapesRoot(lease string) bool {
	return lease == ".." || strings.HasPrefix(lease, "../")
}

// isAbsoluteLease reports whether a normalized lease is absolute.
func isAbsoluteLease(lease string) bool {
	return strings.HasPrefix(lease, "/")
}

// containsGlobMeta reports whether lease contains a glob metacharacter
// ("*", "?", "["). leasesOverlap only ever compares leases as plain
// strings, never as an actual glob match, so any lease containing one of
// these can't be structurally compared and must be treated as unprovable.
func containsGlobMeta(lease string) bool {
	return strings.ContainsAny(lease, "*?[")
}
