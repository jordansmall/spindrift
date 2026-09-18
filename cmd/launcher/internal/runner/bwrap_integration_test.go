//go:build integration

package runner

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// resolveSandboxBin resolves name to an absolute path reachable inside a bwrap
// sandbox that only ro-binds /nix/store: a host FHS-compat symlink like
// /bin/bash lives outside the mounted tree. A path already under /nix/store is
// returned as-is, since resolving further follows multi-call-binary symlinks
// and changes the basename bwrap's argv[0] dispatch relies on.
func resolveSandboxBin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found on PATH", name)
	}
	if strings.HasPrefix(p, "/nix/store/") {
		return p
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("resolve %s symlink: %v", name, err)
	}
	return real
}

// requireRealBwrap skips the test when this host cannot create an unprivileged
// user namespace (non-Linux, bwrap missing, or a nested sandbox without
// CAP_SYS_ADMIN, which the dogfood Box itself hits). It returns the resolved
// bash binary the probes exec, so a real regression fails loudly instead of
// being masked by a missing-binary skip further down.
func requireRealBwrap(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bwrap integration test requires Linux")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not found on PATH")
	}
	bashBin := resolveSandboxBin(t, "bash")
	probe := exec.Command("bwrap", "--ro-bind", "/nix/store", "/nix/store", "--unshare-user", "--uid", "1000", "--gid", "1000", "--tmpfs", "/tmp", "--", bashBin, "-c", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create an unprivileged user namespace here: %v: %s", err, out)
	}
	return bashBin
}

// requireRealBwrapProc guards the nix probes, which keep --proc/--dev because
// nix resolves its own binary via /proc/self/exe. It unshares pid/ipc/uts to
// match buildArgs' unshareFlags: a bare --proc/--dev probe without
// --unshare-pid mounts fine in a nested sandbox that then fails to mount /proc
// once a fresh pid namespace is layered on top. It skips rather than fails.
func requireRealBwrapProc(t *testing.T) string {
	t.Helper()
	bashBin := requireRealBwrap(t)
	probe := exec.Command("bwrap",
		"--ro-bind", "/nix/store", "/nix/store",
		"--proc", "/proc", "--dev", "/dev",
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--tmpfs", "/tmp", "--", bashBin, "-c", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot mount --proc/--dev with a fresh pid namespace in this nested sandbox: %v: %s", err, out)
	}
	return bashBin
}

// requireRealPasta skips the test when this host cannot exercise a real
// pasta+bwrap hierarchy: non-Linux, pasta missing from PATH, or a nested
// sandbox that can't create the network namespace and tap device pasta needs
// (the dogfood Box hits this). The probe invokes pasta with its real hardened
// flags, so a regression in those flags fails loudly instead of skipping.
func requireRealPasta(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("pasta integration test requires Linux")
	}
	if _, err := exec.LookPath("pasta"); err != nil {
		t.Skip("pasta not found on PATH")
	}
	trueBin := resolveSandboxBin(t, "true")
	probeArgs := append([]string{}, pastaHardenedFlags...)
	probeArgs = append(probeArgs, "--dns-forward", pastaDNSForwardAddr, "-f", "--", trueBin)
	probe := exec.Command("pasta", probeArgs...)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("pasta cannot create a network namespace/tap device here: %v: %s", err, out)
	}
}

