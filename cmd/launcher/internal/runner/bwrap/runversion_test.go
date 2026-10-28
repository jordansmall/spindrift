package bwrap

import (
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

// A warm generation whose only snapshot is a stale db-1 while the host DB has
// moved to version 2: Run writes db-2, mounts it, and holds db-2's lock for the
// Box's life, without touching the db-1 lower an older Box may still be reading
// (issue #6072).
func TestBwrapRun_MountsCurrentHostVersionSnapshotAndLeavesStaleOneUntouched(t *testing.T) {
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
	fakeVacuumInto(t, script, "v2", 0, nil)
	stubHostVersion(t, func() (string, error) { return "2", nil })

	genDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "gen")
	staleDB := seedSnapshotDir(t, snapshotVersionDir(genDir, "1"), "v1")
	before, err := os.Stat(staleDB)
	if err != nil {
		t.Fatal(err)
	}
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: genDir,
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: "agent-issue-v2", Env: map[string]string{}}) }()

	var argv []string
	deadline := time.Now().Add(5 * time.Second)
	for argv == nil {
		if b, err := os.ReadFile(argsFile); err == nil {
			argv = strings.Split(strings.TrimSpace(string(b)), "\n")
		} else if time.Now().After(deadline) {
			t.Fatal("fake bwrap never ran")
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}

	v2Dir := snapshotVersionDir(genDir, "2")
	i := slices.Index(argv, "--overlay-src")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != v2Dir {
		t.Errorf("--overlay-src = %v, want %s", argv, v2Dir)
	}
	if got, err := os.ReadFile(snapshotDBPath(v2Dir)); err != nil || string(got) != "v2" {
		t.Errorf("db-2 db.sqlite = %q, %v; want the freshly written snapshot", got, err)
	}
	for _, lock := range []string{SnapshotLockPath(genDir), SnapshotLockPath(v2Dir)} {
		if err := probeExclusiveLock(lock); err != syscall.EWOULDBLOCK {
			t.Errorf("exclusive probe of %s while the Box runs = %v, want EWOULDBLOCK (shared lock held)", lock, err)
		}
	}

	if err := a.Kill("agent-issue-v2"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Kill")
	}
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

func lookTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
