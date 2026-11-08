package forge

import (
	"sync"

	"spindrift.dev/launcher/internal/fakehook"
)

// core holds the Fake fields shared across multiple capability files. A field
// belongs here only when two or more capabilities write it; a field written by
// exactly one capability stays in that capability's file (issue #2358).
type core struct {
	// Hooks lets a test run a callback inside a named fake method (Rebase,
	// Merge, CheckState, MarkReady, ListPRFiles, RelayBundle, OpenPRForBranch,
	// TransitionState, Comment, CreateDraftPR); see fakehook. A hook fires at
	// the top of the method, before the fake records the call.
	fakehook.Hooks

	mu sync.Mutex

	prStates map[string]PRState // Maps a PR URL to its canonical state.

	// LandingCallLog records every MarkReady, MarkDraft, Merge, and
	// EnqueueAutoMerge call in order as "Method:url". Per-method Calls slices
	// can't tell "MarkReady then Merge" from "Merge then MarkReady", so only
	// this cross-method log can assert ordering (issue #1651).
	LandingCallLog []string

	// ProbeErr, when non-nil, is the error Probe returns. Use ErrAuthFailure
	// or ErrRepoNotFound to simulate specific failure modes.
	ProbeErr error
	// ProbeRepo is the resolved repo slug Probe returns on success.
	ProbeRepo string
}
