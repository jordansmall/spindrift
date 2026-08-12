package outcomebackstop

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
)

// fakeGit is a scripted git seam: it records every invocation's args and
// looks up (stdout, stderr, err) by the subcommand (args[0]) in responses.
// A subcommand absent from responses returns ("", "", nil) -- success with
// no output, matching a git command with nothing interesting to say.
type fakeGit struct {
	calls     [][]string
	responses map[string]fakeResult
}

type fakeResult struct {
	stdout, stderr string
	err            error
}

func (f *fakeGit) run(args ...string) (string, string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) == 0 {
		return "", "", nil
	}
	if r, ok := f.responses[args[0]]; ok {
		return r.stdout, r.stderr, r.err
	}
	return "", "", nil
}

func (f *fakeGit) countCalls(subcommand string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == subcommand {
			n++
		}
	}
	return n
}

// fakeClock records every Sleep duration without actually sleeping.
type fakeClock struct {
	slept []time.Duration
}

func (f *fakeClock) clock() retry.Clock {
	return retry.Clock{
		Now:   func() time.Time { return time.Unix(0, 0) },
		Sleep: func(d time.Duration) { f.slept = append(f.slept, d) },
	}
}

func baseConfig(git *fakeGit, clk *fakeClock) Config {
	return Config{
		Repo:               "/repo",
		Issue:              "42",
		Branch:             "agent/issue-42",
		Base:               "origin/main",
		Kind:               "work",
		OutboxRelayCapable: true,
		Clock:              clk.clock(),
		Git:                git.run,
	}
}

func TestRun_ResearchKind(t *testing.T) {
	git := &fakeGit{}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.Kind = "research"

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, "landing=none") || !strings.Contains(line, "status=blocked") {
		t.Fatalf("unexpected line: %q", line)
	}
	if strings.Contains(line, "nonce=") {
		t.Fatalf("expected no nonce field in line: %q", line)
	}
	if len(git.calls) != 0 {
		t.Fatalf("expected git never called for research kind, got %v", git.calls)
	}
}

// TestRun_EmitsSyntheticFlagOnBlocked verifies every backstop-emitted line
// carries synthetic=true (issue #2223), and that it round-trips through
// outcome.Parse as Outcome.Synthetic == true alongside Status == "blocked"
// for a genuinely-blocked scenario (no commits to preserve).
func TestRun_EmitsSyntheticFlagOnBlocked(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, "synthetic=true") {
		t.Fatalf("expected synthetic=true in line, got %q", line)
	}
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("outcome.Parse(%q): %v", line, err)
	}
	if !o.Synthetic {
		t.Fatalf("expected Synthetic == true, got %+v", o)
	}
	if o.Status != "blocked" {
		t.Fatalf("expected Status == blocked, got %+v", o)
	}
}

// TestRun_EmitsSyntheticFlagOnReady pins that Synthetic is unconditional
// regardless of Status: a status=ready line (driver pushed successfully)
// still parses with Synthetic == true, since Synthetic marks who emitted
// the line (the backstop, not the driver), not what it says.
func TestRun_EmitsSyntheticFlagOnReady(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 3

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("outcome.Parse(%q): %v", line, err)
	}
	if !o.Synthetic {
		t.Fatalf("expected Synthetic == true, got %+v", o)
	}
	if o.Status != "ready" {
		t.Fatalf("expected Status == ready, got %+v", o)
	}
}

