package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/driver/claude"
)

// artifactSnapshot is the content hash seedAndInvokePass captures for a
// per-pass round-log handoff artifact path (cfg.dispositionsPath,
// cfg.decisionsPath) immediately before invoking a pass it deliberately left
// the file on disk for (the corresponding state field already non-empty
// going in -- see seedAndInvokePass's own doc comment); recordArtifactPath
// compares this against the same file's post-pass content hash to tell
// "this pass wrote a fresh file" apart from "this pass never touched the
// file at all". A content hash, rather than mtime+size, is required to
// correctly classify both a same-second same-size genuine rewrite
// (mtime+size would wrongly call it stale) and a byte-identical re-save
// (mtime+size would wrongly call it fresh) -- see issue #2982. nil when
// seedAndInvokePass took the other branch (nothing to snapshot). Shared by
// DispositionsPath and DecisionsPath tracking (issue #2550, #2695), which
// otherwise duplicated this snapshot-and-compare shape verbatim.
// PassSummaryPath is not one of these round-log artifacts (issue #2982 is
// scoped to dispositions/decisions/findings freshness only) and keeps its
// own mtime+size passSummarySnapshot instead -- see its doc comment.
type artifactSnapshot struct {
	hash [sha256.Size]byte
}

// snapshotArtifactIfPresent prepares path for this pass's own invocation,
// keyed off target -- the corresponding run-state field's value going in.
// An empty path is a no-op (the artifact is disabled for this run). When
// target == "" (nothing this round references the artifact), any stale file
// left over from a prior pass is removed outright and nil is returned --
// there is nothing to compare a fresh write against once the loop no longer
// expects the file to still be meaningful. Otherwise the file is
// deliberately left alone (a seeded prompt may just have told the agent to
// read it -- removing it here would delete it out from under that
// reference before the agent gets to read it) and its pre-pass content hash
// is snapshotted for the caller's later recordArtifactPath call to compare
// against, or nil if the file isn't present/readable to snapshot. A path
// that stats present but fails to read (e.g. a directory, or a permissions
// error) also returns nil here, deliberately -- see recordArtifactPath's own
// doc comment for why a read failure is treated the same as "nothing to
// compare against".
func snapshotArtifactIfPresent(path, target string) *artifactSnapshot {
	if path == "" {
		return nil
	}
	if target == "" {
		os.Remove(path)
		return nil
	}
	if content, readErr := os.ReadFile(path); readErr == nil {
		return &artifactSnapshot{hash: sha256.Sum256(content)}
	}
	return nil
}

// recordArtifactPath records path into *target only when this pass's own
// invocation actually left a fresh file there -- "fresh" meaning both
// present (a stat-confirmed fs.ErrNotExist clears *target instead; any
// other stat error, or an empty path, leaves the carried-forward value
// alone -- see artifactSnapshot's doc comment for why) and, when preStat is
// non-nil, its content hash differs from snapshotArtifactIfPresent's own
// pre-pass snapshot of the same path. A path that stats present but then
// fails to read (e.g. a directory, or a permissions error) falls through to
// *target = path -- classified fresh, the same fail-open choice a nil
// preStat gets -- because a read failure means the hash compare cannot
// prove no change happened, and silently dropping a real pass's artifact on
// an unprovable read error is the worse failure mode.
func recordArtifactPath(path string, target *string, preStat *artifactSnapshot) {
	if path == "" {
		return
	}
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if preStat != nil {
			if content, readErr := os.ReadFile(path); readErr == nil && sha256.Sum256(content) == preStat.hash {
				*target = ""
				return
			}
		}
		*target = path
	case errors.Is(statErr, fs.ErrNotExist):
		*target = ""
	}
}

// roundLog owns the append-fresh-round-to-log and budget-tripwire behavior
// shared by the orchestrator's three per-round artifacts -- dispositions,
// decisions, and findings (issue #2982) -- so a tripwire or log-append bug
// is fixed once instead of three times. A zero meanCeiling and totalCeiling
// disables the budget tripwire entirely (findings' own instance carries no
// ceiling: reviewer text was never budget-tripwired).
type roundLog struct {
	// phase names this log for op/error-message purposes: "dispositions",
	// "decisions", or "findings". Failures emit a run_state_error op with
	// phase+"_log" (append/read failure) or phase+"_budget" (budget
	// exceeded).
	phase string
	// tempPattern is the os.CreateTemp pattern used to create the backing
	// log file the first time a round is appended.
	tempPattern string
	// meanCeiling/totalCeiling gate this log's own token-budget tripwire.
	// Both <= 0 disables the tripwire entirely.
	meanCeiling, totalCeiling int
}

