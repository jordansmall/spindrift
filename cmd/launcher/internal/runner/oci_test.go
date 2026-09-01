package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCall scripts one invocation of a fake CLI binary: the exit code it
// returns and the stdout it prints.
type fakeCall struct {
	exit   int
	stdout string
}

// newFakeCLI writes a stub runtime binary that records each invocation's
// argv to call-NN.txt (zero-indexed, mirroring prependFakeGH's convention in
// internal/forge/exec_test.go) inside a temp dir, and exits/prints per the
// scripted calls in order. Once the number of invocations exceeds len(calls),
// the last scripted call repeats. Returns the script path (for assignment to
// ociAdapter.cli) and the dir (for reading back recorded calls).
func newFakeCLI(t *testing.T, calls ...fakeCall) (script, dir string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("newFakeCLI: at least one scripted call required")
	}
	dir = t.TempDir()
	script = filepath.Join(dir, "fake-cli")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "n=$(ls %q/call-*.txt 2>/dev/null | wc -l)\n", dir)
	fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %q/call-$(printf '%%02d' $n).txt\n", dir)
	b.WriteString("case $n in\n")
	for i, c := range calls {
		pattern := fmt.Sprintf("%d", i)
		if i == len(calls)-1 {
			pattern += "|*"
		}
		fmt.Fprintf(&b, "%s) printf '%%s' %q; exit %d ;;\n", pattern, c.stdout, c.exit)
	}
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// readCall returns the argv (split on newline) recorded for the n-th
// (zero-indexed) invocation of a fake CLI built by newFakeCLI.
func readCall(t *testing.T, dir string, n int) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call-%02d.txt", n)))
	if err != nil {
		t.Fatalf("call-%02d.txt not written: %v", n, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// callCount returns the number of invocations recorded for a fake CLI built
// by newFakeCLI.
func callCount(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// TestEnsureReady_ImagePresentPrintsMessage verifies that EnsureReady emits
// an "already loaded" line when the image is already loaded, worded so it
// never collides with the freshness probe's distinct "rebuild needed"
// vocabulary (#1885).
func TestEnsureReady_ImagePresentPrintsMessage(t *testing.T) {
	// Fake CLI: exits 0 for any invocation (simulates "image inspect" success).
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-podman")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	// Capture os.Stdout — EnsureReady uses fmt.Printf which writes there.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	ensureErr := a.EnsureReady()

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if ensureErr != nil {
		t.Fatalf("EnsureReady: %v", ensureErr)
	}
	out := buf.String()
	if !strings.Contains(out, "already loaded") {
		t.Errorf("expected 'already loaded' in EnsureReady output when image loaded; got: %q", out)
	}
	if strings.Contains(out, "rebuild") {
		t.Errorf("EnsureReady output must not use 'rebuild' vocabulary (collides with freshness probe); got: %q", out)
	}
}

func TestReapOrphanedRebaseDirs_RemovesStaleAndKeepsOthers(t *testing.T) {
	root := t.TempDir()
	stale := []string{
		filepath.Join(root, "spindrift-rebase-abc123"),
		filepath.Join(root, "spindrift-rebase-def456"),
	}
	for _, d := range stale {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(root, "not-a-rebase-dir")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	reapOrphanedRebaseDirs(root)

	for _, d := range stale {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("expected stale dir %s to be removed", filepath.Base(d))
		}
	}
	if _, err := os.Stat(other); os.IsNotExist(err) {
		t.Errorf("expected non-rebase dir %s to be kept", filepath.Base(other))
	}
}

func TestReapOrphanedRebaseDirs_NoopOnMissingRoot(t *testing.T) {
	// Should not panic when root does not exist.
	reapOrphanedRebaseDirs("/tmp/spindrift-test-nonexistent-root-xyz")
}

func TestIsDigestPinned(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		// This checks the repo@sha256:... shape, not literal equality with the
		// real pin, but the digest below matches lib/build-constants.nix's
		// nixBuilderImage rather than being an independently made-up value.
		{"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8", true},
		{"docker.io/nixos/nix:latest", false},
		{"docker.io/nixos/nix:2.24.9", false},
		{"nixos/nix@sha256:abc123", true},
		{"", false},
	}
	for _, tc := range tests {
		if got := isDigestPinned(tc.image); got != tc.want {
			t.Errorf("isDigestPinned(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}

func TestIsNoBuilderError(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"error: a Linux system is required to build a Linux derivation", true},
		{"error: no build machines available", true},
		{"error: attribute 'nonexistent' missing", false},
		{"", false},
		{`error: Cannot build '/nix/store/y56hw02v3fqnirf98aabalgvparlcasr-spindrift-base.json.drv'.
       Reason: platform mismatch
       Required system: 'aarch64-linux'
       Current system: 'aarch64-darwin'`, true},
	}
	for _, tc := range tests {
		if got := isNoBuilderError(tc.stderr); got != tc.want {
			t.Errorf("isNoBuilderError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestIsTransientRegistryError(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"Trying to pull docker.io/library/busybox:stable...\nError: pinging container registry registry-1.docker.io: Get \"https://registry-1.docker.io/v2/\": dial tcp: i/o timeout", true},
		{"Error: initializing source docker://busybox:stable: pinging container registry registry-1.docker.io: read tcp: i/o timeout", true},
		{"Error: initializing source docker://busybox:stable: dial tcp: lookup registry-1.docker.io: no such host", true},
		{"Error: pulling image: connection refused", true},
		{"Error: pulling image: TLS handshake timeout", true},
		{"Error: creating build container: no such image", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isTransientRegistryError(tc.stderr); got != tc.want {
			t.Errorf("isTransientRegistryError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestIsRuntimeUnusableError(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"Error: OCI runtime error: crun: unknown version specified", true},
		{"Error: OCI runtime error: runc: exec failed", true},
		{"Trying to pull docker.io/library/busybox:stable...\nError: OCI runtime error: crun: unknown version specified", true},
		{"CapEff:\t0000000000000000", false},
		{"Error: pulling image: connection refused", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isRuntimeUnusableError(tc.stderr); got != tc.want {
			t.Errorf("isRuntimeUnusableError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestContainerBuildCmd(t *testing.T) {
	attr := ".#packages.aarch64-linux.agent-image"
	got := containerBuildCmd(attr)

	if !strings.Contains(got, "nix --extra-experimental-features 'nix-command flakes' build '"+attr+"'") {
		t.Errorf("missing nix build invocation for attr %q in: %s", attr, got)
	}
	if !strings.Contains(got, ">/build-output/image-path && cp") {
		t.Errorf("missing tail redirect/copy in: %s", got)
	}
}

// TestContainerBuildCmd_SafeDirectoryPreludePrecedesNixBuild verifies
// containerBuildCmd prepends a safe.directory gitconfig prelude — written
// directly via printf under a writable HOME rooted at /build-output, with no
// dependency on a `git` CLI being present in the builder image — ahead of
// the existing `nix build` invocation (issue #2196).
func TestContainerBuildCmd_SafeDirectoryPreludePrecedesNixBuild(t *testing.T) {
	attr := ".#packages.aarch64-linux.agent-image"
	got := containerBuildCmd(attr)

	nixIdx := strings.Index(got, "nix --extra-experimental-features")
	if nixIdx < 0 {
		t.Fatalf("missing nix build invocation in: %s", got)
	}

	homeIdx := strings.Index(got, "export HOME=/build-output/")
	if homeIdx < 0 {
		t.Fatalf("missing HOME export under /build-output in: %s", got)
	}
	if homeIdx >= nixIdx {
		t.Errorf("HOME export (idx %d) must precede nix build (idx %d) in: %s", homeIdx, nixIdx, got)
	}

	printfIdx := strings.Index(got, "printf '[safe]")
	if printfIdx < 0 {
		t.Fatalf("missing printf-written safe.directory gitconfig in: %s", got)
	}
	if printfIdx >= nixIdx {
		t.Errorf("gitconfig printf (idx %d) must precede nix build (idx %d) in: %s", printfIdx, nixIdx, got)
	}
	if !strings.Contains(got, "directory = *") || !strings.Contains(got, "directory = /workspace") {
		t.Errorf("expected safe.directory entries for '*' and '/workspace' in: %s", got)
	}
	if !strings.Contains(got, ".gitconfig") {
		t.Errorf("expected gitconfig written to $HOME/.gitconfig in: %s", got)
	}
	if strings.Contains(got, "git config") {
		t.Errorf("gitconfig must be written directly via printf, not `git config` (no git CLI dependency); got: %s", got)
	}
}

func TestBuildRunArgsIncludesHardeningFlags(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		pidsLimit:   "512",
		memoryLimit: "4g",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{"ISSUE_NUMBER": "1"}}
	args := a.buildRunArgs(box)

	for _, flag := range []string{
		"--cap-drop=all",
		"--security-opt=no-new-privileges",
		"--pids-limit=512",
		"--memory=4g",
	} {
		if !containsArg(args, flag) {
			t.Errorf("missing flag %q in args: %v", flag, args)
		}
	}
}

func TestBuildRunArgsEmptyLimitsOmitted(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		pidsLimit:   "",
		memoryLimit: "",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	// cap-drop and no-new-privileges are unconditional
	if !containsArg(args, "--cap-drop=all") {
		t.Errorf("--cap-drop=all always required; args: %v", args)
	}
	if !containsArg(args, "--security-opt=no-new-privileges") {
		t.Errorf("--security-opt=no-new-privileges always required; args: %v", args)
	}

	// resource limits must be absent when unset
	for _, flag := range []string{"--pids-limit", "--memory"} {
		for _, arg := range args {
			if arg == flag {
				t.Errorf("unexpected flag %q when limit is empty; args: %v", flag, args)
			}
		}
	}
}

// TestNetworkArg covers networkArg's resolution of the effective --network
// value across cli x networkMode, plus the raw podmanNetwork escape hatch
// (issue #2562). Raw podmanNetwork wins whenever set, even alongside a set
// networkMode: for a non-open networkMode, nix eval-rejects that combination
// for a real Consumer flake (lib/mkHarness.nix networkModeCoherenceOk), and
// networkArg still needs a deterministic answer since it has no way to
// observe "this can't happen"; for networkMode "open" specifically, raw-wins
// is a real, reachable case (checkNetworkModeRuntimeGate in main.go leaves
// it out of scope on purpose), not just defense-in-depth.
func TestNetworkArg(t *testing.T) {
	cases := []struct {
		name          string
		cli           string
		networkMode   string
		podmanNetwork string
		want          string
	}{
		{name: "podman no-host-loopback", cli: "podman", networkMode: "no-host-loopback", want: "pasta"},
		{name: "docker no-host-loopback", cli: "docker", networkMode: "no-host-loopback", want: "bridge"},
		{name: "nerdctl no-host-loopback", cli: "nerdctl", networkMode: "no-host-loopback", want: "bridge"},
		{name: "none any cli", cli: "podman", networkMode: "none", want: "none"},
		{name: "none docker", cli: "docker", networkMode: "none", want: "none"},
		{name: "open no flag", cli: "podman", networkMode: "open", want: ""},
		{name: "unset no flag", cli: "podman", networkMode: "", want: ""},
		{name: "raw wins over mode", cli: "podman", networkMode: "no-host-loopback", podmanNetwork: "slirp4netns:allow_host_loopback=true", want: "slirp4netns:allow_host_loopback=true"},
		{name: "raw wins over open mode", cli: "podman", networkMode: "open", podmanNetwork: "slirp4netns:allow_host_loopback=true", want: "slirp4netns:allow_host_loopback=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &ociAdapter{cli: tc.cli, networkMode: tc.networkMode, podmanNetwork: tc.podmanNetwork}
			if got := a.networkArg(); got != tc.want {
				t.Errorf("networkArg() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildRunArgs_NetworkModeRendersNetworkFlag verifies buildRunArgs itself
// wires networkMode through to --network, not just the networkArg helper.
func TestBuildRunArgs_NetworkModeRendersNetworkFlag(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test", networkMode: "no-host-loopback"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)
	if !containsArg(args, "--network") {
		t.Fatalf("--network missing from args: %v", args)
	}
	for i, arg := range args {
		if arg == "--network" {
			if i+1 >= len(args) || args[i+1] != "pasta" {
				t.Errorf("--network value = %v, want pasta; args: %v", args, args)
			}
		}
	}
}

// TestBuildRunArgs_NetworkModeOpenOmitsFlag verifies the default/unset mode
// renders no --network flag at all when no raw knob is set either.
func TestBuildRunArgs_NetworkModeOpenOmitsFlag(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test", networkMode: "open"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)
	if containsArg(args, "--network") {
		t.Errorf("--network must be absent for networkMode=open; args: %v", args)
	}
}

func TestBuildRunArgsImageIsLast(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:abc123",
		pidsLimit:   "256",
		memoryLimit: "2g",
	}
	box := Box{Name: "agent-issue-99", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	// image must appear before the entrypoint and after all flags
	imageIdx := -1
	for i, arg := range args {
		if arg == "spindrift:abc123" {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 {
		t.Fatalf("image not found in args: %v", args)
	}
	// security flags must precede the image
	for _, flag := range []string{"--cap-drop=all", "--security-opt=no-new-privileges"} {
		flagIdx := -1
		for i, arg := range args {
			if arg == flag {
				flagIdx = i
				break
			}
		}
		if flagIdx >= imageIdx {
			t.Errorf("flag %q (idx %d) must appear before image (idx %d)", flag, flagIdx, imageIdx)
		}
	}
}

func TestBuildRunArgs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{SkillsDir: dir},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/operator-skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_SkillsMountTarget_FromDriverDeclaration is gone (issue
// #2489): the operator-override skills mount now always lands at the fixed
// /operator-skills staging path (see operatorSkillsDir in mount.go),
// independent of the Driver's declared skills dir, so there is no longer a
// driver-declaration-driven mount target for this test to exercise.

// TestBuildRunArgs_IssuesDirMounted verifies that ISSUE_TRACKER=local plus a
// resolved localIssuesDir renders a read-only -v <dir>:/issues:ro entry
// (issue #1691, ADR 0032).
func TestBuildRunArgs_IssuesDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{HostMediatedIssueTracker: true, LocalIssuesDir: dir},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/issues:ro"
	if !containsArg(args, want) {
		t.Errorf("issues mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_IssuesDirNonLocalTracker_NoMount verifies that a
// non-local tracker never renders an /issues mount.
func TestBuildRunArgs_IssuesDirNonLocalTracker_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{HostMediatedIssueTracker: false, LocalIssuesDir: dir},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ":/issues") {
			t.Errorf("unexpected /issues mount for a non-local tracker: %v", args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/.claude/projects"
	if !containsArg(args, want) {
		t.Errorf("driver cache mount %q not found in args: %v", want, args)
	}
	if containsArg(args, want+":ro") {
		t.Errorf("driver cache mount must be writable, not :ro; args: %v", args)
	}
}

// TestBuildRunArgs_RegistryProxySocketMounted verifies that a Box-derived
// RegistryProxy.SocketPath produces a -v <source>:/registry-proxy.sock entry
// (ADR 0044, issue #2849).
func TestBuildRunArgs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, RegistryProxy: RegistryProxyLocation{SocketPath: sock}}
	args := a.buildRunArgs(box)

	want := sock + ":/registry-proxy.sock"
	if !containsArg(args, want) {
		t.Errorf("registry-proxy socket mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_SecretEnvRendersBareFlag verifies that a box.Env key
// listed in bwrapSecrets (shared with the bwrap adapter) renders as a bare
// `-e KEY` on the docker/podman run argv -- never `-e KEY=VALUE` -- so the
// secret value itself never lands in argv, which ps/proc exposes to any
// local user for the container's whole lifetime (issue #3111 finding A).
func TestBuildRunArgs_SecretEnvRendersBareFlag(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{
		"REGISTRY_PROXY_TCP_SECRET": "s3cr3t-token",
		"GH_TOKEN":                  "gh-s3cr3t",
		"ISSUE_NUMBER":              "1",
	}}
	args := a.buildRunArgs(box)

	for _, key := range []string{"REGISTRY_PROXY_TCP_SECRET", "GH_TOKEN"} {
		if !containsArg(args, key) {
			t.Errorf("expected bare -e %s in args: %v", key, args)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "REGISTRY_PROXY_TCP_SECRET=") {
			t.Errorf("REGISTRY_PROXY_TCP_SECRET value must never appear on argv; found %q in args: %v", arg, args)
		}
		if strings.Contains(arg, "GH_TOKEN=") {
			t.Errorf("GH_TOKEN value must never appear on argv; found %q in args: %v", arg, args)
		}
	}
	// non-secret keys are unaffected: still rendered as KEY=VALUE.
	if !containsArg(args, "ISSUE_NUMBER=1") {
		t.Errorf("expected non-secret ISSUE_NUMBER=1 to render unchanged in args: %v", args)
	}
}

// TestOciRunEnv verifies ociRunEnv appends only the bwrapSecrets-listed keys
// present in boxEnv, as KEY=VALUE, on top of the full os.Environ() -- so the
// docker/podman CLI process itself carries the secret in its own process
// environment (for a bare `-e KEY` argv entry to forward), without the value
// ever appearing in the exec.Command args slice.
func TestOciRunEnv(t *testing.T) {
	boxEnv := map[string]string{
		"REGISTRY_PROXY_TCP_SECRET": "s3cr3t-token",
		"GH_TOKEN":                  "gh-s3cr3t",
		"ISSUE_NUMBER":              "1", // not in bwrapSecrets -- must not be appended
	}
	got := ociRunEnv(boxEnv)

	baseline := os.Environ()
	if len(got) != len(baseline)+2 {
		t.Fatalf("ociRunEnv: want len %d (os.Environ()+2 secrets), got %d: %v", len(baseline)+2, len(got), got)
	}
	if !containsArg(got, "REGISTRY_PROXY_TCP_SECRET=s3cr3t-token") {
		t.Errorf("ociRunEnv: missing REGISTRY_PROXY_TCP_SECRET=s3cr3t-token in %v", got)
	}
	if !containsArg(got, "GH_TOKEN=gh-s3cr3t") {
		t.Errorf("ociRunEnv: missing GH_TOKEN=gh-s3cr3t in %v", got)
	}
	if containsArg(got, "ISSUE_NUMBER=1") {
		t.Errorf("ociRunEnv: non-secret key must not be appended; got %v", got)
	}
	pathSeen := false
	for _, e := range baseline {
		if strings.HasPrefix(e, "PATH=") {
			pathSeen = strings.Contains(strings.Join(got, "\n"), e)
			break
		}
	}
	if !pathSeen {
		t.Error("ociRunEnv: expected the surrounding os.Environ() (e.g. PATH) to survive in the returned slice")
	}
}

// TestBuildRunArgs_TCPHostAddHostMounted verifies that a Box-derived
// RegistryProxy.TCPHost renders an --add-host <host>:host-gateway flag
// (issue #3111) — the guest needs an explicit host-gateway mapping for the
// TCP-transport fallback since plain Linux docker offers none by default.
func TestBuildRunArgs_TCPHostAddHostMounted(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, RegistryProxy: RegistryProxyLocation{TCPHost: "host.containers.internal"}}
	args := a.buildRunArgs(box)

	if !containsArg(args, "--add-host") {
		t.Fatalf("--add-host missing from args: %v", args)
	}
	found := false
	for i, arg := range args {
		if arg == "--add-host" && i+1 < len(args) && args[i+1] == "host.containers.internal:host-gateway" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --add-host host.containers.internal:host-gateway in args: %v", args)
	}
}

// TestBuildRunArgs_TCPHostUnset_NoAddHost verifies that an empty TCPHost
// renders no --add-host flag at all.
func TestBuildRunArgs_TCPHostUnset_NoAddHost(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	if containsArg(args, "--add-host") {
		t.Errorf("--add-host must be absent when TCPHost is unset; args: %v", args)
	}
}

// TestHostGatewayHostname covers hostGatewayHostname's cli -> hostname
// mapping: podman uses its own host.containers.internal convention; docker
// and nerdctl share host.docker.internal (Rancher Desktop's containerd/nerdctl
// mode also honors it).
func TestHostGatewayHostname(t *testing.T) {
	tests := []struct {
		cli  string
		want string
	}{
		{"podman", "host.containers.internal"},
		{"docker", "host.docker.internal"},
		{"nerdctl", "host.docker.internal"},
	}
	for _, tc := range tests {
		if got := hostGatewayHostname(tc.cli); got != tc.want {
			t.Errorf("hostGatewayHostname(%q) = %q, want %q", tc.cli, got, tc.want)
		}
	}
}

// TestRegistrySocketProbeArgs verifies the pure arg-builder shape: --rm right
// after "run", the socket mount rendered via the shared candidateSocketMount
// path, and the driver-exec probe trailing command in place of the image's
// default entrypoint — without exec'ing anything.
func TestRegistrySocketProbeArgs(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	args := a.registrySocketProbeArgs(sock, "probe-container")

	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("want args[0:2] = [run --rm], got %v", args)
	}
	want := sock + ":" + registryProxySocketTarget
	if !containsArg(args, want) {
		t.Errorf("missing socket mount %q in args: %v", want, args)
	}
	tail := []string{a.image, "driver-exec", "probe-registry-socket", "-path", registryProxySocketTarget}
	if strings.Join(args[len(args)-len(tail):], " ") != strings.Join(tail, " ") {
		t.Errorf("want trailing command %v, got tail of %v", tail, args)
	}
	if containsArg(args, "/agent/entrypoint.sh") {
		t.Errorf("probe args must not include the real entrypoint; args: %v", args)
	}
	if !containsArg(args, "--name") || !containsArg(args, "probe-container") {
		t.Errorf("missing --name probe-container in args: %v", args)
	}
	if !containsArg(args, "--cap-drop=all") || !containsArg(args, "--security-opt=no-new-privileges") {
		t.Errorf("probe must reuse the same hardening flags as a real Box; args: %v", args)
	}
}

// TestRegistryProxyTransport_ScriptedZeroExit_ReportsSocketCapable verifies
// RegistryProxyTransport reports capable when the probe container exits 0 —
// scripted via a fake CLI so no real container runtime is ever started
// (issue #3111's own acceptance criterion).
func TestRegistryProxyTransport_ScriptedZeroExit_ReportsSocketCapable(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	capable, tcpHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: %v", err)
	}
	if !capable {
		t.Error("RegistryProxyTransport: want capable=true on scripted zero exit")
	}
	if tcpHost != "" {
		t.Errorf("RegistryProxyTransport: want empty tcpHost on capable, got %q", tcpHost)
	}

	call := readCall(t, dir, 0)
	if call[0] != "run" || call[1] != "--rm" {
		t.Fatalf("want call[0:2] = [run --rm], got %v", call)
	}
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, ":"+registryProxySocketTarget) {
		t.Errorf("expected socket mount ending in :%s in call: %v", registryProxySocketTarget, call)
	}
	if !strings.Contains(joined, "driver-exec probe-registry-socket -path "+registryProxySocketTarget) {
		t.Errorf("expected probe trailing command in call: %v", call)
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1: a socket-capable verdict must never launch the tcp-reachability sub-probe", got)
	}
}

// TestRegistryProxyTransport_ScriptedNonZeroExit_ReportsIncapableWithTCPHost
// verifies RegistryProxyTransport treats a non-zero exit from the socket
// probe container as a clean "incapable" answer, not a Go error — matching
// the AC that a stat-only/unconnectable socket case degrades cleanly — as
// long as the second, live TCP-reachability sub-probe (issue #3111 review
// finding B) also confirms the fallback route actually works. Scripts TWO
// calls: call 0 is the socket probe (exit 1, incapable), call 1 is the
// TCP-reachability sub-probe (exit 0, reachable).
func TestRegistryProxyTransport_ScriptedNonZeroExit_ReportsIncapableWithTCPHost(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	// a.cli is a fake-script path, not literally "podman", so
	// hostGatewayHostname falls into its docker/nerdctl branch — pin that
	// literal here instead of calling hostGatewayHostname(a.cli), which would
	// make this assertion tautological against the very function under test.
	const wantTCPHost = "host.docker.internal"

	capable, tcpHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error on scripted non-zero exit, got %v", err)
	}
	if capable {
		t.Error("RegistryProxyTransport: want capable=false on scripted non-zero exit")
	}
	if tcpHost != wantTCPHost {
		t.Errorf("RegistryProxyTransport: tcpHost = %q, want %q", tcpHost, wantTCPHost)
	}

	if got := callCount(t, dir); got != 2 {
		t.Fatalf("callCount = %d, want 2 (socket probe + tcp-reachability sub-probe)", got)
	}
	call := readCall(t, dir, 1)
	if call[0] != "run" || call[1] != "--rm" {
		t.Fatalf("want call[0:2] = [run --rm] for the tcp-reachability sub-probe, got %v", call)
	}
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, "--add-host "+tcpHost+":host-gateway") {
		t.Errorf("expected --add-host %s:host-gateway in tcp-reachability sub-probe call: %v", tcpHost, call)
	}
	if !strings.Contains(joined, "driver-exec probe-registry-tcp -host "+tcpHost+" -port ") {
		t.Errorf("expected probe-registry-tcp trailing command in tcp-reachability sub-probe call: %v", call)
	}
}

// TestRegistryProxyTransport_TCPReachabilitySubProbeIncapable_ReturnsError
// verifies that when the socket probe reports incapable AND the second, live
// TCP-reachability sub-probe (issue #3111 review finding B) also reports the
// --add-host host-gateway route is not reachable, RegistryProxyTransport
// returns a hard error naming the CLI and the host -- rather than the old,
// buggy behavior of trusting the TCP fallback unconditionally and silently
// returning (false, host, nil) with nothing actually listening on the route
// the Box would be told to dial.
func TestRegistryProxyTransport_TCPReachabilitySubProbeIncapable_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	capable, tcpHost, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatalf("RegistryProxyTransport: want error when the tcp-reachability sub-probe also reports incapable, got capable=%v tcpHost=%q", capable, tcpHost)
	}
	if capable {
		t.Error("RegistryProxyTransport: want capable=false when the tcp-reachability sub-probe reports incapable")
	}
	if !strings.Contains(err.Error(), a.cli) {
		t.Errorf("RegistryProxyTransport: error %q should name the CLI %q", err, a.cli)
	}
	if !strings.Contains(err.Error(), hostGatewayHostname(a.cli)) {
		t.Errorf("RegistryProxyTransport: error %q should name the host %q", err, hostGatewayHostname(a.cli))
	}

	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2 (socket probe + tcp-reachability sub-probe)", got)
	}
}

// TestRegistryProxyTransport_NoHostLoopback_SocketIncapable_ReturnsError
// verifies that when the probe reports socket-incapable AND the adapter's
// networkMode denies host-loopback reachability (no-host-loopback or none),
// RegistryProxyTransport refuses the TCP fallback outright with a
// descriptive error, rather than silently returning a (false, tcpHost, nil)
// triple that leaves the Box unable to reach the registry proxy with zero
// diagnostic (issue #3111 finding B). Scripts only ONE fakeCall (exit 1) and
// asserts callCount stays at 1: the deniesHostLoopback branch hard-errors
// before the second, live TCP-reachability sub-probe is ever attempted, so a
// second container must never be launched here.
func TestRegistryProxyTransport_NoHostLoopback_SocketIncapable_ReturnsError(t *testing.T) {
	for _, mode := range []string{NetworkModeNoHostLoopback, NetworkModeNone} {
		t.Run(mode, func(t *testing.T) {
			script, dir := newFakeCLI(t, fakeCall{exit: 1})
			a := &ociAdapter{cli: script, image: "spindrift:test", networkMode: mode}

			capable, tcpHost, err := a.RegistryProxyTransport()
			if err == nil {
				t.Fatalf("RegistryProxyTransport: want error for networkMode=%q + socket-incapable, got capable=%v tcpHost=%q", mode, capable, tcpHost)
			}
			if capable {
				t.Error("RegistryProxyTransport: want capable=false on scripted non-zero exit")
			}
			if !strings.Contains(err.Error(), a.cli) {
				t.Errorf("RegistryProxyTransport: error %q should name the CLI %q", err, a.cli)
			}
			if !strings.Contains(err.Error(), mode) {
				t.Errorf("RegistryProxyTransport: error %q should name the configured NETWORK_MODE %q", err, mode)
			}
			if got := callCount(t, dir); got != 1 {
				t.Errorf("callCount = %d, want 1: the tcp-reachability sub-probe must never run when deniesHostLoopback already hard-errored", got)
			}
		})
	}
}

// TestRegistryProxyTransport_OpenOrUnsetNetworkMode_UnchangedBehavior verifies
// the existing socket-incapable-but-TCP-capable behavior is unchanged when
// networkMode is "open" or unset -- this must not regress
// TestRegistryProxyTransport_ScriptedNonZeroExit_ReportsIncapableWithTCPHost.
// Scripts TWO calls: call 0 is the socket probe (exit 1, incapable), call 1
// is the TCP-reachability sub-probe (exit 0, reachable) -- without a second
// scripted call this would repeat exit 1 and now correctly error, since the
// sub-probe treats its own exit 1 as a hard "not reachable" verdict.
func TestRegistryProxyTransport_OpenOrUnsetNetworkMode_UnchangedBehavior(t *testing.T) {
	for _, mode := range []string{"open", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			script, _ := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 0})
			a := &ociAdapter{cli: script, image: "spindrift:test", networkMode: mode}

			// a.cli is a fake-script path, not literally "podman", so
			// hostGatewayHostname falls into its docker/nerdctl branch --
			// pin that literal here instead of calling
			// hostGatewayHostname(a.cli), which would make this assertion
			// tautological against the very function under test.
			const wantTCPHost = "host.docker.internal"

			capable, tcpHost, err := a.RegistryProxyTransport()
			if err != nil {
				t.Fatalf("RegistryProxyTransport: want nil error for networkMode=%q, got %v", mode, err)
			}
			if capable {
				t.Error("RegistryProxyTransport: want capable=false on scripted non-zero exit")
			}
			if tcpHost != wantTCPHost {
				t.Errorf("RegistryProxyTransport: tcpHost = %q, want %q", tcpHost, wantTCPHost)
			}
		})
	}
}

// TestDeniesHostLoopback verifies the helper's exact membership: only
// no-host-loopback and none deny host-loopback reachability; open/unset/any
// other value does not.
func TestDeniesHostLoopback(t *testing.T) {
	tests := []struct {
		networkMode string
		want        bool
	}{
		{NetworkModeNoHostLoopback, true},
		{NetworkModeNone, true},
		{"open", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := deniesHostLoopback(tc.networkMode); got != tc.want {
			t.Errorf("deniesHostLoopback(%q) = %v, want %v", tc.networkMode, got, tc.want)
		}
	}
}

// TestRegistryProxyTransport_ExecFailure_ReturnsError verifies a genuine
// inability to even start the runtime (CLI missing) surfaces as an error,
// rather than being folded into a clean "incapable" answer.
func TestRegistryProxyTransport_ExecFailure_ReturnsError(t *testing.T) {
	a := &ociAdapter{cli: filepath.Join(t.TempDir(), "nonexistent-cli-binary"), image: "spindrift:test"}

	_, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when the runtime CLI itself cannot be started")
	}
}

// TestRegistryProxyTransport_ScriptedExitCode125_ReturnsError verifies a
// non-1 non-zero exit (125, docker/podman's own "daemon error" convention for
// docker run itself failing) surfaces as a Go error rather than being folded
// into the clean "incapable" verdict — only exit 1 is driver-exec
// probe-registry-socket's own documented incapable answer (issue #3111
// finding 2).
func TestRegistryProxyTransport_ScriptedExitCode125_ReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 125, stdout: "boom"})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	capable, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error on scripted exit 125, got nil")
	}
	if capable {
		t.Error("RegistryProxyTransport: want capable=false on scripted exit 125")
	}
	if !strings.Contains(err.Error(), "125") {
		t.Errorf("RegistryProxyTransport: error %q should mention the exit code 125", err)
	}
}

// TestRegistryProxyTransport_ProbeTimesOut_ReturnsError verifies a wedged
// probe container (never exits) surfaces as a real Go error mentioning the
// timeout, rather than hanging the dispatch path indefinitely (issue #3111
// finding 1). registryProxyProbeTimeout is overridden to a short duration for
// the test, following the execCommand-seam override idiom used elsewhere in
// this package.
func TestRegistryProxyTransport_ProbeTimesOut_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-cli")
	// exec (rather than a plain "sleep 5" line) replaces this shell process
	// with sleep itself, so killing the *exec.Cmd's PID on timeout actually
	// kills the sleeper immediately instead of leaving an orphaned child
	// holding the CombinedOutput pipe open until it finishes on its own.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := registryProxyProbeTimeout
	registryProxyProbeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { registryProxyProbeTimeout = orig })

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	capable, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when the probe times out")
	}
	if capable {
		t.Error("RegistryProxyTransport: want capable=false when the probe times out")
	}
	if !strings.Contains(err.Error(), registryProxyProbeTimeout.String()) {
		t.Errorf("RegistryProxyTransport: error %q should mention the timeout duration %s", err, registryProxyProbeTimeout)
	}
}

// TestProbeRegistryTCPReachable_TimesOut_ReturnsError calls
// probeRegistryTCPReachable directly (rather than through
// RegistryProxyTransport's two-probe chain) so a wedged tcp-reachability
// sub-probe container is exercised in isolation, mirroring
// TestRegistryProxyTransport_ProbeTimesOut_ReturnsError's coverage of the
// first-stage probe's identical context.DeadlineExceeded branch.
func TestProbeRegistryTCPReachable_TimesOut_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := registryProxyProbeTimeout
	registryProxyProbeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { registryProxyProbeTimeout = orig })

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error when the sub-probe times out")
	}
	if !strings.Contains(err.Error(), registryProxyProbeTimeout.String()) {
		t.Errorf("probeRegistryTCPReachable: error %q should mention the timeout duration %s", err, registryProxyProbeTimeout)
	}
}

