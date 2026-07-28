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
// staleTipTag is that same probe's derived tip tag; it's required alongside
// the rev match because a persistent eval/tag-derivation failure (Probe
// returns Applicable=true, Fresh=false, Rev set, but TipTag="" — no tag was
// ever derived) also repeats at the same rev, and without this guard would
// be misclassified as host taint. A genuine host-taint divergence always has
// a derived tip tag (both tags exist but differ); a stuck eval/derive
// failure never does, so an empty staleTipTag keeps retrying (ErrImageStale)
// instead of falsely halting. This heuristic also assumes a rebuild actually
// happened between the two same-rev stale runs (dogfood.sh's
// rebuild-on-exit-4 step) — driving continuous dispatch without that step
// would false-positive halt on the second same-rev stale run.
func classifyStaleOutcome(staleRev string, staleTipTag string, tracker staleRevTracker, diag func() string, out io.Writer) error {
	if freshness.NonConverging(staleRev, tracker.prior()) && staleTipTag != "" {
		fmt.Fprintln(out, diag())
		_ = tracker.clear()
		return errImageHostTainted
	}
	_ = tracker.record(staleRev)
	return waves.ErrImageStale
}
