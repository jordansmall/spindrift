// Package exitcode is the exit-code contract between a launcher child and the
// Daemon that drives it, plus doctor's separate one. Each code's meaning is one
// row in the relevant table.
//
// The two tables are deliberately distinct types: doctor's exit codes share
// small integers with a dispatched child's, so a Doctor can never be read as
// a Dispatch (doctor's 4 is "required labels missing", a child's is
// "image-stale").
//
// It is a leaf package: it states what each exit means, and the Daemon maps
// that onto its own richer types (HaltClass, Halt).
package exitcode

// Action is the daemon loop's next move after interpreting a child's exit
// code.
type Action int

const (
	Continue Action = iota
	Wait
	// HaltPool means the exit demands the whole pool halt, not just this
	// slot.
	HaltPool
	// Backoff means the exit is unclassified: back off this slot alone
	// (Config.FailureBackoff) and refill it, rather than halting the whole
	// pool — an unrecognised exit code from one child is not evidence the
	// rest of the pool is broken.
	Backoff
)

// Halt is which pool halt an exit renders as. It is independent of the
// Daemon's broader HaltClass, which cannot be imported here without a cycle;
// the Daemon maps each value to its own HaltClass.
type Halt int

const (
	NoHalt Halt = iota
	HaltHostTainted
	HaltConfigInvalid
	HaltSignalled
)

// Dispatch is a launcher child's exit code.
type Dispatch int

const (
	Dispatched Dispatch = iota
	Failed
	QueueEmpty
	// NoneDispatchable means work exists but every issue found was claimed
	// or overlap-deferred, distinct from QueueEmpty (no work exists at all):
	// the loop uses it to tell a pool-wide jam from a routine idle.
	NoneDispatchable
	ImageStale
	HostTainted
	ConfigInvalid
	SignalledStop
)

// DispatchRow is one dispatch exit code's meaning. Action and Halt ride the
// same row as Outcome so the halt class can never drift from the outcome
// label it names.
type DispatchRow struct {
	Code    Dispatch
	Name    string // the Code constant's identifier
	Outcome string // stable event-stream label
	Action  Action
	Halt    Halt // NoHalt unless Action is HaltPool

	// StopGated means Action and Halt hold only while the Daemon's stop
	// signal is closed. With it open the exit is an unclassified failure —
	// Backoff with NoHalt — because the same code then means someone else
	// signalled that child, not the operator (#3626).
	StopGated bool
}

// DispatchTable is indexed by Code: row i is exit code i.
var DispatchTable = []DispatchRow{
	{Code: Dispatched, Name: "Dispatched", Outcome: "dispatched", Action: Continue},
	{Code: Failed, Name: "Failed", Outcome: "error", Action: Backoff},
	{Code: QueueEmpty, Name: "QueueEmpty", Outcome: "queue-empty", Action: Wait},
	{Code: NoneDispatchable, Name: "NoneDispatchable", Outcome: "none-dispatchable", Action: Wait},
	// Every child here is born from its own evaluation at a freshly resolved
	// revision, so a stale image is answered by the next iteration's pin,
	// not by an orchestrated rebuild.
	{Code: ImageStale, Name: "ImageStale", Outcome: "image-stale", Action: Continue},
	{Code: HostTainted, Name: "HostTainted", Outcome: "host-tainted", Action: HaltPool, Halt: HaltHostTainted},
	{Code: ConfigInvalid, Name: "ConfigInvalid", Outcome: "config-invalid", Action: HaltPool, Halt: HaltConfigInvalid},
	{Code: SignalledStop, Name: "SignalledStop", Outcome: "signalled-stop", Action: HaltPool, Halt: HaltSignalled, StopGated: true},
}

// LookupDispatch returns the row for code. An unrecognised code is the Failed
// row: one child's odd exit is not evidence the rest of the pool is broken,
// so it backs off that slot and never halts.
func LookupDispatch(code int) DispatchRow {
	if code < 0 || code >= len(DispatchTable) {
		return DispatchTable[Failed]
	}
	return DispatchTable[code]
}

// Verdict is the row's Action and Halt given whether the Daemon's stop signal
// was already closed when the child exited.
func (r DispatchRow) Verdict(stopClosed bool) (Action, Halt) {
	if r.StopGated && !stopClosed {
		return Backoff, NoHalt
	}
	return r.Action, r.Halt
}

// Doctor is a `doctor` process's exit code.
type Doctor int

const (
	DoctorHealthy Doctor = iota
	DoctorUnclassified
	DoctorConfigInvalid
	DoctorConnectivity
	DoctorRequiredLabelsMissing
)

// DoctorRow is one doctor exit code's meaning for the Daemon's startup
// preflight.
type DoctorRow struct {
	Code    Doctor
	Name    string // the Code constant's identifier
	Outcome string // stable event-stream label
	Healthy bool   // may the daemon start
	Detail  string // what failed, in operator terms; empty when Healthy
	Remedy  string // what to do about it; empty when Healthy
}

// DoctorTable is indexed by Code. Every Outcome carries a "doctor-" prefix so
// a preflight event's outcome can never collide with a dispatched child's in
// the event stream.
var DoctorTable = []DoctorRow{
	{Code: DoctorHealthy, Name: "DoctorHealthy", Outcome: "doctor-healthy", Healthy: true},
	{
		Code:    DoctorUnclassified,
		Name:    "DoctorUnclassified",
		Outcome: "doctor-unclassified",
		Detail:  "doctor failed in a way it could not classify",
		Remedy:  "read the doctor report above for the actual cause",
	},
	{
		Code:    DoctorConfigInvalid,
		Name:    "DoctorConfigInvalid",
		Outcome: "doctor-config-invalid",
		Detail:  "the configuration (including the podman machine's RAM against MEMORY_LIMIT x MAX_PARALLEL) is invalid",
		Remedy:  "run `spindrift doctor` interactively to see and fix what's wrong before starting the daemon again",
	},
	{
		Code:    DoctorConnectivity,
		Name:    "DoctorConnectivity",
		Outcome: "doctor-connectivity",
		Detail:  "auth or connectivity to the issue tracker or code forge is failing",
		Remedy:  "check credentials and network access, then run `spindrift doctor` interactively to confirm before starting the daemon again",
	},
	{
		Code:    DoctorRequiredLabelsMissing,
		Name:    "DoctorRequiredLabelsMissing",
		Outcome: "doctor-required-labels-missing",
		Detail:  "required labels are missing",
		Remedy:  "create the missing labels named in the doctor report on the target repo, or run `spindrift doctor` interactively to create every missing label — until then the kinds that need them cannot claim or file",
	},
}

// LookupDoctor returns the row for code; ok is false for an unrecognised one,
// whose fallback wording belongs to the caller.
func LookupDoctor(code int) (DoctorRow, bool) {
	if code < 0 || code >= len(DoctorTable) {
		return DoctorRow{}, false
	}
	return DoctorTable[code], true
}
