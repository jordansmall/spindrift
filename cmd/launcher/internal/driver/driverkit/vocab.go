// Package driverkit is the shared substrate under the Driver seam (ADR
// 0009). Both Driver strategy subpackages (claude, opencode), the driver
// package, and the orchestrator import it, so the transient-classification
// vocabulary, pattern matching, NDJSON line framing, the log-scan degrade
// contract, and the role constants are declared once instead of
// hand-mirrored across every Driver strategy.
package driverkit

import "time"

// Class describes whether a non-zero agent exit is retryable or not.
type Class string

const (
	// Transient exits are retryable infrastructure failures — the agent never
	// got a fair chance (rate limit, API overload, network blip).
	Transient Class = "transient"
	// Terminal exits are genuine task failures — the agent ran but produced
	// no valid result, or encountered an unrecoverable error.
	Terminal Class = "terminal"
)

// Reason identifies the specific cause of a classified exit.
type Reason string

const (
	RateLimit       Reason = "rateLimit"       // API rate limit
	Overloaded      Reason = "overloaded"      // API overload / capacity error
	Network         Reason = "network"         // transient network failure
	TaskFailed      Reason = "taskFailed"      // agent ran but produced no valid result
	UnsupportedFlag Reason = "unsupportedFlag" // driver rejected a CLI option we passed (version skew)
)

// Classification is the result of a Driver's ClassifyTransient, in this
// Driver seam's shared vocabulary — every Driver strategy reports through
// these Class/Reason values, translating its own tool's error taxonomy at
// its own boundary (ADR 0009).
type Classification struct {
	Class   Class
	Reason  Reason
	ResetAt *time.Time // non-nil only for RateLimit with a known reset time
}