// TestProbeRegistryTCPReachable_NonOneExitCode_ReturnsError verifies a
// non-1 non-zero exit (125, docker/podman's own "daemon error" convention)
// from the tcp-reachability sub-probe container surfaces as a Go error
// rather than being folded into the clean "not reachable" verdict — only
// exit 1 is driver-exec probe-registry-tcp's own documented "not reachable"
// answer, mirroring TestRegistryProxyTransport_ScriptedExitCode125_ReturnsError's
// coverage of the first-stage probe's identical branch.
func TestProbeRegistryTCPReachable_NonOneExitCode_ReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 125, stdout: "boom"})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error on scripted exit 125, got nil")
	}
	if !strings.Contains(err.Error(), "125") {
		t.Errorf("probeRegistryTCPReachable: error %q should mention the exit code 125", err)
	}
}

// TestProbeRegistryTCPReachable_ExecFailure_ReturnsError verifies a genuine
// inability to even start the runtime (CLI missing) surfaces as an error,
// mirroring TestRegistryProxyTransport_ExecFailure_ReturnsError's coverage of
// the first-stage probe's identical non-*exec.ExitError branch.
func TestProbeRegistryTCPReachable_ExecFailure_ReturnsError(t *testing.T) {
	a := &ociAdapter{cli: filepath.Join(t.TempDir(), "nonexistent-cli-binary"), image: "spindrift:test"}

	err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error when the runtime CLI itself cannot be started")
	}
}

