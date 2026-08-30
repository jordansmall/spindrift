//go:build integration

package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

// newIntegrationBwrapAdapter builds a bwrapAdapter wired with a real
// agentFiles dir and real passwd/group files (production binds these from
// nix store paths, not a temp dir -- see bwrapAdapter.passwdFile/groupFile
// and lib/image.nix's passwdFile/groupFile derivations, issue #2663), so
// buildArgs produces the exact mount/hardening flags production code would
// send to a real bwrap process.
func newIntegrationBwrapAdapter(t *testing.T, unshareNet bool) *bwrapAdapter {
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
	return &bwrapAdapter{
		agentFiles:    agentFiles,
		agentEnv:      "/fake/agent-env",
		passwdFile:    filepath.Join(etcDir, "passwd"),
		groupFile:     filepath.Join(etcDir, "group"),
		bakedPrefetch: "true",
		unshareNet:    unshareNet,
	}
}

// bwrapProbeArgs takes the real buildArgs() output for box, drops the
// --proc/--dev mounts a nested sandbox can't nest (see stripMountPair), and
// swaps the fixed "-- /agent/entrypoint.sh" tail for script, run via
// bash -c. Every other mount/hardening flag reaches bwrap exactly as
// production would send it.
func bwrapProbeArgs(a *bwrapAdapter, box Box, bashBin, script string) []string {
	args := a.buildArgs(box)
	args = stripMountPair(args, "--proc", "/proc")
	args = stripMountPair(args, "--dev", "/dev")
	args = args[:len(args)-1] // drop "/agent/entrypoint.sh", keep the "--" separator
	return append(args, bashBin, "-c", script)
}

// TestBwrapIntegration_NixStoreReadOnly launches a real bwrap sandbox using
// bwrapAdapter's own buildArgs() and asserts, from a process inside it, that
// /nix/store is not writable — the kernel enforcing the --ro-bind, not just
// the flag being present on argv (issue #576).
func TestBwrapIntegration_NixStoreReadOnly(t *testing.T) {
	bashBin := requireRealBwrap(t)
	// unshareNet=true: skips the --ro-bind /etc/resolv.conf mount (buildArgs
	// only adds it when net is shared), which a nested sandbox can't remount
	// anyway — irrelevant to the /nix/store assertion under test here.
	a := newIntegrationBwrapAdapter(t, true)
	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, box, bashBin, "echo x > /nix/store/spindrift-integration-write-probe")

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
	a := newIntegrationBwrapAdapter(t, true)
	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, box, bashBin, "echo $EUID")

	out, err := exec.Command("bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap probe failed: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1000" {
		t.Errorf("uid inside sandbox = %q, want \"1000\"", got)
	}
}

// TestBwrapIntegration_UnshareNetBlocksNetwork launches a real bwrap sandbox
// with BwrapUnshareNet=true and asserts, from inside it, that outbound
// network access fails — the kernel enforcing --unshare-net, not just the
// flag being present on argv (issue #576).
func TestBwrapIntegration_UnshareNetBlocksNetwork(t *testing.T) {
	bashBin := requireRealBwrap(t)
	a := newIntegrationBwrapAdapter(t, true)
	box := Box{Env: map[string]string{}}
	// bash's /dev/tcp pseudo-device is interpreted by bash itself, so it
	// needs no real /dev mount inside the sandbox.
	args := bwrapProbeArgs(a, box, bashBin, "exec 3<>/dev/tcp/1.1.1.1/80")

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
	a := newIntegrationBwrapAdapter(t, true)

	const marker = "spindrift-integration-home-agent-marker"
	homeAgentDir := filepath.Join(a.agentFiles, "home", "agent")
	if err := os.MkdirAll(homeAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeAgentDir, "marker.txt"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	box := Box{Env: map[string]string{}}
	args := bwrapProbeArgs(a, box, bashBin, catBin+" "+homeAgentStagingDir+"/marker.txt")

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
	a := newIntegrationBwrapAdapter(t, true)
	box := Box{Env: map[string]string{"GH_TOKEN": marker}}
	args := bwrapProbeArgs(a, box, bashBin, sleepBin+" 2")

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
