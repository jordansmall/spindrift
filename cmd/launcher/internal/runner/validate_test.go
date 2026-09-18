package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRuntime_Empty(t *testing.T) {
	if err := ValidateRuntime(""); err == nil {
		t.Fatal("ValidateRuntime(\"\") should error")
	}
}

func TestValidateRuntime_NotOnPath(t *testing.T) {
	if err := ValidateRuntime("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("ValidateRuntime should error for a binary absent from PATH")
	}
}

func TestValidateRuntime_OnPath(t *testing.T) {
	if err := ValidateRuntime("echo"); err != nil {
		t.Errorf("ValidateRuntime(\"echo\") = %v, want nil", err)
	}
}

// ValidateRuntime("rancher") looks up "nerdctl", not the literal name (issue
// #1274). Some hosts ship nerdctl, so the test branches: when it is present
// the call succeeds, and when it is absent the error names nerdctl and
// Rancher Desktop.
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

// Callers with their own PATH lookup (quickstart's Environment.LookPath) must
// get the same nerdctl lookup and the same message text as ValidateRuntime
// (issue #2561).
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

func TestValidatePastaWithLookup_Found(t *testing.T) {
	fakeLookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	if err := ValidatePastaWithLookup(fakeLookPath); err != nil {
		t.Errorf("ValidatePastaWithLookup() = %v, want nil when pasta resolves", err)
	}
}

// The error must name both pasta and the NETWORK_MODE=host opt-out (issue
// #2666), so the launcher refuses to start rather than silently falling back
// to a shared host network namespace.
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

func TestValidatePasta_NotOnPath(t *testing.T) {
	// A host that does ship pasta would fail this assertion, so skip there
	// rather than report a false failure.
	if _, err := exec.LookPath("pasta"); err == nil {
		t.Skip("pasta is on PATH in this environment; nothing to assert")
	}
	if err := ValidatePasta(); err == nil {
		t.Fatal("ValidatePasta() should error when pasta is absent from PATH")
	}
}

// The passing fake exec stands in for a kernel that allows unprivileged
// overlayfs mounts inside a user namespace.
func TestValidateOverlayWithExec_Succeeds(t *testing.T) {
	fakeExec := func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	if err := ValidateOverlayWithExec(fakeExec); err != nil {
		t.Errorf("ValidateOverlayWithExec() = %v, want nil when the smoke test succeeds", err)
	}
}

// On a host without unprivileged overlayfs support (issue #2665, ADR 0042)
// the error must name the nixStoreWritable knob and what is missing, not just
// pass through a raw bwrap mount error.
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

// A host with no cgroup v2 delegation to this process (ADR 0042) must get a
// descriptive error. The seam swap mirrors provisionCgroup's own
// TestBwrapRun_NoCgroupDelegationWarnsAndProceeds.
func TestValidateCgroupDelegation_NotDelegated(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	// No parent "/x" dir exists under this root, so the probe os.Mkdir fails
	// exactly as it would on a host with no writable delegated subtree.
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	err := ValidateCgroupDelegation([]string{"memory", "pids"})
	if err == nil {
		t.Fatal("ValidateCgroupDelegation([memory pids]) should error when the probe subtree can't be created")
	}
	if !strings.Contains(err.Error(), "cgroup") {
		t.Errorf("error = %q, want it to mention cgroup", err.Error())
	}
}

// probeDirName duplicates the PID-keyed name ValidateCgroupDelegation
// computes internally, so tests can predict the path it creates and removes
// without exporting the naming scheme. Change one and change the other.
func probeDirName() string {
	return fmt.Sprintf("spindrift-doctor-probe-%d", os.Getpid())
}

// Real cgroup v2 auto-populates pids.max and memory.max in a fresh subtree
// when the parent's cgroup.subtree_control enables those controllers. A tmpfs
// test dir has no such kernel behaviour, so the test swaps
// statCgroupControllerFile to report both present. The leftover-entries check
// pins that the probe directory is removed before returning.
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

	if err := ValidateCgroupDelegation([]string{"memory", "pids"}); err != nil {
		t.Fatalf("ValidateCgroupDelegation([memory pids]) = %v, want nil", err)
	}

	entries, err := os.ReadDir(cgroupFSRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cgroupFSRoot has leftover entries after ValidateCgroupDelegation: %v", entries)
	}
}