// TestRegistryProxyTransport_LongTMPDIR_StillWorks pins issue #3077's
// acceptance criterion for this probe too: a $TMPDIR long enough that
// os.TempDir()-based candidate would overflow AF_UNIX's sun_path limit once
// "spindrift-registry-probe-*/probe.sock" is appended -- the shape nix
// develop's own nix-shell.XXXXXX/ prefix nested under macOS's per-user
// $TMPDIR produces in practice, and the shape a nix build sandbox's own deep
// working directory can produce too -- must not error out; probeSocketDir
// falls back to /tmp exactly as dispatch.registryProxySocketDir already does
// for the real registry-proxy socket.
func TestRegistryProxyTransport_LongTMPDIR_StillWorks(t *testing.T) {
	longBase := filepath.Join(t.TempDir(), strings.Repeat("x", 200))
	if err := os.MkdirAll(longBase, 0o755); err != nil {
		t.Fatalf("MkdirAll long TMPDIR base: %v", err)
	}
	t.Setenv("TMPDIR", longBase)

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	capable, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error under a long TMPDIR, got %v", err)
	}
	if !capable {
		t.Error("RegistryProxyTransport: want capable=true on scripted zero exit")
	}
}

// TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive verifies that the
// writable cache mount, scoped to /home/agent/.claude/projects, does not
// shadow /home/agent/.claude/skills baked into the image — the regression a
// mount at the parent /home/agent/.claude would cause (OCI has no host-side
// path to re-mount baked skills over, unlike bwrap's agentFiles fallback).
func TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if arg == "/home/agent/.claude" || strings.HasSuffix(arg, ":/home/agent/.claude") || strings.HasSuffix(arg, ":/home/agent/.claude:ro") {
			t.Errorf("cache mount must not target the whole /home/agent/.claude (shadows baked skills); args: %v", args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirMounted_HardeningPreserved(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, flag := range []string{"--cap-drop=all", "--security-opt=no-new-privileges"} {
		if !containsArg(args, flag) {
			t.Errorf("writable driver cache mount must not weaken hardening; missing %q in args: %v", flag, args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirUnset_NoMount(t *testing.T) {
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, "/home/agent/.claude/projects") {
			t.Errorf("unexpected driver cache mount in args when DriverCacheDir is empty: %v", args)
		}
	}
}

// TestBuildRunArgs_DriverCacheMountTarget_FromDriverDeclaration verifies the
// box-side session-cache mount target comes from the adapter's
// driverSessionCacheDir field (populated by the Driver declaration, ADR
// 0009) rather than a hardcoded ".claude/projects" literal.
func TestBuildRunArgs_DriverCacheMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{DriverSessionCacheDir: "/home/agent/custom-driver/state"},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/custom-driver/state"
	if !containsArg(args, want) {
		t.Errorf("driver cache mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache mount even when a
// host DriverCacheDir is present -- there is no in-box target to mount it
// over (issue #448).
func TestBuildRunArgs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.HasPrefix(arg, dir+":") {
			t.Errorf("unexpected driver cache mount in args when Driver declares no session-cache dir: %v", args)
		}
	}
}

func TestBuildRunArgs_SkillsDirUnset_NoMount(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{SkillsDir: ""},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ".claude/skills") {
			t.Errorf("unexpected skills mount in args when skillsDir is empty: %v", args)
		}
	}
}

// TestRun_AlreadyRunningContainerSkipsLaunch verifies that Run detects a
// same-named container already in the "running" state and returns
// ErrAlreadyRunning without ever invoking `podman/docker run` — the
// collision must not be attempted, only recognized (issue #562).
func TestRun_AlreadyRunningContainerSkipsLaunch(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "run-invoked")
	script := filepath.Join(dir, "fake-podman")
	scriptContent := "#!/bin/sh\ncase \"$1\" in\n" +
		"  inspect) echo running ;;\n" +
		"  run) touch " + marker + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("Run: launched the container despite it already running")
	}
}

