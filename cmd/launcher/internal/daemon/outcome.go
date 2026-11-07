package daemon

import "spindrift.dev/launcher/internal/exitcode"

// Action is the daemon loop's next move after interpreting a child's exit
// code; see exitcode.Action.
type Action = exitcode.Action

const (
	Continue = exitcode.Continue
	Wait     = exitcode.Wait
	HaltPool = exitcode.HaltPool
	Backoff  = exitcode.Backoff
)

// outcomeNoneDispatchable is the outcome label of exitcode.NoneDispatchable;
// loop.go compares on it to tell a pool-wide jam from a routine idle Wait.
var outcomeNoneDispatchable = exitcode.DispatchTable[exitcode.NoneDispatchable].Outcome

// haltClassFor maps each exitcode.Halt a DispatchTable row can carry to the
// HaltClass it renders as.
var haltClassFor = map[exitcode.Halt]HaltClass{
	exitcode.NoHalt:            HaltNone,
	exitcode.HaltHostTainted:   HaltChildHostTainted,
	exitcode.HaltConfigInvalid: HaltChildConfigInvalid,
	exitcode.HaltSignalled:     HaltChildSignalled,
}

// Interpret maps a child's exit code to a stable outcome label for the event
// stream, the loop's next action, and — for the exits that halt the pool —
// the HaltClass that outcome renders as. It reads exitcode.DispatchTable, so
// the class can never drift from the outcome label it names. stopClosed is
// whether cfg.Stop was already closed when the child exited; it only matters
// for stop-gated rows (see exitcode.DispatchRow.StopGated, #3626).
func Interpret(exit int, stopClosed bool) (outcome string, action Action, halt HaltClass) {
	row := exitcode.LookupDispatch(exit)
	action, h := row.Verdict(stopClosed)
	return row.Outcome, action, haltClassFor[h]
}
