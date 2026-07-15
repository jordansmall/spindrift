// Package settle drives a Dispatch from Box-exit to its terminal lifecycle
// state (issue #442): interpreting the Outcome line, watching CI, self-heal
// fix passes, the merge or push-only landing under MERGE_MODE and the Merge
// guard, merged-verification (tripwire), and usage-comment posting. The seam
// is Settler + Settle (the prod adapter, constructed once with its config and
// the forge client) + Fake.
package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/terminate"
)

// Config carries the subset of launcher config a Settle needs.
type Config struct {
	// MergeMode controls post-green behavior: "immediate" merges the PR,
	// "manual" leaves it open, "auto" enqueues GitHub's native auto-merge.
	MergeMode string

	// MergeGuardPaths is a comma-separated list of globs matched against
	// every changed path in the PR; a hit downgrades the merge to manual
	// regardless of MergeMode. Empty disables the guard.
	MergeGuardPaths string

	// CompleteLabel is the label name verifyMerged checks for on the tripwire
	// path.
	CompleteLabel string

	// Merge gate polling knobs.
	MergePollInterval int
	MergePollTimeout  int
	MaxFixAttempts    int
	MaxRebaseAttempts int
}

// Settler is the seam callers depend on so tests can inject a Fake instead of
// a real Settle.
type Settler interface {
	// Settle interprets result (a Dispatcher's Run outcome) and drives num to
	// its terminal label: CI-watch, self-heal fix passes via d, merge modes,
	// the Merge guard, merged-verification, and the usage comment.
	Settle(d dispatch.Dispatcher, num string, result dispatch.Result)

	// SettleAdopted runs the same merge gate as Settle, for an
	// already-discovered open non-draft PR with no outcome line (the
	// reconcile/recover entry point).
	SettleAdopted(d dispatch.Dispatcher, num, prURL string)
}

// Settle is the prod adapter: constructed once per top-level dispatch entry
// point with its config, IssueTracker, and CodeForge, then reused across
// every issue in that invocation. Safe for concurrent use across dispatchWave
// goroutines because it holds no mutable state of its own beyond the
// (concurrency-safe) it/cf.
type Settle struct {
	cfg Config
	it  forge.IssueTracker
	cf  forge.CodeForge
	// pr is cf's PRForge surface, resolved once at construction via a type
	// assertion — nil for the push-only git adapter. Callers branch on pr ==
	// nil instead of a removed PushOnly() flag.
	pr forge.PRForge
	// term is checked at every CI-watch/fix-pass/merge-gate loop checkpoint
	// so a Terminate (ADR 0024, issue #649) landing mid-settle is noticed and
	// abandoned instead of corrupting the issue's state after Terminate
	// already reclaimed it. Nil (every construction site but the Console's)
	// means "never terminated" — terminate.Registry is nil-safe.
	term *terminate.Registry
}

// SetTerminated wires reg as this Settle's termination registry — called
// once by the Console's launcher wiring (issue #649). A Settle with no
// registry set (every headless dispatch path) behaves exactly as before.
func (s *Settle) SetTerminated(reg *terminate.Registry) {
	s.term = reg
}

// terminated reports whether num has been marked terminated on s.term.
func (s *Settle) terminated(num string) bool {
	return s.term.Marked(num)
}

var _ Settler = (*Settle)(nil)

// New constructs a Settle. pr is resolved from cf once via a type assertion
// (nil when cf is push-only, e.g. the git adapter).
func New(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	pr, _ := cf.(forge.PRForge)
	return &Settle{cfg: cfg, it: it, cf: cf, pr: pr}
}
