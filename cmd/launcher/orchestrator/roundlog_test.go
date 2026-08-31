package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSnapshotArtifactIfPresentNilOnReadFailureAfterStat covers the read-
// failure edge case of snapshotArtifactIfPresent: path stats successfully
// (it exists) but os.ReadFile fails on it -- here because path is itself a
// directory, which reliably errors on read on Linux. The function must
// return nil, the same "nothing to compare against" result as a genuinely
// absent file, deliberately -- see the function's own doc comment.
func TestSnapshotArtifactIfPresentNilOnReadFailureAfterStat(t *testing.T) {
	dir := t.TempDir()
	unreadablePath := filepath.Join(dir, "artifact-dir")
	if err := os.Mkdir(unreadablePath, 0o755); err != nil {
		t.Fatal(err)
	}

	got := snapshotArtifactIfPresent(unreadablePath, "carried-forward-value")

	if got != nil {
		t.Errorf("snapshotArtifactIfPresent = %+v, want nil (stat-ok/read-fail must be treated as nothing to snapshot)", got)
	}
}

// TestRecordArtifactPathFreshOnReadFailureAfterStat covers the read-failure
// edge case of recordArtifactPath: preStat is present (a genuine pre-pass
// snapshot exists) and the post-pass path stats successfully but fails to
// read -- here because path is itself a directory. The function must fall
// through to *target = path (classified fresh), the same fail-open choice a
// nil preStat gets, rather than clearing *target -- see the function's own
// doc comment for why.
func TestRecordArtifactPathFreshOnReadFailureAfterStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.json")
	if err := os.WriteFile(path, []byte("original content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	preStat := snapshotArtifactIfPresent(path, "carried-forward-value")
	if preStat == nil {
		t.Fatalf("snapshotArtifactIfPresent returned nil, want a snapshot of the existing file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	target := "carried-forward-value"
	recordArtifactPath(path, &target, preStat)

	if target != path {
		t.Errorf("target = %q, want %q (stat-ok/read-fail post-pass must be classified fresh, not cleared)", target, path)
	}
}

// TestRoundLogCheckBudget verifies roundLog.checkBudget flags a round whose
// mean tokens-per-entry exceeds meanCeiling or whose total exceeds
// totalCeiling, mirroring the pre-#2982
// TestCheckDispositionsTokenBudget/TestCheckDecisionsTokenBudget cases this
// method replaces, plus the zero-ceiling ("tripwire disabled") case those
// per-artifact functions never had to cover.
func TestRoundLogCheckBudget(t *testing.T) {
	rl := roundLog{phase: "dispositions", meanCeiling: dispositionsMeanTokenCeiling, totalCeiling: dispositionsTotalTokenCeiling}

	compact := "run.go:1 -- fixed in commit abc123\nrun.go:88 -- won't-fix: out of scope, see #2551"
	if mean, total, exceeded := rl.checkBudget(compact); exceeded {
		t.Errorf("checkBudget(%q) = mean %.1f, total %d, exceeded %v, want under the ceiling", compact, mean, total, exceeded)
	}

	// A compact, well-formed entry using multi-byte UTF-8 (a non-ASCII file
	// path/reason) must not trip the ceiling on byte count alone -- the
	// same terse entry in ASCII stays comfortably under it.
	nonASCII := "café.go:1 -- fixed in commit abc123: résumé überprüft"
	if mean, total, exceeded := rl.checkBudget(nonASCII); exceeded {
		t.Errorf("checkBudget(%q) = mean %.1f, total %d, exceeded %v, want under the ceiling (rune count, not byte count)", nonASCII, mean, total, exceeded)
	}

	oversized := "run.go:1 -- fixed in commit abc123 by rewriting the function as follows: " +
		strings.Repeat("func example() { doSomething(); doSomethingElse(); } ", 20)
	if mean, total, exceeded := rl.checkBudget(oversized); !exceeded {
		t.Errorf("checkBudget(%q) = mean %.1f, total %d, exceeded %v, want over the mean ceiling", oversized, mean, total, exceeded)
	}

	// A pasted diff hunk: many individually short lines, each comfortably
	// under the mean ceiling on its own, but the round's total balloons --
	// the mean check alone is blind to this restatement mode.
	var pastedDiffHunk strings.Builder
	pastedDiffHunk.WriteString("run.go:1 -- fixed in commit abc123\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&pastedDiffHunk, "+    line %d of the pasted diff\n", i)
	}
	if mean, total, exceeded := rl.checkBudget(pastedDiffHunk.String()); !exceeded {
		t.Errorf("checkBudget(pasted diff hunk) = mean %.1f, total %d, exceeded %v, want over the total ceiling", mean, total, exceeded)
	} else if mean > dispositionsMeanTokenCeiling {
		t.Errorf("checkBudget(pasted diff hunk) = mean %.1f, want it to stay UNDER the mean ceiling %d -- this case is meant to prove the TOTAL check catches what the mean check alone misses", mean, dispositionsMeanTokenCeiling)
	}

	if mean, total, exceeded := rl.checkBudget(""); exceeded || mean != 0 || total != 0 {
		t.Errorf("checkBudget(\"\") = mean %.1f, total %d, exceeded %v, want mean 0, total 0, not exceeded", mean, total, exceeded)
	}

	// Both ceilings zero (findings' own instance) disables the tripwire
	// entirely, regardless of how large content is.
	zeroCeiling := roundLog{phase: "findings"}
	if mean, total, exceeded := zeroCeiling.checkBudget(oversized); exceeded {
		t.Errorf("checkBudget(oversized) with zero ceilings = mean %.1f, total %d, exceeded %v, want never exceeded", mean, total, exceeded)
	}
}

// TestRoundLogAppendFreshNoOpOnEmptyContent verifies appendFresh is a no-op
// when content == "" -- no log file is created, *logPath stays untouched,
// and nothing is written to stdout.
func TestRoundLogAppendFreshNoOpOnEmptyContent(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	var logPath string
	var stdout bytes.Buffer

	rl.appendFresh(&logPath, 1, "## Round 1", "", &stdout)

	if logPath != "" {
		t.Errorf("logPath = %q, want empty (no content, nothing to append)", logPath)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no op emitted for a no-op append)", stdout.String())
	}
}

// TestRoundLogAppendFreshCreatesAndAccumulates verifies the first
// appendFresh call creates the log file via tempPattern and records it in
// *logPath, and a second call appends to the same file rather than
// overwriting it -- both rounds' content present, in order.
func TestRoundLogAppendFreshCreatesAndAccumulates(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	var logPath string
	var stdout bytes.Buffer

	rl.appendFresh(&logPath, 1, "## Round 1", "run.go:1 -- fixed in commit abc123", &stdout)
	if logPath == "" {
		t.Fatal("logPath is empty after first appendFresh, want it set to the created log file")
	}
	firstPath := logPath

	rl.appendFresh(&logPath, 2, "## Round 2", "run.go:2 -- won't-fix: out of scope", &stdout)
	if logPath != firstPath {
		t.Errorf("logPath = %q after second appendFresh, want unchanged at %q (second round reuses the same log file)", logPath, firstPath)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	round1Idx := strings.Index(content, "## Round 1")
	round2Idx := strings.Index(content, "## Round 2")
	if round1Idx == -1 || round2Idx == -1 {
		t.Fatalf("log content = %q, want both round headers present", content)
	}
	if round1Idx > round2Idx {
		t.Errorf("log content = %q, want Round 1 before Round 2", content)
	}
	if !strings.Contains(content, "run.go:1 -- fixed in commit abc123") || !strings.Contains(content, "run.go:2 -- won't-fix: out of scope") {
		t.Errorf("log content = %q, want both rounds' content present", content)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (neither round exceeded a budget -- this roundLog carries no ceilings)", stdout.String())
	}
}

// TestRoundLogAppendFreshEmitsRunStateErrorOnBudgetExceeded verifies
// appendFresh surfaces a budget-exceeding round as a "run_state_error"
// spindrift op with Phase "<phase>_budget" on stdout, mirroring the
// pre-#2982 appendFreshDispositionsRound/appendFreshDecisionsRound
// functions' own budget-tripwire behavior.
func TestRoundLogAppendFreshEmitsRunStateErrorOnBudgetExceeded(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md", meanCeiling: dispositionsMeanTokenCeiling, totalCeiling: dispositionsTotalTokenCeiling}
	var logPath string
	var stdout bytes.Buffer

	oversized := "run.go:1 -- fixed in commit abc123 by rewriting the function as follows: " +
		strings.Repeat("func example() { doSomething(); doSomethingElse(); } ", 20)

	rl.appendFresh(&logPath, 1, "## Round 1", oversized, &stdout)

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"dispositions_budget"`) {
		t.Errorf("stdout = %q, want a run_state_error op with phase dispositions_budget", stdout.String())
	}
	// A budget-exceeding round is still appended -- the tripwire flags the
	// content, it does not drop it.
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), oversized) {
		t.Errorf("log content = %q, want the oversized round's content still appended", string(got))
	}
}

// TestRoundLogAppendFreshEmitsRunStateErrorOnAppendFailure verifies
// appendFresh surfaces an append failure -- here, *logPath pre-seeded to
// point at a directory rather than a file -- as a "run_state_error"
// spindrift op with Phase "<phase>_log" on stdout.
func TestRoundLogAppendFreshEmitsRunStateErrorOnAppendFailure(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	logPath := t.TempDir()
	var stdout bytes.Buffer

	rl.appendFresh(&logPath, 1, "## Round 1", "run.go:1 -- fixed in commit abc123", &stdout)

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"dispositions_log"`) {
		t.Errorf("stdout = %q, want a run_state_error op with phase dispositions_log", stdout.String())
	}
}

// TestRoundLogAppendFreshEmitsRunStateErrorOnCreateFailure verifies
// appendFresh surfaces a log-file creation failure -- here, an uncreatable
// TMPDIR so os.CreateTemp("", rl.tempPattern) fails -- as a
// "run_state_error" spindrift op with Phase "<phase>_log" on stdout.
func TestRoundLogAppendFreshEmitsRunStateErrorOnCreateFailure(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	var logPath string
	var stdout bytes.Buffer

	rl.appendFresh(&logPath, 1, "## Round 1", "run.go:1 -- fixed in commit abc123", &stdout)

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"dispositions_log"`) {
		t.Errorf("stdout = %q, want a run_state_error op with phase dispositions_log", stdout.String())
	}
	if logPath != "" {
		t.Errorf("logPath = %q after a create failure, want it left empty", logPath)
	}
}

// TestRoundLogReadAndAppendFreshNoOpWhenSourcePathEmpty verifies
// readAndAppendFresh is a no-op when sourcePath == "" -- the artifact is
// disabled for this run entirely -- even if *statePath carries a non-empty
// value.
func TestRoundLogReadAndAppendFreshNoOpWhenSourcePathEmpty(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "dispositions.md")
	if err := os.WriteFile(statePath, []byte("run.go:1 -- fixed in commit abc123"), 0o644); err != nil {
		t.Fatal(err)
	}
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh("", &statePath, &logPath, &round, &stdout)

	if round != 0 {
		t.Errorf("round = %d, want unchanged at 0 (artifact disabled)", round)
	}
	if logPath != "" {
		t.Errorf("logPath = %q, want empty (artifact disabled)", logPath)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no op emitted when disabled)", stdout.String())
	}
}

// TestRoundLogReadAndAppendFreshNoOpWhenStatePathEmpty verifies
// readAndAppendFresh is a no-op when *statePath == "" -- recordArtifactPath's
// own path == "" no-op left it untouched, or this pass never wrote a fresh
// file -- even though sourcePath is non-empty (the artifact is enabled for
// this run).
func TestRoundLogReadAndAppendFreshNoOpWhenStatePathEmpty(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var statePath, logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh("/tmp/dispositions.md", &statePath, &logPath, &round, &stdout)

	if round != 0 {
		t.Errorf("round = %d, want unchanged at 0 (nothing fresh this pass)", round)
	}
	if logPath != "" {
		t.Errorf("logPath = %q, want empty (nothing fresh this pass)", logPath)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no op emitted when nothing is fresh)", stdout.String())
	}
}

// TestRoundLogReadAndAppendFreshEmitsRunStateErrorOnReadFailure verifies
// readAndAppendFresh surfaces a state-file read failure as a
// "run_state_error" spindrift op with Phase "<phase>_log" on stdout --
// pointing *statePath at a directory makes os.ReadFile fail.
func TestRoundLogReadAndAppendFreshEmitsRunStateErrorOnReadFailure(t *testing.T) {
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	statePath := t.TempDir()
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh("/tmp/dispositions.md", &statePath, &logPath, &round, &stdout)

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"dispositions_log"`) {
		t.Errorf("stdout = %q, want a run_state_error op with phase dispositions_log", stdout.String())
	}
	if round != 0 {
		t.Errorf("round = %d, want unchanged at 0 (a read failure appends nothing)", round)
	}
	if logPath != "" {
		t.Errorf("logPath = %q, want empty (a read failure appends nothing)", logPath)
	}
}

// TestRoundLogReadAndAppendFreshHappyPathIncrementsRoundAndAppends verifies
// the happy path: *statePath holds fresh, non-empty content, so
// readAndAppendFresh increments *round and appends it under a "## Round N"
// header.
func TestRoundLogReadAndAppendFreshHappyPathIncrementsRoundAndAppends(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "dispositions.md")
	if err := os.WriteFile(statePath, []byte("run.go:1 -- fixed in commit abc123"), 0o644); err != nil {
		t.Fatal(err)
	}
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh("/tmp/dispositions.md", &statePath, &logPath, &round, &stdout)

	if round != 1 {
		t.Errorf("round = %d, want 1 (fresh content found)", round)
	}
	if logPath == "" {
		t.Fatal("logPath is empty, want it set to the created log file")
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "## Round 1") || !strings.Contains(string(got), "run.go:1 -- fixed in commit abc123") {
		t.Errorf("log content = %q, want header \"## Round 1\" and the fresh content", string(got))
	}
}

