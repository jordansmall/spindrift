//go:build integration

package runner

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// resolveSandboxBin resolves name to an absolute path reachable inside a
// bwrap sandbox that only ro-binds /nix/store. exec.LookPath alone is not
// enough: a host FHS-compat symlink like /bin/bash lives outside the
// mounted tree, so it must be resolved down to its real /nix/store target.
// A path already under /nix/store is returned as-is — resolving it further
// would follow multi-call-binary symlinks (e.g. .../bin/sleep -> coreutils)
// and change the basename bwrap's argv[0] dispatch relies on.
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

// requireRealBwrap skips the test when this host cannot create an
// unprivileged user namespace (non-Linux, bwrap missing, or a nested sandbox
// without CAP_SYS_ADMIN for further namespace/mount nesting — the dogfood
// Box itself hits this). It returns the resolved bash binary the probes
// exec, so a real regression fails loudly instead of being masked by a
// missing-binary skip further down.
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

// requireRealPasta skips the test when this host cannot exercise a real
// pasta+bwrap hierarchy: non-Linux, pasta missing from PATH, or (mirroring
// requireRealBwrap) a nested sandbox that can't create the network namespace
// and tap device pasta itself needs (no /dev/net/tun, can't mount a fresh
// /proc) -- the dogfood Box's own sandbox hits this. The probe actually
// invokes pasta with its real hardened flags rather than just checking
// LookPath, so a genuine regression in those flags (typo, removed flag)
// fails loudly instead of being silently masked by an environment-can't-
// do-this skip further down.
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

// stripMountPair removes a "--flag target" pair from a bwrap argv. Used to
// drop --proc /proc and --dev /dev: mounting a fresh procfs/devfs needs
// CAP_SYS_ADMIN in the *outer* namespace, which a nested sandbox (like the
// dogfood Box) doesn't have, even though the isolation properties under test
// here (uid mapping, ro-bind, unshare-net, secret exclusion) don't depend on
// either mount.
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

