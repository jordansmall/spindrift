package outcomebackstop

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// fakeGit records every invocation's args and looks up (stdout, stderr, err)
// by subcommand (args[0]) in responses. A subcommand absent from responses
// returns ("", "", nil), a success with no output.
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

func baseConfig(git *fakeGit) Config {
	return Config{
		Repo:         "/repo",
		Issue:        "42",
		Branch:       "agent/issue-42",
		Base:         "origin/main",
		Kind:         "work",
		RelayCapable: true,
		Git:          git.run,
	}
}

func TestRun_ResearchKind(t *testing.T) {
	git := &fakeGit{}
	cfg := baseConfig(git)
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

// Every backstop-emitted line carries synthetic=true and round-trips through
// outcome.Parse as Synthetic == true (issue #2223).
func TestRun_EmitsSyntheticFlagOnBlocked(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	cfg := baseConfig(git)

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

// Synthetic is unconditional regardless of Status: it marks who emitted the
// line, the backstop rather than the driver, not what the line says.
func TestRun_EmitsSyntheticFlagOnReady(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

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
	cfg := baseConfig(git)

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
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true

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
	cfg := baseConfig(git)
	cfg.RelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "branch relayed via outbox bundle") {
		t.Fatalf("unexpected note: %q", line)
	}
	if !strings.Contains(line, "status=ready") {
		t.Fatalf("expected status=ready, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}

func TestRun_SalvageSucceeds(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: " M some/file.go\n"},
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

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
	cfg := baseConfig(git)
	cfg.RelayCapable = true

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

func TestRun_SalvageStatusFails(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {err: fmt.Errorf("index corrupt")},
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "could not check the tree for uncommitted work") {
		t.Fatalf("expected tree-check-failed note, got %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked (tree state unknown), got %q", line)
	}
	for _, sub := range []string{"add", "commit", "push"} {
		if git.countCalls(sub) != 0 {
			t.Fatalf("expected no %s after status failed, got %v", sub, git.calls)
		}
	}
}

// The relay arms never push, so only the !salvageOK arm's place first in
// Run's switch keeps a status failure from reporting ready under them.
func TestRun_SalvageStatusFailsBlocksRelayArms(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"host-mediated": func(c *Config) { c.HostMediatedRemote = true },
		"read-only":     func(c *Config) { c.RelayCapable = true },
	} {
		t.Run(name, func(t *testing.T) {
			git := &fakeGit{responses: map[string]fakeResult{
				"status":   {err: fmt.Errorf("index corrupt")},
				"rev-list": {stdout: "1\n"},
			}}
			cfg := baseConfig(git)
			mutate(&cfg)

			var buf bytes.Buffer
			if err := Run(cfg, &buf); err != nil {
				t.Fatalf("Run: %v", err)
			}
			line := buf.String()
			if !strings.Contains(line, "status=blocked") {
				t.Fatalf("expected status=blocked (tree state unknown), got %q", line)
			}
			if strings.Contains(line, "branch relayed via outbox bundle") {
				t.Fatalf("expected relay arm not reached, got %q", line)
			}
		})
	}
}

func TestRun_SalvageAddFailsSkipsCommit(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: " M some/file.go\n"},
		"add":      {err: fmt.Errorf("index locked")},
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

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

// The note is one physical line of the SPINDRIFT_OUTCOME protocol: git's
// multi-line stderr must not split it, or the host parses only the first line.
func TestRun_RevListMultiLineStderrKeepsOutcomeOneLine(t *testing.T) {
	const revListStderr = "fatal: ambiguous argument 'origin/main..b': unknown revision or path not in the working tree.\n" +
		"Use '--' to separate paths from revisions, like this:\n" +
		"'git <command> [<revision>...] -- [<file>...]'\n"
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stderr: revListStderr, err: fmt.Errorf("exit status 128")},
	}}
	cfg := baseConfig(git)

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected exactly one outcome line, got %q", out)
	}
	if !strings.Contains(out, "fatal: ambiguous argument") {
		t.Fatalf("expected the rev-list cause in the note, got %q", out)
	}
}

// A CRLF-terminated stderr must not leak a CR into the one-line outcome.
func TestRun_RevListCRLFStderrDropsCR(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stderr: "fatal: x\r\nhint\r\n", err: fmt.Errorf("exit status 128")},
	}}
	cfg := baseConfig(git)

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\r") {
		t.Fatalf("expected no CR in the outcome line, got %q", out)
	}
	if !strings.Contains(out, "fatal: x") || strings.Contains(out, "hint") {
		t.Fatalf("expected only the first stderr line in the note, got %q", out)
	}
}

func TestRun_RevListEmptyStderrLeavesNoDanglingColon(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {err: fmt.Errorf("bad revision")},
	}}
	cfg := baseConfig(git)

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "bad revision") {
		t.Fatalf("expected the rev-list error in the note, got %q", out)
	}
	if strings.Contains(out, "bad revision:") {
		t.Fatalf("expected no dangling colon after the error, got %q", out)
	}
}

