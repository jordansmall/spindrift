package console

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// queueSettler wraps a settle.Settler so a successful settle also marks the
// queue's matching pick PickSettled — the last leg of a launched pick's row
// ("queued -> claiming -> running -> settled") — and signals notify, so a
// settle (a tracker write) triggers Run's auto-refresh (#647 AC4). SettleAdopted
// is unused by the continuous engine's launch path, so it is left to the
// embedded Settler unmodified.
type queueSettler struct {
	settle.Settler
	q      *Queue
	notify func()
	// terminated is checked before touching the pick row: a Terminate
	// (ADR 0024, issue #649) can land in the window between Run()
	// succeeding and this Settle call completing, after which the wrapped
	// Settler itself abandons internally (landingAbandoned) but still
	// returns normally — this wrapper has no way to tell that apart from an
	// ordinary settle except by checking the same registry Terminate marked,
	// so it must not overwrite Terminate's own PickTerminated back to
	// PickSettled. Nil (every construction site but the Console's) means
	// "never terminated".
	terminated *terminate.Registry
}

// Settle delegates to the wrapped Settler, then — unless num was terminated
// while this settle was in flight — marks num settled and notifies.
func (qs queueSettler) Settle(d dispatch.Dispatcher, num string, result dispatch.Result) {
	qs.Settler.Settle(d, num, result)
	if qs.terminated.Marked(num) {
		return
	}
	qs.q.setState(num, PickSettled, "")
	if qs.notify != nil {
		qs.notify()
	}
}
