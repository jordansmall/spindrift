package outcomebackstop

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

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
		Repo:      "/repo",
		Issue:     "42",
		Branch:    "agent/issue-42",
		Base:      "origin/main",
		Kind:      "work",
		CodeForge: "github",
		Nonce:     "abc123",
		Clock:     clk.clock(),
		Git:       git.run,
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
	if !strings.Contains(line, "nonce=abc123") {
		t.Fatalf("expected nonce in line: %q", line)
	}
	if len(git.calls) != 0 {
		t.Fatalf("expected git never called for research kind, got %v", git.calls)
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
	cfg.CodeForge = "local"
	cfg.WriteEnabled = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "no writable remote under CODE_FORGE=local") {
		t.Fatalf("unexpected note: %q", line)
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
	cfg.CodeForge = "github"
	cfg.WriteEnabled = false

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "branch relayed via outbox bundle (read-only Box)") {
		t.Fatalf("unexpected note: %q", line)
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
	cfg.CodeForge = "github"
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
}

func TestRun_PushFailsEveryAttempt(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
		"push":     {stderr: "line one\nfatal: some failure\n", err: fmt.Errorf("exit status 1")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.CodeForge = "github"
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
	cfg.CodeForge = "github"
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
	cfg.CodeForge = "github"
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
	cfg.CodeForge = "github"

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
	cfg.CodeForge = "github"

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "failed to salvage uncommitted work") {
		t.Fatalf("expected salvage-failed note, got %q", line)
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
	cfg.CodeForge = "github"

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "failed to salvage uncommitted work") {
		t.Fatalf("expected salvage-failed note, got %q", line)
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
	cfg.CodeForge = "github"

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if strings.Contains(line, "no work to preserve") {
		t.Fatalf("did not expect 'no work to preserve' note: %q", line)
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
	cfg.CodeForge = "github"

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if strings.Contains(line, "no work to preserve") {
		t.Fatalf("did not expect 'no work to preserve' note: %q", line)
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

func TestRun_NegativeBackoffJitterClampsAndDoesNotPanic(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
		"push":     {stderr: "boom\n", err: fmt.Errorf("exit status 1")},
	}}
	clk := &fakeClock{}
	cfg := baseConfig(git, clk)
	cfg.WriteEnabled = true
	cfg.CodeForge = "github"
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