// relayArms and countFailures cross every relay arm with every way the commit
// count can fail, shared by Run's and DemoteAlreadyResolved's relay-arm tests.
var relayArms = map[string]func(*Config){
	"host-mediated": func(c *Config) { c.HostMediatedRemote = true },
	"read-only":     func(c *Config) { c.RelayCapable = true },
}

var countFailures = map[string]fakeResult{
	"error":       {err: fmt.Errorf("bad revision")},
	"unparseable": {stdout: "not-a-number\n"},
}

// bundleout writes no bundle when the count fails, so a relay arm that claimed
// "relayed" would report ready for a branch that never left the Box.
func TestRun_RevListFailureBlocksRelayArms(t *testing.T) {
	for armName, mutate := range relayArms {
		for failName, resp := range countFailures {
			t.Run(armName+"/"+failName, func(t *testing.T) {
				git := &fakeGit{responses: map[string]fakeResult{"rev-list": resp}}
				cfg := baseConfig(git)
				mutate(&cfg)

				var buf bytes.Buffer
				if err := Run(cfg, &buf); err != nil {
					t.Fatalf("Run: %v", err)
				}
				line := buf.String()
				if !strings.Contains(line, "status=blocked") {
					t.Fatalf("expected status=blocked (no bundle written), got %q", line)
				}
				if !strings.Contains(line, "could not count commits on") {
					t.Fatalf("expected the count error in the note, got %q", line)
				}
				if strings.Contains(line, "relayed via outbox bundle") {
					t.Fatalf("expected no relay claim, got %q", line)
				}
				if git.countCalls("push") != 0 {
					t.Fatalf("expected no push on a relay arm, got %v", git.calls)
				}
			})
		}
	}
}

func TestRun_RecoveryAttempted(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	cfg := baseConfig(git)
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

// Reproduces the #2349 shape that motivated issue #2380: a driver finished a
// clean, checked, pushed run but left a malformed self-report line, so the
// backstop ran anyway. The backstop must resolve status=ready from its own git
// evidence, a clean tree plus commits on base..branch plus a successful push,
// never from the driver's garbled text, which it never reads.
func TestRun_Issue2380_LandedAndPushedResolvesReady(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"status":   {stdout: ""},
		"rev-list": {stdout: "3\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

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

// Issue #2459: an unresolved BLOCK verdict in the run-state artifact keeps
// status=blocked even under otherwise-ready conditions. The reviewer's last
// word overrides the git-observed-only backstop decision.
func TestRun_UnresolvedBlockOverridesHostMediatedRemote(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true
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

// A RunStateFilePath pointing at a nonexistent file degrades to no verdict
// known, identical to leaving it unset, never an error or a crash.
func TestRun_MissingRunStateFileBehavesAsUnset(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true
	cfg.RunStateFilePath = filepath.Join(t.TempDir(), "does-not-exist.json")

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()

	git2 := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg2 := baseConfig(git2)
	cfg2.HostMediatedRemote = true

	var buf2 bytes.Buffer
	if err := Run(cfg2, &buf2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line2 := buf2.String()

	if line != line2 {
		t.Fatalf("expected missing run-state file to behave as unset:\n  with path:    %q\n  without path: %q", line, line2)
	}
}

// An APPROVE verdict leaves status selection unchanged from the
// git-observed-only logic. Only an unresolved BLOCK verdict changes behavior.
func TestRun_ApproveVerdictBehavesAsUnset(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "APPROVE"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true
	cfg.RunStateFilePath = runStatePath

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()

	git2 := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg2 := baseConfig(git2)
	cfg2.HostMediatedRemote = true

	var buf2 bytes.Buffer
	if err := Run(cfg2, &buf2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line2 := buf2.String()

	if line != line2 {
		t.Fatalf("expected APPROVE verdict to behave as unset:\n  with path:    %q\n  without path: %q", line, line2)
	}
}

// The reviewer-blocking-findings fragment is additive: it appears alongside
// the relay/push detail fragment, not in place of it.
func TestRun_UnresolvedBlockNoteIsAdditive(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true
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

// Issue #2459 for the outbox-relay arm (cfg.RelayCapable): an unresolved
// BLOCK verdict keeps status=blocked even under otherwise-ready relay
// conditions.
func TestRun_UnresolvedBlockOverridesOutboxRelay(t *testing.T) {
	dir := t.TempDir()
	runStatePath := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runStatePath, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}

	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "1\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true
	cfg.RunStateFilePath = runStatePath

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "branch relayed via outbox bundle") {
		t.Fatalf("expected relay note, got %q", line)
	}
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked despite read-only relay conditions, got %q", line)
	}
	if !strings.Contains(line, "reviewer's blocking findings were never cleared") {
		t.Fatalf("expected reviewer-blocking-findings note, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push attempt on the read-only relay path, got %v", git.calls)
	}
}

// alreadyResolvedLine builds a minimal already-resolved SPINDRIFT_OUTCOME line
// for feeding to DemoteAlreadyResolved as priorOutcomeLine.
func alreadyResolvedLine() string {
	return outcome.Outcome{Issue: "42", Landing: "none", Status: outcome.StatusAlreadyResolved}.Line()
}

// Issue #4016: a claimed already-resolved run with no actual commits on the
// branch leaves the claim alone — the #4015 zero-commits path already covers
// that case, so DemoteAlreadyResolved must write nothing and never push.
func TestDemoteAlreadyResolved_ZeroCommitsNoOp(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "0\n"},
	}}
	cfg := baseConfig(git)

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, alreadyResolvedLine(), &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}

// Issue #4016: under a read-only outbox-relay Box, the commits still exist so
// the claim still demotes, but there is no push token to preserve them with —
// the later bundle-out step relays instead.
func TestDemoteAlreadyResolved_ReadOnlyOutboxRelayNoPush(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "3\n"},
	}}
	cfg := baseConfig(git)
	cfg.RelayCapable = true

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, alreadyResolvedLine(), &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
	line := buf.String()
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked, got %q", line)
	}
	if !strings.Contains(line, "branch relayed via outbox bundle") {
		t.Fatalf("expected relay note, got %q", line)
	}
}

