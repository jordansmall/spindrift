// Package outcomebackstop decides what a Box emits when the Driver's run, plus
// one resume pass if attempted, produced no parseable SPINDRIFT_OUTCOME line
// (issue #2157). The status comes from the git-observed evidence Run gathers
// while salvaging and pushing, never from the driver's own possibly malformed
// text, so a run that landed clean still resolves to ready (issue #2380).
package outcomebackstop

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
)

// Config is everything Run needs to decide and emit the backstop outcome.
type Config struct {
	Repo   string
	Issue  string
	Branch string
	// Base is the full base ref, e.g. "origin/main".
	Base string
	// Kind is the dispatch kind. A research dispatch never cuts a branch
	// (ADR 0022), so Run never touches git for it.
	Kind string
	// HostMediatedRemote reports whether this run's CODE_FORGE has no writable
	// remote to push to in-box at all (ADR 0033: CODE_FORGE=local, #2267).
	HostMediatedRemote bool
	// OutboxRelayCapable reports whether the active CODE_FORGE backend gets the
	// outbox-relay treatment under a read-only Box (#1918: github and forgejo).
	OutboxRelayCapable bool
	// WriteEnabled reports whether BOX_WRITE_ENABLED was present. A read-only
	// github or forgejo Box holds no push token by design.
	WriteEnabled bool
	// RecoveryAttempted reports whether a resume pass already ran and also
	// produced no outcome.
	RecoveryAttempted bool
	// MaxAttempts bounds the push retry loop; values < 1 clamp to 1.
	MaxAttempts int
	// Backoff and Jitter feed retry.LinearBackoff{Unit,Jitter}; a negative
	// value clamps to zero there, not here.
	Backoff, Jitter time.Duration
	// Clock is the retry sleep seam; the zero value defaults to retry.RealClock().
	Clock retry.Clock
	// Git runs `git -C Repo <args>` returning (stdout, stderr, err); nil
	// defaults to a real exec.Command runner.
	Git func(args ...string) (string, string, error)
	// RunStateFilePath points at the run-state handoff artifact carrying the
	// reviewer's last verdict word. Empty, missing, unreadable, or unparseable
	// all quietly mean "no verdict known", never an error (issue #2459).
	RunStateFilePath string
}

// Run salvages a dirty tree, pushes Branch when there is a writable remote, and
// always emits one synthetic SPINDRIFT_OUTCOME line so the launcher gets a
// terminal signal to classify (issue #593). Status lands on "ready" only when
// the tree ended up clean, there was work to preserve, and it was relayed or
// pushed; every other case stays "blocked" (issue #2380, ADR 0036).
func Run(cfg Config, w io.Writer) error {
	git := cfg.Git
	if git == nil {
		git = realGit(cfg.Repo)
	}
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = retry.RealClock()
	}

	note := "driver exited without emitting an outcome"
	if cfg.RecoveryAttempted {
		note += "; a resume attempt also produced no outcome"
	}

	if cfg.Kind == "research" {
		return emit(w, cfg.Issue, "none", "blocked", note)
	}

	unresolvedBlock := readLastVerdict(cfg.RunStateFilePath) == "BLOCK"
	if unresolvedBlock {
		note += "; reviewer's blocking findings were never cleared"
	}

	note, salvageOK := salvage(git, note)

	count, err := commitCount(git, cfg.Base, cfg.Branch)
	if err != nil {
		// Assume work exists rather than let an unresolvable count skip the
		// always-emit outcome invariant (#593): a needless push attempt beats a
		// "no work to preserve" note reporting the wrong thing.
		count = 1
	}

	status := "blocked"
	switch {
	case !salvageOK:
		// A tree that could not be cleaned is not a landed state.
	case count == 0:
		note += "; no work to preserve"
	case cfg.HostMediatedRemote:
		note += "; branch relayed via outbox bundle (no writable remote under CODE_FORGE=local)"
		if !unresolvedBlock {
			status = "ready"
		}
	case !cfg.WriteEnabled && cfg.OutboxRelayCapable:
		note += "; branch relayed via outbox bundle (read-only Box)"
		if !unresolvedBlock {
			status = "ready"
		}
	default:
		var pushed bool
		note, pushed = pushWithRetry(git, clock, cfg, note)
		if pushed && !unresolvedBlock {
			status = "ready"
		}
	}

	return emit(w, cfg.Issue, cfg.Branch, status, note)
}

// emit writes the final SPINDRIFT_OUTCOME line, flagged synthetic (issue #2223)
// because the backstop manufactured it. Synthetic marks who emitted the line,
// not what it says, so status is unconditional on it.
func emit(w io.Writer, issue, landing, status, note string) error {
	o := outcome.Outcome{
		Issue:     issue,
		Landing:   landing,
		Status:    status,
		Note:      note,
		Synthetic: true,
	}
	line := o.Line()
	_, err := fmt.Fprintln(w, line)
	return err
}

// salvage commits any dirty tree before the commit-count check runs, so that
// check sees the salvaged state too. A failure never aborts the caller: a
// needless note beats skipping the always-emit outcome invariant (#593).
func salvage(git func(args ...string) (string, string, error), note string) (string, bool) {
	stdout, _, err := git("status", "--porcelain")
	if err != nil || strings.TrimSpace(stdout) == "" {
		return note, true
	}
	if _, _, addErr := git("add", "-A"); addErr != nil {
		return note + "; failed to salvage uncommitted work", false
	}
	if _, _, commitErr := git("commit", "-m", "chore: salvage uncommitted work before exiting without an outcome"); commitErr != nil {
		return note + "; failed to salvage uncommitted work", false
	}
	return note + "; salvaged uncommitted work into a commit", true
}

func commitCount(git func(args ...string) (string, string, error), base, branch string) (int, error) {
	stdout, stderr, err := git("rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, fmt.Errorf("outcomebackstop: rev-list --count %s..%s: %w: %s", base, branch, err, stderr)
	}
	n, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return 0, fmt.Errorf("outcomebackstop: parse rev-list output %q: %w", stdout, err)
	}
	return n, nil
}

// pushWithRetry best-effort pushes Branch with bounded retry-with-backoff on a
// transient failure (issue #2095), appending a "push failed" note only once
// every attempt is exhausted; a successful push adds no note.
func pushWithRetry(git func(args ...string) (string, string, error), clock retry.Clock, cfg Config, note string) (string, bool) {
	attempts := cfg.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	b := retry.LinearBackoff{Unit: cfg.Backoff, Jitter: cfg.Jitter, Clock: clock}

	for attempt := 1; ; attempt++ {
		_, stderr, err := git("push", "--force-with-lease", "origin", cfg.Branch)
		if err == nil {
			return note, true
		}
		if attempt >= attempts {
			return note + fmt.Sprintf("; push failed after %d attempt(s): %s", attempt, lastLine(stderr)), false
		}
		b.Do(attempt)
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func realGit(repo string) func(args ...string) (string, string, error) {
	return func(args ...string) (string, string, error) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
}
