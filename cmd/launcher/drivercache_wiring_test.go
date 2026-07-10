package main

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestTransitionState_EvictsCacheOnTerminalState verifies that transitioning
// an issue to Complete or Failed evicts that issue's driver cache entry.
func TestTransitionState_EvictsCacheOnTerminalState(t *testing.T) {
	tests := []forge.DispatchState{forge.Complete, forge.Failed}
	for _, to := range tests {
		cache, err := newDriverCache()
		if err != nil {
			t.Fatalf("newDriverCache: %v", err)
		}
		dir := cache.dirFor("7")

		activeDriverCache = cache
		fc := forge.NewFake()
		transitionState(fc, "7", forge.InProgress, to)
		activeDriverCache = nil

		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("transition to %v: expected cache dir evicted, stat err=%v", to, err)
		}
		cache.cleanup()
	}
}

// TestTransitionState_KeepsCacheOnNonTerminalState verifies that
// transitioning to InProgress (not terminal) leaves the cache entry intact.
func TestTransitionState_KeepsCacheOnNonTerminalState(t *testing.T) {
	cache, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	defer cache.cleanup()
	dir := cache.dirFor("7")

	activeDriverCache = cache
	fc := forge.NewFake()
	transitionState(fc, "7", forge.Dispatchable, forge.InProgress)
	activeDriverCache = nil

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected cache dir to survive a non-terminal transition: %v", err)
	}
}

// TestRunOne_PopulatesBoxDriverCacheDir verifies runOne forwards the active
// driver cache's per-issue directory onto the dispatched Box.
func TestRunOne_PopulatesBoxDriverCacheDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	cache, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	defer cache.cleanup()
	activeDriverCache = cache
	defer func() { activeDriverCache = nil }()

	fr := runner.NewFake()
	c := config{}
	iss := issue{number: "55", title: "T"}
	if err := runOne(c, dir, fr, iss); err != nil {
		t.Fatalf("runOne: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	want := cache.dirFor("55")
	if got := fr.RunCalls[0].DriverCacheDir; got != want {
		t.Errorf("Box.DriverCacheDir: got %q, want %q", got, want)
	}
}

// TestRunFix_PopulatesBoxDriverCacheDirWithSameKeyAsInitialRun verifies
// runFix forwards the same per-issue cache directory runOne would have used
// for the same issue -- the whole point being that the fix Box mounts back
// the initial run's session data.
func TestRunFix_PopulatesBoxDriverCacheDirWithSameKeyAsInitialRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	cache, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	defer cache.cleanup()
	activeDriverCache = cache
	defer func() { activeDriverCache = nil }()

	fr := runner.NewFake()
	c := config{}
	iss := issue{number: "55", title: "T"}
	if err := runFix(c, dir, fr, iss, 1, ""); err != nil {
		t.Fatalf("runFix: %v", err)
	}

	want := cache.dirFor("55")
	if got := fr.RunCalls[0].DriverCacheDir; got != want {
		t.Errorf("Box.DriverCacheDir: got %q, want %q", got, want)
	}
}