// Issue #4016: under CODE_FORGE=local's host-mediated remote there is no
// writable remote in-box at all, so no push, same as Run's own mode split.
func TestDemoteAlreadyResolved_HostMediatedRemoteNoPush(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{
		"rev-list": {stdout: "3\n"},
	}}
	cfg := baseConfig(git)
	cfg.HostMediatedRemote = true

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, alreadyResolvedLine(), &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
	line := buf.String()
	if !strings.Contains(line, "status=blocked") {
		t.Fatalf("expected status=blocked, got %q", line)
	}
	if !strings.Contains(line, "branch relayed via outbox bundle (no writable remote under CODE_FORGE=local)") {
		t.Fatalf("expected relay note, got %q", line)
	}
}

// Issue #4016: a prior line whose status isn't already-resolved (ready here)
// leaves the claim untouched — nothing to demote.
func TestDemoteAlreadyResolved_PriorReadyNoOp(t *testing.T) {
	git := &fakeGit{}
	cfg := baseConfig(git)

	priorLine := outcome.Outcome{Issue: "42", Landing: cfg.Branch, Status: "ready"}.Line()

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, priorLine, &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
	if len(git.calls) != 0 {
		t.Fatalf("expected no git calls, got %v", git.calls)
	}
}

// Issue #4016: an unparseable or empty prior line also leaves the claim
// untouched, same as a non-already-resolved status.
func TestDemoteAlreadyResolved_UnparseablePriorLineNoOp(t *testing.T) {
	git := &fakeGit{}
	cfg := baseConfig(git)

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, "", &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

// The demote path shares Run's reason: a failed count writes no bundle, so a
// relay arm must not claim the branch was relayed.
func TestDemoteAlreadyResolved_RevListFailureBlocksRelayArms(t *testing.T) {
	for armName, mutate := range relayArms {
		for failName, resp := range countFailures {
			t.Run(armName+"/"+failName, func(t *testing.T) {
				git := &fakeGit{responses: map[string]fakeResult{"rev-list": resp}}
				cfg := baseConfig(git)
				mutate(&cfg)

				var buf bytes.Buffer
				if err := DemoteAlreadyResolved(cfg, alreadyResolvedLine(), &buf); err != nil {
					t.Fatalf("DemoteAlreadyResolved: %v", err)
				}
				line := buf.String()
				if !strings.Contains(line, "status=blocked") {
					t.Fatalf("expected status=blocked, got %q", line)
				}
				if !strings.Contains(line, "could not be counted") {
					t.Fatalf("expected the count error in the note, got %q", line)
				}
				if strings.Contains(line, "relayed via outbox bundle") {
					t.Fatalf("expected no relay claim, got %q", line)
				}
				if git.countCalls("push") != 0 {
					t.Fatalf("expected no push on a relay arm, got %v", git.calls)
				}
			})
		}
	}
}

// Issue #4016: an advise-only dispatch kind (research) never cuts a branch,
// so DemoteAlreadyResolved must never touch git for it, mirroring Run's own
// advise-only short-circuit.
func TestDemoteAlreadyResolved_AdviseOnlyKindNoOp(t *testing.T) {
	git := &fakeGit{}
	cfg := baseConfig(git)
	cfg.Kind = "research"

	var buf bytes.Buffer
	if err := DemoteAlreadyResolved(cfg, alreadyResolvedLine(), &buf); err != nil {
		t.Fatalf("DemoteAlreadyResolved: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
	if len(git.calls) != 0 {
		t.Fatalf("expected no git calls, got %v", git.calls)
	}
}

// A forge with no outbox relay leaves the branch nowhere to go, and the Box
// never pushes, so the backstop must not claim ready.
func TestRun_NoRelayForgeBlocks(t *testing.T) {
	git := &fakeGit{responses: map[string]fakeResult{"rev-list": {stdout: "2\n"}}}
	cfg := baseConfig(git)
	cfg.RelayCapable = false

	var buf bytes.Buffer
	if err := Run(cfg, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "status=blocked") || !strings.Contains(line, "no outbox relay for this forge") {
		t.Fatalf("expected blocked with the no-relay note, got %q", line)
	}
	if git.countCalls("push") != 0 {
		t.Fatalf("expected no push, got %v", git.calls)
	}
}
