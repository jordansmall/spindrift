package bwrap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

// startFakeBox runs a Box under genDir against a fake bwrap that records its
// argv and sleeps, waits for that argv, and returns the adapter, the Run result
// channel and the argv. The host DB sits at version host and the VACUUM writes
// payload "v"+host.
func startFakeBox(t *testing.T, genDir, name, host string) (*bwrapAdapter, chan error, []string) {
	t.Helper()
	return startFakeBoxWith(t, genDir, name, "v"+host, func() (string, error) { return host, nil })
}

// startFakeBoxWith is startFakeBox with both inputs explicit: the host-version
// probe stub (so a test can make it fail) and the payload the VACUUM writes.
func startFakeBoxWith(t *testing.T, genDir, name, payload string, probe func() (string, error)) (*bwrapAdapter, chan error, []string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "fake-bwrap")
	// Written to a temp name and renamed, so the test never reads a half-written
	// argv; absolute tool paths because Run hands the Box no PATH.
	mv, sleep := lookTool(t, "mv"), lookTool(t, "sleep")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + ".tmp\n" + mv + " " + argsFile + ".tmp " + argsFile + "\nexec " + sleep + " 5\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeVacuumInto(t, script, payload, 0, nil)
	stubHostVersion(t, probe)

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: genDir,
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: name, Env: map[string]string{}}) }()

	// Run tracks the process only after Start returns, so argv alone can land
	// first; a Kill in that gap is a silent miss.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(argsFile); err == nil && a.tracked(name) {
			return a, done, strings.Split(strings.TrimSpace(string(b)), "\n")
		} else if time.Now().After(deadline) {
			t.Fatal("fake bwrap never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func killFakeBox(t *testing.T, a *bwrapAdapter, done chan error, name string) {
	t.Helper()
	if err := a.Kill(name); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Kill")
	}
}

// A warm generation whose only snapshot is a stale db-1 that another Box still
// holds, while the host DB has moved to version 2: Run writes db-2, mounts it,
// and holds db-2's lock for the Box's life, without touching the db-1 lower
// that Box is reading (issue #6072).
func TestBwrapRun_MountsCurrentHostVersionSnapshotAndLeavesHeldStaleOneUntouched(t *testing.T) {
	genDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "gen")
	staleDir := snapshotVersionDir(genDir, "1")
	staleDB := seedSnapshotDir(t, staleDir, "v1")
	holdShared(t, SnapshotLockPath(staleDir))
	before, err := os.Stat(staleDB)
	if err != nil {
		t.Fatal(err)
	}

	a, done, argv := startFakeBox(t, genDir, "agent-issue-v2", "2")

	v2Dir := snapshotVersionDir(genDir, "2")
	assertOverlaySrc(t, argv, v2Dir)
	if got, err := os.ReadFile(snapshotDBPath(v2Dir)); err != nil || string(got) != "v2" {
		t.Errorf("db-2 db.sqlite = %q, %v; want the freshly written snapshot", got, err)
	}
	for _, lock := range []string{SnapshotLockPath(genDir), SnapshotLockPath(v2Dir)} {
		if err := probeExclusiveLock(lock); err != syscall.EWOULDBLOCK {
			t.Errorf("exclusive probe of %s while the Box runs = %v, want EWOULDBLOCK (shared lock held)", lock, err)
		}
	}

	killFakeBox(t, a, done, "agent-issue-v2")
	if err := probeExclusiveLock(SnapshotLockPath(v2Dir)); err != nil {
		t.Errorf("db-2 lock after Run returned: want released, got %v", err)
	}

	after, err := os.Stat(staleDB)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("stale db-1 db.sqlite changed identity (err=%v); a running Box's lower must be left alone", err)
	}
	if got, _ := os.ReadFile(staleDB); string(got) != "v1" {
		t.Errorf("stale db-1 db.sqlite = %q, want untouched", got)
	}
}

// A long-lived daemon reclaims only at bootstrap, so Run itself drops the
// superseded unlocked db-1 once the host moved to version 2 (issue #6072).
func TestBwrapRun_SweepsSupersededUnlockedVersionAndKeepsCurrent(t *testing.T) {
	genDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "gen")
	staleDir := snapshotVersionDir(genDir, "1")
	seedSnapshotDir(t, staleDir, "v1")
	if err := os.WriteFile(SnapshotLockPath(staleDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	a, done, _ := startFakeBox(t, genDir, "agent-issue-sweep", "2")
	t.Cleanup(func() { killFakeBox(t, a, done, "agent-issue-sweep") })

	assertExists(t, staleDir, false)
	assertExists(t, SnapshotLockPath(staleDir), false)
	assertExists(t, snapshotDBPath(snapshotVersionDir(genDir, "2")), true)
}

// A Box that mounts a flat genDir snapshot (the host probe failed, no complete
// db-N) must not sweep: an incomplete unlocked db-N is inside the lower that this
// Box and its siblings mount as /nix/var (issue #6072).
func TestBwrapRun_DoesNotSweepVersionDirsInsideFlatGenDirLower(t *testing.T) {
	genDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "gen")
	seedSnapshotDir(t, genDir, "flat")
	partialDir := snapshotVersionDir(genDir, "1")
	if err := os.MkdirAll(partialDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SnapshotLockPath(partialDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// The payload is inert: the flat snapshot already exists, so no VACUUM runs.
	a, done, argv := startFakeBoxWith(t, genDir, "agent-issue-flat", "unused", func() (string, error) {
		return "", errors.New("no host db")
	})
	t.Cleanup(func() { killFakeBox(t, a, done, "agent-issue-flat") })

	assertOverlaySrc(t, argv, genDir)
	assertExists(t, partialDir, true)
	assertExists(t, SnapshotLockPath(partialDir), true)
}

func lookTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// assertOverlaySrc fails unless argv's --overlay-src operand is want.
func assertOverlaySrc(t *testing.T, argv []string, want string) {
	t.Helper()
	i := slices.Index(argv, "--overlay-src")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != want {
		t.Errorf("--overlay-src = %v, want %s", argv, want)
	}
}