// TestRun_ExitedContainerReapedThenLaunches verifies today's behavior stays
// intact for the non-collision case: a stale (exited/created, i.e. not
// running) same-named container is reaped with `rm -f`, and the launch
// proceeds normally (issue #562 acceptance criterion 3).
func TestRun_ExitedContainerReapedThenLaunches(t *testing.T) {
	dir := t.TempDir()
	rmMarker := filepath.Join(dir, "rm-invoked")
	runMarker := filepath.Join(dir, "run-invoked")
	script := filepath.Join(dir, "fake-podman")
	scriptContent := "#!/bin/sh\ncase \"$1\" in\n" +
		"  inspect) echo exited ;;\n" +
		"  rm) touch " + rmMarker + " ;;\n" +
		"  run) touch " + runMarker + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(rmMarker); statErr != nil {
		t.Error("Run: did not reap the stale exited container")
	}
	if _, statErr := os.Stat(runMarker); statErr != nil {
		t.Error("Run: did not launch after reaping the stale container")
	}
}

// TestRun_ExitCodeSurfacedAsRunError verifies that a non-zero exit from the
// scripted `podman/docker run` invocation surfaces as a *RunError carrying
// that exit code, so later slices can detect signal-kill exit codes (128+N)
// through a runtime-agnostic type instead of a raw *exec.ExitError.
func TestRun_ExitCodeSurfacedAsRunError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-podman")
	scriptContent := "#!/bin/sh\ncase \"$1\" in\n" +
		"  inspect) echo exited ;;\n" +
		"  rm) : ;;\n" +
		"  run) exit 143 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run: want error to unwrap to *RunError, got %v (%T)", err, err)
	}
	if runErr.ExitCode != 143 {
		t.Errorf("RunError.ExitCode: want 143, got %d", runErr.ExitCode)
	}
}

