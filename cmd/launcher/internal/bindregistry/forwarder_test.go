package bindregistry

import (
	"errors"
	"testing"
	"time"
)

// TestEnsureForwarderReady_AlreadyReady verifies that when probe reports the
// Forwarder is already listening, EnsureForwarderReady returns success
// without ever calling spawn -- the double-spawn-prevention path a re-apply
// run depends on.
func TestEnsureForwarderReady_AlreadyReady(t *testing.T) {
	spawnCalled := false
	probe := func(port int) bool { return true }
	spawn := func(socketPath string, port int) error {
		spawnCalled = true
		return nil
	}

	ready, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if !ready {
		t.Errorf("EnsureForwarderReady ready = false, want true")
	}
	if spawnCalled {
		t.Errorf("spawn was called, want it never called when already ready")
	}
}

// TestEnsureForwarderReady_SpawnThenReady verifies that when probe first
// reports not-ready, EnsureForwarderReady spawns exactly once and then polls
// probe until it flips ready.
func TestEnsureForwarderReady_SpawnThenReady(t *testing.T) {
	probeCalls := 0
	probe := func(port int) bool {
		probeCalls++
		// First call: pre-spawn already-ready check (false). Then a few
		// more false polls before flipping ready.
		return probeCalls > 3
	}
	spawnCalls := 0
	spawn := func(socketPath string, port int) error {
		spawnCalls++
		return nil
	}

	ready, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 200*time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if !ready {
		t.Errorf("EnsureForwarderReady ready = false, want true")
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times, want exactly 1", spawnCalls)
	}
}

// TestEnsureForwarderReady_SpawnThenTimeout verifies that when probe never
// flips ready after a successful spawn, EnsureForwarderReady gives up after
// timeout and returns (false, nil) -- a timeout is not itself a Go error.
func TestEnsureForwarderReady_SpawnThenTimeout(t *testing.T) {
	probe := func(port int) bool { return false }
	spawnCalls := 0
	spawn := func(socketPath string, port int) error {
		spawnCalls++
		return nil
	}

	ready, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if ready {
		t.Errorf("EnsureForwarderReady ready = true, want false on timeout")
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times, want exactly 1", spawnCalls)
	}
}

// TestEnsureForwarderReady_SpawnError verifies that a spawn failure (e.g.
// socat missing from PATH) short-circuits: EnsureForwarderReady returns the
// spawn error verbatim without polling probe again afterward.
func TestEnsureForwarderReady_SpawnError(t *testing.T) {
	wantErr := errors.New("exec: \"socat\": executable file not found in $PATH")
	probeCallsAfterSpawn := 0
	spawnAttempted := false
	probe := func(port int) bool {
		if spawnAttempted {
			probeCallsAfterSpawn++
		}
		return false
	}
	spawn := func(socketPath string, port int) error {
		spawnAttempted = true
		return wantErr
	}

	ready, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureForwarderReady err = %v, want %v", err, wantErr)
	}
	if ready {
		t.Errorf("EnsureForwarderReady ready = true, want false on spawn error")
	}
	if probeCallsAfterSpawn != 0 {
		t.Errorf("probe called %d times after spawn error, want 0", probeCallsAfterSpawn)
	}
}
