package forge

import (
	"sync"
)

// core holds the Fake fields shared across multiple capability files. A
// member is admitted to core only when it has writers in two or more
// different capabilities (e.g. fake_codeforge.go, fake_prforge.go,
// fake_tracker.go) — a field written by exactly one capability stays local
// to that capability's file instead (issue #2358).
type core struct {
	mu sync.Mutex

	prStates map[string]PRState // URL → canonical PR state

	// LandingCallLog records, in order, every call to MarkReady, MarkDraft,
	// Merge, and EnqueueAutoMerge as "Method:url" — the landing-path methods
	// a caller can reorder relative to each other. A per-method Calls slice
	// alone can't distinguish "MarkReady then Merge" from "Merge then
	// MarkReady": both leave the same final Calls-slice contents, so a test
	// asserting call presence on each slice separately passes either way.
	// This single, cross-method log is what lets a test assert genuine
	// ordering (issue #1651's "ready-flip precedes the merge/enqueue call").
	LandingCallLog []string

	// ProbeErr, if non-nil, is returned by Probe. Use ErrAuthFailure or
	// ErrRepoNotFound to simulate specific failure modes.
	ProbeErr error
	// ProbeRepo is the resolved repo slug returned by Probe on success.
	ProbeRepo string
}