// stripMountPair removes a "--flag target" pair from a bwrap argv. It drops
// --proc /proc and --dev /dev: mounting a fresh procfs/devfs needs
// CAP_SYS_ADMIN in the outer namespace, which a nested sandbox like the
// dogfood Box doesn't have, and the isolation properties under test here don't
// depend on either mount.
func stripMountPair(args []string, flag, target string) []string {
	out := args[:0:0]
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) && args[i+1] == target {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// newIntegrationBwrapAdapter wires a bwrapAdapter and etc dir the same way
// bwrapAdapter.Run does, with real passwd/group temp files mirroring
// lib/image.nix (issue #2663), so buildArgs produces production's exact flags.
// It pins networkMode="none" rather than the raw unshareNet knob: since issue
// #2666 unshareNet=true gets pasta-wrapped, breaking the single-token strip.
func newIntegrationBwrapAdapter(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	agentFiles := t.TempDir()
	if err := os.MkdirAll(filepath.Join(agentFiles, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	// buildArgs unconditionally ro-binds agentFiles/home/agent (issue #2843),
	// and production's baked agentFiles always has this subtree.
	if err := os.MkdirAll(filepath.Join(agentFiles, "home", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	etcDir := t.TempDir()
	// Mirrors lib/image.nix's passwdFile/groupFile content verbatim. Keep these
	// in sync by hand when that derivation changes.
	passwd := "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000:agent:/home/agent:/bin/bash\n"
	group := "root:x:0:\nagent:x:1000:\n"
	if err := os.WriteFile(filepath.Join(etcDir, "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etcDir, "group"), []byte(group), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &bwrapAdapter{
		agentFiles:    agentFiles,
		agentEnv:      "/fake/agent-env",
		passwdFile:    filepath.Join(etcDir, "passwd"),
		groupFile:     filepath.Join(etcDir, "group"),
		bakedPrefetch: "true",
		networkMode:   NetworkModeNone,
	}
	return a, etcDir
}

// newIntegrationBwrapAdapterIsolated switches to the default (issue #2666)
// isolate-with-pasta path: the zero-value networkMode, which pastaPath()
// treats like any non-"host"/non-"none" value. It is a separate helper so the
// probes above keep their networkMode="none" pin. It writes the
// /etc/resolv.conf Run would write, since callers here go through execTarget.
func newIntegrationBwrapAdapterIsolated(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	a, etcDir := newIntegrationBwrapAdapter(t)
	a.networkMode = ""
	resolvConf := "nameserver " + pastaDNSForwardAddr + "\n"
	if err := os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte(resolvConf), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, etcDir
}

// newIntegrationBwrapAdapterWithNix wires ADR 0042's in-box nix mechanism
// (issue #2664): a real nix.conf mirroring lib/image.nix, and a store-db
// snapshot made with the same "VACUUM INTO" statement
// bwrapBuildAdapter.snapshotStoreDB runs, called directly here so the test
// stays decoupled from bwrap.go's build-vs-run adapter split.
func newIntegrationBwrapAdapterWithNix(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not found on PATH")
	}
	if _, err := os.Stat(hostNixDBPath); err != nil {
		t.Skipf("host nix store db not found at %s", hostNixDBPath)
	}
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not found on PATH")
	}

	a, etcDir := newIntegrationBwrapAdapter(t)

	nixConfPath := filepath.Join(t.TempDir(), "nix.conf")
	conf := "experimental-features = nix-command flakes\nsandbox = false\nfilter-syscalls = false\n"
	if err := os.WriteFile(nixConfPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	a.nixConfigFile = nixConfPath

	snapDir := t.TempDir()
	dbDir := filepath.Join(snapDir, "nix", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dbDir, "db.sqlite")
	stmt := fmt.Sprintf("VACUUM INTO '%s';", strings.ReplaceAll(dest, "'", "''"))
	if out, err := exec.Command("sqlite3", hostNixDBPath, stmt).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 vacuum-into store db snapshot: %v: %s", err, out)
	}
	a.nixVarSnapshotDir = snapDir

	return a, etcDir
}

// newIntegrationBwrapAdapterWithWritableStore covers issue #2665's writable
// /nix/store overlay. buildArgs only swaps /nix/store's --ro-bind for
// --overlay-src+--tmp-overlay when nixConfigFile is set AND nixStoreWritable
// is true, so this reuses newIntegrationBwrapAdapterWithNix for the first half
// of that gate.
func newIntegrationBwrapAdapterWithWritableStore(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	a.nixStoreWritable = true
	return a, etcDir
}

// bwrapProbeArgs takes the real buildArgs() output for box, drops the
// --proc/--dev mounts a nested sandbox can't nest (see stripMountPair), and
// swaps the fixed "-- /agent/entrypoint.sh" tail for script, run via bash -c.
// Every other mount/hardening flag reaches bwrap as production would send it.
func bwrapProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) []string {
	args := a.buildArgs(etcDir, box)
	args = stripMountPair(args, "--proc", "/proc")
	args = stripMountPair(args, "--dev", "/dev")
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return append(args, bashBin, "-c", script)
}

// nixProbeArgs is bwrapProbeArgs's sibling for the nix-in-a-Box probes: unlike
// every other probe here it does NOT strip --proc/--dev, because nix resolves
// its own binary via /proc/self/exe and fails immediately without a real /proc
// mounted. Those probes guard with requireRealBwrapProc instead of
// requireRealBwrap.
func nixProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) []string {
	args := a.buildArgs(etcDir, box)
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return append(args, bashBin, "-c", script)
}

// pastaProbeArgs is bwrapProbeArgs's sibling for the pasta-wrapped exec
// target: it takes a's real execTarget output (pasta as the top-level program,
// with bwrap's own argv nested inside), strips the same --proc/--dev pair from
// that nested argv, and swaps the nested tail for script. The returned program
// is "pasta" whenever pastaPath() applies, matching what Run() would exec.
func pastaProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) (string, []string) {
	program, args, _ := a.execTarget(etcDir, box)
	args = stripMountPair(args, "--proc", "/proc")
	args = stripMountPair(args, "--dev", "/dev")
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return program, append(args, bashBin, "-c", script)
}

// TestBwrapIntegration_NixStoreReadOnly asserts from inside a real bwrap
// sandbox that /nix/store is not writable: the kernel enforcing the --ro-bind,
// not just the flag being present on argv (issue #576).
func TestBwrapIntegration_NixStoreReadOnly(t *testing.T) {
	bashBin := requireRealBwrap(t)
	// networkMode="none" skips the --ro-bind /etc/resolv.conf mount, which a
	// nested sandbox can't remount and which this assertion doesn't need.
	a, etcDir := newIntegrationBwrapAdapter(t)
	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "echo x > /nix/store/spindrift-integration-write-probe")

	cmd := exec.Command("bwrap", args...)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("expected write into /nix/store to fail inside the sandbox; bwrap output: %s", out)
	}
	if !strings.Contains(string(out), "Read-only file system") {
		t.Fatalf("expected a read-only-filesystem failure, got: %s (%v)", out, runErr)
	}
}

