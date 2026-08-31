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

// requireRealBwrapProc extends requireRealBwrap with a further preflight
// probe that keeps --proc/--dev mounted, unlike every other probe in this
// file (see stripMountPair's doc comment for why those are normally
// stripped). TestBwrapIntegration_NixTrustsHostStorePaths and its negative
// control cannot strip either: nix resolves its own binary via
// /proc/self/exe and fails immediately without a real /proc mounted. The
// probe also unshares pid/ipc/uts, matching buildArgs' own unshareFlags: a
// bare --proc/--dev probe without --unshare-pid mounts fine even in a
// nested sandbox that then fails to mount /proc once a fresh pid namespace
// is layered on top (a real regression this file's own dogfood Box hit
// while writing this test -- "bwrap: Can't mount proc on /newroot/proc:
// Operation not permitted" -- so probing with the weaker flag set would
// have let the real test fail outright here instead of skipping). Skips
// (doesn't fail) with the captured stderr when this specific probe can't
// nest a fresh pid/procfs/devfs namespace (no CAP_SYS_ADMIN in a
// doubly-nested sandbox), mirroring requireRealBwrap's own skip-not-fail
// contract for the bare-namespace case.
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

// newIntegrationBwrapAdapterWithNix is newIntegrationBwrapAdapter's sibling
// for probes that exercise ADR 0042's in-box nix mechanism (issue #2664): it
// additionally wires nixConfigFile to a real temp nix.conf (content mirrors
// lib/image.nix's nixConfigFile derivation verbatim) and nixVarSnapshotDir to
// a real temp dir populated by VACUUMing the host's live nix store DB into it
// directly with sqlite3 -- the same "VACUUM INTO" statement
// bwrapBuildAdapter.snapshotStoreDB runs in production -- rather than
// invoking that production seam, so this test stays decoupled from
// bwrap.go's internal build-vs-run adapter split. Skips (not fails) when
// sqlite3 isn't on PATH, when the host has no live nix store db to snapshot,
// or when nix itself isn't on PATH, mirroring requireRealBwrap's
// skip-on-missing-prerequisite style -- this mechanism needs all three
// present to prove anything real.
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

