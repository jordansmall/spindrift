package settle

import (
	"sync"
	"sync/atomic"

	"spindrift.dev/launcher/internal/dispatchkey"
)

// DispatchOutcome is one Dispatch's record this run: its terminal state as
// settled, or InProgress for a Dispatch that never settled (a merge-blocked
// handoff or an off-script Box status).
type DispatchOutcome struct {
	Key                  dispatchkey.Key
	State, Reason, PRURL string
}

// Tally collects the terminal records Settled emits during one run, plus the
// InProgress records of Dispatches that never settle (blockHandoff's
// merge-blocks and Settle's off-script-status arm). Wave
// goroutines settle concurrently, so it is mutex-guarded. The zero value is
// ready to use.
type Tally struct {
	mu    sync.Mutex
	order []dispatchkey.Key
	byKey map[dispatchkey.Key]DispatchOutcome
}

func (t *Tally) record(o DispatchOutcome) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byKey == nil {
		t.byKey = map[dispatchkey.Key]DispatchOutcome{}
	}
	if _, seen := t.byKey[o.Key]; !seen {
		t.order = append(t.order, o.Key)
	}
	t.byKey[o.Key] = o
}

// Outcomes returns a copy of the recorded outcomes, one per Key (the last
// write wins), in first-settled order.
func (t *Tally) Outcomes() []DispatchOutcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]DispatchOutcome, 0, len(t.order))
	for _, k := range t.order {
		out = append(out, t.byKey[k])
	}
	return out
}

var installed atomic.Pointer[Tally]

// InstallTally makes t the tally Settled feeds and returns a func restoring
// the previous one. Process-wide, like report.Install.
func InstallTally(t *Tally) (restore func()) {
	prev := installed.Swap(t)
	return func() { installed.Store(prev) }
}

// tallyOutcome records o on the installed tally, if any.
func tallyOutcome(o DispatchOutcome) {
	if t := installed.Load(); t != nil {
		t.record(o)
	}
}