func TestRun_NoCommits(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "no work to preserve") {
		t.Fatalf("expected 'no work to preserve' note, got %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}

func TestRun_CodeForgeLocal(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.HostMediatedRemote = true
	cfg.WriteEnabled = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "branch relayed via outbox bundle (no writable remote under CODE_FORGE=local)") {
		t.Fatalf("unexpected note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}

func TestRun_ReadOnlyGithub(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.OutboxRelayCapable = true
	cfg.WriteEnabled = false

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "branch relayed via outbox bundle (read-only Box)") {
		t.Fatalf("unexpected note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}

func TestRun_WritablePushSuccess(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 3

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if git.countCalls("push") != 1 {
		t.Fatalf("expected exactly one push, got %v", git.calls)
	}
	if strings.Contains(line, "push failed") {
		t.Fatalf("unexpected push failure note: %q", line)
	}
	if !strings.Contains(line, "landing=agent/issue-42") {
		t.Fatalf("expected landing=branch, got %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready, got %q", line)
	}
}

func TestRun_PushFailsEveryAttempt(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
		"push":     {stderr: "line one\nfatal: some failure\n", err: fmt.Errorf("exit status 1")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 2

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if git.countCalls("push") != 2 {
		t.Fatalf("expected 2 push attempts, got %v", git.calls)
	}
	if !strings.Contains(line, "push failed after 2 attempt(s): fatal: some failure") {
		t.Fatalf("unexpected note: %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked, got %q", line)
	}
	if len(clk.slept) != 1 {
		t.Fatalf("expected exactly one sleep between the two attempts, got %v", clk.slept)
	}
}

func TestRun_PushTransientThenSucceeds(t *testing.T) {
	attempt := 0
	git := &fakeGit{}
	git.responses = map[string]fakeResult{}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 3
	// Override Git with a closure that fails the first push, succeeds the second.
	cfg.Git = func(args ...string) (string, string, error) {
		git.calls = append(git.calls, append([]string(nil), args...))
		if len(args) == 0 {
			return "", "", nil
		}
		switch args[0] {
		case "rev-list":
			return "1\n", "", nil
		case "push":
			attempt++
			if attempt == 1 {
				return "", "transient failure\n", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		}
		return "", "", nil
	}

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if git.countCalls("push") != 2 {
		t.Fatalf("expected 2 push attempts, got %v", git.calls)
	}
	if strings.Contains(line, "push failed") {
		t.Fatalf("unexpected push failure note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready, got %q", line)
	}
	if len(clk.slept) != 1 {
		t.Fatalf("expected one sleep before the retry, got %v", clk.slept)
	}
}

func TestRun_MaxAttemptsClampsToOne(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
		"push":     {stderr: "nope\n", err: fmt.Errorf("exit status 1")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 0

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if git.countCalls("push") != 1 {
		t.Fatalf("expected exactly 1 push attempt, got %v", git.calls)
	}
	if !strings.Contains(line, "push failed after 1 attempt(s): nope") {
		t.Fatalf("unexpected note: %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked, got %q", line)
	}
	if len(clk.slept) != 0 {
		t.Fatalf("expected no sleep on a single-attempt clamp, got %v", clk.slept)
	}
}

func TestRun_SalvageSucceeds(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: " M some/file.go\n"},
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "salvaged uncommitted work into a commit") {
		t.Fatalf("expected salvage-succeeded note, got %q", line)
	}
	if git.countCalls("add") != 1 || git.countCalls("commit") != 1 {
		t.Fatalf("expected add and commit to be called, got %v", git.calls)
	}
}

func TestRun_SalvageFails(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: " M some/file.go\n"},
		"commit":   {err: fmt.Errorf("nothing to commit")},
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "failed to salvage uncommitted work") {
		t.Fatalf("expected salvage-failed note, got %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked (tree left dirty), got %q", line)
	}
}

func TestRun_SalvageAddFailsSkipsCommit(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: " M some/file.go\n"},
		"add":      {err: fmt.Errorf("index locked")},
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "failed to salvage uncommitted work") {
		t.Fatalf("expected salvage-failed note, got %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked (tree left dirty), got %q", line)
	}
	if git.countCalls("commit") != 0 {
		t.Fatalf("expected commit to be skipped after add failed, got %v", git.calls)
	}
}

func TestRun_RevListErrorTreatedAsWorkExists(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {err: fmt.Errorf("bad revision")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if strings.Contains(line, "no work to preserve") {
		t.Fatalf("did not expect 'no work to preserve' note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready (push succeeds by default), got %q", line)
	}
	if git.countCalls("push") != 1 {
		t.Fatalf("expected a push attempt when rev-list errors, got %v", git.calls)
	}
}

func TestRun_RevListUnparseableTreatedAsWorkExists(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "not-a-number\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if strings.Contains(line, "no work to preserve") {
		t.Fatalf("did not expect 'no work to preserve' note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready (push succeeds by default), got %q", line)
	}
	if git.countCalls("push") != 1 {
		t.Fatalf("expected a push attempt when rev-list is unparseable, got %v", git.calls)
	}
}

func TestRun_RecoveryAttempted(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.RecoveryAttempted = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "a resume attempt also produced no outcome") {
		t.Fatalf("expected resume-attempt note, got %q", line)
	}
}

// TestRun_Issue2380_LandedAndPushedResolvesReady reproduces the #2349 shape
// that motivated issue #2380: a driver finished a clean, checked, pushed
// run but left backstop to run anyway because its own final self-report
// line was malformed (e.g. "SPINDRIFT_OUTCOME: MERGED", missing
// landing=/status= fields). Backstop must resolve status=ready from its own
// already-verified git evidence -- a clean tree (nothing to salvage),
// commits on base..branch, and a successful push -- not from the driver's
// garbled text, which it never reads at all.
func TestRun_Issue2380_LandedAndPushedResolvesReady(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: ""},
		"rev-list": {stdout: "3\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("outcome.Parse(%q): %v", line, err)
	}
	if o.Status != "ready" {
		t.Fatalf("expected Status == ready, got %+v", o)
	}
	if !o.Synthetic {
		t.Fatalf("expected Synthetic == true, got %+v", o)
	}
	if o.Landing != cfg.Branch {
		t.Fatalf("expected Landing == %q, got %+v", cfg.Branch, o)
	}
}

// TestRun_UnresolvedBlockOverridesHostMediatedRemote pins issue #2459: an
// unresolved BLOCK verdict recorded in the run-state artifact must keep
// status=blocked even under otherwise-ready conditions (host-mediated
// remote, commits present) -- the reviewer's last word overrides the
// git-observed-only backstop decision.
func TestRun_UnresolvedBlockOverridesHostMediatedRemote(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.HostMediatedRemote = true
	cfg.WriteEnabled = true
	cfg.RunStateFilePath = runStatePath

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked despite host-mediated remote, got %q", line)
	}
	if !strings.Contains(line, "reviewer's blocking findings were never cleared") {
		t.Fatalf("expected reviewer-blocking-findings note, got %q", line)
	}
}