// newIntegrationBwrapAdapter builds a bwrapAdapter and etc dir wired the same
// way bwrapAdapter.Run wires them: real passwd/group temp files (mirroring
// lib/image.nix's passwdFile/groupFile derivations, issue #2663, since these
// probes have no real baked nix store paths to point at) and a real
// agentFiles dir, so buildArgs produces the exact mount/hardening flags
// production code would send to a real bwrap process. It always isolates via
// networkMode="none" (bare --unshare-net, no pasta helper) rather than the
// raw unshareNet knob: since issue #2666, unshareNet=true would otherwise
// get pasta-wrapped like any other isolating mode, which needs a real pasta
// binary reachable inside the sandboxed exec target and would break
// bwrapProbeArgs's single-trailing-token strip (it assumes a bare "--
// /agent/entrypoint.sh" tail). None of these probes care about pasta/DNS/
// egress — they exercise uid mapping, ro-bind, secret-argv-exclusion, home-
// agent staging, and "no network reachable at all", which "none" still
// gives. The returned etcDir is only needed downstream by
// newIntegrationBwrapAdapterIsolated (resolv.conf) and pastaProbeArgs.
func newIntegrationBwrapAdapter(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	agentFiles := t.TempDir()
	if err := os.MkdirAll(filepath.Join(agentFiles, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	// buildArgs unconditionally ro-binds agentFiles/home/agent (issue #2843);
	// production's baked agentFiles always has this subtree (lib/image.nix),
	// so match that here even for probes that don't care about its contents.
	if err := os.MkdirAll(filepath.Join(agentFiles, "home", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	etcDir := t.TempDir()
	// Mirrors lib/image.nix's passwdFile/groupFile content verbatim -- keep
	// these two in sync by hand if that derivation's content ever changes.
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

// newIntegrationBwrapAdapterIsolated is newIntegrationBwrapAdapter's sibling
// for probes that need the actual default (issue #2666) isolate-with-pasta
// path: the Go zero-value networkMode, which pastaPath() treats the same as
// any other non-"host"/non-"none" value. It is a separate helper rather than
// a parameter on newIntegrationBwrapAdapter itself so the 5 existing probes
// above keep depending on that helper's networkMode="none" pin unchanged
// (see its own doc comment for why they need bare --unshare-net, no pasta).
// It also writes /etc/resolv.conf into etcDir, mirroring what
// bwrapAdapter.Run itself does before invoking bwrap when pastaPath()
// applies (buildArgs ro-binds this path unconditionally in that case) --
// callers here build args directly via execTarget rather than going through
// Run, so they must supply it too.
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

// bwrapProbeArgs takes the real buildArgs() output for box, drops the
// --proc/--dev mounts a nested sandbox can't nest (see stripMountPair), and
// swaps the fixed "-- /agent/entrypoint.sh" tail for script, run via
// bash -c. Every other mount/hardening flag reaches bwrap exactly as
// production would send it.
func bwrapProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) []string {
	args := a.buildArgs(etcDir, box)
	args = stripMountPair(args, "--proc", "/proc")
	args = stripMountPair(args, "--dev", "/dev")
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return append(args, bashBin, "-c", script)
}

// pastaProbeArgs is bwrapProbeArgs's sibling for the pasta-wrapped exec
// target (execTarget, not buildArgs directly): it takes a's real
// execTarget(etcDir, box) output -- pasta as the top-level program, with
// bwrap's own argv (including the fixed "-- /agent/entrypoint.sh" tail)
// nested inside it -- strips the same --proc/--dev pair bwrapProbeArgs does
// (nested inside pasta's argv, not at pasta's own top level, but the nested-
// sandbox mount-nesting limitation stripMountPair documents applies to them
// identically), then swaps that nested tail for script run via bash -c. The
// returned program is "pasta" whenever pastaPath() applies to a, matching
// what a real bwrap.go Run() would exec.Command.
func pastaProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) (string, []string) {
	program, args := a.execTarget(etcDir, box)
	args = stripMountPair(args, "--proc", "/proc")
	args = stripMountPair(args, "--dev", "/dev")
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return program, append(args, bashBin, "-c", script)
}

// TestBwrapIntegration_NixStoreReadOnly launches a real bwrap sandbox using
// bwrapAdapter's own buildArgs() and asserts, from a process inside it, that
// /nix/store is not writable — the kernel enforcing the --ro-bind, not just
// the flag being present on argv (issue #576).
func TestBwrapIntegration_NixStoreReadOnly(t *testing.T) {
	bashBin := requireRealBwrap(t)
	// newIntegrationBwrapAdapter isolates via networkMode="none", which skips
	// the --ro-bind /etc/resolv.conf mount (buildArgs only adds it when net
	// is shared) — a nested sandbox can't remount it anyway, and it's
	// irrelevant to the /nix/store assertion under test here.
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

// TestBwrapIntegration_SandboxUID launches a real bwrap sandbox and asserts,
// from inside it, that the process runs as uid 1000 — the --uid/--gid
// mapping bwrapAdapter.buildArgs sets is enforced by the kernel, not just
// present on argv (issue #576).
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

// TestBwrapIntegration_UnshareNetBlocksNetwork launches a real bwrap sandbox
// with networkMode="none" (fully helper-free, no pasta) and asserts, from
// inside it, that outbound network access fails — the kernel enforcing
// --unshare-net with no egress path, not just the flag being present on
// argv (issue #576).
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

// TestBwrapIntegration_HomeAgentStagingReadable launches a real bwrap sandbox
// and asserts, from inside it, that a file staged under agentFiles'
// home/agent/ subtree is actually readable at the staging path bwrap.go's
// ro-bind targets — not just that the ro-bind pair appears on argv (issue
// #2843). A prior version of this fix ro-bound the staged content nested
// under /agent (already itself an existing read-only bind by the time that
// mount was appended), which bubblewrap cannot mkdir into: it would fail
// this test with a "Read-only file system" error from bwrap itself, not a
// Go-level assertion.
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
// asserts, by reading /proc/<pid>/cmdline of the real, running bwrap
// process, that it never reaches argv — the same place a local `ps` would
// look. This exercises the actual argv the kernel received for a live
// process, not just the Go string slice buildArgs returns (issue #576).
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

// TestBwrapIntegration_PastaBlocksHostLoopback is the central missing
// guarantee issue #2666 asks for: a listener on the host's own loopback must
// be unreachable from inside a real pasta-wrapped bwrap Box using the
// default (isolate-by-default) NETWORK_MODE. Without --no-map-gw
// (pastaHardenedFlags), pasta's own documented default behavior splices a
// guest connection to its gateway address (pastaDNSForwardAddr) through to
// the host's 127.0.0.1 on the same port; with --no-map-gw, that splice is
// closed and the guest's SYN to the gateway address on this arbitrary
// (non-DNS) port is simply dropped -- proven here against a real pasta
// process, not a fake one (issue #2666's own review finding: no test, fake
// or real, previously exercised the pasta-wrapped path at all).
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

	// Accept (not just listen-and-ignore) so a stray successful splice is
	// observable and fails the test, rather than silently succeeding
	// unnoticed on the host side while the guest-side assertion alone passes.
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
		// No connection ever reached the listener -- the expected outcome;
		// the guest-side assertion above already proved the connect attempt
		// failed, so there is nothing further to wait for.
	}
}

// TestBwrapIntegration_PastaProvidesWorkingEgressInterface is a CI-safe
// proxy for "pasta actually gave the sandbox working egress" that does not
// depend on real internet reachability -- this file's own
// TestBwrapIntegration_UnshareNetBlocksNetwork only ever asserts a *failure*
// against 1.1.1.1 for the same flakiness reason. It asserts pasta created
// and configured a non-loopback network interface inside the namespace --
// the actual regression this fix closes: before issue #2666, a Box asking
// for isolation-with-egress reached bwrap.go's buildArgs with no pasta
// wrapping at all, so it could never have had a working interface, let alone
// one it could actually route packets out of.
func TestBwrapIntegration_PastaProvidesWorkingEgressInterface(t *testing.T) {
	requireRealPasta(t)
	bashBin := requireRealBwrap(t)
	ipBin := resolveSandboxBin(t, "ip")
	a, etcDir := newIntegrationBwrapAdapterIsolated(t)

	box := Box{Env: map[string]string{}}
	// "ip link show" talks to the kernel over an AF_NETLINK socket -- unlike
	// /sys/class/net, it needs neither /sys nor /proc mounted, both stripped
	// from this probe's argv (see pastaProbeArgs) for the same nested-
	// sandbox reason bwrapProbeArgs strips them.
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
