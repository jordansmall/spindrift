package console

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/settle"
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
}

// Settle delegates to the wrapped Settler, marks num settled, then notifies.
func (qs queueSettler) Settle(d dispatch.Dispatcher, num string, result dispatch.Result) {
	qs.Settler.Settle(d, num, result)
	qs.q.setState(num, PickSettled, "")
	if qs.notify != nil {
		qs.notify()
	}
}
