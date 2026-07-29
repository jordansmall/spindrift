package main

import (
	"errors"
)

// errImageHostTainted is returned by runContinuousDispatch when a stale
// image divergence proved non-converging — it persisted after dogfood.sh
// already rebuilt to the current base tip, the signature of a host-system
// derivation reaching the image graph through a consumer flake (issue
// #2113). Distinct from waves.ErrImageStale so runExitCode can map it to a
// halt (exit 5) instead of the rebuild-and-retry exit 4 that would loop
// forever on a divergence a rebuild can never fix.
var errImageHostTainted = errors.New("image host-tainted; rebuild cannot converge")
