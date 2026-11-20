package github

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// The Box has no backend rows and keeps its own gh (#6757), so its open-PR
// query is the one gh call core still makes; every host-side operation goes
// through the Connector.

// ghCommandErrStderrCap caps the gh stderr folded into an error message, so a
// pathological failure cannot dump unbounded output into the logs that carry
// it.
const ghCommandErrStderrCap = 4096

// rateLimitMarkers is GitHub's fixed rate-limit vocabulary as it appears in
// gh's stderr.
var rateLimitMarkers = []string{
	"api rate limit exceeded",
	"already exceeded",
	"secondary rate limit",
	"abuse detection",
}

// boxOpenPRForBranch reports whether repo has an open PR for branch, through
// the Box's own gh.
func boxOpenPRForBranch(repo, branch string) (bool, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--jq", `.[0].url // ""`,
	).Output()
	if err != nil {
		return false, ghCommandErr("gh pr list", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// ghCommandErr wraps an error from cmd.Output() with gh's own stderr
// diagnostic, recovered from the *exec.ExitError. Any other error, such as a
// missing gh binary, falls back to "description: err". Stderr naming a rate
// limit yields a *forge.RateLimitError.
func ghCommandErr(description string, err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("%s: %w", description, err)
	}
	stderr := string(exitErr.Stderr)
	base := fmt.Errorf("%s: %w", description, err)
	if msg := strings.TrimSpace(stderr); msg != "" {
		if len(msg) > ghCommandErrStderrCap {
			cut := ghCommandErrStderrCap
			for cut > 0 && !utf8.RuneStart(msg[cut]) {
				cut--
			}
			msg = msg[:cut] + "...(truncated)"
		}
		base = fmt.Errorf("%s: %w: %s", description, err, msg)
	}
	if gitplumbing.MatchesAnyMarker(stderr, rateLimitMarkers) {
		return &forge.RateLimitError{Err: base}
	}
	return base
}
