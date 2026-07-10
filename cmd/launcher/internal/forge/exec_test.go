package forge

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prependFakeGH writes a counting-wrapper gh script to a temp dir, prepends
// that dir to PATH, and returns the dir. Each invocation of the fake gh
// records its argv to call-NN.txt (zero-indexed) inside the dir.
// The caller must use the returned dir to read recorded args.
func prependFakeGH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/call-$(printf '%%02d' $n).txt"
%s`, dir, dir, body)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", dir+":"+old)
	return dir
}

// TestProbe_PositionalSlug verifies that Probe passes the slug as a positional
// argument to `gh repo view` with no --repo/-R flag.
func TestProbe_PositionalSlug(t *testing.T) {
	// Both gh calls exit 0. Probe may error on empty output — that's fine.
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", DispatchLabels{})
	c.(interface{ Probe() (string, error) }).Probe() //nolint:errcheck

	// call-01.txt is the `gh repo view …` invocation.
	raw, err := os.ReadFile(filepath.Join(dir, "call-01.txt"))
	if err != nil {
		t.Fatalf("call-01.txt not written: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")

	found := false
	for _, a := range args {
		if a == "owner/repo" {
			found = true
		}
		if a == "--repo" || a == "-R" {
			t.Fatalf("Probe passed %q flag to gh repo view; args: %q", a, args)
		}
	}
	if !found {
		t.Fatalf("slug not found as positional arg in gh repo view; args: %q", args)
	}
}

// TestProbe_StderrSurfaced verifies that when gh repo view fails, the returned
// error contains gh's actual stderr rather than just the configured slug.
func TestProbe_StderrSurfaced(t *testing.T) {
	// Call 0: gh auth status — succeed.
	// Call 1: gh repo view — fail with a distinctive stderr.
	prependFakeGH(t, `if [ "$1" = "repo" ]; then
  printf 'unknown flag: --repo\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", DispatchLabels{})
	_, err := c.(interface{ Probe() (string, error) }).Probe()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("want ErrRepoNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error must contain gh's stderr; got: %v", err)
	}
}

// TestFailureDetail_GraphQLArgShape verifies that FailureDetail queries via
// `gh api graphql` (fine-grained-PAT-safe) rather than `gh pr checks` (REST
// check-runs, 403s under a fine-grained PAT), passing the PR number as a
// GraphQL variable, and renders the failing check's name and summary.
func TestFailureDetail_GraphQLArgShape(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "api" ]; then
  printf '[{"__typename":"CheckRun","name":"test","conclusion":"FAILURE","summary":"boom"}]\n'
fi
`)

	c := NewExecClient("owner/repo", DispatchLabels{})
	detail, err := c.(interface {
		FailureDetail(string) (string, error)
	}).FailureDetail("https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(detail, "test: FAILURE") || !strings.Contains(detail, "boom") {
		t.Fatalf("detail missing failing check content: %q", detail)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt not written: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "graphql") {
		t.Fatalf("FailureDetail must use gh api graphql, not REST; args: %q", args)
	}
	if strings.Contains(joined, "checks") {
		t.Fatalf("FailureDetail must not use `gh pr checks`; args: %q", args)
	}
	found42 := false
	for _, a := range args {
		if a == "number=42" {
			found42 = true
		}
	}
	if !found42 {
		t.Fatalf("PR number not passed as a GraphQL variable; args: %q", args)
	}
}

// TestRenderFailureDetail verifies the failing-context filter and the
// maxFailureDetailBytes truncation.
func TestRenderFailureDetail(t *testing.T) {
	t.Run("filters out passing and non-failing conclusions", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "unit-tests", Conclusion: "SUCCESS", Summary: "all good"},
			{TypeName: "CheckRun", Name: "lint", Conclusion: "FAILURE", Summary: "2 errors"},
			{TypeName: "StatusContext", Context: "legacy-ci", State: "SUCCESS"},
			{TypeName: "StatusContext", Context: "legacy-status", State: "ERROR", Description: "build broke"},
		}
		got := renderFailureDetail(contexts)
		if strings.Contains(got, "unit-tests") || strings.Contains(got, "legacy-ci") {
			t.Fatalf("passing contexts must be filtered out: %q", got)
		}
		if !strings.Contains(got, "lint: FAILURE") || !strings.Contains(got, "2 errors") {
			t.Fatalf("failing CheckRun missing: %q", got)
		}
		if !strings.Contains(got, "legacy-status: ERROR") || !strings.Contains(got, "build broke") {
			t.Fatalf("failing StatusContext missing: %q", got)
		}
	})

	t.Run("no failing contexts returns empty string", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "unit-tests", Conclusion: "SUCCESS"},
		}
		if got := renderFailureDetail(contexts); got != "" {
			t.Fatalf("want empty string, got %q", got)
		}
	})

	t.Run("truncates to maxFailureDetailBytes", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "huge", Conclusion: "FAILURE", Summary: strings.Repeat("x", maxFailureDetailBytes*2)},
		}
		got := renderFailureDetail(contexts)
		if len(got) > maxFailureDetailBytes {
			t.Fatalf("detail not bounded: got %d bytes, want <= %d", len(got), maxFailureDetailBytes)
		}
	})
}

// TestGitForcePush_CapturesStderr verifies that a rejected force-with-lease
// push returns an error containing git's stderr, not just the exit status.
func TestGitForcePush_CapturesStderr(t *testing.T) {
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
	// Base "other"'s main explicitly on origin/main: the bare repo's HEAD
	// symref may still point at the git installation's default branch name
	// (e.g. "master"), which wouldn't exist yet, leaving "checkout -B main"
	// with no start point to build on.
	run(other, "checkout", "-B", "main", "origin/main")
	run(other, "config", "user.email", "test@example.com")
	run(other, "config", "user.name", "Test")
	writeFile(filepath.Join(other, "b.txt"), "two\n")
	run(other, "add", "b.txt")
	run(other, "commit", "-m", "second")
	run(other, "push", "origin", "main")

	// work's remote-tracking ref is now stale relative to origin/main.
	run(work, "commit", "--allow-empty", "-m", "local change")

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stale info") {
		t.Fatalf("want error to include git's stderr (stale info), got: %v", err)
	}
}

// TestGitForcePush_TransientFailureIsRetryable verifies that a push failure
// with no ref-rejection markers in its stderr — e.g. a network or forge
// outage — is classified as transient so callers can retry it.
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

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrTransientPushFailure) {
		t.Fatalf("want ErrTransientPushFailure, got: %v", err)
	}
}

// TestGitForcePush_StaleLeaseIsNotTransient verifies that a genuine
// stale-lease rejection — the branch moved since the last fetch, so the
// rebase really is out of date — is NOT classified as transient: retrying it
// would be pointless, so callers must treat it as terminal.
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

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, ErrTransientPushFailure) {
		t.Fatalf("want a terminal (non-transient) error for a genuine stale-lease rejection, got: %v", err)
	}
}
