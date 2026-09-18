package driver

import "spindrift.dev/launcher/internal/driver/driverkit"

// Class says whether a non-zero agent exit is retryable. This and the names
// below are true type aliases, so driverkit owns the Driver seam's vocabulary
// and every strategy's values are identical by construction.
type Class = driverkit.Class

const (
	// Transient means the agent never got a fair chance (rate limit, API
	// overload, network blip), so the run can be retried.
	Transient = driverkit.Transient
	// Terminal means the agent ran but produced no valid result, or hit an
	// unrecoverable error.
	Terminal = driverkit.Terminal
)

// Reason identifies the specific cause of a classified exit.
type Reason = driverkit.Reason

const (
	RateLimit       = driverkit.RateLimit       // API rate limit
	Overloaded      = driverkit.Overloaded      // API overload / capacity error
	Network         = driverkit.Network         // transient network failure
	TaskFailed      = driverkit.TaskFailed      // agent ran but produced no valid result
	UnsupportedFlag = driverkit.UnsupportedFlag // driver rejected a CLI option we passed (version skew)
)

// Classification is the result of a Driver's ClassifyTransient. Each strategy
// translates its own tool's error taxonomy into these Class and Reason values
// at its own boundary (ADR 0009).
type Classification = driverkit.Classification
