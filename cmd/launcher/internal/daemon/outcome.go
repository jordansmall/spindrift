package daemon

// Action is the daemon loop's next move after interpreting a child's exit
// code.
type Action int

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
