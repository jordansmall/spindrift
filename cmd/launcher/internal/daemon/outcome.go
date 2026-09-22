package daemon

// Action is the daemon loop's next move after interpreting a child's exit
// code.
type Action int

const (
	Continue Action = iota
	Wait
	// HaltPool means the exit demands the whole pool halt, not just this
	// slot — renamed from Halt (issue #3622) once Halt became the typed
	// halt-reason value's own name.
	HaltPool
	// Backoff means the exit is unclassified: back off this slot alone
	// (Config.FailureBackoff) and refill it, rather than halting the whole
	// pool — an unrecognised exit code from one child is not evidence the
	// rest of the pool is broken.
	Backoff
)

// outcomeNoneDispatchable is exit 3's outcome label: work exists but every
// issue found was claimed or overlap-deferred, distinct from exit 2's
// "queue-empty" (no work exists at all) — loop.go uses it to decide whether
// a Wait is a pool-wide jam or a routine idle.
const outcomeNoneDispatchable = "none-dispatchable"

// Interpret maps a child's exit code to a stable outcome label for the event
// stream, the loop's next action, and — for the exits that halt the pool —
// the HaltClass that outcome renders as. All three ride the same table so
// the class can never drift from the outcome label it names: a second table
// would risk, say, exit 6 changing its outcome string here without its
// HaltClass following along. Every other exit returns HaltNone:
// Continue/Wait/Backoff never halt, so there is nothing to classify.
// It mirrors cmd/launcher/main.go's exitCodeFor taxonomy (main.go:1774-1798,
// exitConfigInvalid/exitSignalledStop at main.go:1183-1192) — the exit codes
// are that loop's contract, not this package's to redefine.
//
// stopClosed is whether cfg.Stop was already closed when the child exited:
// it only changes exit 7's answer (every other exit ignores it), because
// exit 7 itself is ambiguous — it means "the operator stopped this child"
// only while Stop is closed; while Stop is open, it means someone else
// signalled that child, an unclassified failure like any other (#3626).
func Interpret(exit int, stopClosed bool) (outcome string, action Action, halt HaltClass) {
	switch exit {
	case 0:
		return "dispatched", Continue, HaltNone
	case 2:
		return "queue-empty", Wait, HaltNone
	case 3:
		return outcomeNoneDispatchable, Wait, HaltNone
	case 4:
		// Every child here is born from its own evaluation at a freshly
		// resolved revision, so a stale image is answered by the next
		// iteration's pin, not by an orchestrated rebuild — unlike
		// dogfood.sh, which must rebuild-and-re-invoke on this code.
		return "image-stale", Continue, HaltNone
	case 5:
		return "host-tainted", HaltPool, HaltChildHostTainted
	case 6:
		return "config-invalid", HaltPool, HaltChildConfigInvalid
	case 7:
		if stopClosed {
			return "signalled-stop", HaltPool, HaltChildSignalled
		}
		return "signalled-stop", Backoff, HaltNone
	default:
		return "error", Backoff, HaltNone
	}
}