// TestBwrapIntegration_SandboxUID asserts from inside a real bwrap sandbox
// that the process runs as uid 1000: the --uid/--gid mapping buildArgs sets is
// enforced by the kernel, not just present on argv (issue #576).
func TestBwrapIntegration_SandboxUID(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapter(t)
	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "echo $EUID")

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap probe failed: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1000" {
		t.Errorf("uid inside sandbox = %q, want \"1000\"", got)
	}
}

// TestBwrapIntegration_UnshareNetBlocksNetwork asserts from inside a real
// bwrap sandbox with networkMode="none" (no pasta) that outbound network
// access fails: the kernel enforcing --unshare-net with no egress path, not
// just the flag being present on argv (issue #576).
func TestBwrapIntegration_UnshareNetBlocksNetwork(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapter(t)
	box := Box{Env: map[string]string{}}
	// bash's /dev/tcp pseudo-device is interpreted by bash itself, so it
	// needs no real /dev mount inside the sandbox.
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "exec 3<>/dev/tcp/1.1.1.1/80")

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err == nil {
		t.Fatalf("expected outbound connection to fail with --unshare-net; bwrap output: %s", out)
	}
	if !strings.Contains(string(out), "Network is unreachable") {
		t.Fatalf("expected a network-unreachable failure, got: %s (%v)", out, err)
	}
}

// TestBwrapIntegration_HomeAgentStagingReadable asserts from inside a real
// bwrap sandbox that a file staged under agentFiles' home/agent/ subtree is
// readable at the path bwrap.go's ro-bind targets (issue #2843). A prior fix
// ro-bound the staged content nested under /agent, already a read-only bind by
// then, which bubblewrap cannot mkdir into.
func TestBwrapIntegration_HomeAgentStagingReadable(t *testing.T) {
	bashBin := requireRealBwrap(t)
	catBin := resolveSandboxBin(t, "cat")
	a, etcDir := newIntegrationBwrapAdapter(t)

	const marker = "spindrift-integration-home-agent-marker"
	homeAgentDir := filepath.Join(a.agentFiles, "home", "agent")
	if err := os.MkdirAll(homeAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeAgentDir, "marker.txt"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, etcDir, box, bashBin, catBin+" "+homeAgentStagingDir+"/marker.txt")

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap probe failed: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != marker {
		t.Errorf("marker read from %s/marker.txt = %q, want %q", homeAgentStagingDir, got, marker)
	}
}