func TestReapAfterSuccess(t *testing.T) {
	if !reapAfterSuccess(nil) {
		t.Error("exit 0 (nil error) must reap the container")
	}
	if reapAfterSuccess(errors.New("exit status 1")) {
		t.Error("non-zero exit must retain the container (not reap)")
	}
}

func TestBuildRunArgs_NoRmFlag(t *testing.T) {
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	if containsArg(args, "--rm") {
		t.Errorf("--rm must not be in buildRunArgs (lifecycle is managed by Run); args: %v", args)
	}
}

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// wantTriple reports whether args contains the contiguous 3-element
// subsequence a0, a1, a2 (e.g. a flag and its two positional values).
func wantTriple(args []string, a0, a1, a2 string) bool {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == a0 && args[i+1] == a1 && args[i+2] == a2 {
			return true
		}
	}
	return false
}

// TestReap_NeverRemovesRunningContainer verifies the safety guard: when the
// fake CLI reports the container is running, Reap must not issue `rm -f`.
func TestReap_NeverRemovesRunningContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: "running"},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Reap: want 1 call (inspect only), got %d", calls)
	}
}

// TestReap_RemovesStaleContainer verifies the other side of the guard: when
// the fake CLI reports the container is not running, Reap issues `rm -f`.
func TestReap_RemovesStaleContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: "exited"},
		fakeCall{},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "agent-issue-1") {
		t.Errorf("Reap: want `rm -f agent-issue-1`, got %v", rm)
	}
}

