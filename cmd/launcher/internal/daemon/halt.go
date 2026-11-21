package daemon

import (
	"fmt"

	"spindrift.dev/launcher/internal/report"
)

// HaltClass distinguishes the reasons the daemon loop can halt, so
// Halt.String() and Halt.ExitCode() pick grammar and exit code by class alone.
type HaltClass int

const (
	// HaltNone is the zero value: no halt was recorded. Halt.String()
	// renders it as "".
	HaltNone HaltClass = iota
	HaltOperatorStop
	HaltChildSignalled
	HaltChildHostTainted
	HaltChildConfigInvalid
	// HaltSelfChanged: the daemon attribute's store path at the fetched tip
	// no longer matches the running build (see Config.SelfProgram).
	// Recorded in drain mode only, once the successor's prebuild passes
	// (issue #5012); exit 10 plus a service restart policy loads the new
	// build. Under --restart-in-place (ADR 0063) no halt is recorded.
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
	// HaltFeatureBranchGone: the remote definitively answered that the
	// daemon's --feature-branch no longer exists (FeatureBranchGoneError).
	// The campaign is over, not a blip a retry could fix, so the pool halts
	// rather than backing off.
	HaltFeatureBranchGone

	// haltClassCount sits after every real class so
	// TestHaltRenderings_CoversEveryClass catches a new class with no
	// haltRenderings row, which would otherwise compile and render a silent
	// "" / exit 1.
	haltClassCount
)

// haltRendering is one HaltClass's documented rendering. Halt.String(),
// Halt.Diagnostic() and Halt.ExitCode() all read this one table so they
// can't drift the way parallel switches can.
type haltRendering struct {
	// name is the class's Go identifier, kept in step by hand: nothing
	// asserts the two match.
	name   string
	reason string
	// detailed: reason is a prefix joined to Halt.Detail with ": ", not
	// the whole reason.
	detailed bool
	// bareDiagnostic: the daemon prints Detail alone on stderr instead of
	// String()'s prefixed form, while the halt event still carries the prefix.
	bareDiagnostic bool
	exit           int
}

var haltRenderings = map[HaltClass]haltRendering{
	HaltNone:               {name: "HaltNone", reason: "", detailed: false, bareDiagnostic: false, exit: 1},
	HaltOperatorStop:       {name: "HaltOperatorStop", reason: "context-cancelled", detailed: true, bareDiagnostic: false, exit: 0},
	HaltChildSignalled:     {name: "HaltChildSignalled", reason: "outcome: signalled-stop", detailed: false, bareDiagnostic: false, exit: 0},
	HaltChildHostTainted:   {name: "HaltChildHostTainted", reason: "outcome: host-tainted", detailed: false, bareDiagnostic: false, exit: 1},
	HaltChildConfigInvalid: {name: "HaltChildConfigInvalid", reason: "outcome: config-invalid", detailed: false, bareDiagnostic: false, exit: 1},
	HaltSelfChanged:        {name: "HaltSelfChanged", reason: "self-changed", detailed: true, bareDiagnostic: false, exit: ExitSelfChanged},
	HaltSelfBuild:          {name: "HaltSelfBuild", reason: "self-build", detailed: true, bareDiagnostic: false, exit: 1},
	HaltBreaker:            {name: "HaltBreaker", reason: "breaker", detailed: true, bareDiagnostic: false, exit: 1},
	HaltInvalidConfig:      {name: "HaltInvalidConfig", reason: "config-invalid", detailed: true, bareDiagnostic: false, exit: 1},
	HaltPreflight:          {name: "HaltPreflight", reason: "preflight", detailed: true, bareDiagnostic: false, exit: ExitPreflightFailed},
	HaltInstanceLock:       {name: "HaltInstanceLock", reason: "instance-lock", detailed: true, bareDiagnostic: true, exit: 1},
	HaltFeatureBranchGone:  {name: "HaltFeatureBranchGone", reason: "feature-branch-gone", detailed: true, bareDiagnostic: false, exit: ExitFeatureBranchGone},
}

// String returns c's Go identifier name (e.g. "HaltSelfChanged"), not the
// operator-facing grammar (Halt.String()). It exists so a %v in a test
// failure names the class.
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
	// Handoff is set, with Class HaltNone, when the pool handed its busy slots
	// off for an in-place restart (ADR 0063) instead of halting.
	Handoff *Handoff
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

// Diagnostic returns the text the daemon prints after "daemon: " on stderr
// for a pre-loop halt: String(), except for a class whose row sets
// bareDiagnostic, which prints Detail as-is.
func (h Halt) Diagnostic() string {
	if row, ok := haltRenderings[h.Class]; ok && row.bareDiagnostic {
		return h.Detail
	}
	return h.String()
}

// ExitCode maps h to this process's exit code. HaltOperatorStop and
// HaltChildSignalled are the only clean stops (0); the other child halts
// (host-tainted, config-invalid) are real failures. HaltSelfChanged,
// HaltPreflight, and HaltFeatureBranchGone carry dedicated codes so a
// supervisor can tell them apart. Every other class exits 1, HaltNone
// included: an unclassified halt must not silently exit 0.
func (h Halt) ExitCode() int {
	row, ok := haltRenderings[h.Class]
	if !ok {
		return 1
	}
	return row.exit
}

// Event renders h as the daemon's "halt" event record; a pre-pool halt's
// zero Kind/Revision are omitted.
func (h Halt) Event() Event {
	return Event{Event: report.EventHalt, Kind: h.Kind, Revision: h.Revision, Reason: h.String()}
}

// ExitSelfChanged is the daemon's exit code for a build changed at the
// fetched tip, distinct from every other code so a service unit can restart
// on it alone. It sits outside the child launcher's 0-7 band (Interpret) so
// the two taxonomies cannot be confused in one log.
const ExitSelfChanged = 10

// ExitPreflightFailed is the daemon's exit code for a refused start: the
// startup doctor preflight found a Required-tier failure or could not run.
// It is 11, sharing neither doctor's 1-4 nor the child launcher's 0-7 band,
// so it cannot be misattributed. A restart cannot clear it (a missing label,
// an undersized VM), so a supervisor must not treat it as retryable.
const ExitPreflightFailed = 11

// ExitFeatureBranchGone is the daemon's own exit code for a --feature-branch
// that no longer exists on origin (see FeatureBranchGoneError and
// HaltFeatureBranchGone for the rationale). Distinct from ExitSelfChanged
// (10) so a restart wrapper can tell the two apart; like ExitPreflightFailed,
// not one a plain restart clears.
const ExitFeatureBranchGone = 12
