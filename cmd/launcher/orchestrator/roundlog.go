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

// artifactSnapshot is the pre-pass content hash of a round-log handoff
// artifact; recordArtifactPath compares it against the post-pass hash to
// tell "this pass wrote a fresh file" from "this pass never touched it".
//
// A content hash rather than mtime+size, because mtime+size misclassifies
// both a same-second same-size genuine rewrite (wrongly stale) and a
// byte-identical re-save (wrongly fresh). nil means nothing to compare
// against.
type artifactSnapshot struct {
	hash [sha256.Size]byte
}

// snapshotArtifactIfPresent prepares path for this pass's invocation, keyed
// off target -- the corresponding run-state field's value going in. An empty
// path is a no-op (artifact disabled for this run). target == "" means
// nothing this round references the artifact, so a stale file from a prior
// pass is removed and nil returned. Otherwise the file is deliberately left
// alone -- a seeded prompt may have told the agent to read it, and removing
// it here would delete it out from under that reference -- and its pre-pass
// hash is snapshotted for recordArtifactPath.
//
// Returns nil when the file is absent or unreadable: an unreadable path is
// treated as "nothing to compare against", see recordArtifactPath.
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

// recordArtifactPath records path into *target only when this pass left a
// fresh file there: present, and (when preStat is non-nil) hashing
// differently than the pre-pass snapshot. A stat-confirmed fs.ErrNotExist
// clears *target; any other stat error, or an empty path, leaves the
// carried-forward value alone.
//
// A path that stats present but fails to read is classified fresh -- the
// hash compare cannot prove nothing changed, and silently dropping a real
// pass's artifact on an unprovable read error is the worse failure mode.
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
// decisions, and findings -- so a tripwire or log-append bug is fixed once
// instead of three times.
type roundLog struct {
	// phase names this log in ops and error messages: "dispositions",
	// "decisions", or "findings". Failures emit a run_state_error op with
	// phase+"_log" (append/read) or phase+"_budget".
	phase string
	// tempPattern is the os.CreateTemp pattern for the backing log file.
	tempPattern string
	// Both <= 0 disables the token-budget tripwire (findings carries no
	// ceiling: reviewer text was never budget-tripwired).
	meanCeiling, totalCeiling int
}

// checkBudget reports content's mean and total estimated tokens -- one entry
// per non-empty line -- and whether either ceiling is exceeded. Both
// ceilings <= 0, or content with no entries, returns exceeded=false.
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

// appendFresh checks content's token budget and appends it, under header, to
// the per-run log at *logPath, creating the file on first use. A no-op when
// content == "". Best-effort throughout: a budget or append failure goes to
// stderr and a run_state_error op on stdout, never fatal to the pass,
// matching every other handoff-artifact write in this package.
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
// header and content to it. Split out from appendFresh so that caller's
// error handling stays a flat block regardless of which step failed.
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

// readAndAppendFresh reads *statePath and, when this pass left a genuinely
// fresh file there, appends it to the per-run log at *logPath under a
// "## Round N" header, incrementing *round. Both guards must hold:
// sourcePath == "" means the artifact is disabled for this run, and
// *statePath == "" means either recordArtifactPath left a stale value from a
// reused state file untouched (which must never be re-appended) or this pass
// wrote nothing fresh.
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
