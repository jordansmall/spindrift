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
)

// Config carries the subset of launcher config a Settle needs.
type Config struct {
	// BranchPrefix builds an issue's agent branch name (BranchPrefix + num)
	// for PR discovery when a box exits with no outcome line.
	BranchPrefix string

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
// point with its config and forge client, then reused across every issue in
// that invocation. Safe for concurrent use across fanOut goroutines because
// it holds no mutable state of its own beyond the (concurrency-safe) fc.
type Settle struct {
	cfg Config
	fc  forge.Client
}

var _ Settler = (*Settle)(nil)

// New constructs a Settle.
func New(cfg Config, fc forge.Client) *Settle {
	return &Settle{cfg: cfg, fc: fc}
}