// TestKill_MissingContainer_ReturnsNilNotError verifies the common
// settle-phase case (CI watch, merge gate): the initial Box already exited
// successfully and Run's own reapAfterSuccess already removed it, so there
// is no container left to kill. Runner.Kill's contract treats that as
// success, not a failure Terminate would otherwise misreport.
func TestKill_MissingContainer_ReturnsNilNotError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1}) // inspect: no such container
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Errorf("Kill: got %v, want nil for a missing container", err)
	}
	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Kill: want 1 call (inspect only, no rm attempted), got %d", calls)
	}
}

// TestKill_RemovesExistingContainerRegardlessOfRunningState verifies Kill's
// contract is the opposite of Reap's for a container that does exist: it
// issues `rm -f` unconditionally once existence is confirmed, so it reaches
// a genuinely live container Reap would refuse to touch.
func TestKill_RemovesExistingContainerRegardlessOfRunningState(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{}, fakeCall{}) // inspect: found; rm -f: ok
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if calls := callCount(t, dir); calls != 2 {
		t.Errorf("Kill: want 2 calls (inspect then rm -f), got %d", calls)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "agent-issue-1") {
		t.Errorf("Kill: want `rm -f agent-issue-1`, got %v", rm)
	}
}

// TestKill_RemovalFailureOnExistingContainer_ReturnsError verifies a
// scripted rm failure against a container confirmed to exist is returned,
// not swallowed — Terminate needs to know a genuine reap failure happened.
func TestKill_RemovalFailureOnExistingContainer_ReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{}, fakeCall{exit: 1}) // inspect: found; rm -f: fails
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err == nil {
		t.Error("Kill: want error from scripted rm failure, got nil")
	}
}