// TestRun_MissingRunStateFileBehavesAsUnset pins that a RunStateFilePath
// pointing at a nonexistent file degrades to "no verdict known" -- identical
// status/note to leaving RunStateFilePath unset entirely, never an error or
// crash.
func TestRun_MissingRunStateFileBehavesAsUnset(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.HostMediatedRemote = true
	cfg.WriteEnabled = true
	cfg.RunStateFilePath = filepath.Join(t.TempDir(), "does-not-exist.json")

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()

	git2 := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk2 := &fakeClock{}
	cfg2 := baseConfig(git2, clk2)
	cfg2.HostMediatedRemote = true
	cfg2.WriteEnabled = true

	var buf2 bytes.Buffer
	if err := Run(cfg2, &buf2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line2 := buf2.String()

	if line != line2 {
		t.Fatalf("expected missing run-state file to behave as unset:\n  with path:    %q\n  without path: %q", line, line2)
	}
}

// TestRun_ApproveVerdictBehavesAsUnset pins that a run-state artifact
// recording an APPROVE verdict leaves status selection unchanged from
// today's git-observed-only logic -- only an unresolved BLOCK verdict
// changes behavior.
func TestRun_ApproveVerdictBehavesAsUnset(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "APPROVE"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.HostMediatedRemote = true
	cfg.WriteEnabled = true
	cfg.RunStateFilePath = runStatePath

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()

	git2 := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk2 := &fakeClock{}
	cfg2 := baseConfig(git2, clk2)
	cfg2.HostMediatedRemote = true
	cfg2.WriteEnabled = true

	var buf2 bytes.Buffer
	if err := Run(cfg2, &buf2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line2 := buf2.String()

	if line != line2 {
		t.Fatalf("expected APPROVE verdict to behave as unset:\n  with path:    %q\n  without path: %q", line, line2)
	}
}

// TestRun_UnresolvedBlockNoteIsAdditive pins that the reviewer-blocking-
// findings fragment is additive: it appears alongside the existing
// relay/push detail fragment, not in place of it.
func TestRun_UnresolvedBlockNoteIsAdditive(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.HostMediatedRemote = true
	cfg.WriteEnabled = true
	cfg.RunStateFilePath = runStatePath

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "reviewer's blocking findings were never cleared") {
		t.Fatalf("expected reviewer-blocking-findings note, got %q", line)
	}
	if !strings.Contains(line, "branch relayed via outbox bundle (no writable remote under CODE_FORGE=local)") {
		t.Fatalf("expected relay note still present alongside verdict note, got %q", line)
	}
}

func TestRun_NegativeBackoffJitterClampsAndDoesNotPanic(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
		"push":     {stderr: "boom\n", err: fmt.Errorf("exit status 1")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.OutboxRelayCapable = true
	cfg.MaxAttempts = 2
	cfg.Backoff = -5 * time.Second
	cfg.Jitter = -3 * time.Second

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(clk.slept) != 1 {
		t.Fatalf("expected exactly one sleep, got %v", clk.slept)
	}
	if clk.slept[0] != 0 {
		t.Fatalf("expected clamped sleep duration of 0, got %v", clk.slept[0])
	}
}
