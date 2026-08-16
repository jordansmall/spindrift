package main

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
//     existing batch only if none of its leases (compared by plain string
//     equality on path entries) overlap any lease already claimed by a
//     slice already placed in that batch, and that batch doesn't already
//     hold a slice with empty/undeclared FileLeases.
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
// Placement is a single pass over slices in their given order: for each
// slice, compute its earliest eligible batch index from DependsOn (0 if
// none declared or none present in this manifest). If that exact batch
// already exists and the slice can join it under the lease rules above,
// append it there. Otherwise open a brand-new batch at the end of the
// batches built so far and place the slice there alone. This never
// backfills an earlier batch once skipped, and never searches batches past
// the earliest-eligible index -- deliberately conservative in exchange for
// simple, deterministic behavior (it need not find every possible
// parallelization opportunity, only ones that are safe and simple to
// reason about).
func scheduleSlices(slices []ManifestSlice) [][]ManifestSlice {
	if len(slices) == 0 {
		return nil
	}

	var batches [][]ManifestSlice
	batchIndexOf := make(map[string]int, len(slices))
	// batchLeases[i] is the union of FileLeases claimed by slices already
	// placed in batches[i]; batchHasUndeclared[i] tracks whether batches[i]
	// already holds a slice with empty/undeclared FileLeases, which makes
	// that batch solo (see doc comment above).
	batchLeases := make([]map[string]bool, 0, len(slices))
	batchHasUndeclared := make([]bool, 0, len(slices))

	for _, s := range slices {
		earliest := 0
		for _, dep := range s.DependsOn {
			if depBatch, ok := batchIndexOf[dep]; ok && depBatch+1 > earliest {
				earliest = depBatch + 1
			}
		}

		placed := false
		if earliest < len(batches) && canJoin(s, batchLeases[earliest], batchHasUndeclared[earliest]) {
			batches[earliest] = append(batches[earliest], s)
			claimLeases(s, batchLeases[earliest])
			if len(s.FileLeases) == 0 {
				batchHasUndeclared[earliest] = true
			}
			placed = true
		}

		if !placed {
			idx := len(batches)
			batches = append(batches, []ManifestSlice{s})
			leases := make(map[string]bool, len(s.FileLeases))
			claimLeases(s, leases)
			batchLeases = append(batchLeases, leases)
			batchHasUndeclared = append(batchHasUndeclared, len(s.FileLeases) == 0)
			earliest = idx
		}

		batchIndexOf[s.Name] = earliest
	}

	return batches
}

// canJoin reports whether slice s may join a batch already holding leases
// (claimed by slices already placed in it) and hasUndeclared (whether that
// batch already contains a slice with empty/undeclared FileLeases).
func canJoin(s ManifestSlice, leases map[string]bool, hasUndeclared bool) bool {
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
		if leases[lease] {
			return false
		}
	}
	return true
}

// claimLeases records every one of s's FileLeases into leases.
func claimLeases(s ManifestSlice, leases map[string]bool) {
	for _, lease := range s.FileLeases {
		leases[lease] = true
	}
}
