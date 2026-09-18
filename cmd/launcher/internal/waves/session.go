package waves

import "spindrift.dev/launcher/internal/terminate"

// Session carries the Console-only state one live operator session shares
// across every RunContinuous call it makes (#1547). A nil Session gives the
// pre-#1547 behaviour: a fixed limiter from cfg.MaxParallel, no registry.
type Session struct {
	// Limiter is the concurrency bound RunContinuous takes a slot from before
	// claiming an issue. Nil means a fixed cap built fresh from cfg.MaxParallel
	// (ADR 0023, issue #653). The Console passes one persistent Limiter per
	// session so a live resize reaches the RunContinuous call already in flight.
	Limiter *Limiter

	// Terminated tells RunContinuous, after a Box exits, that the operator
	// terminated the issue, so it is neither failed nor settled; Terminate
	// already reclaimed it (ADR 0024, issue #649). Nil means never terminated.
	Terminated *terminate.Registry
}
