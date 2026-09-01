// Package runstate owns the RunState handoff artifact; see RunState for
// what it carries and why.
package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RunState is the compact run-state handoff artifact the orchestrator owns: a
// tmp file outside the repo -- the same durability convention SCOUT's
// /tmp/brief.md relies on to survive context compaction -- recording what a
// fresh implementor pass needs to continue without reading a transcript.
// Anything not written here is lost between passes; raw driver output, prompt
// text, and reviewer prose beyond the verdict word are deliberately not
// carried and must be re-derived or re-scoped into a new prompt.
//
// Every *Path field is referenced by path, never inlined, so this artifact
// stays small and the referenced file stays the single source of truth. Every
// *LogPath field is per-run and append-only, one "## Round N" section per
// pass, and never drops or collapses an entry -- safe only because
// review-loop-orchestrator.md's contract keeps every entry a terse reference
// (commit SHA, file path, issue number) rather than restated
// diff/file/transcript content. Log paths are empty until the first pass
// appends to one.
type RunState struct {
	// DoneSlices and RemainingSlices are dispatch-internal bookkeeping only,
	// never rendered into an implementor-facing prompt. Currently
	// write-nothing/carry-forward: the parallel worker dedup mechanism that
	// consumed them is gone, but they stay on the wire because released
	// handoff artifacts carry them.
	DoneSlices      []string `json:"done_slices"`
	RemainingSlices []string `json:"remaining_slices"`
	// LastVerdict is the most recent reviewer verdict word (e.g. "APPROVE"
	// or "BLOCK"), empty when no review has run yet.
	LastVerdict string `json:"last_verdict"`
	// ScoutBriefPath is the scout's brief, conventionally /tmp/brief.md.
	ScoutBriefPath string `json:"scout_brief_path"`
	// PassSummaryPath is the most recent implement/fix pass's free-form
	// summary of what it did and what remains.
	PassSummaryPath string `json:"pass_summary_path"`
	// DispositionsPath holds the fix pass's terse per-finding lines (e.g.
	// "finding -> fixed in commit X" or "finding -> won't-fix: reason").
	// Deliberately a separate file from PassSummaryPath so review-prompt
	// seeding never has to parse dispositions out of narrative.
	DispositionsPath string `json:"dispositions_path"`
	// DispositionsLogPath accumulates every fix pass's DispositionsPath
	// content. seedReviewPromptFromState reads this log rather than the
	// latest DispositionsPath alone, so a round-N review pass sees every
	// won't-fix decided so far.
	DispositionsLogPath string `json:"dispositions_log_path,omitempty"`
	// ReviewedCommitAnchor is the commit SHA the orchestrator's repo workdir
	// was at when the most recent review pass ran. A round-N (N>1) review
	// focuses its hunt on the range since this commit, with the full branch
	// diff still available. Missing or invalid degrades to a full review --
	// never an error. Empty until the first review pass records it.
	ReviewedCommitAnchor string `json:"reviewed_commit_anchor,omitempty"`
	// DecisionsPath holds the implement/fix pass's terse per-decision lines
	// (what was chosen, what was rejected, the constraint that drove it).
	DecisionsPath string `json:"decisions_path"`
	// DecisionsLogPath accumulates every implement/fix pass's DecisionsPath
	// content. Excluded from IsEmpty for the same reason as the dispositions
	// fields -- see the note in IsEmpty's body.
	DecisionsLogPath string `json:"decisions_log_path,omitempty"`
	// ReviewFindings is the review pass's final message -- the "VERDICT: ..."
	// line plus its Blocking/Non-blocking sections, verbatim -- so the next
	// fix pass's prompt carries the actual findings, not just the fact that
	// review blocked.
	ReviewFindings string `json:"review_findings"`
	// TerminalLand is true once this run has committed to its one allowed
	// terminal land pass, set when a cap (maxSlices, maxReviewRounds, or a
	// review pass producing no verdict) would otherwise stop the loop with no
	// terminal outcome. The loop then runs exactly one more implement/fix
	// pass so a run that exhausts its budget still lands and reports an
	// honest outcome rather than stopping silently.
	TerminalLand bool `json:"terminal_land"`
	// CapFired names which cap triggered TerminalLand (e.g. "max slices
	// reached", "no verdict"), carried into the seeded prompt so the terminal
	// pass can say why it is running. Empty when TerminalLand is false.
	CapFired string `json:"cap_fired"`
	// FindingsLogPath accumulates every review round's findings text, so a
	// later pass can dedupe and file the union across rounds rather than only
	// the last one (ReviewFindings still carries only the last round's).
	FindingsLogPath string `json:"findings_log_path,omitempty"`
}

// IsEmpty reports whether s carries nothing worth seeding into a fresh pass's
// prompt -- the zero value, or the cold-start case where no prior pass left a
// handoff behind. Deliberately not an all-fields-empty check.
func (s RunState) IsEmpty() bool {
	// Excluded on purpose: DoneSlices/RemainingSlices (dispatch-internal
	// bookkeeping, never rendered into a prompt) and the dispositions/
	// decisions fields, which seedPromptFromState -- IsEmpty's only caller --
	// never renders. Those are governed instead by the narrower checks in
	// seedReviewPromptFromState and by seedPromptFromState's own fresh read
	// of DecisionsLogPath before its early-return. Including any of them
	// would make IsEmpty return false for a state whose only set field is a
	// stale path, rendering a "Run-state handoff" section with no bullets.
	return s.LastVerdict == "" &&
		s.ScoutBriefPath == "" &&
		s.PassSummaryPath == "" &&
		s.ReviewFindings == "" &&
		s.FindingsLogPath == "" &&
		!s.TerminalLand
}

// ReadRunState reads and parses the run-state artifact at path. An empty
// path or a path with no file yet (the first pass of a run) returns a zero
// RunState with no error -- there is nothing to hand off yet, not a failure.
func ReadRunState(path string) (RunState, error) {
	if path == "" {
		return RunState{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RunState{}, nil
	}
	if err != nil {
		return RunState{}, fmt.Errorf("read run state %s: %w", path, err)
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return RunState{}, fmt.Errorf("parse run state %s: %w", path, err)
	}
	return s, nil
}

// WriteRunState writes s to path as indented JSON, for the next implementor
// pass (or a human) to inspect. A no-op when path is empty.
//
// It writes to a temp file in the same directory and renames it into place
// rather than truncating path: a kill mid-write (OOM, SIGKILL, host
// preemption) against an in-place truncate would leave invalid JSON at path,
// silently discarding a prior pass's progress. Rename leaves either the old
// valid file or an orphaned temp file, never a half-written path.
func WriteRunState(path string, s RunState) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".run-state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp run state: %w", err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp run state %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp run state %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp run state %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp run state %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename temp run state into %s: %w", path, err)
	}
	return nil
}
