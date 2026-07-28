package main

import (
	"errors"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/waves"
)

// errImageHostTainted is returned by runContinuousDispatch when a stale
// image divergence proved non-converging — it persisted after dogfood.sh
// already rebuilt to the current base tip, the signature of a host-system
// derivation reaching the image graph through a consumer flake (issue
// #2113). Distinct from waves.ErrImageStale so runExitCode can map it to a
// halt (exit 5) instead of the rebuild-and-retry exit 4 that would loop
// forever on a divergence a rebuild can never fix.
var errImageHostTainted = errors.New("image host-tainted; rebuild cannot converge")

// classifyStaleOutcome decides, for a continuous run that ended
// waves.ErrImageStale, whether the divergence is content staleness (a new
// base tip a rebuild will fix — record the rev and return ErrImageStale so
// the driving loop rebuilds and retries) or a non-converging host-tainted
// divergence (the same tip was already rebuilt against yet is still stale —
// print diag once, clear the tracker, and return errImageHostTainted to
// halt the loop). staleRev is the base-tip rev the last stale probe
// fetched ("" if the fetch itself failed, which is never non-converging).
func classifyStaleOutcome(staleRev string, tracker staleRevTracker, diag func() string, out io.Writer) error {
	if freshness.NonConverging(staleRev, tracker.prior()) {
		fmt.Fprintln(out, diag())
		_ = tracker.clear()
		return errImageHostTainted
	}
	_ = tracker.record(staleRev)
	return waves.ErrImageStale
}
