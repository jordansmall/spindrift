package main

import "path"

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
//     for equality or path-prefix overlap, see leasesOverlap) overlap any
//     lease already claimed by a slice already placed in that batch, and
//     that batch doesn't already hold a slice with empty/undeclared
//     FileLeases.
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
func scheduleSlices(slices []ManifestSlice) [][]ManifestSlice {
	if len(slices) == 0 {
		return nil
	}

	ordered := dependencyOrder(slices)

	var batches [][]ManifestSlice
	batchIndexOf := make(map[string]int, len(ordered))
	// batchLeases[i] is the set of normalized FileLeases claimed by slices
	// already placed in batches[i]; batchHasUndeclared[i] tracks whether
	// batches[i] already holds a slice with empty/undeclared FileLeases,
	// which makes that batch solo (see doc comment above).
	batchLeases := make([][]string, 0, len(ordered))
	batchHasUndeclared := make([]bool, 0, len(ordered))

	for _, s := range ordered {
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
func dependencyOrder(slices []ManifestSlice) []ManifestSlice {
	present := make(map[string]bool, len(slices))
	for _, s := range slices {
		present[s.Name] = true
	}

	placed := make(map[string]bool, len(slices))
	ordered := make([]ManifestSlice, 0, len(slices))

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
			for _, s := range slices {
				if !placed[s.Name] {
					ordered = append(ordered, s)
					placed[s.Name] = true
				}
			}
			break
		}
	}

	return ordered
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
// the orchestrator.
func normalizeLease(lease string) string {
	return path.Clean(lease)
}

// leasesOverlap reports whether two normalized lease paths are not provably
// disjoint: they're equal, or one is a path-prefix of the other at a "/"
// boundary (e.g. "cmd/x" overlaps "cmd/x/dispatch.go", since the latter
// names a file inside the directory the former names).
func leasesOverlap(a, b string) bool {
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