// TestBwrapIntegration_SecretNotOnProcessArgv sets a secret in Box.Env and
// reads /proc/<pid>/cmdline of the real, running bwrap process, the same place
// a local ps would look, to assert the secret never reaches argv. This
// exercises the argv the kernel received for a live process, not just the Go
// string slice buildArgs returns (issue #576).
func TestBwrapIntegration_SecretNotOnProcessArgv(t *testing.T) {
	bashBin := requireRealBwrap(t)
	sleepBin := resolveSandboxBin(t, "sleep")
	const marker = "spindrift-integration-secret-9f3c2a"
	t.Setenv("GH_TOKEN", marker)
	a, etcDir := newIntegrationBwrapAdapter(t)
	box := Box{Env: map[string]string{"GH_TOKEN": marker}}
	args := bwrapProbeArgs(a, etcDir, box, bashBin, sleepBin+" 2")

	cmd := exec.Command("bwrap", args...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bwrap: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
	if err != nil {
		t.Fatalf("read /proc/%d/cmdline: %v", cmd.Process.Pid, err)
	}
	if bytes.Contains(cmdline, []byte(marker)) {
		t.Errorf("secret %q found in the running bwrap process's argv: %q", marker, cmdline)
	}
}

// TestBwrapIntegration_PastaBlocksHostLoopback is issue #2666's central
// guarantee: a listener on the host's own loopback must be unreachable from
// inside a real pasta-wrapped Box on the default NETWORK_MODE. Without
// --no-map-gw (pastaHardenedFlags) pasta splices a guest connection to its
// gateway address through to the host's 127.0.0.1 on the same port.
func TestBwrapIntegration_PastaBlocksHostLoopback(t *testing.T) {
	requireRealPasta(t)
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapterIsolated(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on host loopback: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Accept rather than listen-and-ignore, so a stray successful splice is
	// observable and fails the test instead of passing unnoticed.
	accepted := make(chan bool, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			accepted <- false
			return
		}
		defer conn.Close()
		_, _ = conn.Read(make([]byte, 16))
		accepted <- true
	}()

	box := Box{Env: map[string]string{}}
	script := fmt.Sprintf("exec 3<>/dev/tcp/%s/%d && echo CONNECTED || echo BLOCKED", pastaDNSForwardAddr, port)
	program, args := pastaProbeArgs(a, etcDir, box, bashBin, script)

	out, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("pasta+bwrap probe failed to run: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "BLOCKED" {
		t.Errorf("guest connection to the host loopback via pasta's gateway address = %q, want BLOCKED (--no-map-gw should drop it)", got)
	}

	select {
	case wasAccepted := <-accepted:
		if wasAccepted {
			t.Error("host listener observed an accepted connection from inside the pasta-wrapped sandbox -- --no-map-gw should have prevented the splice")
		}
	case <-time.After(2 * time.Second):
		// No connection ever reached the listener, the expected outcome. The
		// guest-side assertion above already proved the connect attempt failed.
	}
}

// TestBwrapIntegration_PastaProvidesWorkingEgressInterface is a CI-safe proxy
// for "pasta gave the sandbox working egress" that does not depend on real
// internet reachability. It asserts pasta created a non-loopback interface
// inside the namespace: before issue #2666 a Box asking for
// isolation-with-egress reached buildArgs with no pasta wrapping at all.
func TestBwrapIntegration_PastaProvidesWorkingEgressInterface(t *testing.T) {
	requireRealPasta(t)
	bashBin := requireRealBwrap(t)
	ipBin := resolveSandboxBin(t, "ip")
	a, etcDir := newIntegrationBwrapAdapterIsolated(t)

	box := Box{Env: map[string]string{}}
	// "ip link show" talks to the kernel over an AF_NETLINK socket, so unlike
	// /sys/class/net it needs neither /sys nor /proc mounted, both stripped
	// from this probe's argv.
	program, args := pastaProbeArgs(a, etcDir, box, bashBin, ipBin+" -o link show")

	out, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("pasta+bwrap probe failed: %v: %s", err, out)
	}
	foundNonLoopback := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.Contains(line, ": lo:") || strings.Contains(line, ": lo@") {
			continue
		}
		foundNonLoopback = true
	}
	if !foundNonLoopback {
		t.Errorf("no non-loopback network interface found inside the pasta-wrapped sandbox (ip link show output: %q); pasta should have created a tap device", out)
	}
}

