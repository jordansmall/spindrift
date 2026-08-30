package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRuntime_Empty verifies ValidateRuntime rejects an unset
// RUNTIME before any adapter is constructed.
func TestValidateRuntime_Empty(t *testing.T) {
	if err := ValidateRuntime(""); err == nil {
		t.Fatal("ValidateRuntime(\"\") should error")
	}
}

// TestValidateRuntime_NotOnPath verifies ValidateRuntime rejects a runtime
// binary that cannot be found on PATH.
func TestValidateRuntime_NotOnPath(t *testing.T) {
	if err := ValidateRuntime("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("ValidateRuntime should error for a binary absent from PATH")
	}
}

// TestValidateRuntime_OnPath verifies ValidateRuntime accepts a binary
// present on PATH.
func TestValidateRuntime_OnPath(t *testing.T) {
	if err := ValidateRuntime("echo"); err != nil {
		t.Errorf("ValidateRuntime(\"echo\") = %v, want nil", err)
	}
}

// TestValidateRuntime_RancherLooksUpNerdctl verifies ValidateRuntime("rancher")
// looks up "nerdctl" on PATH (not the literal string "rancher"): when nerdctl
// is absent it reports a Rancher-Desktop/containerd-mode-flavored error
// naming nerdctl; when present (some hosts ship it) it succeeds like any
// other on-PATH runtime (issue #1274).
func TestValidateRuntime_RancherLooksUpNerdctl(t *testing.T) {
	err := ValidateRuntime("rancher")
	if _, lookErr := exec.LookPath("nerdctl"); lookErr == nil {
		if err != nil {
			t.Errorf("ValidateRuntime(\"rancher\") = %v, want nil (nerdctl on PATH)", err)
		}
		return
	}
	if err == nil {
		t.Fatal("ValidateRuntime(\"rancher\") should error when nerdctl is absent from PATH")
	}
	if !strings.Contains(err.Error(), "nerdctl") {
		t.Errorf("error = %q, want it to mention nerdctl", err.Error())
	}
	if !strings.Contains(err.Error(), "Rancher Desktop") {
		t.Errorf("error = %q, want it to mention Rancher Desktop", err.Error())
	}
}

