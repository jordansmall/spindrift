package gitplumbing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestMatchesAnyMarker(t *testing.T) {
	cases := []struct {
		name    string
		stderr  string
		markers []string
		want    bool
	}{
		{
			name:    "mixed-case stderr matches lowercase marker",
			stderr:  "HTTP 502: Bad Gateway",
			markers: []string{"bad gateway"},
			want:    true,
		},
		{
			name:    "empty marker slice never matches",
			stderr:  "error: bad gateway",
			markers: []string{},
			want:    false,
		},
		{
			name:    "no marker present",
			stderr:  "error: permission denied",
			markers: []string{"bad gateway", "timeout"},
			want:    false,
		},
		{
			name:    "first marker hits",
			stderr:  "error: timeout while connecting",
			markers: []string{"timeout", "bad gateway"},
			want:    true,
		},
		{
			name:    "later marker hits",
			stderr:  "error: bad gateway",
			markers: []string{"timeout", "bad gateway"},
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesAnyMarker(tc.stderr, tc.markers); got != tc.want {
				t.Fatalf("MatchesAnyMarker(%q, %v) = %v, want %v", tc.stderr, tc.markers, got, tc.want)
			}
		})
	}
}

// MatchesAnyMarker requires its markers to be lowercase already, because a
// mixed-case marker would silently never match. This checks every
// package-level marker slice fed to it.
func TestMarkerVars_AreLowercase(t *testing.T) {
	// Register every package-level marker slice here as it is added.
	sets := map[string][]string{
		"mergeConflictMarkers":      mergeConflictMarkers,
		"mergeTransientMarkers":     mergeTransientMarkers,
		"stalePushRejectionMarkers": stalePushRejectionMarkers,
	}
	for name, markers := range sets {
		for _, marker := range markers {
			if marker == "" {
				t.Errorf("%s: contains an empty marker", name)
			}
			if marker != strings.ToLower(marker) {
				t.Errorf("%s: marker %q is not lowercase", name, marker)
			}
		}
	}
}

func TestIsMergeConflict_DetectsMergeConflictMarker(t *testing.T) {
	if !IsMergeConflict("error: merge conflict in file.go") {
		t.Fatal("want true for stderr containing 'merge conflict'")
	}
}

func TestIsMergeConflict_IgnoresUnrelatedError(t *testing.T) {
	if IsMergeConflict("error: permission denied") {
		t.Fatal("want false for unrelated stderr")
	}
}

func TestIsMergeTransient(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "502 bad gateway",
			stderr: "HTTP 502: Bad Gateway (https://api.github.com/repos/org/repo/pulls/1/merge)",
			want:   true,
		},
		{
			name:   "timeout",
			stderr: "error: context deadline exceeded: request timeout",
			want:   true,
		},
		{
			name:   "genuine merge conflict",
			stderr: "error: merge conflict in file.go: not mergeable",
			want:   false,
		},
		{
			name:   "unrelated auth failure",
			stderr: "error: permission denied: bad credentials",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMergeTransient(tc.stderr); got != tc.want {
				t.Fatalf("IsMergeTransient(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

// A genuine stale-lease rejection, where the branch moved since the last
// fetch, must be terminal rather than transient, because retrying it would be
// pointless. The stderr-substring assertion also subsumes the removed
// TestGitForcePush_CapturesStderr (b1d0489 / #684).
func TestGitForcePush_StaleLeaseIsNotTransient(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	other := filepath.Join(dir, "other")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("", "init", "--bare", bare)
	run("", "clone", bare, work)
	run(work, "checkout", "-B", "main")
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	writeFile(filepath.Join(work, "a.txt"), "one\n")
	run(work, "add", "a.txt")
	run(work, "commit", "-m", "first")
	run(work, "push", "-u", "origin", "main")

	run("", "clone", bare, other)
	run(other, "checkout", "-B", "main", "origin/main")
	run(other, "config", "user.email", "test@example.com")
	run(other, "config", "user.name", "Test")
	writeFile(filepath.Join(other, "b.txt"), "two\n")
	run(other, "add", "b.txt")
	run(other, "commit", "-m", "second")
	run(other, "push", "origin", "main")

	// work's remote-tracking ref is now stale relative to origin/main.
	run(work, "commit", "--allow-empty", "-m", "local change")

	err := GitForcePush(context.Background(), work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stale info") {
		t.Fatalf("want error to include git's stderr (stale info), got: %v", err)
	}
	if errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("want a terminal (non-transient) error for a genuine stale-lease rejection, got: %v", err)
	}
}

// A push failure with no ref-rejection markers in its stderr, such as a
// network or forge outage, is transient so that callers can retry it.
func TestGitForcePush_TransientFailureIsRetryable(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	run("", "init", work)
	run(work, "checkout", "-B", "main")
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "a.txt")
	run(work, "commit", "-m", "first")
	// No real remote: the push fails on a generic infra-shaped error, with
	// no stale-lease/rejection markers in stderr.
	run(work, "remote", "add", "origin", filepath.Join(dir, "does-not-exist"))

	err := GitForcePush(context.Background(), work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("want forge.ErrTransientPushFailure, got: %v", err)
	}
}

// wrapForcePushError must never echo a credential from git's stderr, because
// the error flows unmodified into a public GitHub issue comment
// (settle.mergeImmediate). Git anonymizes URLs in its own connect diagnostics,
// so a subprocess test would pass either way, but it still echoes the full URL
// for other failure shapes like the 403 below, which is why this crafts one.
func TestWrapForcePushError_RedactsCredentialsFromStderr(t *testing.T) {
	const secret = "sometoken123"
	stderr := "fatal: unable to access 'https://oauth2:" + secret + "@git.example.com/org/repo.git/': The requested URL returned error: 403"

	err := wrapForcePushError(errors.New("exit status 128"), stderr)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("wrapForcePushError leaks embedded credential: %v", err)
	}
}
