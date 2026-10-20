// Package hostbundle registers the built-in Records digests with the
// recordsdigest registry (ADR 0070 decision 2). Only the binaries that run
// the butler sweep import it, so the Box and driver-exec link no settle,
// tuning, dispatch or Records store. It is separate from the Driver bundle,
// which box and driver-exec import, for exactly that reason.
package hostbundle

import (
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/tuning"
)

func init() {
	recordsdigest.Register(tuning.Strategy)
}