// newIntegrationBwrapAdapterWithWritableStore is
// newIntegrationBwrapAdapterWithNix's sibling for issue #2665's writable
// /nix/store overlay: buildArgs' own AND-gate only swaps /nix/store's
// --ro-bind for --overlay-src+--tmp-overlay when both nixConfigFile != "" AND
// nixStoreWritable are true, so this helper wires the identical real
// nix.conf + real VACUUMed store-db snapshot newIntegrationBwrapAdapterWithNix
// sets up (nixConfigFile alone still activates the unconditional /nix/var
// overlay -- that machinery has to be present and correct even though these
// probes never invoke nix itself) and additionally sets nixStoreWritable so
// the /nix/store mount itself becomes the ephemeral tmpfs overlay under test.
// Skips for the same reasons newIntegrationBwrapAdapterWithNix does (no
// sqlite3/nix on PATH, no host nix store db to snapshot).
func newIntegrationBwrapAdapterWithWritableStore(t *testing.T) (*bwrapAdapter, string) {
	t.Helper()
	a, etcDir := newIntegrationBwrapAdapterWithNix(t)
	a.nixStoreWritable = true
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

// nixProbeArgs is bwrapProbeArgs's sibling for the four nix-in-a-Box probes
// (TestBwrapIntegration_NixTrustsHostStorePaths,
// TestBwrapIntegration_NixRejectsEmptyStoreSnapshot,
// TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot, and
// TestBwrapIntegration_NixBuildAttemptsSubstitutionOnEmptyStoreSnapshot):
// unlike every other probe here, it does NOT strip --proc/--dev from the
// real buildArgs() output. nix resolves its own binary via
// /proc/self/exe and fails immediately ("error: reading symbolic link
// \"/proc/self/exe\": No such file or directory") without a real /proc
// mounted, so these probes use requireRealBwrapProc instead of
// requireRealBwrap to guard for that.
func nixProbeArgs(a *bwrapAdapter, etcDir string, box Box, bashBin, script string) []string {
	args := a.buildArgs(etcDir, box)
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
	program, args, _ := a.execTarget(etcDir, box)
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

// TestBwrapIntegration_NixTrustsHostStorePaths is issue #2664's missing
// acceptance criterion 4: "In a real sandbox, nix reports host store paths
// as valid and a devShell entry substitutes nothing." It launches a real
// bwrap sandbox wired with ADR 0042's in-box nix mechanism exactly as
// bwrapAdapter.buildArgs assembles it: a real nix.conf ro-bound to
// /etc/nix/nix.conf, and a real, freshly-VACUUMed snapshot of the host's own
// /nix/var/nix/db/db.sqlite overlaid onto /nix/var (--overlay-src +
// --tmp-overlay), with /nix/store itself staying the plain, unrelated
// --ro-bind newIntegrationBwrapAdapter already wires up. Network is fully
// isolated (NetworkModeNone, mirroring
// TestBwrapIntegration_UnshareNetBlocksNetwork), so an accidental
// substitution attempt fails loudly against a real store-path lookup rather
// than silently succeeding over the network.
//
// bash's own resolved binary stands in for "a devShell entry": it's already
// a realized host store path, exactly what the acceptance criterion
// describes, and is otherwise unrelated to the nix.conf/snapshot machinery
// under test. The assertion is nix exiting 0, reporting that path, and never
// logging "will be fetched"/"copying path" -- i.e. it trusted the snapshot's
// own records for a path it never had to re-realize. Those two substring
// checks are, on their own, vacuous evidence for the "substitutes nothing"
// half of the criterion: path-info is a pure metadata lookup with no
// substituter codepath at all, so it could never log either phrase even if
// the whole snapshot/overlay mechanism were broken. What actually proves
// "substitutes nothing" is TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot
// below, which runs `nix build` (a command with a real substituter
// codepath) against the same path instead.
//
// This test's negative control, TestBwrapIntegration_NixRejectsEmptyStoreSnapshot,
// proves the assertion here isn't vacuous: point --overlay-src at a
// snapshot dir with no db.sqlite at all, and nix instead reports the exact
// same path as invalid.
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

// TestBwrapIntegration_NixRejectsEmptyStoreSnapshot is
// TestBwrapIntegration_NixTrustsHostStorePaths's negative control (issue
// #2664, ADR 0042): with nixVarSnapshotDir pointing at an empty directory
// (no db.sqlite at all, unlike the real VACUUMed snapshot the positive test
// wires in), the identical `nix path-info` command against the identical
// resolved store path instead fails with "is not valid" -- nix falls back
// to a fresh, empty chroot store under /nix/var and has no record of the
// path at all. Without this control, the positive test's assertions would
// also hold for a `nix path-info` that always exits 0 regardless of whether
// the snapshot mechanism does anything at all.
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

// TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot is the
// other half of issue #2664's acceptance criterion 4 that
// TestBwrapIntegration_NixTrustsHostStorePaths's own `nix path-info` probe
// cannot cover: path-info never touches a substituter at all, so its
// "will be fetched"/"copying path" checks would pass even if the
// snapshot/overlay mechanism substituted paths freely. `nix build` does have
// a real substituter codepath (empirically verified: it fails outright
// against an unregistered path with "there is no substituter that can build
// it", the same error TestBwrapIntegration_NixBuildAttemptsSubstitutionOnEmptyStoreSnapshot
// below asserts on), so a clean, silent exit 0 against the identical
// resolved bash path here is real evidence the snapshot satisfied the build
// without reaching for a substituter.
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

// TestBwrapIntegration_NixBuildAttemptsSubstitutionOnEmptyStoreSnapshot is
// TestBwrapIntegration_NixBuildSubstitutesNothingForValidSnapshot's negative
// control, the same role TestBwrapIntegration_NixRejectsEmptyStoreSnapshot
// plays for the path-info probe: with nixVarSnapshotDir pointing at an empty
// directory, the identical `nix build --no-link` against the identical
// resolved store path can no longer find it in the snapshot's records, so it
// must reach for a substituter to satisfy the build -- and, network fully
// isolated (NetworkModeNone), that reach fails outright rather than silently
// succeeding the way the positive case's build does. Without this control,
// the positive test's silence would also hold for a `nix build` that always
// exits 0 regardless of whether the snapshot mechanism does anything at all.
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

// TestBwrapIntegration_StoreWritableWhenOverlayEnabled is
// TestBwrapIntegration_NixStoreReadOnly's mirror image and issue #2665's
// central positive proof: with nixStoreWritable set (on top of nixConfigFile,
// satisfying buildArgs' AND-gate), the very same write into /nix/store that
// fails against the plain --ro-bind must now succeed, because /nix/store is
// instead --overlay-src/--tmp-overlay'd -- a real tmpfs upper the kernel
// lets a process write into, not just a different flag pair present on argv
// (ADR 0042).
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
// #2665's second acceptance criterion: "the host's store is unchanged
// afterwards." The write-into-/nix/store probe above only shows the guest
// process's own write syscall succeeded; it says nothing about where that
// write actually landed. This test performs the identical write, then -- from
// the test process itself, running on the host, not inside any sandbox --
// stats the host's real /nix/store directory for the exact marker filename
// and asserts it is absent. --tmp-overlay gives bwrap a tmpfs upper that only
// exists for the lifetime of this one bwrap invocation; a write is expected
// to disappear along with it rather than ever reaching the read-only
// --overlay-src lower (the host's real store).
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

// buildDenySyscallFilterBPF hand-assembles a classic-BPF seccomp program
// (the same raw format bubblewrap's own --seccomp FD reads: a flat array of
// 8-byte struct sock_filter{code uint16, jt uint8, jf uint8, k uint32}
// entries, no sock_fprog envelope or length header) that denies sysNr and
// allows everything else. This duplicates production's compiled-filter
// shape as a Go literal rather than shelling out to `nix build` on
// lib/seccomp.nix at test time, mirroring
// newIntegrationBwrapAdapterWithNix's own doc comment on why this file
// prefers that convention. Skips (not fails) on any GOARCH other than
// amd64/arm64: this test only ever runs on its own native arch, so the
// AUDIT_ARCH table only needs those two entries.
//
// Instruction layout (indices 0-5). jt/jf each count how many FOLLOWING
// instructions to skip, zero-indexed from the instruction right after the
// jump itself:
//
//	0: load seccomp_data.arch (offset 4 of the struct)
//	1: arch == this GOARCH's AUDIT_ARCH?
//	     jt=0 -> fall through to 2 (the syscall-nr check)
//	     jf=3 -> skip 2,3,4 and land on 5 (ALLOW) -- an arch mismatch
//	             never happens in this test (no foreign-arch re-exec), so
//	             treating it as "allow everything" instead of a hard kill
//	             keeps the fixture simple without weakening what's tested.
//	2: load seccomp_data.nr (offset 0 of the struct)
//	3: nr == sysNr?
//	     jt=0 -> fall through to 4 (DENY)
//	     jf=1 -> skip 4, land on 5 (ALLOW)
//	4: RET deny  (SECCOMP_RET_ERRNO | EPERM)
//	5: RET allow (SECCOMP_RET_ALLOW)
func buildDenySyscallFilterBPF(t *testing.T, sysNr uint64) []byte {
	t.Helper()

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
	// bash's "kill -0 $$" is a builtin that calls kill(2) directly against
	// bash's own pid -- no extra binary needed beyond bash itself, and
	// nothing else bash does at startup touches kill(2).
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

// TestBwrapIntegration_SyscallFilterAllowsNormalWork is
// TestBwrapIntegration_SyscallFilterDeniesKill's positive control: the same
// filter, attached the same way, must not collaterally break an ordinary
// command that never touches the denied syscall -- proving the filter is
// scoped to kill(2), not a blanket deny that would sink every Dispatch.
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
// #2665's third acceptance criterion: "paths built in one Box are absent from
// a freshly started Box." It runs the write probe as a first, independent
// bwrap invocation (asserting it succeeds, same as
// TestBwrapIntegration_StoreWritableWhenOverlayEnabled above), then launches
// a second, entirely separate bwrap invocation using the identical
// writable-store adapter/args and checks, from inside that second sandbox,
// whether the first invocation's marker file is visible under /nix/store.
// --tmp-overlay's tmpfs upper is scoped to a single bwrap process's mount
// namespace; nothing about --overlay-src/--tmp-overlay persists an upper
// across separate invocations even when both point at the identical lower
// and identical host paths, so the second sandbox must report the file
// absent -- direct evidence each Box gets its own fresh, throwaway overlay
// rather than one that quietly accumulates state across runs.
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
