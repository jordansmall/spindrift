package console

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// queueSettler wraps a settle.Settler so a settle also marks the queue's
// matching pick and signals notify, letting a tracker write trigger Run's
// auto-refresh (#647 AC4). The continuous engine's launch path never calls
// SettleAdopted, so the embedded Settler handles it unmodified.
type queueSettler struct {
	settle.Settler
	q      *Queue
	notify func()
	// A Terminate (ADR 0024, issue #649) can land between Run() succeeding
	// and Settle completing; the wrapped Settler then abandons internally
	// but still returns normally, so checking the registry Terminate marked
	// is the only way to keep this wrapper from overwriting PickTerminated
	// with PickSettled. A nil registry means nothing was ever terminated.
	terminated *terminate.Registry
}

// Settle delegates to the wrapped Settler, then marks num settled and notifies
// unless num was terminated in flight. gen is the terminate.Registry generation
// (waves.Issue.Generation, issue #743) this dispatch launched under, so a
// re-pick's later generation cannot clear the mark under a stale settle.
func (qs queueSettler) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	qs.Settler.Settle(d, num, gen, result)
	if qs.terminated.Marked(num, gen) {
		return
	}
	qs.q.setState(num, PickSettled, "")
	if qs.notify != nil {
		qs.notify()
	}
}

// Fail delegates to the wrapped Settler, then marks num failed and notifies
// unless num was terminated in flight, so a Box that exited non-zero reaches a
// terminal queue row instead of stranding at PickRunning (issue #705). gen is
// as in Settle.
func (qs queueSettler) Fail(num string, gen uint64, result dispatch.Result) {
	qs.Settler.Fail(num, gen, result)
	if qs.terminated.Marked(num, gen) {
		return
	}
	qs.q.setState(num, PickFailed, "box exited non-zero")
	if qs.notify != nil {
		qs.notify()
	}
}
