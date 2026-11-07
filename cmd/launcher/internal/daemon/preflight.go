package daemon

import (
	"fmt"

	"spindrift.dev/launcher/internal/exitcode"
)

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

// ClassifyPreflight maps a `doctor` exit code to the daemon's startup
// preflight verdict by reading exitcode.DoctorTable — a separate table from
// Interpret's, since the two processes share small integers with different
// meanings. A code the table does not know is "doctor-unknown".
func ClassifyPreflight(exit int) PreflightVerdict {
	if row, ok := exitcode.LookupDoctor(exit); ok {
		return PreflightVerdict{Exit: exit, Outcome: row.Outcome, Healthy: row.Healthy, Detail: row.Detail, Remedy: row.Remedy}
	}
	return PreflightVerdict{
		Exit:    exit,
		Outcome: "doctor-unknown",
		Detail:  fmt.Sprintf("doctor exited %d, a code this daemon does not recognise", exit),
		Remedy:  "read the doctor report above for the actual cause",
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
