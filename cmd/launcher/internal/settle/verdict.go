package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// Verdict is a terminal transition's Settle reason and the state it records.
// The state comes from the reason table, never from the caller, so a reason
// cannot be written with the wrong state; only Transition's refusal keeps a
// displaced state (issue #6609), and the off-script-status arm leaves the
// Dispatch in progress with no reason.
type Verdict struct {
	state  forge.DispatchState
	reason string
}

// InProgress is the off-script Verdict: a settle that left its Dispatch in
// progress with no reason, the Box's status being untrusted.
var InProgress = Verdict{state: forge.InProgress}

// ForReason returns the Verdict for reason, a dispatchrecord.Reason* const. It
// panics when reason has no fixed state (an unknown name, or
// transition-refused): that is a programmer error, not a runtime condition.
func ForReason(reason string) Verdict {
	state := dispatchrecord.ReasonState(reason)
	for _, s := range []forge.DispatchState{forge.Complete, forge.Failed, forge.Recoverable, forge.Ambiguous} {
		if s.String() == state {
			return Verdict{s, reason}
		}
	}
	panic(fmt.Sprintf("settle: reason %q has no fixed state", reason))
}

// State is the lifecycle state the Verdict records.
func (v Verdict) State() forge.DispatchState { return v.state }

// Reason is the Settle reason the Verdict records.
func (v Verdict) Reason() string { return v.reason }

// refused returns the transition-refused Verdict that keeps displaced's state.
func refused(displaced Verdict) Verdict {
	return Verdict{displaced.state, dispatchrecord.ReasonTransitionRefused}
}
