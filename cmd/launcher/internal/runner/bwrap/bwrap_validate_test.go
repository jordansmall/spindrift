package bwrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

func lookPathOK(file string) (string, error) { return "/usr/bin/" + file, nil }

// fakeRun returns a run seam whose command is `sh -c script`, recording the
// program and argv it was asked to exec.
func fakeRun(script string, gotName *string, gotArgs *[]string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		*gotName, *gotArgs = name, args
		return exec.Command("sh", "-c", script)
	}
}

// The probe must hand pasta exactly the production flag set, then "--", then
// this binary, never a copy of the flags.
func TestValidatePastaWith_Usable(t *testing.T) {
	var name string
	var args []string
	flags := pastaFlags()
	run := fakeRun("echo "+netnsProbeOK, &name, &args)
	if err := validatePastaWith(lookPathOK, run, flags); err != nil {
		t.Fatalf("validatePastaWith() = %v, want nil when the namespace is usable", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, flags...), "--", exe, netnsProbeArg)
	if name != "pasta" || strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("probe ran %q %q, want pasta %q", name, args, want)
	}
}

// The error must name both pasta and the NETWORK_MODE=host opt-out (issue
// #2666), so the launcher refuses to start rather than silently falling back
// to a shared host network namespace.
func TestValidatePastaWith_NotFound(t *testing.T) {
	notFound := func(string) (string, error) { return "", fmt.Errorf("not found") }
	var name string
	var args []string
	err := validatePastaWith(notFound, fakeRun("exit 0", &name, &args), nil)
	if err == nil {
		t.Fatal("validatePastaWith() should error when pasta is absent from PATH")
	}
	want := "pasta not found on PATH — required to give a bwrap Box its own network namespace with the host loopback blocked (issue #2666); install pasta (the passt project) on PATH, or set NETWORK_MODE=host to explicitly opt into the pre-#2666 shared-network-namespace behaviour"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if name != "" {
		t.Errorf("probe ran %q despite pasta being missing", name)
	}
}