// A host whose cgroup.subtree_control does not delegate pids and memory can
// still create the subtree, and provisionCgroup's later writes would then
// fail silently. This test keeps the real statCgroupControllerFile, so an
// empty temp dir behaves exactly like such a host.
func TestValidateCgroupDelegation_MissingController(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	err := ValidateCgroupDelegation([]string{"memory", "pids"})
	if err == nil {
		t.Fatal("ValidateCgroupDelegation([memory pids]) should error when pids.max/memory.max are missing from the probe subtree")
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

// A doctor run killed between Mkdir and Remove leaves an empty probe dir
// under the same PID-keyed name. Without the self-heal, every later run fails
// on EEXIST and misreports a delegated host as non-delegated.
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

	// Pre-create the exact PID-keyed probe dir the function will compute,
	// standing in for a stale leftover from a killed prior run.
	dir := filepath.Join(cgroupFSRoot, probeDirName())
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidateCgroupDelegation([]string{"memory", "pids"})
	if err != nil {
		t.Fatalf("ValidateCgroupDelegation([memory pids]) = %v, want nil (should self-heal the stale leftover directory)", err)
	}

	entries, err := os.ReadDir(cgroupFSRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cgroupFSRoot has leftover entries after ValidateCgroupDelegation self-heal: %v", entries)
	}
}

// The probe must run at the resolved anchor (user@1000.service), not the
// launcher's own scope several levels below it, which is the #3273 fix. The
// captured paths prove where it ran, since the seam receives the full
// control-file path.
func TestValidateCgroupDelegation_ResolvesSystemdAnchor(t *testing.T) {
	anchor, _ := systemdUserSessionFixture(t, "memory pids")

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	var gotPaths []string
	statCgroupControllerFile = func(path string) (os.FileInfo, error) {
		gotPaths = append(gotPaths, path)
		return nil, nil
	}

	if err := ValidateCgroupDelegation([]string{"memory", "pids"}); err != nil {
		t.Fatalf("ValidateCgroupDelegation([memory pids]) = %v, want nil", err)
	}

	if len(gotPaths) == 0 {
		t.Fatal("statCgroupControllerFile was never called")
	}
	for _, p := range gotPaths {
		dir := filepath.Dir(p)
		if filepath.Dir(dir) != anchor {
			t.Errorf("probe control file %q not under anchor %q", p, anchor)
		}
	}

	if _, err := os.Stat(filepath.Join(anchor, probeDirName())); !os.IsNotExist(err) {
		t.Errorf("probe dir under anchor %q was not removed (stat err = %v)", anchor, err)
	}
}

// With neither limit configured, provisionCgroup writes no limit file, so the
// probe must ask no controller question either. A controller-less host still
// passes the writability check.
func TestValidateCgroupDelegation_NoControllersChecksWritabilityOnly(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	statCgroupControllerFile = func(path string) (os.FileInfo, error) {
		t.Errorf("statCgroupControllerFile(%q) called with no configured controllers", path)
		return nil, nil
	}

	if err := ValidateCgroupDelegation(nil); err != nil {
		t.Fatalf("ValidateCgroupDelegation(nil) = %v, want nil", err)
	}
}

// The dogfood Linux default sets PIDS_LIMIT, leaves MEMORY_LIMIT unset, and
// gets a delegation carrying only pids. Doctor must ask exactly what the
// runner will, probing only pids.max at the same anchor (issue #3273). Asking
// for memory too finds no anchor, falls back to the launcher's own scope, and
// reports the row failing while the runner enforces PIDS_LIMIT fine.
func TestValidateCgroupDelegation_PidsOnlyDelegation(t *testing.T) {
	anchor, scope := systemdUserSessionFixture(t, "pids")

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	var gotPaths []string
	statCgroupControllerFile = func(path string) (os.FileInfo, error) {
		gotPaths = append(gotPaths, path)
		if filepath.Base(path) != "pids.max" {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}

	if err := ValidateCgroupDelegation([]string{"pids"}); err != nil {
		t.Fatalf("ValidateCgroupDelegation([pids]) = %v, want nil", err)
	}

	if len(gotPaths) != 1 {
		t.Fatalf("statCgroupControllerFile calls = %v, want exactly one (pids.max)", gotPaths)
	}
	if got := filepath.Dir(filepath.Dir(gotPaths[0])); got != anchor {
		t.Errorf("probed under %q, want the resolveCgroupAnchor anchor %q", got, anchor)
	}
	if _, err := os.Stat(filepath.Join(scope, probeDirName())); !os.IsNotExist(err) {
		t.Errorf("probe dir created under the launcher's own scope %q, want the anchor instead", scope)
	}
}

// The doctor row and the runner must stay in lockstep, both going through the
// shared cgroupParentDir seam, so the two resolve to the same directory on
// the same fixture.
func TestValidateCgroupDelegation_AgreesWithResolveCgroupAnchor(t *testing.T) {
	anchor, _ := systemdUserSessionFixture(t, "memory pids")

	origStat := statCgroupControllerFile
	t.Cleanup(func() { statCgroupControllerFile = origStat })
	statCgroupControllerFile = func(string) (os.FileInfo, error) { return nil, nil }

	if err := ValidateCgroupDelegation([]string{"memory", "pids"}); err != nil {
		t.Fatalf("ValidateCgroupDelegation([memory pids]) = %v, want nil", err)
	}

	got, ok := resolveCgroupAnchor(systemdSelfCgroup, []string{"memory", "pids"})
	if !ok || got != anchor {
		t.Errorf("resolveCgroupAnchor = (%q, %v), want (%q, true)", got, ok, anchor)
	}
}
