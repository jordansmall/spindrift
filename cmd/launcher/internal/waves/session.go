package waves

import "spindrift.dev/launcher/internal/terminate"

// Session carries the Console-only state one live operator session shares
// across every RunContinuous call it makes (#1547): the resizable
// concurrency cap (ADR 0023, issue #653) and the operator-termination
// registry (ADR 0024, issue #649). Headless callers pass a nil Session —
// every pre-#1547 call site left the fields this splits out of Config at
// their zero value — and RunContinuous falls back to a fixed limiter built
// fresh from cfg.MaxParallel plus a nil "never terminated" registry,
// matching that prior behaviour exactly.
type Session struct {
	// Limiter is the resizable concurrency bound RunContinuous acquires a
	// slot from before claiming an issue. Nil means "build one fresh from
	// cfg.MaxParallel for this call and never resize it" — a fixed cap. The
	// Console builds one persistent Limiter per session and passes it here
	// so a live "+"/"-" resize takes effect on the RunContinuous call
	// already in flight, not just the next one.
	Limiter *Limiter

	// Terminated is checked by RunContinuous's per-issue goroutine after a
	// Box exits, so an issue the operator Terminated while it was running is
	// neither transitioned to Failed nor handed to Settle — Terminate
	// already reclaimed it. Nil means "never terminated"; only the Console
	// wires a Registry.
	Terminated *terminate.Registry
}