// TestBwrapIntegration_NixTrustsHostStorePaths is issue #2664's acceptance
// criterion 4 (ADR 0042): in a real sandbox, nix reports host store paths as
// valid. The "will be fetched"/"copying path" checks are vacuous on their own,
// since path-info has no substituter codepath at all; the NixBuild tests below
// prove that half, and the empty-snapshot test is the negative control.
func TestBwrapIntegration_NixTrustsHostStorePaths(t *testing.T) {
	bashBin := requireRealBwrapProc(t)
	nixBin := resolveSandboxBin(t, "nix")
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	box := Box{Env: map[string]string{}}

	script := fmt.Sprintf("export HOME=/tmp; %s --extra-experimental-features 'nix-command flakes' path-info %s", nixBin, bashBin)
	args := nixProbeArgs(a, etcDir, box, bashBin, script)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("nix path-info failed inside the sandbox: %v: %s", err, out)
	}
	if !strings.Contains(string(out), bashBin) {
		t.Errorf("nix path-info output = %q, want it to contain the resolved store path %q", out, bashBin)
	}
	if strings.Contains(string(out), "will be fetched") || strings.Contains(string(out), "copying path") {
		t.Errorf("nix path-info attempted to substitute/copy a path it should have trusted from the snapshot instead: %s", out)
	}
}

// TestBwrapIntegration_NixRejectsEmptyStoreSnapshot is the negative control
// for TestBwrapIntegration_NixTrustsHostStorePaths (issue #2664, ADR 0042):
// with nixVarSnapshotDir pointing at an empty directory, nix falls back to a
// fresh chroot store and reports the identical path as "is not valid". Without
// it, the positive test would also pass for a path-info that always exits 0.
func TestBwrapIntegration_NixRejectsEmptyStoreSnapshot(t *testing.T) {
	bashBin := requireRealBwrapProc(t)
	nixBin := resolveSandboxBin(t, "nix")
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	a.nixVarSnapshotDir = t.TempDir() // empty: no nix/db/db.sqlite in it at all
	box := Box{Env: map[string]string{}}

	script := fmt.Sprintf("export HOME=/tmp; %s --extra-experimental-features 'nix-command flakes' path-info %s", nixBin, bashBin)
	args := nixProbeArgs(a, etcDir, box, bashBin, script)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err == nil {
		t.Fatalf("expected nix path-info to fail against an empty store snapshot; bwrap output: %s", out)
	}
	if !strings.Contains(string(out), "is not valid") {
		t.Fatalf("expected an \"is not valid\" failure, got: %s (%v)", out, err)
	}
}

// TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot covers the
// half of issue #2664's criterion 4 that path-info cannot: `nix build` does
// have a real substituter codepath (it fails against an unregistered path with
// "there is no substituter that can build it"), so a silent exit 0 here is
// evidence the snapshot satisfied the build without substituting.
func TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot(t *testing.T) {
	bashBin := requireRealBwrapProc(t)
	nixBin := resolveSandboxBin(t, "nix")
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	box := Box{Env: map[string]string{}}

	script := fmt.Sprintf("export HOME=/tmp; %s --extra-experimental-features 'nix-command flakes' build --no-link %s", nixBin, bashBin)
	args := nixProbeArgs(a, etcDir, box, bashBin, script)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("nix build failed inside the sandbox: %v: %s", err, out)
	}
	for _, needle := range []string{"will be fetched", "copying path", "downloading", "querying info about"} {
		if strings.Contains(string(out), needle) {
			t.Errorf("nix build attempted to substitute a path it should have trusted from the snapshot instead (output contains %q): %s", needle, out)
		}
	}
}

// TestBwrapIntegration_NixBuildAttemptsSubstitutionOnEmptyStoreSnapshot is the
// negative control for the positive build probe above: with an empty
// nixVarSnapshotDir the identical `nix build --no-link` cannot find the path in
// the snapshot's records, so it reaches for a substituter, and with the network
// isolated (NetworkModeNone) that reach fails outright.
func TestBwrapIntegration_NixBuildAttemptsSubstitutionOnEmptyStoreSnapshot(t *testing.T) {
	bashBin := requireRealBwrapProc(t)
	nixBin := resolveSandboxBin(t, "nix")
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	a.nixVarSnapshotDir = t.TempDir() // empty: no nix/db/db.sqlite in it at all
	box := Box{Env: map[string]string{}}

	script := fmt.Sprintf("export HOME=/tmp; %s --extra-experimental-features 'nix-command flakes' build --no-link %s", nixBin, bashBin)
	args := nixProbeArgs(a, etcDir, box, bashBin, script)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err == nil {
		t.Fatalf("expected nix build to fail against an empty store snapshot; bwrap output: %s", out)
	}
	if !strings.Contains(string(out), "there is no substituter that can build it") {
		t.Fatalf("expected a \"there is no substituter that can build it\" failure, got: %s (%v)", out, err)
	}
}

