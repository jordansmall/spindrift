package driver

import "spindrift.dev/launcher/internal/driver/driverkit"

// Class describes whether a non-zero agent exit is retryable or not. It is
// a true alias of driverkit.Class: this package's shared Driver-seam
// vocabulary is sourced from driverkit, not declared locally.
type Class = driverkit.Class

const (
	// Transient exits are retryable infrastructure failures — the agent never
	// got a fair chance (rate limit, API overload, network blip).
	Transient = driverkit.Transient
	// Terminal exits are genuine task failures — the agent ran but produced
	// no valid result, or encountered an unrecoverable error.
	Terminal = driverkit.Terminal
)

// Reason identifies the specific cause of a classified exit. It is a true
// alias of driverkit.Reason.
type Reason = driverkit.Reason

const (
	RateLimit  = driverkit.RateLimit  // API rate limit
	Overloaded = driverkit.Overloaded // API overload / capacity error
	Network    = driverkit.Network    // transient network failure
	TaskFailed = driverkit.TaskFailed // agent ran but produced no valid result
)

// Classification is the result of a Driver's ClassifyTransient, in this
// Driver seam's shared vocabulary — every Driver strategy reports through
// these Class/Reason values, translating its own tool's error taxonomy at
// its own boundary (ADR 0009). It is a true alias of driverkit.Classification,
// so every Driver strategy's Classification is identical by construction.
type Classification = driverkit.Classification
