package daemon

import "fmt"

// PreflightVerdict is ClassifyPreflight's result: whether the host may run
// at all, and — when it may not — what an operator needs to know and do
// about it.
type PreflightVerdict struct {
	Exit    int    // the doctor exit code this verdict was read from
	Outcome string // stable event-stream label, always "doctor-"-prefixed
	Healthy bool   // may the daemon start
	Detail  string // what failed, in operator terms; empty when Healthy
	Remedy  string // what to do about it; empty when Healthy
}

// ClassifyPreflight maps a `doctor` exit code to the daemon's own startup
// preflight verdict. This is deliberately a second, separate mapping
// function rather than a case added to Interpret: Interpret's table is a
// *child dispatch's* exit-code contract (0-7, cmd/launcher/main.go's
// exitCodeFor), while doctor's exit codes (0/1/2/3/4, this package's table
// sourced from cmd/launcher/doctor.go's doctorExitCodeFor) are a wholly
// different process's contract that happens to share small integers with
// Interpret's. Folding both into one function would make it possible for a
// caller to misread, say, doctor's exit 4 as Interpret's exit 4
// ("image-stale") instead of "required labels missing". Every Outcome
// label below carries a "doctor-" prefix precisely so that confusion is
// impossible even downstream, in the event stream itself: a "preflight"
// event's outcome can never collide with a dispatched child's outcome.
func ClassifyPreflight(exit int) PreflightVerdict {
	switch exit {
	case 0:
		return PreflightVerdict{Exit: exit, Outcome: "doctor-healthy", Healthy: true}
	case 1:
		return PreflightVerdict{
			Exit:    exit,
			Outcome: "doctor-unclassified",
			Detail:  "doctor failed in a way it could not classify",
			Remedy:  "read the doctor report above for the actual cause",
		}
	case 2:
		return PreflightVerdict{
			Exit:    exit,
			Outcome: "doctor-config-invalid",
			Detail:  "the configuration (including the podman machine's RAM against MEMORY_LIMIT x MAX_PARALLEL) is invalid",
			Remedy:  "run `spindrift doctor` interactively to see and fix what's wrong before starting the daemon again",
		}
	case 3:
		return PreflightVerdict{
			Exit:    exit,
			Outcome: "doctor-connectivity",
			Detail:  "auth or connectivity to the issue tracker or code forge is failing",
			Remedy:  "check credentials and network access, then run `spindrift doctor` interactively to confirm before starting the daemon again",
		}
	case 4:
		return PreflightVerdict{
			Exit:    exit,
			Outcome: "doctor-required-labels-missing",
			Detail:  "required triage labels are missing",
			Remedy:  "create the four triage labels on the target repo, or run `spindrift doctor` interactively to create them — until then every claim fails and the daemon would run doing nothing",
		}
	default:
		return PreflightVerdict{
			Exit:    exit,
			Outcome: "doctor-unknown",
			Detail:  fmt.Sprintf("doctor exited %d, a code this daemon does not recognise", exit),
			Remedy:  "read the doctor report above for the actual cause",
		}
	}
}

// Halt renders v as a typed Halt. The zero Halt (class HaltNone, which
// String() renders "") on a healthy verdict keeps the same "never call this
// on a healthy verdict" contract the string-returning version carried,
// without a second special case.
func (v PreflightVerdict) Halt() Halt {
	if v.Healthy {
		return Halt{}
	}
	return Halt{Class: HaltPreflight, Detail: fmt.Sprintf("doctor exit %d: %s — remedy: %s", v.Exit, v.Detail, v.Remedy)}
}