// TestBwrapIntegration_StoreWritableWhenOverlayEnabled is issue #2665's
// central positive proof and the mirror of TestBwrapIntegration_NixStoreReadOnly:
// with nixStoreWritable set on top of nixConfigFile, the same write into
// /nix/store must now succeed, because the mount is --overlay-src/--tmp-overlay'd
// instead of --ro-bind (ADR 0042).
func TestBwrapIntegration_StoreWritableWhenOverlayEnabled(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapterWithWritableStore(t)
	box := Box{Env: map[string]string{}}
	const marker = "spindrift-integration-overlay-write-probe"
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "echo x > /nix/store/"+marker)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("expected write into the overlaid /nix/store to succeed inside the sandbox: %v: %s", err, out)
	}
}

// TestBwrapIntegration_HostStoreUnchangedAfterOverlayWrite proves issue
// #2665's second acceptance criterion, that the host's store is unchanged
// afterwards. The probe above only shows the guest's write syscall succeeded,
// not where it landed, so this test stats the host's real /nix/store from the
// test process and asserts the marker is absent.
func TestBwrapIntegration_HostStoreUnchangedAfterOverlayWrite(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapterWithWritableStore(t)
	box := Box{Env: map[string]string{}}
	const marker = "spindrift-integration-overlay-write-probe-host-check"
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "echo x > /nix/store/"+marker)

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("write into the overlaid /nix/store failed inside the sandbox: %v: %s", err, out)
	}

	hostPath := filepath.Join("/nix/store", marker)
	if _, statErr := os.Stat(hostPath); statErr == nil {
		t.Errorf("marker file %s was written to the host's real /nix/store; the overlay write should have stayed in the tmpfs upper", hostPath)
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("stat %s: %v", hostPath, statErr)
	}
}

// buildDenySyscallFilterBPF hand-assembles a classic-BPF seccomp program in the
// raw format bubblewrap's --seccomp FD reads: a flat array of 8-byte
// sock_filter entries, no sock_fprog envelope or length header. It denies sysNr
// and allows everything else, an arch mismatch included, which never happens
// here. jt and jf each count how many following instructions to skip.
func buildDenySyscallFilterBPF(t *testing.T, sysNr uint64) []byte {
	t.Helper()

	// The test only ever runs on its own native arch, so two entries suffice.
	var auditArch uint32
	switch runtime.GOARCH {
	case "amd64":
		auditArch = 0xC000003E // AUDIT_ARCH_X86_64
	case "arm64":
		auditArch = 0xC00000B7 // AUDIT_ARCH_AARCH64
	default:
		t.Skipf("no AUDIT_ARCH constant wired up for GOARCH %q", runtime.GOARCH)
	}

	const (
		bpfLdWAbs = 0x20 // BPF_LD  | BPF_W | BPF_ABS
		bpfJeqK   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
		bpfRetK   = 0x06 // BPF_RET | BPF_K

		seccompRetErrno    = 0x00050000 // SECCOMP_RET_ERRNO
		seccompRetDataMask = 0x0000ffff // SECCOMP_RET_DATA_MASK
		seccompRetAllow    = 0x7fff0000 // SECCOMP_RET_ALLOW
	)

	type sockFilter struct {
		code uint16
		jt   uint8
		jf   uint8
		k    uint32
	}
	instrs := []sockFilter{
		{code: bpfLdWAbs, k: 4},
		{code: bpfJeqK, k: auditArch, jt: 0, jf: 3},
		{code: bpfLdWAbs, k: 0},
		{code: bpfJeqK, k: uint32(sysNr), jt: 0, jf: 1},
		{code: bpfRetK, k: seccompRetErrno | (uint32(syscall.EPERM) & seccompRetDataMask)},
		{code: bpfRetK, k: seccompRetAllow},
	}

	buf := make([]byte, 0, len(instrs)*8)
	for _, in := range instrs {
		var b [8]byte
		binary.LittleEndian.PutUint16(b[0:2], in.code)
		b[2] = in.jt
		b[3] = in.jf
		binary.LittleEndian.PutUint32(b[4:8], in.k)
		buf = append(buf, b[:]...)
	}
	return buf
}