// TestLoadImage_InvokesLoadThenTag verifies loadImage issues `load -i
// <archive>` followed by `tag spindrift:latest <imageTag>`, in that order.
func TestLoadImage_InvokesLoadThenTag(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{},
		fakeCall{},
	)
	a := &ociAdapter{cli: script, imageTag: "spindrift:abc123"}

	if err := a.loadImage("/tmp/spindrift-image.tar"); err != nil {
		t.Fatalf("loadImage: %v", err)
	}

	load := readCall(t, dir, 0)
	want := []string{"load", "-i", "/tmp/spindrift-image.tar"}
	if strings.Join(load, " ") != strings.Join(want, " ") {
		t.Errorf("load call: got %v, want %v", load, want)
	}

	tag := readCall(t, dir, 1)
	want = []string{"tag", "spindrift:latest", "spindrift:abc123"}
	if strings.Join(tag, " ") != strings.Join(want, " ") {
		t.Errorf("tag call: got %v, want %v", tag, want)
	}
}

// TestLoadImage_DriverScopedRepo_TagsFromMatchingSourceTag verifies that
// loadImage re-tags from "<repo>:latest" where repo is derived from the
// adapter's own imageTag — not a hardcoded "spindrift:latest" — so a
// driver-scoped archive (e.g. an opencode image, which loads as
// "spindrift-opencode:latest") is found by the re-tag (#262).
func TestLoadImage_DriverScopedRepo_TagsFromMatchingSourceTag(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{},
		fakeCall{},
	)
	a := &ociAdapter{cli: script, imageTag: "spindrift-opencode:abc123"}

	if err := a.loadImage("/tmp/spindrift-opencode-image.tar"); err != nil {
		t.Fatalf("loadImage: %v", err)
	}

	tag := readCall(t, dir, 1)
	want := []string{"tag", "spindrift-opencode:latest", "spindrift-opencode:abc123"}
	if strings.Join(tag, " ") != strings.Join(want, " ") {
		t.Errorf("tag call: got %v, want %v", tag, want)
	}
}