// TestRoundLogReadAndAppendFreshNoOpOnWhitespaceOnlyContent verifies
// readAndAppendFresh treats whitespace-only state-file content the same as
// empty content: a no-op that leaves *round unchanged.
func TestRoundLogReadAndAppendFreshNoOpOnWhitespaceOnlyContent(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "dispositions.md")
	if err := os.WriteFile(statePath, []byte("   \n\t\n  "), 0o644); err != nil {
		t.Fatal(err)
	}
	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh("/tmp/dispositions.md", &statePath, &logPath, &round, &stdout)

	if round != 0 {
		t.Errorf("round = %d, want unchanged at 0 (whitespace-only content is nothing to append)", round)
	}
	if logPath != "" {
		t.Errorf("logPath = %q, want empty (nothing appended)", logPath)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no op emitted)", stdout.String())
	}
}

// TestRoundLogReadAndAppendFreshAppendsRoundOnSameSecondSameSizeRewrite composes the
// real snapshotArtifactIfPresent -> recordArtifactPath -> readAndAppendFresh
// pipeline end to end (issue #2982 acceptance criterion 2's "appends a
// round" half) -- unlike TestArtifactSnapshotDetectsSameSecondSameSizeRewrite,
// which only asserts what recordArtifactPath does to a bare target string,
// this drives a genuine same-second same-size rewrite all the way through to
// a real round appended to a real log file on disk.
func TestRoundLogReadAndAppendFreshAppendsRoundOnSameSecondSameSizeRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dispositions.md")
	if err := os.WriteFile(path, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	preStat := snapshotArtifactIfPresent(path, "carried-forward-value")
	if preStat == nil {
		t.Fatalf("snapshotArtifactIfPresent returned nil, want a snapshot of the existing file")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before rewrite: %v", err)
	}
	preModTime := info.ModTime()

	// Same length as "original content" and mtime forced back to the
	// pre-pass value, so neither a size-only nor a mtime-only compare can
	// tell this apart from a no-op.
	if err := os.WriteFile(path, []byte("modified content"), 0o644); err != nil {
		t.Fatalf("WriteFile (rewrite): %v", err)
	}
	if err := os.Chtimes(path, preModTime, preModTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	var statePath string
	recordArtifactPath(path, &statePath, preStat)
	if statePath != path {
		t.Fatalf("statePath = %q, want %q (same-second same-size rewrite must be detected as fresh)", statePath, path)
	}

	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh(path, &statePath, &logPath, &round, &stdout)

	if round != 1 {
		t.Errorf("round = %d, want 1 (genuine rewrite must append a round)", round)
	}
	if logPath == "" {
		t.Fatal("logPath is empty, want it set to the created log file")
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "## Round 1") || !strings.Contains(string(got), "modified content") {
		t.Errorf("log content = %q, want header \"## Round 1\" and the rewritten content", string(got))
	}
}

// TestRoundLogReadAndAppendFreshSkipsRoundOnByteIdenticalRewrite is
// TestRoundLogReadAndAppendFreshAppendsRoundOnSameSecondSameSizeRewrite's
// counterpart: a byte-identical rewrite (only mtime moves forward) must
// leave *statePath cleared, and readAndAppendFresh must find nothing fresh
// to read -- no round appended, no log file even created.
func TestRoundLogReadAndAppendFreshSkipsRoundOnByteIdenticalRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dispositions.md")
	content := []byte("identical content")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	preStat := snapshotArtifactIfPresent(path, "carried-forward-value")
	if preStat == nil {
		t.Fatalf("snapshotArtifactIfPresent returned nil, want a snapshot of the existing file")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before rewrite: %v", err)
	}
	laterModTime := info.ModTime().Add(time.Second)

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile (rewrite): %v", err)
	}
	if err := os.Chtimes(path, laterModTime, laterModTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	statePath := "carried-forward-value"
	recordArtifactPath(path, &statePath, preStat)
	if statePath != "" {
		t.Fatalf("statePath = %q, want \"\" (byte-identical rewrite must be detected as not fresh)", statePath)
	}

	rl := roundLog{phase: "dispositions", tempPattern: "orchestrator-dispositions-log-*.md"}
	round := 0
	var logPath string
	var stdout bytes.Buffer

	rl.readAndAppendFresh(path, &statePath, &logPath, &round, &stdout)

	if round != 0 {
		t.Errorf("round = %d, want unchanged at 0 (byte-identical rewrite must append nothing)", round)
	}
	if logPath != "" {
		t.Errorf("logPath = %q, want empty (no log file created for a byte-identical rewrite)", logPath)
	}
}