// TestBwrapIntegration_SyscallFilterDeniesKill is issue #2670's central
// missing guarantee: a syscall the compiled BPF filter denies must actually
// fail inside a real bwrap sandbox, using the same fd-passing mechanism
// production uses (bwrapAdapter.buildArgs' "--seccomp 3" plus
// cmd.ExtraFiles), not just a Go-level assertion about argv.
func TestBwrapIntegration_SyscallFilterDeniesKill(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapter(t)

	filterPath := filepath.Join(t.TempDir(), "deny-kill.bpf")
	if err := os.WriteFile(filterPath, buildDenySyscallFilterBPF(t, uint64(syscall.SYS_KILL)), 0o644); err != nil {
		t.Fatal(err)
	}
	a.syscallFilterPath = filterPath

	box := Box{Env: map[string]string{}}
	// bash's "kill -0 $$" is a builtin that calls kill(2) against bash's own
	// pid, so no extra binary is needed, and nothing else bash does at startup
	// touches kill(2).
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "kill -0 $$")
	if !strings.Contains(strings.Join(args, " "), "--seccomp 3") {
		t.Fatalf("expected --seccomp 3 in the constructed argv: %v", args)
	}

	filterFile, err := os.Open(filterPath)
	if err != nil {
		t.Fatal(err)
	}
	defer filterFile.Close()

	cmd := exec.Command("bwrap", args...)
	cmd.ExtraFiles = []*os.File{filterFile}
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("expected kill(2) to fail under the syscall filter; bwrap output: %s", out)
	}
	if !strings.Contains(string(out), "Operation not permitted") {
		t.Fatalf("expected an \"Operation not permitted\" failure, got: %s (%v)", out, runErr)
	}
}

// TestBwrapIntegration_SyscallFilterAllowsNormalWork is the positive control
// for TestBwrapIntegration_SyscallFilterDeniesKill: the same filter, attached
// the same way, must not break an ordinary command that never touches the
// denied syscall, proving the filter is scoped to kill(2) rather than a
// blanket deny that would sink every Dispatch.
func TestBwrapIntegration_SyscallFilterAllowsNormalWork(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapter(t)

	filterPath := filepath.Join(t.TempDir(), "deny-kill.bpf")
	if err := os.WriteFile(filterPath, buildDenySyscallFilterBPF(t, uint64(syscall.SYS_KILL)), 0o644); err != nil {
		t.Fatal(err)
	}
	a.syscallFilterPath = filterPath

	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, etcDir, box, bashBin, "echo ok")
	if !strings.Contains(strings.Join(args, " "), "--seccomp 3") {
		t.Fatalf("expected --seccomp 3 in the constructed argv: %v", args)
	}

	filterFile, err := os.Open(filterPath)
	if err != nil {
		t.Fatal(err)
	}
	defer filterFile.Close()

	cmd := exec.Command("bwrap", args...)
	cmd.ExtraFiles = []*os.File{filterFile}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap probe under the syscall filter failed: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "ok" {
		t.Errorf("output = %q, want \"ok\"", got)
	}
}

// TestBwrapIntegration_OverlayUpperNotSharedAcrossInvocations proves issue
// #2665's third acceptance criterion, that paths built in one Box are absent
// from a freshly started Box: --tmp-overlay's tmpfs upper is scoped to a single
// bwrap process's mount namespace, so a marker written by the first invocation
// must be absent inside a second, independent one using identical args.
func TestBwrapIntegration_OverlayUpperNotSharedAcrossInvocations(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a, etcDir := newIntegrationBwrapAdapterWithWritableStore(t)
	box := Box{Env: map[string]string{}}
	const marker = "spindrift-integration-overlay-write-probe-not-shared"
	storePath := "/nix/store/" + marker

	firstArgs := bwrapProbeArgs(a, etcDir, box, bashBin, "echo x > "+storePath)
	if out, err := exec.Command("bwrap", firstArgs...).CombinedOutput(); err != nil {
		t.Fatalf("first bwrap invocation's write into the overlaid /nix/store failed: %v: %s", err, out)
	}

	secondArgs := bwrapProbeArgs(a, etcDir, box, bashBin, "test -e "+storePath+" && echo FOUND || echo ABSENT")
	out, err := exec.Command("bwrap", secondArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("second bwrap invocation failed to run: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "ABSENT" {
		t.Errorf("marker written by the first bwrap invocation was visible in a second, independent invocation's overlay = %q, want ABSENT (each invocation should get its own fresh tmpfs upper)", got)
	}
}