// Pasta made a namespace but left it without an address or default route, the
// shape of the missing --config-net (issue #5981).
func TestValidatePastaWith_NamespaceUnconfigured(t *testing.T) {
	var name string
	var args []string
	run := fakeRun("echo '"+netnsProbeMarker+" no IPv4 default route via an UP interface'", &name, &args)
	err := validatePastaWith(lookPathOK, run, nil)
	if err == nil {
		t.Fatal("validatePastaWith() should error when the namespace has no default route")
	}
	for _, want := range []string{"no IPv4 default route via an UP interface", "NETWORK_MODE=host", "unusable", "#5981", "--config-net"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if errors.Is(err, runner.ErrDegraded) {
		t.Error("an unusable namespace must be a hard error, not ErrDegraded")
	}
}

// A pasta that cannot create the namespace at all (nested sandbox, host barring
// unprivileged user/net namespaces) never reaches the in-namespace check.
func TestValidatePastaWith_CannotCreateNamespace(t *testing.T) {
	var name string
	var args []string
	run := fakeRun("echo 'Failed to create user namespace: Operation not permitted'; exit 1", &name, &args)
	err := validatePastaWith(lookPathOK, run, nil)
	if err == nil {
		t.Fatal("validatePastaWith() should error when pasta cannot create the namespace")
	}
	for _, want := range []string{"Operation not permitted", "exit status 1", "NETWORK_MODE=host", "unprivileged user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if !strings.Contains(err.Error(), "could not create a network namespace") {
		t.Errorf("error = %q, want it to say pasta could not create the namespace", err.Error())
	}
	if strings.Contains(err.Error(), "unusable") {
		t.Errorf("error = %q, must be distinct from the unusable-namespace message", err.Error())
	}
	if errors.Is(err, runner.ErrDegraded) {
		t.Error("a namespace pasta cannot create must be a hard error, not ErrDegraded")
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

func TestNetnsUnusable(t *testing.T) {
	const hdr = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"
	defRoute := func(iface, flags string) string {
		return hdr + iface + "\t00000000\t0100A8C0\t" + flags + "\t0\t0\t0\t00000000\t0\t0\t0\n" +
			iface + "\t0000A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	}
	lo := netnsIface{Name: "lo", Up: true, Loopback: true, HasV4: true}
	eth0 := netnsIface{Name: "eth0", Up: true, HasV4: true}
	cases := []struct {
		name   string
		ifaces []netnsIface
		routes string
		want   string
	}{
		{"usable", []netnsIface{lo, eth0}, defRoute("eth0", "0003"), ""},
		{"no interfaces", nil, defRoute("eth0", "0003"), "no non-loopback interface is UP"},
		{"loopback only", []netnsIface{lo}, defRoute("lo", "0003"), "no non-loopback interface is UP"},
		{"non-loopback down", []netnsIface{lo, {Name: "eth0", HasV4: true}}, defRoute("eth0", "0003"), "no non-loopback interface is UP"},
		{"up without v4", []netnsIface{lo, {Name: "eth0", Up: true}}, defRoute("eth0", "0003"), "no UP non-loopback interface has an IPv4 address"},
		{"no default route", []netnsIface{lo, eth0}, hdr + "eth0\t0000A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n", "no IPv4 default route via an UP interface with an IPv4 address"},
		{"empty route table", []netnsIface{lo, eth0}, "", "no IPv4 default route via an UP interface with an IPv4 address"},
		{"default route via non-UP iface", []netnsIface{lo, eth0, {Name: "eth1", HasV4: true}}, defRoute("eth1", "0003"), "no IPv4 default route via an UP interface with an IPv4 address"},
		{"default route not RTF_UP", []netnsIface{lo, eth0}, defRoute("eth0", "0002"), "no IPv4 default route via an UP interface with an IPv4 address"},
		{"route via address-less UP iface", []netnsIface{lo, eth0, {Name: "eth1", Up: true}}, defRoute("eth1", "0003"), "no IPv4 default route via an UP interface with an IPv4 address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netnsUnusable(tc.ifaces, tc.routes); got != tc.want {
				t.Errorf("netnsUnusable() = %q, want %q", got, tc.want)
			}
		})
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

// Re-executing the test binary with the probe env set must hit init's
// in-namespace branch: one marker line, exit 0, whatever the host's netns is.
func TestNetnsProbeInit_PrintsMarker(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, netnsProbeArg)
	cmd.Env = append(os.Environ(), netnsProbeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe child = %v, output %q, want exit 0", err, out)
	}
	if line := strings.TrimSpace(string(out)); !strings.HasPrefix(line, netnsProbeMarker) || strings.Contains(line, "\n") {
		t.Errorf("probe child output = %q, want one %q line", out, netnsProbeMarker)
	}
}

// pasta exiting 0 without ever running the child is not a host that bars
// namespaces; the message must not blame one.
func TestValidatePastaWith_ExitsWithoutRunningProbe(t *testing.T) {
	var name string
	var args []string
	err := validatePastaWith(lookPathOK, fakeRun("exit 0", &name, &args), nil)
	if err == nil {
		t.Fatal("validatePastaWith() should error when pasta exits 0 without the probe marker")
	}
	for _, want := range []string{"exited without running", "passt install", "NETWORK_MODE=host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "unprivileged") {
		t.Errorf("error = %q, must not blame the host's namespace policy", err.Error())
	}
	if errors.Is(err, runner.ErrDegraded) {
		t.Error("a probe that never ran must be a hard error, not ErrDegraded")
	}
}

// A stray netnsProbeEnv must not turn a normal launcher run into the probe.
func TestNetnsProbeRequested(t *testing.T) {
	cases := []struct {
		name string
		env  string
		args []string
		want bool
	}{
		{"env and arg", "1", []string{"x", netnsProbeArg}, true},
		{"env only", "1", []string{"x"}, false},
		{"env with other args", "1", []string{"x", "run"}, false},
		{"extra args", "1", []string{"x", netnsProbeArg, "y"}, false},
		{"arg only", "", []string{"x", netnsProbeArg}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netnsProbeRequested(tc.env, tc.args); got != tc.want {
				t.Errorf("netnsProbeRequested(%q, %q) = %v, want %v", tc.env, tc.args, got, tc.want)
			}
		})
	}
}