// TestIsReady_ImageAbsentReturnsError verifies IsReady surfaces a descriptive
// error when `image inspect` fails (the image is not loaded).
func TestIsReady_ImageAbsentReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err == nil {
		t.Error("IsReady: want error when image absent, got nil")
	}
}

// TestIsReady_ImagePresentReturnsNil verifies IsReady succeeds when `image
// inspect` exits 0 (the image is loaded).
func TestIsReady_ImagePresentReturnsNil(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady: want nil when image present, got %v", err)
	}
}

// TestIsRunning_ScriptedStatuses verifies IsRunning reports true only for
// the exact "running" status string, across scripted successive calls with
// different outputs and exit codes.
func TestIsRunning_ScriptedStatuses(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{stdout: "running"},
		fakeCall{stdout: "exited"},
		fakeCall{exit: 1},
	)
	a := &ociAdapter{cli: script}

	if !a.IsRunning("c") {
		t.Error(`IsRunning: want true for "running" status`)
	}
	if a.IsRunning("c") {
		t.Error(`IsRunning: want false for "exited" status`)
	}
	if a.IsRunning("c") {
		t.Error("IsRunning: want false when inspect fails (exit 1)")
	}
}

// TestEnsureReady_ImageAbsentFallsBackToContainerBuild verifies the
// image-absent branch: EnsureReady tries a host build first, and when that
// fails with a builder-missing error, falls back to buildInContainer — which
// emits the non-digest-pinned supply-chain warning for an unpinned builder
// image.
func TestEnsureReady_ImageAbsentFallsBackToContainerBuild(t *testing.T) {
	cliScript, _ := newFakeCLI(t,
		fakeCall{exit: 1}, // image inspect: absent
		fakeCall{},        // run (container build)
		fakeCall{},        // load
		fakeCall{},        // tag
	)

	// nix build is invoked directly (not through the cli field); stub it on
	// PATH so the host build fails with a builder-missing error.
	nixDir := t.TempDir()
	nixStub := "#!/bin/sh\necho 'error: a Linux system is required to build a Linux derivation' 1>&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(nixDir, "nix"), []byte(nixStub), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", nixDir+":"+oldPath)

	a := &ociAdapter{
		cli:             cliScript,
		image:           "spindrift:abc123",
		imageDrv:        "/nix/store/fake.drv",
		imageTag:        "spindrift:abc123",
		nixBuilderImage: "docker.io/nixos/nix:latest", // unpinned -> triggers warning
		nixVolume:       "spindrift-nix",
		pwd:             "/work",
		flakeImageAttr:  ".#packages.aarch64-linux.agent-image",
	}

	// Capture stderr — the supply-chain warning is written there directly.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	ensureErr := a.EnsureReady()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if ensureErr != nil {
		t.Fatalf("EnsureReady: %v", ensureErr)
	}
	stderr := buf.String()
	if !strings.Contains(stderr, "not digest-pinned") {
		t.Errorf("expected supply-chain warning in stderr; got: %q", stderr)
	}
}

// TestEnsureReady_HostNixBuildInvokedViaSeam verifies that the host `nix
// build` step in EnsureReady goes through the execCommand seam, and that a
// genuine (non-builder-missing) scripted failure surfaces as an error
// without falling back to the container build.
func TestEnsureReady_HostNixBuildInvokedViaSeam(t *testing.T) {
	cliScript, _ := newFakeCLI(t, fakeCall{exit: 1}) // image inspect: absent

	nixScript, nixDir := newFakeCLI(t, fakeCall{exit: 1, stdout: "genuine derivation error"})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(nixScript, args...)
	}

	a := &ociAdapter{
		cli:      cliScript,
		image:    "spindrift:abc123",
		imageDrv: "/nix/store/fake.drv",
	}

	err := a.EnsureReady()

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Error("expected error from scripted nix build failure, got nil")
	}
	if got := callCount(t, nixDir); got != 1 {
		t.Errorf("callCount = %d, want 1 (no container-build fallback for a genuine error)", got)
	}
}