// TestValidateRuntimeWithLookup_RancherLooksUpNerdctl verifies
// ValidateRuntimeWithLookup("rancher", ...) drives the same nerdctl lookup
// and Rancher-Desktop-flavored error message as ValidateRuntime, but through
// an injectable lookPath func instead of the real PATH — so callers with
// their own PATH-lookup abstraction (e.g. quickstart's Environment.LookPath)
// can reuse this exact validation logic and message text (issue #2561).
func TestValidateRuntimeWithLookup_RancherLooksUpNerdctl(t *testing.T) {
	fakeLookPath := func(file string) (string, error) {
		if file == "nerdctl" {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := ValidateRuntimeWithLookup("rancher", fakeLookPath)
	if err == nil {
		t.Fatal("ValidateRuntimeWithLookup(\"rancher\", ...) should error when nerdctl is absent from PATH")
	}
	if !strings.Contains(err.Error(), "nerdctl") {
		t.Errorf("error = %q, want it to mention nerdctl", err.Error())
	}
	if !strings.Contains(err.Error(), "Rancher Desktop") {
		t.Errorf("error = %q, want it to mention Rancher Desktop", err.Error())
	}
}

// TestValidatePastaWithLookup_Found verifies ValidatePastaWithLookup accepts
// a lookPath that resolves "pasta".
func TestValidatePastaWithLookup_Found(t *testing.T) {
	fakeLookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	if err := ValidatePastaWithLookup(fakeLookPath); err != nil {
		t.Errorf("ValidatePastaWithLookup() = %v, want nil when pasta resolves", err)
	}
}

// TestValidatePastaWithLookup_NotFound verifies ValidatePastaWithLookup
// rejects a lookPath that cannot resolve "pasta", with an actionable error
// naming both pasta itself and the NETWORK_MODE=host opt-out (issue #2666) —
// so the launcher refuses to start rather than silently falling back to a
// shared host network namespace.
func TestValidatePastaWithLookup_NotFound(t *testing.T) {
	fakeLookPath := func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	err := ValidatePastaWithLookup(fakeLookPath)
	if err == nil {
		t.Fatal("ValidatePastaWithLookup() should error when pasta is absent from PATH")
	}
	if !strings.Contains(err.Error(), "pasta") {
		t.Errorf("error = %q, want it to mention pasta", err.Error())
	}
	if !strings.Contains(err.Error(), "NETWORK_MODE=host") {
		t.Errorf("error = %q, want it to mention NETWORK_MODE=host", err.Error())
	}
}

// TestValidatePasta_NotOnPath verifies ValidatePasta (the real-PATH entry
// point) rejects a binary name that cannot be found on PATH, mirroring
// TestValidateRuntime_NotOnPath.
func TestValidatePasta_NotOnPath(t *testing.T) {
	// pasta is unlikely to be on the test-runner's PATH; if it is, this test
	// is a no-op success case rather than a false failure.
	if _, err := exec.LookPath("pasta"); err == nil {
		t.Skip("pasta is on PATH in this environment; nothing to assert")
	}
	if err := ValidatePasta(); err == nil {
		t.Fatal("ValidatePasta() should error when pasta is absent from PATH")
	}
}

// TestValidateOverlayWithExec_Succeeds verifies ValidateOverlayWithExec
// returns nil when the injected exec seam's smoke-test command succeeds
// (standing in for a kernel/host that does allow unprivileged overlayfs
// mounts inside a user namespace).
func TestValidateOverlayWithExec_Succeeds(t *testing.T) {
	fakeExec := func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	if err := ValidateOverlayWithExec(fakeExec); err != nil {
		t.Errorf("ValidateOverlayWithExec() = %v, want nil when the smoke test succeeds", err)
	}
}

// TestValidateOverlayWithExec_Fails verifies ValidateOverlayWithExec returns
// an actionable, non-nil error when the injected exec seam's smoke-test
// command fails (standing in for a host without unprivileged overlayfs
// support, issue #2665 / ADR 0042) -- naming the nixStoreWritable knob and
// what's missing, not just surfacing a raw bwrap mount error.
func TestValidateOverlayWithExec_Fails(t *testing.T) {
	fakeExec := func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	err := ValidateOverlayWithExec(fakeExec)
	if err == nil {
		t.Fatal("ValidateOverlayWithExec() should error when the smoke test fails")
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("error = %q, want it to mention overlay", err.Error())
	}
	if !strings.Contains(err.Error(), "nixStoreWritable") && !strings.Contains(err.Error(), "NIX_STORE_WRITABLE") {
		t.Errorf("error = %q, want it to mention the nixStoreWritable/NIX_STORE_WRITABLE knob", err.Error())
	}
}

// TestValidateCgroupDelegation_NotDelegated verifies ValidateCgroupDelegation
// returns a non-nil, descriptive error when the probe subtree can't be
// created -- standing in for a host with no cgroup v2 delegation to this
// process (ADR 0042), mirroring provisionCgroup's own
// TestBwrapRun_NoCgroupDelegationWarnsAndProceeds seam-swap.
func TestValidateCgroupDelegation_NotDelegated(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	// No parent "/x" dir exists under this root, so the probe os.Mkdir fails
	// exactly as it would on a host with no writable delegated subtree.
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	err := ValidateCgroupDelegation()
	if err == nil {
		t.Fatal("ValidateCgroupDelegation() should error when the probe subtree can't be created")
	}
	if !strings.Contains(err.Error(), "cgroup") {
		t.Errorf("error = %q, want it to mention cgroup", err.Error())
	}
}

// probeDirName mirrors the PID-keyed probe directory name
// ValidateCgroupDelegation computes internally, so tests can predict the
// exact path it will Mkdir/Stat/Remove without exporting the naming scheme
// itself.
func probeDirName() string {
	return fmt.Sprintf("spindrift-doctor-probe-%d", os.Getpid())
}

// TestValidateCgroupDelegation_Delegated verifies ValidateCgroupDelegation
// returns nil when the probe subtree can be created and the pids.max/
// memory.max controller files exist inside it, and that it removes the probe
// directory again before returning -- nothing should be left behind for the
// caller to clean up. Real cgroup v2 auto-populates pids.max/memory.max in a
// freshly created subtree whenever the parent's cgroup.subtree_control
// enables those controllers; a plain tmpfs test dir has no such kernel
// behaviour, so the test swaps the statCgroupControllerFile seam to report
// both files present, standing in for a host that does delegate them.
func TestValidateCgroupDelegation_Delegated(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	statCgroupControllerFile = func(string) (os.FileInfo, error) { return nil, nil }

	if err := ValidateCgroupDelegation(); err != nil {
		t.Fatalf("ValidateCgroupDelegation() = %v, want nil", err)
	}

	entries, err := os.ReadDir(cgroupFSRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cgroupFSRoot has leftover entries after ValidateCgroupDelegation: %v", entries)
	}
}

// TestValidateCgroupDelegation_MissingController verifies
// ValidateCgroupDelegation returns a descriptive, non-nil error when the
// probe subtree can be created but is missing pids.max/memory.max -- standing
// in for a host whose cgroup.subtree_control doesn't delegate the pids/
// memory controllers, where provisionCgroup's later writes would otherwise
// silently fail even though subtree creation itself succeeded. It uses the
// real statCgroupControllerFile (os.Stat), so a plain empty temp dir behaves
// exactly like a controller-not-delegated host.
func TestValidateCgroupDelegation_MissingController(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	err := ValidateCgroupDelegation()
	if err == nil {
		t.Fatal("ValidateCgroupDelegation() should error when pids.max/memory.max are missing from the probe subtree")
	}
	if !strings.Contains(err.Error(), "pids.max") && !strings.Contains(err.Error(), "memory.max") {
		t.Errorf("error = %q, want it to mention pids.max or memory.max", err.Error())
	}

	entries, err := os.ReadDir(cgroupFSRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cgroupFSRoot has leftover entries after a failed ValidateCgroupDelegation: %v", entries)
	}
}

// TestValidateCgroupDelegation_StaleLeftoverSelfHeals verifies
// ValidateCgroupDelegation does not permanently misreport a delegated host as
// non-delegated when a prior run's probe directory (same PID-keyed name) was
// left behind empty -- e.g. a doctor run killed between Mkdir and Remove. It
// should clear the stale directory and retry the Mkdir rather than failing on
// EEXIST forever.
func TestValidateCgroupDelegation_StaleLeftoverSelfHeals(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	statCgroupControllerFile = func(string) (os.FileInfo, error) { return nil, nil }

	// Pre-create the exact PID-keyed probe dir the function under test will
	// compute, standing in for a stale leftover from a killed prior run.
	dir := filepath.Join(cgroupFSRoot, probeDirName())
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidateCgroupDelegation()
	if err != nil {
		t.Fatalf("ValidateCgroupDelegation() = %v, want nil (should self-heal the stale leftover directory)", err)
	}

	entries, err := os.ReadDir(cgroupFSRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cgroupFSRoot has leftover entries after ValidateCgroupDelegation self-heal: %v", entries)
	}
}
