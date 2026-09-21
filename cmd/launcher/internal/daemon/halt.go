package daemon

import "fmt"

// HaltClass distinguishes the reasons the daemon loop can halt: distinct
// enough that String() and ExitCode() can pick documented grammar and a
// wire exit code by class alone, without re-deriving either from a raw
// string prefix.
type HaltClass int

const (
	// HaltNone is the zero value: no halt was recorded. Halt.String()
	// renders it as "", the same empty reason the loop's halt carried
	// back when it was a bare string.
	HaltNone HaltClass = iota
	HaltOperatorStop
	HaltChildSignalled
	HaltChildHostTainted
	HaltChildConfigInvalid
	// HaltSelfChanged: the daemon attribute's store path at the fetched tip
	// no longer matches the daemon's own running build (Config.SelfProgram).
	// The pool finishes what is already running and halts at the iteration
	// boundary rather than orchestrating fresh Boxes from stale code; it
	// never re-execs itself — composing this halt with a service restart
	// policy is how an operator opts into self-update.
	HaltSelfChanged
	HaltSelfBuild
	HaltBreaker
	HaltInvalidConfig
	// HaltPreflight: the startup doctor preflight (issue #3544) refused to
	// let the daemon start — see cmd/launcher/daemon/main.go's
	// startupPreflight.
	HaltPreflight
	// HaltInstanceLock: AcquireCheckoutLock refused a second daemon against
	// a checkout another daemon already holds — see
	// cmd/launcher/daemon/main.go's mainRun.
	HaltInstanceLock

	// haltClassCount sits after every real class so a class added above it
	// (and only above it) is covered by the completeness check on
	// haltRenderings — TestHaltRenderings_CoversEveryClass fails the moment
	// a new class lands with no row. A missing row still compiles, so that
	// test is the only thing standing between it and a silent "" / exit 1
	// at runtime.
	haltClassCount
)

// haltRendering is one HaltClass's documented rendering: its Go identifier
// name (for HaltClass.String()), the reason text, whether that text is a
// prefix joined to Halt.Detail with ": " or the whole reason on its own,
// and the wire exit code. Halt.String() and Halt.ExitCode() both read this
// one table so the two can't drift the way two parallel switches can.
type haltRendering struct {
	// name is the class's own Go identifier, kept in step by hand:
	// nothing asserts the two match, so renaming a class means editing
	// its row too or HaltClass.String() misnames it in a test failure.
	name     string
	reason   string
	detailed bool
	exit     int
}

var haltRenderings = map[HaltClass]haltRendering{
	HaltNone:               {name: "HaltNone", reason: "", detailed: false, exit: 1},
	HaltOperatorStop:       {name: "HaltOperatorStop", reason: "context-cancelled", detailed: true, exit: 0},
	HaltChildSignalled:     {name: "HaltChildSignalled", reason: "outcome: signalled-stop", detailed: false, exit: 0},
	HaltChildHostTainted:   {name: "HaltChildHostTainted", reason: "outcome: host-tainted", detailed: false, exit: 1},
	HaltChildConfigInvalid: {name: "HaltChildConfigInvalid", reason: "outcome: config-invalid", detailed: false, exit: 1},
	HaltSelfChanged:        {name: "HaltSelfChanged", reason: "self-changed", detailed: true, exit: ExitSelfChanged},
	HaltSelfBuild:          {name: "HaltSelfBuild", reason: "self-build", detailed: true, exit: 1},
	HaltBreaker:            {name: "HaltBreaker", reason: "breaker", detailed: true, exit: 1},
	HaltInvalidConfig:      {name: "HaltInvalidConfig", reason: "config-invalid", detailed: true, exit: 1},
	HaltPreflight:          {name: "HaltPreflight", reason: "preflight", detailed: true, exit: ExitPreflightFailed},
	HaltInstanceLock:       {name: "HaltInstanceLock", reason: "instance-lock", detailed: true, exit: 1},
}

// String returns c's Go identifier name (e.g. "HaltSelfChanged") — not the
// operator-facing grammar, which is Halt.String()'s job on the (Class,
// Detail) pair. It has no production caller by design: it exists so a %v in
// a test failure names the class instead of printing a bare integer.
func (c HaltClass) String() string {
	if row, ok := haltRenderings[c]; ok {
		return row.name
	}
	return fmt.Sprintf("HaltClass(%d)", int(c))
}

// Halt is the daemon loop's typed halt reason: what stopped the loop, plus
// whatever the event stream and exit code need to say about it.
type Halt struct {
	Class    HaltClass
	Detail   string
	Kind     Kind
	Revision string
}

// String renders h in the operator-facing grammar docs/reference.md
// documents (the `halt` event row, and the **Self-change halt** /
// **Instance lock** / **Startup preflight** sections) — changing any of
// these strings is a documented-behaviour change, not a rename.
func (h Halt) String() string {
	row, ok := haltRenderings[h.Class]
	if !ok {
		return ""
	}
	if row.detailed {
		return row.reason + ": " + h.Detail
	}
	return row.reason
}

// ExitCode maps h to this process's exit code. HaltOperatorStop and
// HaltChildSignalled are the only two clean stops (0) — every other child
// halt (host-tainted, config-invalid) is a real failure despite also
// coming from a child exit, hence 1 alongside every non-child class.
// HaltNone also exits 1: a zero Halt reaching ExitCode is a halt that was
// never classified, not a clean stop, so it must not silently exit 0.
func (h Halt) ExitCode() int {
	row, ok := haltRenderings[h.Class]
	if !ok {
		return 1
	}
	return row.exit
}

// Event renders h as the daemon's "halt" event record. Kind/Revision are
// omitempty on Event, so a pre-pool halt (h.Kind/h.Revision left zero)
// marshals identically to today's pre-pool halt events.
func (h Halt) Event() Event {
	return Event{Event: "halt", Kind: h.Kind, Revision: h.Revision, Reason: h.String()}
}

// ExitSelfChanged is the daemon's own exit code for the one halt an
// operator may want to act on automatically: its build changed at the
// fetched tip. It is deliberately distinct from every other code this
// binary returns (0 clean stop, 1 anything else) so a service unit can
// restart on it alone — see HaltSelfChanged's doc for why that restart,
// not an automatic re-exec, is how an operator opts into self-update. It
// sits outside the 0-7 band the *child* launcher's exit codes occupy
// (Interpret, cmd/launcher/internal/daemon/outcome.go) so the two
// taxonomies cannot be confused when both appear in one log.
const ExitSelfChanged = 10

// ExitPreflightFailed is the daemon's own exit code for a refused start: the
// startup doctor preflight found a Required-tier failure (missing triage
// labels, an invalid config) or could not run at all. It is 11, not a
// pass-through of doctor's own 1/2/3/4 (a different table where the same
// integers mean something else) nor of the *child* launcher's 0-7 band
// (Interpret) — sharing either would let a reader misattribute this halt to
// the wrong process's contract. Unlike ExitSelfChanged (10), which an
// operator deliberately composes with a restart policy, a restart cannot
// clear this one: nothing the daemon does fixes a missing label or an
// undersized VM, so a supervisor must not treat this code as retryable.
const ExitPreflightFailed = 11
