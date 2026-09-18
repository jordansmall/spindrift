// Package driverkit holds the vocabulary shared across the Driver seam (ADR
// 0009): transient classification, pattern matching, NDJSON line framing, the
// log-scan degrade contract, and the role constants. Declaring them here keeps
// every Driver strategy from hand-mirroring its own copy.
package driverkit

import "time"

// Class describes whether a non-zero agent exit is retryable or not.
type Class string

const (
	// Transient exits are retryable infrastructure failures. The agent never got
	// a fair chance: rate limit, API overload, network blip.
	Transient Class = "transient"
	// Terminal exits are genuine task failures. The agent ran but produced no
	// valid result, or hit an unrecoverable error.
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

// AllReasons enumerates every Reason this package declares, in declaration
// order. A test walks this slice against the re-exports in
// driver.classification.go, so a Reason added here without an alias there
// fails that test instead of vanishing from launcher-facing code (issue #2269).
var AllReasons = []Reason{RateLimit, Overloaded, Network, TaskFailed, UnsupportedFlag}

// Classification is the result of a Driver's ClassifyTransient. Every Driver
// strategy translates its own tool's error taxonomy into these values at its
// own boundary (ADR 0009).
type Classification struct {
	Class   Class
	Reason  Reason
	ResetAt *time.Time // non-nil only for RateLimit with a known reset time
}
