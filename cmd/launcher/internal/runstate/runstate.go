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

// RunState is the compact run-state handoff artifact the orchestrator owns
// (issue #1997, parent #1627): a tmp file outside the repo -- the same
// durability convention SCOUT's /tmp/brief.md already relies on to survive
// context compaction -- recording what a fresh implementor pass needs to
// continue without reading a transcript. Anything not written here is lost
// between passes, so these are exactly the fields a later slice can depend
// on; everything else (raw driver output, prompt text, reviewer prose beyond
// the verdict word) is intentionally not carried and must be re-derived or
// re-scoped into a new prompt instead.
type RunState struct {
	// DoneSlices names each implementor slice that has already landed, in
	// completion order. Dispatch-internal bookkeeping only (issue #2059's
	// parallel worker dedup safety mechanism in orchestrator/dispatch.go,
	// which skips re-dispatching a slice already present here) -- not part
	// of the implementor-facing seeded-prompt handoff (issue #2549 retired
	// its narrative render in favor of WorkerFindings' richer prose).
	DoneSlices []string `json:"done_slices"`
	// RemainingSlices names each implementor slice still to run, in the
	// order the loop intends to take them. Dispatch-internal bookkeeping
	// only, same as DoneSlices above -- not part of the implementor-facing
	// seeded-prompt handoff (issue #2549).
	RemainingSlices []string `json:"remaining_slices"`
	// LastVerdict is the most recent reviewer verdict word (e.g. "APPROVE"
	// or "BLOCK"), empty when no review has run yet.
	LastVerdict string `json:"last_verdict"`
	// ScoutBriefPath is the path to the scout's brief (conventionally
	// /tmp/brief.md) -- referenced by path, not inlined, so this file stays
	// small and the brief stays the single source of truth for scout
	// findings.
	ScoutBriefPath string `json:"scout_brief_path"`
	// PassSummaryPath is the path to the most recent implement/fix pass's own
	// free-form summary of what it did and what remains (issue #2549) --
	// referenced by path, not inlined, mirroring ScoutBriefPath's own
	// convention -- so a later fix pass resumes from what was actually done
	// instead of re-deriving it from a transcript.
	PassSummaryPath string `json:"pass_summary_path"`
	// ReviewFindings is the code-owned review pass's own final message --
	// the "VERDICT: ..." line plus its Blocking/Non-blocking sections,
	// verbatim -- recorded here (distinct from the bare LastVerdict word)
	// so the next fix pass's seeded prompt can carry the reviewer's actual
	// findings, not just the fact that it blocked (issue #2037).
	ReviewFindings string `json:"review_findings"`
	// TerminalLand is true once this run has committed to running its one
	// allowed terminal land pass -- set when a cap (maxSlices,
	// maxReviewRounds, or a review pass producing no verdict) would
	// otherwise stop the loop with no terminal outcome. Instead of exiting
	// outcome-less, the loop runs exactly one more implement/fix-role pass
	// seeded with this flag, so a run that exhausts its budget still lands
	// and reports an honest outcome rather than stopping silently (issue
	// #2457).
	TerminalLand bool `json:"terminal_land"`
	// CapFired is a short human-readable name of which cap or condition
	// triggered TerminalLand (e.g. "max slices reached", "max review
	// rounds reached", "no verdict") -- carried into the seeded prompt so
	// the terminal pass can name the reason it is running instead of the
	// usual next step. Empty when TerminalLand is false (issue #2457).
	CapFired string `json:"cap_fired"`
	// WorkerFindings summarizes the last parallel worker dispatch's
	// per-slice outcome (issue #2059) -- one line per slice, e.g.
	// "slice-a: done", "slice-b: timed out", "slice-c: crashed (exit 1)" --
	// seeded into the next pass's prompt the same way ReviewFindings seeds a
	// fix pass, so the coordinator can see what happened without reading any
	// worker's own transcript.
	WorkerFindings string `json:"worker_findings,omitempty"`
	// FindingsLogPath is the path to the per-run findings log (issue #2552):
	// every review round's own findings text, appended one "## Round N"
	// section at a time, so a later pass can dedupe and file the union across
	// every round instead of only the last one (state.ReviewFindings above
	// still carries only the last round's, unchanged, for that field's
	// existing consumers). Empty until the first review round appends to it.
	FindingsLogPath string `json:"findings_log_path,omitempty"`
}

// IsEmpty reports whether s carries nothing worth seeding into a fresh
// pass's PROMPT specifically -- the zero value, or the common cold-start
// case where no prior pass has left any handoff behind. Not an
// all-fields-empty check: DoneSlices/RemainingSlices are excluded below on
// purpose, since they're dispatch-internal bookkeeping never rendered into
// a prompt (see the comment in the method body). Kept on RunState itself so
// a new prompt-relevant field only needs to be added here once, rather than
// at every caller that otherwise open-codes its own check
// (seedPromptFromState's own check, before this method existed, was one
// such site).
func (s RunState) IsEmpty() bool {
	// DoneSlices/RemainingSlices are deliberately excluded: they are
	// dispatch-internal bookkeeping only (issue #2059's dedup mechanism),
	// not part of the implementor-facing seeded-prompt handoff, and are
	// never rendered by seedPromptFromState (issue #2549).
	return s.LastVerdict == "" &&
		s.ScoutBriefPath == "" &&
		s.PassSummaryPath == "" &&
		s.ReviewFindings == "" &&
		s.WorkerFindings == "" &&
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
// It writes to a temp file in the same directory as path and renames it into
// place, rather than truncating path directly: once the multi-pass loop
// lands (S3+ of #1627), LastVerdict and friends carry real handoff data
// between passes, and a kill mid-write (OOM, SIGKILL, host preemption)
// against an in-place truncate would leave invalid JSON at path,
// silently discarding a prior pass's progress. The temp-file-then-rename
// pattern keeps a kill mid-write from ever producing a half-written file at
// path: it either leaves the old valid file untouched (killed before rename)
// or an orphaned temp file (killed after the temp write but before rename).
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
