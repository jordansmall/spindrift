package waves

import (
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// Session carries the state a caller shares with Dispatch and RunContinuous
// (#1547). Only the Console sets Limiter; every production caller sets
// Terminated. A nil Session is tests-only; see the field docs.
type Session struct {
	// Limiter is the concurrency bound RunContinuous takes a slot from before
	// claiming an issue. Nil means a fixed cap built fresh from cfg.MaxParallel
	// (ADR 0023, issue #653). The Console passes one persistent Limiter per
	// session so a live resize reaches the RunContinuous call already in flight.
	// Dispatch's one-shot wave never reads this field (#3522): a batch is
	// sized once at the start of the wave and never resized mid-run, so there
	// is nothing for a live Limiter to buy that path.
	Limiter *Limiter

	// Terminated tells RunContinuous, and Dispatch's one-shot wave (#3522),
	// after a Box exits, that the operator terminated the issue, so it is
	// neither failed nor settled; Terminate already reclaimed it (ADR 0024,
	// issue #649). Every production caller passes a non-nil one (main's
	// registryFor, the Console's Launcher.registry), so nil is tests-only:
	// dispatchWave and RunContinuous then substitute a fresh registry, which
	// a signalled abort's Reclaim can still mark, so an issue can still come
	// back terminated.
	Terminated *terminate.Registry

	// Tally folds the Settlement each of this session's settles returns,
	// keyed by issue. Nil folds nothing.
	Tally *settle.Tally

	// BoxFailed, when non-nil, is called after a Box that exited non-zero was
	// transitioned to Failed, so the Console's queue can move its row out of
	// PickRunning (issue #705). Nil means no observer (headless runs).
	BoxFailed func(num string, gen uint64)
}

// record folds st into the session's Tally under issue num; nil-safe on both
// the Session and its Tally.
func (s *Session) record(num string, st settle.Settlement) {
	if s != nil {
		s.Tally.Record(dispatchkey.Issue(num), st)
	}
}

// boxFailed calls the BoxFailed hook; nil-safe on both the Session and the hook.
func (s *Session) boxFailed(num string, gen uint64) {
	if s != nil && s.BoxFailed != nil {
		s.BoxFailed(num, gen)
	}
}
