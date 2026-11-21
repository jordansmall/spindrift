package waves

import (
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// Session carries the state a caller shares with Dispatch and RunContinuous
// (#1547). Every production caller sets Terminated. A nil Session is
// tests-only; see the field docs.
type Session struct {
	// Terminated tells RunContinuous, and Dispatch's one-shot wave (#3522),
	// after a Box exits, that the issue was terminated (a signalled abort or a
	// drain decline), so it is neither failed nor settled; Reclaim already
	// returned it to Dispatchable (ADR 0024, issue #649). Every production
	// caller passes a non-nil one (main's launchContext.terminated), so nil is
	// tests-only: dispatchWave and RunContinuous then substitute a fresh
	// registry, which a signalled abort's Reclaim can still mark, so an issue
	// can still come back terminated.
	Terminated *terminate.Registry

	// Tally folds the Settlement each of this session's settles returns,
	// keyed by issue. Nil folds nothing.
	Tally *settle.Tally

	// BoxFailed, when non-nil, is called after a Box that exited non-zero was
	// transitioned to Failed: an optional observer hook (issue #705). Nil means
	// no observer.
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
