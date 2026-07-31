// Package outcomebackstop owns the in-box "no-outcome backstop" decision
// (issue #2157): what a Box does when the Driver's run — and, if attempted,
// one resume pass — produced no parseable SPINDRIFT_OUTCOME line. It
// replaces the retired agent/entrypoint.sh emit_outcome_backstop shell
// function; the note strings below are byte-identical to that bash's, since
// the launcher's own SPINDRIFT_OUTCOME grammar and last-line-wins log scan
// depend on the exact wording only insofar as callers/tests pin it, but
// changing it needlessly would break any such pinned assertion or dashboard.
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
	// Repo is the path to the git repository holding Branch.
	Repo string
	// Issue is the issue number, carried into the emitted outcome line.
	Issue string
	// Branch is the agent branch name, e.g. "agent/issue-42".
	Branch string
	// Base is the full base ref, e.g. "origin/main".
	Base string
	// Kind is the dispatch kind: "work" | "research" | ... . A research
	// dispatch never cuts a branch (ADR 0022), so Run never touches git for
	// it.
	Kind string
	// CodeForge is "github" | "git" | "local".
	CodeForge string
	// WriteEnabled reports whether BOX_WRITE_ENABLED was present -- a
	// read-only github Box holds no push token by design.
	WriteEnabled bool
	// RecoveryAttempted reports whether a resume pass already ran and also
	// produced no outcome.
	RecoveryAttempted bool
	// Nonce is this run's control nonce (RUN_NONCE), appended to the
	// emitted line unconditionally, matching the bash's `nonce=${RUN_NONCE:-}`.
	Nonce string
	// MaxAttempts bounds the push retry loop; values < 1 clamp to 1.
	MaxAttempts int
	// Backoff and Jitter feed retry.LinearBackoff{Unit,Jitter}; a negative
	// value clamps to zero there, not here.
	Backoff, Jitter time.Duration
	// Clock is the retry sleep seam; the zero value defaults to
	// retry.RealClock().
	Clock retry.Clock
	// Git runs `git -C Repo <args>` and returns (stdout, stderr, err); a nil
	// Git defaults to a real exec.Command runner.
	Git func(args ...string) (string, string, error)
}

// Run reproduces the retired entrypoint.sh emit_outcome_backstop decision:
// salvage any dirty working tree into a commit, decide whether Branch has
// anything worth preserving over Base, best-effort push it (bounded retry on
// a transient failure) when there's a writable remote to push it to, and
// finally emit a single synthetic status=blocked SPINDRIFT_OUTCOME line to w
// so the launcher always gets a terminal signal to classify (issue #593).
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
		return emit(w, cfg.Issue, "none", note, cfg.Nonce)
	}

	note = salvage(git, note)

	count, err := commitCount(git, cfg.Base, cfg.Branch)
	if err != nil {
		// Assume work exists rather than let an unresolvable count skip the
		// always-emit outcome invariant (#593) -- a needless push attempt
		// beats a needless "no work to preserve" note reporting the wrong
		// thing.
		count = 1
	}

	switch {
	case count == 0:
		note += "; no work to preserve"
	case cfg.CodeForge == "local":
		note += "; no bundle was ever emitted (no writable remote under CODE_FORGE=local)"
	case !cfg.WriteEnabled && cfg.CodeForge == "github":
		note += "; branch relayed via outbox bundle (read-only Box)"
	default:
		note = pushWithRetry(git, clock, cfg, note)
	}

	return emit(w, cfg.Issue, cfg.Branch, note, cfg.Nonce)
}

// emit builds and writes the final SPINDRIFT_OUTCOME line for w, flagged
// synthetic=true (issue #2223) since it's the backstop's own manufactured
// terminal signal, not one the driver emitted.
func emit(w io.Writer, issue, landing, note, nonce string) error {
	o := outcome.Outcome{
		Issue:     issue,
		Landing:   landing,
		Status:    "blocked",
		Note:      note,
		Synthetic: true,
	}
	line := o.Line() + " nonce=" + nonce
	_, err := fmt.Fprintln(w, line)
	return err
}

// salvage commits any dirty working tree/index before the commit-count
// check runs, so that check sees the salvaged state too. A salvage failure
// never aborts the caller -- a needless note beats skipping the always-emit
// outcome invariant (#593).
func salvage(git func(args ...string) (string, string, error), note string) string {
	stdout, _, err := git("status", "--porcelain")
	if err != nil || strings.TrimSpace(stdout) == "" {
		return note
	}
	if _, _, addErr := git("add", "-A"); addErr != nil {
		return note + "; failed to salvage uncommitted work"
	}
	if _, _, commitErr := git("commit", "-m", "chore: salvage uncommitted work before exiting without an outcome"); commitErr != nil {
		return note + "; failed to salvage uncommitted work"
	}
	return note + "; salvaged uncommitted work into a commit"
}

// commitCount returns the number of commits on base..branch.
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

// pushWithRetry best-effort pushes Branch with bounded retry-with-backoff on
// a transient failure (issue #2095), appending a "push failed" note only
// once every attempt is exhausted; a successful push adds no note at all.
func pushWithRetry(git func(args ...string) (string, string, error), clock retry.Clock, cfg Config, note string) string {
	attempts := cfg.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	b := retry.LinearBackoff{Unit: cfg.Backoff, Jitter: cfg.Jitter, Clock: clock}

	for attempt := 1; ; attempt++ {
		_, stderr, err := git("push", "--force-with-lease", "origin", cfg.Branch)
		if err == nil {
			return note
		}
		if attempt >= attempts {
			return note + fmt.Sprintf("; push failed after %d attempt(s): %s", attempt, lastLine(stderr))
		}
		b.Do(attempt)
	}
}

// lastLine returns the last non-empty line of s, matching the retired
// bash's `tail -1`.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// realGit returns the production Git seam: `git -C repo <args>`, capturing
// stdout and stderr separately.
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
