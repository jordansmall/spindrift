package main

import (
	"errors"
)

// errImageHostTainted marks a stale image divergence that a rebuild cannot fix:
// it persisted after dogfood.sh rebuilt to the current base tip, the signature
// of a host-system derivation reaching the image graph through a consumer flake
// (issue #2113). runExitCode maps it to a halt (exit 5) rather than the
// rebuild-and-retry exit 4, which would loop forever on it.
var errImageHostTainted = errors.New("image host-tainted; rebuild cannot converge")
