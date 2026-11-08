package settle

import (
	"sync"

	"spindrift.dev/launcher/internal/dispatchkey"
)

// DispatchOutcome pairs a Settlement with the Dispatch it settled.
type DispatchOutcome struct {
	Key dispatchkey.Key
	Settlement
}

// Tally folds the Settlements a run's settles return, one outcome per Key. Wave
// goroutines record concurrently, so it is mutex-guarded. The zero value is
// ready to use; Record on a nil *Tally folds nothing.
type Tally struct {
	mu    sync.Mutex
	order []dispatchkey.Key
	byKey map[dispatchkey.Key]DispatchOutcome
}

// Record folds st under key: the last write wins, keeping the key's
// first-recorded position. The zero Settlement (no decision) is ignored.
func (t *Tally) Record(key dispatchkey.Key, st Settlement) {
	if t == nil || st == (Settlement{}) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byKey == nil {
		t.byKey = map[dispatchkey.Key]DispatchOutcome{}
	}
	if _, seen := t.byKey[key]; !seen {
		t.order = append(t.order, key)
	}
	t.byKey[key] = DispatchOutcome{Key: key, Settlement: st}
}

// Outcomes returns a copy of the recorded outcomes, one per Key (the last
// write wins), in first-recorded order.
func (t *Tally) Outcomes() []DispatchOutcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]DispatchOutcome, 0, len(t.order))
	for _, k := range t.order {
		out = append(out, t.byKey[k])
	}
	return out
}
