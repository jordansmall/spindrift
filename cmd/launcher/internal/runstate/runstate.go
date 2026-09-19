// Package runstate owns the RunState handoff artifact passed between passes.
package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RunState is the run-state handoff artifact the orchestrator owns (issue
// #1997, parent #1627), written to a tmp file outside the repo so it survives
// context compaction. Anything not recorded here is lost between passes, so
// raw driver output, prompt text, and reviewer prose beyond the verdict word
// must be re-derived or re-scoped into a new prompt instead.
type RunState struct {
	// DoneSlices names each implementor slice that has already landed, in
	// completion order. Dispatch-internal bookkeeping that nothing reads any
	// more (the parallel worker dedup mechanism that consumed it is gone),
	// kept on the wire because released handoff artifacts carry it. Never
	// rendered into a seeded prompt (issue #2549).
	DoneSlices []string `json:"done_slices"`
	// RemainingSlices names each implementor slice still to run, in loop
	// order. Dispatch-internal bookkeeping only, same as DoneSlices above
	// (issue #2549).
	RemainingSlices []string `json:"remaining_slices"`
	// LastVerdict is the most recent reviewer verdict word (e.g. "APPROVE"
	// or "BLOCK"), empty when no review has run yet.
	LastVerdict string `json:"last_verdict"`
	// ScoutBriefPath is the path to the scout's brief (conventionally
	// /tmp/brief.md), referenced by path rather than inlined so this file
	// stays small and the brief stays the single source of scout findings.
	ScoutBriefPath string `json:"scout_brief_path"`
	// PassSummaryPath is the path to the most recent implement/fix pass's
	// free-form summary of what it did and what remains (issue #2549).
	PassSummaryPath string `json:"pass_summary_path"`
	// DispositionsPath is the path to the fix pass's per-finding dispositions
	// file (issue #2550), one terse line per finding. Deliberately separate
	// from PassSummaryPath so review-prompt seeding never has to parse
	// narrative out of a summary file to find dispositions.
	DispositionsPath string `json:"dispositions_path"`
	// DispositionsLogPath is the path to the per-run, append-only
	// dispositions log (issue #2550): each fix pass's DispositionsPath
	// content appended as one "## Round N" section, never collapsed, so
	// seedReviewPromptFromState sees every round. Entries must stay terse
	// references, since the log grows for the life of the run.
	DispositionsLogPath string `json:"dispositions_log_path,omitempty"`
	// ReviewedCommitAnchor is the commit SHA the orchestrator's repo workdir
	// was at when the most recent review pass finished (issue #2551).
	// seedReviewPromptFromState uses it on a round-N (N>1) pass to focus the
	// reviewer on the range since that commit. A missing or invalid value
	// degrades to a full review and is never an error.
	ReviewedCommitAnchor string `json:"reviewed_commit_anchor,omitempty"`
	// DecisionsPath is the path to the implement/fix pass's per-decision file
	// (issue #2695), terse lines naming what was chosen, what was rejected,
	// and the constraint that drove it.
	DecisionsPath string `json:"decisions_path"`
	// DecisionsLogPath is the path to the per-run, append-only decisions
	// log (issue #2695): each implement/fix pass's DecisionsPath content
	// appended as one "## Round N" section, under the same terse-entry
	// contract as DispositionsLogPath. Excluded from IsEmpty below because
	// seedPromptFromState does its own fresh read of this file.
	DecisionsLogPath string `json:"decisions_log_path,omitempty"`
	// ReviewFindings is the review pass's final message verbatim, the
	// "VERDICT: ..." line plus its Blocking/Non-blocking sections, so the
	// next fix pass's seeded prompt carries the findings themselves rather
	// than only the bare LastVerdict word (issue #2037).
	ReviewFindings string `json:"review_findings"`
	// TerminalLand is true once the run has committed to its one allowed
	// terminal land pass, set when a cap (maxSlices, maxReviewRounds, or a
	// review pass producing no verdict) would otherwise stop the loop with no
	// terminal outcome. The loop then runs exactly one more implement/fix
	// pass seeded with this flag so the run still lands (issue #2457).
	TerminalLand bool `json:"terminal_land"`
	// CapFired names which cap triggered TerminalLand (e.g. "max slices
	// reached", "no verdict"), carried into the seeded prompt so the terminal
	// pass can say why it is running. Empty when TerminalLand is false
	// (issue #2457).
	CapFired string `json:"cap_fired"`
	// FindingsLogPath is the path to the per-run findings log (issue #2552):
	// each review round's findings appended as one "## Round N" section, so a
	// later pass can dedupe and file the union across rounds. ReviewFindings
	// above still carries only the last round's, for its existing consumers.
	FindingsLogPath string `json:"findings_log_path,omitempty"`
}

// IsEmpty reports whether s carries nothing worth seeding into a fresh pass's
// prompt. Not an all-fields-empty check: the fields the body excludes are
// either dispatch-internal or read by a different seeding path, and counting
// them would render a "Run-state handoff" section with no bullets in it.
func (s RunState) IsEmpty() bool {
	// DoneSlices/RemainingSlices are dispatch-internal bookkeeping (issue
	// #2059) that seedPromptFromState never renders (issue #2549).
	// DispositionsPath, DispositionsLogPath and ReviewedCommitAnchor are read
	// only by seedReviewPromptFromState's narrower check (issue #2550), and
	// seedPromptFromState freshly reads the decisions files (issue #2695).
	return s.LastVerdict == "" &&
		s.ScoutBriefPath == "" &&
		s.PassSummaryPath == "" &&
		s.ReviewFindings == "" &&
		s.FindingsLogPath == "" &&
		!s.TerminalLand
}

// ReadRunState reads and parses the run-state artifact at path. An empty path
// or a path with no file yet (the first pass of a run) returns a zero RunState
// and no error.
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

// WriteRunState writes s to path as indented JSON. A no-op when path is empty.
// It writes a temp file in path's directory and renames it into place: a kill
// mid-write (OOM, SIGKILL, host preemption) against an in-place truncate would
// leave invalid JSON at path and silently discard a prior pass's progress. The
// rename leaves the old valid file or an orphan temp, never a half-written one.
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
