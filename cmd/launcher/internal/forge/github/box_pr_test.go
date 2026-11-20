package github

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// shellExitErr runs script under sh and returns the *exec.ExitError whose
// Stderr cmd.Output captured, as boxOpenPRForBranch's gh call produces.
func shellExitErr(t *testing.T, script string) error {
	t.Helper()
	_, err := exec.Command("sh", "-c", script).Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}
	return err
}

// MatchesAnyMarker requires lowercase markers, so a mixed-case marker would
// silently never match.
func TestRateLimitMarkers_AreLowercase(t *testing.T) {
	for _, marker := range rateLimitMarkers {
		if marker == "" {
			t.Error("rateLimitMarkers: contains an empty marker")
		}
		if marker != strings.ToLower(marker) {
			t.Errorf("rateLimitMarkers: marker %q is not lowercase", marker)
		}
	}
}

// ghCommandErr must recognize GitHub's primary hourly-quota phrasing and its
// secondary and abuse-detection phrasings, without pulling in unrelated gh
// failures such as auth, not-found and network errors (issue #2865).
func TestGhCommandErr_RateLimitClassification(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"primary quota exceeded", "API rate limit exceeded for installation ID 12345678.", true},
		{"secondary abuse detection", "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation.", true},
		{"secondary generic rate limit", "You have exceeded a secondary rate limit and have been temporarily blocked from content creation.", true},
		{"already exceeded phrasing", "You have already exceeded your GraphQL points budget for this hour.", true},
		{"unauthorized", "HTTP 401: Bad credentials (https://api.github.com/graphql)", false},
		{"not found", "GraphQL: Could not resolve to a Repository with the name 'owner/repo'. (repository)", false},
		{"network failure", "dial tcp: lookup api.github.com: no such host", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := shellExitErr(t, fmt.Sprintf("printf '%%s' %q 1>&2; exit 1", tt.stderr))
			got := ghCommandErr("gh pr list", err)
			if is := errors.Is(got, forge.ErrRateLimit); is != tt.want {
				t.Errorf("errors.Is(ErrRateLimit) = %v, want %v for %q (err: %v)", is, tt.want, tt.stderr, got)
			}
		})
	}
}

// ghCommandErr folds an *exec.ExitError's captured Stderr into the message.
func TestGhCommandErr_StderrSurfaced(t *testing.T) {
	err := shellExitErr(t, "printf hello 1>&2; exit 1")
	got := ghCommandErr("gh pr list", err)
	for _, want := range []string{"gh pr list", "hello", "exit status"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error should contain %q, got: %v", want, got)
		}
	}
	var exitErr *exec.ExitError
	if !errors.As(got, &exitErr) {
		t.Errorf("error should still wrap the original *exec.ExitError, got: %v", got)
	}
}

// Empty or whitespace-only stderr must not leave a dangling ": " suffix.
func TestGhCommandErr_EmptyStderrDegradesCleanly(t *testing.T) {
	for name, script := range map[string]string{
		"empty stderr":           "exit 1",
		"whitespace-only stderr": "printf '   \\n\\t ' 1>&2; exit 1",
	} {
		t.Run(name, func(t *testing.T) {
			err := shellExitErr(t, script)
			got := ghCommandErr("gh pr list", err)
			if want := fmt.Sprintf("gh pr list: %s", err); got.Error() != want {
				t.Errorf("error = %q, want %q (no dangling separator)", got.Error(), want)
			}
		})
	}
}
