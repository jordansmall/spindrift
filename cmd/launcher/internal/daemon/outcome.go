package daemon

// Action is the daemon loop's next move after interpreting a child's exit
// code.
type Action int

// HaltSelfChanged is the prefix of the halt reason Loop returns when the
// daemon attribute's store path at the fetched tip no longer matches the
// daemon's own running build (Config.SelfProgram): the pool finishes what
// is already running and halts at the iteration boundary rather than
// orchestrating fresh Boxes from stale code. It never re-execs itself —
// composing this halt with a service restart policy is how an operator
// opts into self-update.
const HaltSelfChanged = "self-changed"

// HaltSelfBuildPrefix is the halt-reason prefix a SelfPath seam error backs
// off or halts under (pool.go's checkSelfBuild): docs/reference.md's
// **Self-change halt** section and its `backoff`/`halt` event rows document
// this exact string as operator-facing grammar, so changing it is a
// documented-behaviour change, not a rename.
const HaltSelfBuildPrefix = "self-build: "

// HaltInstanceLockPrefix is the sibling of HaltSelfBuildPrefix for the
// per-checkout instance lock's halt reason, emitted by
// cmd/launcher/daemon/main.go's AcquireCheckoutLock failure path.
// docs/reference.md's **Instance lock** section and its `halt` event row
// document this exact string as operator-facing grammar, so changing it is a
// documented-behaviour change, not a rename.
const HaltInstanceLockPrefix = "instance-lock: "

const (
	Continue Action = iota
	Wait
	Halt
	// Backoff means the exit is unclassified: back off this slot alone
	// (Config.FailureBackoff) and refill it, rather than halting the whole
	// pool — an unrecognised exit code from one child is not evidence the
	// rest of the pool is broken.
	Backoff
)

// Interpret maps a child's exit code to a stable outcome label for the event
// stream and the loop's next action. It mirrors cmd/launcher/main.go's
// exitCodeFor taxonomy (main.go:1774-1798, exitConfigInvalid/exitSignalledStop
// at main.go:1183-1192) — the exit codes are that loop's contract, not this
// package's to redefine.
func Interpret(exit int) (outcome string, action Action) {
	switch exit {
	case 0:
		return "dispatched", Continue
	case 2:
		return "queue-empty", Wait
	case 3:
		return "none-dispatchable", Wait
	case 4:
		// Every child here is born from its own evaluation at a freshly
		// resolved revision, so a stale image is answered by the next
		// iteration's pin, not by an orchestrated rebuild — unlike
		// dogfood.sh, which must rebuild-and-re-invoke on this code.
		return "image-stale", Continue
	case 5:
		return "host-tainted", Halt
	case 6:
		return "config-invalid", Halt
	case 7:
		return "signalled-stop", Halt
	default:
		return "error", Backoff
	}
}