// checkBudget reports content's own mean and total estimated tokens -- one
// entry per non-empty line -- and whether either rl.meanCeiling or
// rl.totalCeiling is exceeded, mirroring the pre-#2982
// checkDispositionsTokenBudget/checkDecisionsTokenBudget pure functions'
// logic exactly. Both ceilings <= 0 (findings' own instance) skips the work
// entirely and returns exceeded=false: a zero ceiling means the tripwire is
// disabled. Empty content (no entries) never exceeds either budget; there
// is nothing to measure.
func (rl roundLog) checkBudget(content string) (mean float64, total int, exceeded bool) {
	if rl.meanCeiling <= 0 && rl.totalCeiling <= 0 {
		return 0, 0, false
	}
	var entries []string
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}
	if len(entries) == 0 {
		return 0, 0, false
	}
	for _, entry := range entries {
		total += estimateTokens(entry)
	}
	mean = float64(total) / float64(len(entries))
	exceeded = (rl.meanCeiling > 0 && mean > float64(rl.meanCeiling)) || (rl.totalCeiling > 0 && total > rl.totalCeiling)
	return mean, total, exceeded
}

// appendFresh checks content's own token budget and appends it, under
// header, to the per-run log at *logPath -- creating the log file via
// rl.tempPattern on first use and recording its path in *logPath, mirroring
// the pre-#2982 appendDispositionsRound/appendDecisionsRound functions'
// shape exactly. A no-op when content == "": there is nothing to append.
// Best-effort throughout: a budget or append failure is logged to stderr
// and surfaced as a run_state_error op on stdout, never treated as fatal to
// the pass, matching every other handoff-artifact write in this package.
func (rl roundLog) appendFresh(logPath *string, roundNum int, header, content string, stdout io.Writer) {
	if content == "" {
		return
	}
	if mean, total, exceeded := rl.checkBudget(content); exceeded {
		msg := fmt.Sprintf("round %d mean %.1f/entry (ceiling %d), total %d tokens (ceiling %d)", roundNum, mean, rl.meanCeiling, total, rl.totalCeiling)
		fmt.Fprintln(os.Stderr, "orchestrator: "+rl.phase+" budget exceeded:", msg)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: rl.phase + "_budget", Error: msg}))
	}
	if err := rl.appendRound(logPath, header, content); err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: append "+rl.phase+" log:", err)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: rl.phase + "_log", Error: err.Error()}))
	}
}

// appendRound creates *logPath via rl.tempPattern on first use, then appends
// header+"\n\n"+content+"\n\n" to it -- the shared create-once/append-always
// mechanics behind appendFresh, split out so appendFresh's own error
// handling stays a flat, single-shape block regardless of which step failed.
func (rl roundLog) appendRound(logPath *string, header, content string) error {
	if *logPath == "" {
		f, err := os.CreateTemp("", rl.tempPattern)
		if err != nil {
			return fmt.Errorf("create %s log: %w", rl.phase, err)
		}
		path := f.Name()
		if err := f.Close(); err != nil {
			return fmt.Errorf("create %s log: %w", rl.phase, err)
		}
		*logPath = path
	}
	f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append %s log: %w", rl.phase, err)
	}
	defer f.Close()
	if _, err := fmt.Fprint(f, header+"\n\n"+content+"\n\n"); err != nil {
		return fmt.Errorf("append %s log: %w", rl.phase, err)
	}
	return nil
}

// readAndAppendFresh reads *statePath and, when this pass's own invocation
// left a genuinely fresh file there, appends it to the per-run log at
// *logPath under a "## Round N" header, incrementing *round -- mirroring
// the pre-#2982 appendFreshDispositionsRound/appendFreshDecisionsRound
// functions' shape exactly. sourcePath == "" means the artifact is disabled
// for this run entirely; *statePath == "" means recordArtifactPath's own
// path == "" no-op left it untouched (a stale value from a reused state
// file must never be re-read and re-appended) or this pass never wrote a
// fresh file -- either way there is nothing fresh to append, so both guards
// must hold for a no-op. Emits a run_state_error op on stdout for a read
// failure exactly as appendFresh does for an append/budget failure -- both
// are this log's own hiccups an operator should see on the same channel.
func (rl roundLog) readAndAppendFresh(sourcePath string, statePath *string, logPath *string, round *int, stdout io.Writer) {
	if sourcePath == "" || *statePath == "" {
		return
	}
	content, readErr := os.ReadFile(*statePath)
	if readErr != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read "+rl.phase+" for log:", readErr)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: rl.phase + "_log", Error: readErr.Error()}))
		return
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return
	}
	*round++
	rl.appendFresh(logPath, *round, fmt.Sprintf("## Round %d", *round), trimmed, stdout)
}
