package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSeedWorkerPromptComposesAddendumOverOriginal verifies seedWorkerPrompt
// writes a fresh temp file carrying the original prompt content plus an
// addendum naming the slice and the result/sentinel paths, leaving the
// original prompt file untouched (issue #2059).
func TestSeedWorkerPromptComposesAddendumOverOriginal(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("ORIGINAL WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	seeded, err := seedWorkerPrompt(promptPath, ManifestSlice{Name: "slice-a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
	if err != nil {
		t.Fatalf("seedWorkerPrompt() error = %v", err)
	}

	if seeded == promptPath {
		t.Fatalf("seedWorkerPrompt() returned original path %q, want a fresh temp file", promptPath)
	}

	seededContent, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("ReadFile(seeded): %v", err)
	}
	got := string(seededContent)
	for _, want := range []string{"ORIGINAL WORKER PROMPT", "slice-a", "/tmp/slice-a.result", "/tmp/slice-a.done"} {
		if !strings.Contains(got, want) {
			t.Errorf("seeded prompt missing substring %q; got:\n%s", want, got)
		}
	}

	originalContent, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("ReadFile(original): %v", err)
	}
	if string(originalContent) != "ORIGINAL WORKER PROMPT" {
		t.Errorf("original prompt file mutated: got %q", string(originalContent))
	}
}

// TestSeedWorkerPromptErrorsOnMissingPromptFile verifies seedWorkerPrompt
// surfaces a non-nil error when promptFile can't be read (issue #2059).
func TestSeedWorkerPromptErrorsOnMissingPromptFile(t *testing.T) {
	_, err := seedWorkerPrompt("/nonexistent/prompt.txt", ManifestSlice{Name: "slice-a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
	if err == nil {
		t.Fatal("seedWorkerPrompt() error = nil, want non-nil")
	}
}

// TestWaitForSentinelReturnsTrueWhenFileAppears verifies waitForSentinel
// notices a sentinel file that appears mid-poll, returning well before the
// context's own generous deadline (issue #2059 AC2/AC3).
func TestWaitForSentinelReturnsTrueWhenFileAppears(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(sentinelPath, []byte(""), 0o644)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 5*time.Millisecond)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("waitForSentinel() = false, want true")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("waitForSentinel() took %v, want well under the 500ms context deadline", elapsed)
	}
}

// TestWaitForSentinelReturnsFalseOnContextTimeout verifies waitForSentinel
// gives up promptly when ctx expires before the sentinel ever appears
// (issue #2059 AC2), rather than busy-looping or ignoring ctx.
func TestWaitForSentinelReturnsFalseOnContextTimeout(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 5*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("waitForSentinel() = true, want false")
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitForSentinel() took %v, want close to the 30ms context deadline", elapsed)
	}
}

// TestWaitForSentinelReturnsTrueImmediatelyWhenFileAlreadyExists verifies
// waitForSentinel checks immediately before ever sleeping, so a
// pre-existing sentinel returns true without waiting out a full
// pollInterval (issue #2059 AC2/AC3).
func TestWaitForSentinelReturnsTrueImmediatelyWhenFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")
	if err := os.WriteFile(sentinelPath, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 200*time.Millisecond)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("waitForSentinel() = false, want true")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("waitForSentinel() took %v, want under the 200ms pollInterval", elapsed)
	}
}
