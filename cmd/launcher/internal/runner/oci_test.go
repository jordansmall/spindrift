package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryprobe"
)

// fakeCall scripts one invocation of a fake CLI binary: the exit code it
// returns and the stdout/stderr it prints. Real podman/docker print their
// name-collision refusal to stderr, not stdout, so a test pinning that
// refusal must use stderr to actually exercise the stream Run tees.
type fakeCall struct {
	exit   int
	stdout string
	stderr string
}

// newFakeCLI writes a stub runtime binary that records each invocation's argv
// to call-NN.txt (zero-indexed) in a temp dir and exits/prints per the scripted
// calls in order. Once the invocation count exceeds len(calls), the last
// scripted call repeats. Returns the script path (for ociAdapter.cli) and the
// dir (for reading recorded calls back).
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
		fmt.Fprintf(&b, "%s) printf '%%s' %q; printf '%%s' %q >&2; exit %d ;;\n", pattern, c.stdout, c.stderr, c.exit)
	}
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// inspectJSONStdout builds the stdout inspectContainer now expects from
// `inspect --format={{json .}}`: an object with Id, Created (RFC3339Nano,
// what both podman and docker emit) and a nested State.Status. The field
// stays spelled `Id` against production's `ID` on purpose — that mismatch is
// what exercises encoding/json's case-insensitive match against the key the
// runtimes actually emit, so do not "fix" it to match the Go initialism.
func inspectJSONStdout(t *testing.T, id, status string, created time.Time) string {
	t.Helper()
	body := struct {
		Id      string
		Created string
		State   struct {
			Status string
		}
	}{Id: id, Created: created.Format(time.RFC3339Nano)}
	body.State.Status = status
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

// EnsureReady emits an "already loaded" line when the image is already loaded,
// worded so it never collides with the freshness probe's distinct "rebuild
// needed" vocabulary (#1885).
func TestEnsureReady_ImagePresentPrintsMessage(t *testing.T) {
	// Fake CLI: exits 0 for any invocation (simulates "image inspect" success).
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-podman")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	// EnsureReady prints with fmt.Printf, so capture os.Stdout to read it back.
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
	// The call must not panic on a missing root; there is nothing else to assert.
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

// looksLikeNameCollision gates the whole name-collision classification path
// in Run, so its true/false boundary needs a direct pin rather than only the
// end-to-end coverage the Run tests give it. Both short-circuit arms of the
// pre-filter are pinned, including the substring over-match a bare
// strings.Contains accepts — deliberate, since Run proves the collision with
// a post-failure inspect and never trusts this on its own.
func TestLooksLikeNameCollision(t *testing.T) {
	cases := []struct {
		name string
		out  string
		box  string
		want bool
	}{
		{
			name: "podman refusal for the right name",
			out:  `Error: creating container storage: the container name "agent-issue-1" is already in use by abc123`,
			box:  "agent-issue-1",
			want: true,
		},
		{
			name: "docker refusal for the right name",
			out:  `docker: Error response from daemon: Conflict. The container name "/agent-issue-1" is already in use by container "abc123def456". You have to remove (or rename) that container to be able to reuse that name.`,
			box:  "agent-issue-1",
			want: true,
		},
		{
			name: "refusal naming a different box",
			out:  `Error: creating container storage: the container name "agent-issue-2" is already in use by abc123`,
			box:  "agent-issue-1",
			want: false,
		},
		{
			name: "arbitrary output mentioning the name but not the phrase",
			out:  "starting agent-issue-1: pulling image",
			box:  "agent-issue-1",
			want: false,
		},
		{
			name: "refusal carrying the phrase but no name at all",
			out:  "Error: creating container storage: the container name is already in use",
			box:  "agent-issue-1",
			want: false,
		},
		{
			name: "refusal naming a box this one's name is a prefix of",
			out:  `Error: creating container storage: the container name "agent-issue-10" is already in use by abc123`,
			box:  "agent-issue-1",
			want: true, // accepted over-match: Run confirms with a real inspect
		},
		{
			name: "empty output",
			out:  "",
			box:  "agent-issue-1",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeNameCollision(tc.out, tc.box); got != tc.want {
				t.Errorf("looksLikeNameCollision(%q, %q) = %v, want %v", tc.out, tc.box, got, tc.want)
			}
		})
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

// containerBuildCmd prepends a safe.directory gitconfig prelude ahead of the
// `nix build` invocation (issue #2196). The prelude is written directly via
// printf under a writable HOME rooted at /build-output, so it does not depend
// on a `git` CLI being present in the builder image.
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

	if !containsArg(args, "--cap-drop=all") {
		t.Errorf("--cap-drop=all always required; args: %v", args)
	}
	if !containsArg(args, "--security-opt=no-new-privileges") {
		t.Errorf("--security-opt=no-new-privileges always required; args: %v", args)
	}

	for _, flag := range []string{"--pids-limit", "--memory"} {
		for _, arg := range args {
			if arg == flag {
				t.Errorf("unexpected flag %q when limit is empty; args: %v", flag, args)
			}
		}
	}
}

// TestNetworkArg covers networkArg across cli x networkMode plus the raw
// podmanNetwork escape hatch (issue #2562). Raw podmanNetwork wins whenever
// set: for a non-open networkMode nix eval-rejects that combination, but
// networkArg cannot observe that and still needs a deterministic answer; for
// "open" the raw-wins case is genuinely reachable, not defense-in-depth.
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

// buildRunArgs itself wires networkMode through to --network, not just the
// networkArg helper.
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

// The default or unset mode renders no --network flag at all when no raw knob
// is set either.
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

// TestBuildRunArgs_NeverRendersIssuesMount is a per-adapter rendering
// regression guard (issue #3471): a zero-value mountParams must never render
// an /issues mount. The discriminating pins live elsewhere, in mount_test.go
// (structural) and main_test.go (end-to-end).
func TestBuildRunArgs_NeverRendersIssuesMount(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: MountParams{},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ":/issues") {
			t.Errorf("unexpected /issues mount: %v", args)
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

// TestBuildRunArgs_RegistryProxySocketMounted: a Box-derived
// RegistryProxy.Endpoint's unix path produces a -v <source>:/registry-proxy.sock
// entry (ADR 0044, issue #2849).
func TestBuildRunArgs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(sock)}}
	args := a.buildRunArgs(box)

	want := sock + ":/registry-proxy.sock"
	if !containsArg(args, want) {
		t.Errorf("registry-proxy socket mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_OffArgvKeyRendersBareFlag: a box.Env key listed in
// offArgvKeys (shared with the bwrap adapter) renders as a bare `-e KEY`,
// never `-e KEY=VALUE`, so the value never lands in argv, which ps/proc
// exposes to any local user for the container's whole lifetime (issue #3111
// finding A).
func TestBuildRunArgs_OffArgvKeyRendersBareFlag(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{
		"REGISTRY_PROXY_TCP_SECRET": "s3cr3t-token",
		"GH_TOKEN":                  "gh-s3cr3t",
		"FORGEJO_TOKEN":             "forgejo-s3cr3t",
		"ISSUE_TEXT":                "private issue body\nwith a secret-shaped line",
		"ISSUE_NUMBER":              "1",
	}}
	args := a.buildRunArgs(box)

	for _, key := range []string{"REGISTRY_PROXY_TCP_SECRET", "GH_TOKEN", "FORGEJO_TOKEN", "ISSUE_TEXT"} {
		if !containsArg(args, key) {
			t.Errorf("expected bare -e %s in args: %v", key, args)
		}
		for _, arg := range args {
			if strings.Contains(arg, key+"=") {
				t.Errorf("%s must render as a bare -e flag, never -e %s=...; found %q in args: %v", key, key, arg, args)
			}
		}
	}
	// The ISSUE_TEXT fixture is deliberately two lines, so assert on both:
	// a `KEY=` probe alone would miss a render that split the value off
	// from its key.
	for _, arg := range args {
		if strings.Contains(arg, "private issue body") || strings.Contains(arg, "with a secret-shaped line") {
			t.Errorf("ISSUE_TEXT value must never appear on argv; found %q in args: %v", arg, args)
		}
	}
	if !containsArg(args, "ISSUE_NUMBER=1") {
		t.Errorf("expected off-argv-exempt ISSUE_NUMBER=1 to render unchanged in args: %v", args)
	}
}

// TestOciRunEnv: ociRunEnv appends only the offArgvKeys-listed keys present in
// boxEnv, as KEY=VALUE, on top of the full os.Environ(), so the docker/podman
// CLI process carries the value in its own environment (for a bare `-e KEY`
// argv entry to forward) without it ever reaching the exec.Command args slice.
func TestOciRunEnv(t *testing.T) {
	boxEnv := map[string]string{
		"REGISTRY_PROXY_TCP_SECRET": "s3cr3t-token",
		"GH_TOKEN":                  "gh-s3cr3t",
		"FORGEJO_TOKEN":             "forgejo-s3cr3t",
		"ISSUE_TEXT":                "issue-text-value",
		"ISSUE_NUMBER":              "1", // not in offArgvKeys, must not be appended
	}
	got := ociRunEnv(boxEnv)

	// Spelled out rather than derived from offArgvKeys: deriving it would
	// shrink the expectation and the actual together if a key were ever
	// dropped from the set, leaving the length assert green. The membership
	// guard below fails loudly in that case instead.
	offArgv := []string{"FORGEJO_TOKEN", "GH_TOKEN", "ISSUE_TEXT", "REGISTRY_PROXY_TCP_SECRET"}
	for _, k := range offArgv {
		if !offArgvKeys[k] {
			t.Fatalf("ociRunEnv fixture: %s is no longer an offArgvKeys member; update this test alongside the set", k)
		}
	}

	baseline := os.Environ()
	if len(got) != len(baseline)+len(offArgv) {
		t.Fatalf("ociRunEnv: want len %d (os.Environ()+%d off-argv keys), got %d: %v", len(baseline)+len(offArgv), len(offArgv), len(got), got)
	}
	if !containsArg(got, "REGISTRY_PROXY_TCP_SECRET=s3cr3t-token") {
		t.Errorf("ociRunEnv: missing REGISTRY_PROXY_TCP_SECRET=s3cr3t-token in %v", got)
	}
	if !containsArg(got, "GH_TOKEN=gh-s3cr3t") {
		t.Errorf("ociRunEnv: missing GH_TOKEN=gh-s3cr3t in %v", got)
	}
	if !containsArg(got, "FORGEJO_TOKEN=forgejo-s3cr3t") {
		t.Errorf("ociRunEnv: missing FORGEJO_TOKEN=forgejo-s3cr3t in %v", got)
	}
	if !containsArg(got, "ISSUE_TEXT=issue-text-value") {
		t.Errorf("ociRunEnv: missing ISSUE_TEXT=issue-text-value in %v", got)
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

// TestBuildRunArgs_IssueTextAbsentOrEmpty covers issue #3470's two remaining
// acceptance criteria on the OCI runner. An ISSUE_TEXT key absent from box.Env
// emits no "-e ISSUE_TEXT" at all, since dispatch.go's IssueTextFor only sets
// the key when the resolved text is non-empty. A present-but-empty value still
// emits the bare flag, because that rendering never looks at the value.
func TestBuildRunArgs_IssueTextAbsentOrEmpty(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}

	t.Run("absent", func(t *testing.T) {
		box := Box{Name: "agent-issue-1", Env: map[string]string{"ISSUE_NUMBER": "1"}}
		args := a.buildRunArgs(box)
		if containsArg(args, "ISSUE_TEXT") {
			t.Errorf("buildRunArgs emitted -e ISSUE_TEXT for a box.Env with no ISSUE_TEXT key: %v", args)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		box := Box{Name: "agent-issue-1", Env: map[string]string{"ISSUE_TEXT": ""}}
		args := a.buildRunArgs(box)
		if !containsArg(args, "ISSUE_TEXT") {
			t.Errorf("buildRunArgs: current behaviour still emits bare -e ISSUE_TEXT for an empty-string value; got %v", args)
		}
		for _, arg := range args {
			if strings.Contains(arg, "ISSUE_TEXT=") {
				t.Errorf("empty ISSUE_TEXT must never render as -e ISSUE_TEXT=...; found %q in %v", arg, args)
			}
		}
	})
}

// TestOciRunEnv_IssueTextAbsentOrEmpty covers the same two cases on ociRunEnv:
// absent from boxEnv appends no "ISSUE_TEXT=" entry; present-but-empty appends
// an empty one, since the boxEnv lookup checks presence, not emptiness. The
// "absent" subtest must blank the launcher's own ambient ISSUE_TEXT first:
// ociRunEnv starts from os.Environ(), which can carry a real one already.
func TestOciRunEnv_IssueTextAbsentOrEmpty(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		if orig, ok := os.LookupEnv("ISSUE_TEXT"); ok {
			os.Unsetenv("ISSUE_TEXT")
			t.Cleanup(func() { os.Setenv("ISSUE_TEXT", orig) })
		}
		got := ociRunEnv(map[string]string{"ISSUE_NUMBER": "1"})
		for _, kv := range got {
			if strings.HasPrefix(kv, "ISSUE_TEXT=") {
				t.Errorf("ociRunEnv appended an ISSUE_TEXT= entry for a boxEnv with no ISSUE_TEXT key: %v", got)
			}
		}
	})

	t.Run("empty string", func(t *testing.T) {
		got := ociRunEnv(map[string]string{"ISSUE_TEXT": ""})
		if !containsArg(got, "ISSUE_TEXT=") {
			t.Errorf("ociRunEnv: current behaviour still appends ISSUE_TEXT= (empty value) for a present-but-empty boxEnv entry; got %v", got)
		}
	})
}

// TestBuildRunArgs_TCPHostAddHostMounted: a Box-derived TCP
// RegistryProxy.Endpoint's host renders an --add-host <host>:host-gateway flag
// (issue #3111). The guest needs an explicit host-gateway mapping for the
// TCP-transport fallback, since plain Linux docker offers none by default.
func TestBuildRunArgs_TCPHostAddHostMounted(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewTCPEndpoint("host.containers.internal", ""), TCPAddHost: true}}
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

// TestBuildRunArgs_TCPHostWithoutAddHost_OmitsAddHost pins the case the flag
// exists for: a Box whose runtime resolves TCPHost on its own must NOT get an
// --add-host mapping. The mapping is an override, not an addition: on a
// VM-backed runtime it replaces a name that already points at the launcher
// with the in-VM bridge gateway (measured: reachable without it, refused with).
func TestBuildRunArgs_TCPHostWithoutAddHost_OmitsAddHost(t *testing.T) {
	a := &ociAdapter{cli: "docker", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewTCPEndpoint("host.docker.internal", "5000")}}
	args := a.buildRunArgs(box)

	if containsArg(args, "--add-host") {
		t.Errorf("--add-host must be absent when TCPAddHost is false; args: %v", args)
	}
}

func TestBuildRunArgs_TCPHostUnset_NoAddHost(t *testing.T) {
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	if containsArg(args, "--add-host") {
		t.Errorf("--add-host must be absent when TCPHost is unset; args: %v", args)
	}
}

// TestHostGatewayHostname covers the mapping from cli to hostname: podman uses
// its own host.containers.internal convention; docker and nerdctl share
// host.docker.internal (Rancher Desktop's containerd/nerdctl mode honors it too).
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

// TestRegistrySocketProbeArgs covers the pure arg-builder shape: --rm right
// after "run", the socket mount rendered via the shared candidateSocketMount
// path, and the driver-exec probe trailing command in place of the image's
// default entrypoint, without exec'ing anything.
func TestRegistrySocketProbeArgs(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &ociAdapter{cli: "podman", image: "spindrift:test"}
	args := a.registrySocketProbeArgs(sock, "probe-container")

	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("want args[0:2] = [run --rm], got %v", args)
	}
	want := sock + ":" + RegistryProxySocketTarget
	if !containsArg(args, want) {
		t.Errorf("missing socket mount %q in args: %v", want, args)
	}
	tail := []string{a.image, "probe-registry-socket", "-path", RegistryProxySocketTarget}
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

// TestRegistryProbeArgs_OverrideImageEntrypoint pins that both throwaway probes
// replace the image's own entrypoint rather than appending the verb to it.
// lib/image.nix sets Entrypoint to /bin/bash, so "<image> driver-exec <verb>"
// hands bash an ELF binary as a script and bash exits 126, neither the probe
// contract's 0 (capable) nor its 1 (incapable), so dispatch aborts with no Box.
func TestRegistryProbeArgs_OverrideImageEntrypoint(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &ociAdapter{cli: "docker", image: "spindrift:test"}

	cases := []struct {
		name string
		args []string
		verb string
	}{
		{"socket", a.registrySocketProbeArgs(sock, "probe-container"), "probe-registry-socket"},
		{"tcp", a.registryTCPProbeArgs("host.docker.internal", 8080, "probe-container", true), "probe-registry-tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(strings.Join(tc.args, " "), "--entrypoint driver-exec") {
				t.Errorf("probe must override the image entrypoint with driver-exec; args: %v", tc.args)
			}
			for i, arg := range tc.args {
				if arg != a.image {
					continue
				}
				if i+1 >= len(tc.args) || tc.args[i+1] != tc.verb {
					t.Errorf("want %q immediately after the image, got %v", tc.verb, tc.args[i:])
				}
				return
			}
			t.Errorf("image %q absent from args: %v", a.image, tc.args)
		})
	}
}

// TestRegistryProxyTransport_ScriptedCapableExit_ReportsSocketCapable:
// RegistryProxyTransport reports capable when the probe container exits
// registryprobe.ExitCapable, scripted via a fake CLI so no real container
// runtime is ever started (issue #3111's own acceptance criterion).
func TestRegistryProxyTransport_ScriptedCapableExit_ReportsSocketCapable(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: %v", err)
	}
	if !endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a unix endpoint on scripted zero exit")
	}
	if endpoint.Host() != "" {
		t.Errorf("RegistryProxyTransport: want empty host on a unix endpoint, got %q", endpoint.Host())
	}

	call := readCall(t, dir, 0)
	if call[0] != "run" || call[1] != "--rm" {
		t.Fatalf("want call[0:2] = [run --rm], got %v", call)
	}
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, ":"+RegistryProxySocketTarget) {
		t.Errorf("expected socket mount ending in :%s in call: %v", RegistryProxySocketTarget, call)
	}
	if !strings.Contains(joined, "--entrypoint driver-exec") ||
		!strings.Contains(joined, "probe-registry-socket -path "+RegistryProxySocketTarget) {
		t.Errorf("expected probe trailing command in call: %v", call)
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1: a socket-capable verdict must never launch the tcp-reachability sub-probe", got)
	}
}

// registryprobe.ExitIncapable from the socket probe is a clean "incapable"
// answer, not a Go error, so a stat-only or unconnectable socket degrades
// cleanly, as long as the live TCP-reachability sub-probe (issue #3111 review
// finding B) confirms the fallback route works. Scripts two calls: the socket
// probe (ExitIncapable), then the TCP sub-probe (ExitCapable, reachable).
func TestRegistryProxyTransport_ScriptedIncapableExit_ReportsIncapableWithTCPHost(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitCapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	// a.cli is a fake-script path, not literally "podman", so
	// hostGatewayHostname falls into its docker/nerdctl branch. Pin that literal
	// rather than calling hostGatewayHostname(a.cli), which would make the
	// assertion tautological against the function under test.
	const wantTCPHost = "host.docker.internal"

	endpoint, addHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error on scripted non-zero exit, got %v", err)
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted non-zero exit")
	}
	if !endpoint.IsTCP() {
		t.Fatal("RegistryProxyTransport: want a TCP endpoint on scripted non-zero exit")
	}
	if endpoint.Host() != wantTCPHost {
		t.Errorf("RegistryProxyTransport: host = %q, want %q", endpoint.Host(), wantTCPHost)
	}
	// The runtime's own resolution is probed first and, here, succeeds, so no
	// --add-host override is needed or wanted.
	if addHost {
		t.Error("RegistryProxyTransport: want addHost=false when the sub-probe succeeds without the mapping")
	}

	if got := callCount(t, dir); got != 2 {
		t.Fatalf("callCount = %d, want 2 (socket probe + tcp-reachability sub-probe)", got)
	}
	call := readCall(t, dir, 1)
	if call[0] != "run" || call[1] != "--rm" {
		t.Fatalf("want call[0:2] = [run --rm] for the tcp-reachability sub-probe, got %v", call)
	}
	joined := strings.Join(call, " ")
	if strings.Contains(joined, "--add-host") {
		t.Errorf("first tcp-reachability sub-probe must try the runtime's own resolution, with no --add-host: %v", call)
	}
	if !strings.Contains(joined, "--entrypoint driver-exec") ||
		!strings.Contains(joined, "probe-registry-tcp -host "+endpoint.Host()+" -port ") {
		t.Errorf("expected probe-registry-tcp trailing command in tcp-reachability sub-probe call: %v", call)
	}
}

// When the socket probe reports incapable AND the live TCP-reachability
// sub-probe (issue #3111 review finding B) reports the --add-host host-gateway
// route is not reachable, RegistryProxyTransport must return a hard error
// naming the CLI and the host. The old behavior trusted the TCP fallback
// unconditionally, with nothing listening on the route the Box would dial.
func TestRegistryProxyTransport_TCPReachabilitySubProbeIncapable_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitIncapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatalf("RegistryProxyTransport: want error when the tcp-reachability sub-probe also reports incapable, got endpoint=%+v", endpoint)
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint when the tcp-reachability sub-probe reports incapable")
	}
	if !strings.Contains(err.Error(), a.cli) {
		t.Errorf("RegistryProxyTransport: error %q should name the CLI %q", err, a.cli)
	}
	if !strings.Contains(err.Error(), hostGatewayHostname(a.cli)) {
		t.Errorf("RegistryProxyTransport: error %q should name the host %q", err, hostGatewayHostname(a.cli))
	}

	// Both wirings must be tried before giving up: the socket probe, then the
	// sub-probe without --add-host, then the sub-probe with it. Failing after
	// only the first wiring would condemn whichever platform needs the other.
	if got := callCount(t, dir); got != 3 {
		t.Errorf("callCount = %d, want 3 (socket probe + tcp sub-probe without and with --add-host)", got)
	}
	withoutCall := strings.Join(readCall(t, dir, 1), " ")
	if strings.Contains(withoutCall, "--add-host") {
		t.Errorf("call 1 must probe without the mapping: %v", withoutCall)
	}
	withCall := strings.Join(readCall(t, dir, 2), " ")
	if !strings.Contains(withCall, "--add-host "+hostGatewayHostname(a.cli)+":host-gateway") {
		t.Errorf("call 2 must probe with the mapping: %v", withCall)
	}
}

// The plain Linux docker case: the runtime does not resolve the TCP host on its
// own, so the first sub-probe fails and the --add-host host-gateway mapping is
// what makes the route work. The mapping must then be reported so the real Box
// is launched with it. Scripts three calls: socket probe (ExitIncapable),
// sub-probe without the mapping (ExitIncapable), sub-probe with it (ExitCapable).
func TestRegistryProxyTransport_TCPNeedsAddHost_ReportsAddHost(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitCapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, addHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error when the mapping makes the route work, got %v", err)
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint")
	}
	if endpoint.Host() != "host.docker.internal" {
		t.Errorf("host = %q, want %q", endpoint.Host(), "host.docker.internal")
	}
	if !addHost {
		t.Error("RegistryProxyTransport: want addHost=true when only the mapped route is reachable")
	}
	if got := callCount(t, dir); got != 3 {
		t.Fatalf("callCount = %d, want 3", got)
	}
}

// When the probe reports socket-incapable AND networkMode denies host-loopback
// reachability (no-host-loopback or none), RegistryProxyTransport must refuse
// the TCP fallback with a descriptive error, not hand back a route the Box
// cannot reach with no diagnostic (issue #3111 finding B). Scripts one call and
// asserts callCount stays at 1: deniesHostLoopback hard-errors before any more.
func TestRegistryProxyTransport_NoHostLoopback_SocketIncapable_ReturnsError(t *testing.T) {
	for _, mode := range []string{NetworkModeNoHostLoopback, NetworkModeNone} {
		t.Run(mode, func(t *testing.T) {
			script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable})
			a := &ociAdapter{cli: script, image: "spindrift:test", networkMode: mode}

			endpoint, _, err := a.RegistryProxyTransport()
			if err == nil {
				t.Fatalf("RegistryProxyTransport: want error for networkMode=%q + socket-incapable, got endpoint=%+v", mode, endpoint)
			}
			if endpoint.IsUnix() {
				t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted non-zero exit")
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

// The socket-incapable-but-TCP-capable behavior must be unchanged when
// networkMode is "open" or unset, so this must not regress
// TestRegistryProxyTransport_ScriptedIncapableExit_ReportsIncapableWithTCPHost.
// Scripts two calls (socket ExitIncapable, TCP sub-probe ExitCapable); with one,
// the repeat would give the sub-probe ExitIncapable and now correctly error.
func TestRegistryProxyTransport_OpenOrUnsetNetworkMode_UnchangedBehavior(t *testing.T) {
	for _, mode := range []string{"open", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			script, _ := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitCapable})
			a := &ociAdapter{cli: script, image: "spindrift:test", networkMode: mode}

			// a.cli is a fake-script path, not literally "podman", so
			// hostGatewayHostname falls into its docker/nerdctl branch. Pin
			// that literal rather than calling hostGatewayHostname(a.cli),
			// which would make the assertion tautological.
			const wantTCPHost = "host.docker.internal"

			endpoint, _, err := a.RegistryProxyTransport()
			if err != nil {
				t.Fatalf("RegistryProxyTransport: want nil error for networkMode=%q, got %v", mode, err)
			}
			if endpoint.IsUnix() {
				t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted non-zero exit")
			}
			if endpoint.Host() != wantTCPHost {
				t.Errorf("RegistryProxyTransport: host = %q, want %q", endpoint.Host(), wantTCPHost)
			}
		})
	}
}

// Issue #3113's headline acceptance criterion: once a socket-capable verdict is
// probed and cached under a given pwd, a second adapter sharing that pwd (and
// the same cli/image/networkMode) replays it without starting another probe
// container.
func TestRegistryProxyTransport_CachesSocketCapableVerdict(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}

	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}
	if !endpoint.IsUnix() {
		t.Fatal("RegistryProxyTransport (first call): want a unix endpoint")
	}
	if got := callCount(t, dir); got != 1 {
		t.Fatalf("callCount after first call = %d, want 1", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	endpoint, _, err = b.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if !endpoint.IsUnix() {
		t.Fatal("RegistryProxyTransport (second call): want a unix endpoint replayed from the cache")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount after second call = %d, want still 1: a cache hit must start no container", got)
	}
}

// Issue #3113's AC that a cached TCP verdict reproduces the exact transport an
// inline probe would have selected, --add-host mode included. Scripts three
// calls (socket ExitIncapable, sub-probe without the mapping ExitIncapable,
// sub-probe with it ExitCapable) so the first call resolves tcpAddHost=true; a
// second adapter sharing pwd must replay both without re-probing.
func TestRegistryProxyTransport_CachesTCPVerdictWithAddHostMode(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitIncapable}, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}

	endpoint, addHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}
	if !endpoint.IsTCP() || !addHost {
		t.Fatalf("RegistryProxyTransport (first call): want a TCP endpoint with addHost=true, got endpoint=%+v addHost=%v", endpoint, addHost)
	}
	wantHost := endpoint.Host()
	if got := callCount(t, dir); got != 3 {
		t.Fatalf("callCount after first call = %d, want 3", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	endpoint, addHost, err = b.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if !endpoint.IsTCP() {
		t.Fatal("RegistryProxyTransport (second call): want a TCP endpoint replayed from the cache")
	}
	if endpoint.Host() != wantHost {
		t.Errorf("RegistryProxyTransport (second call): host = %q, want %q", endpoint.Host(), wantHost)
	}
	if !addHost {
		t.Error("RegistryProxyTransport (second call): want addHost=true replayed from the cache")
	}
	if got := callCount(t, dir); got != 3 {
		t.Errorf("callCount after second call = %d, want still 3: a cache hit must start no container", got)
	}
}

// A cached verdict probed under one runtime binary is never replayed for a
// different one sharing the same pwd: the cache key covers cli, so a runtime
// swap re-probes rather than trusting a stale answer.
func TestRegistryProxyTransport_RuntimeChangeInvalidatesCache(t *testing.T) {
	script1, dir1 := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script1, image: "spindrift:test", pwd: pwd}
	if _, _, err := a.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}

	script2, dir2 := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	b := &ociAdapter{cli: script2, image: "spindrift:test", pwd: pwd}
	if _, _, err := b.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if got := callCount(t, dir1); got != 1 {
		t.Errorf("callCount(dir1) = %d, want 1", got)
	}
	if got := callCount(t, dir2); got != 1 {
		t.Errorf("callCount(dir2) = %d, want 1: a different runtime binary must re-probe, not replay dir1's cached verdict", got)
	}
}

// A cached verdict probed under one image reference is never replayed for a
// different one sharing the same pwd: per #3120, an old image's missing
// probe-registry-socket verb reads as socket-incapable, so the image ref must
// invalidate a stale verdict too.
func TestRegistryProxyTransport_ImageChangeInvalidatesCache(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	if _, _, err := a.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}
	if got := callCount(t, dir); got != 1 {
		t.Fatalf("callCount after first call = %d, want 1", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:other", pwd: pwd}
	if _, _, err := b.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount after second call = %d, want 2: a different image ref must re-probe, not replay the other image's cached verdict", got)
	}
}

// A verdict cached under one networkMode is never replayed for a different one
// sharing the same pwd: oci.go's socket-incapable path branches on networkMode
// and hard-errors under a host-loopback-denying mode instead of falling back to
// TCP. "open" and "" both reach the live probe rather than deniesHostLoopback's
// hard-error branch, so both sides of this comparison actually probe.
func TestRegistryProxyTransport_NetworkModeChangeInvalidatesCache(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd, networkMode: "open"}
	if _, _, err := a.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}
	if got := callCount(t, dir); got != 1 {
		t.Fatalf("callCount after first call = %d, want 1", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd, networkMode: ""}
	if _, _, err := b.RegistryProxyTransport(); err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount after second call = %d, want 2: a different networkMode must re-probe, not replay the other mode's cached verdict", got)
	}
}

// A damaged cache file at registryProbeCachePath(pwd) counts as a miss,
// matching the freshness Guard's corruption-tolerance idiom, rather than
// failing the dispatch: the probe still runs and returns the right verdict.
func TestRegistryProxyTransport_CorruptCacheFallsBackToProbing(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	pwd := t.TempDir()
	writeRegistryProbeCacheFile(t, pwd, []byte("not json"))

	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: %v", err)
	}
	if !endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a unix endpoint despite the corrupt cache file")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1: a corrupt cache file must fall back to a live probe", got)
	}
}

// A probe error is never persisted: a failed probe is not a verdict to
// remember, and caching it would turn one transient infrastructure failure
// into a persistent one.
func TestRegistryProxyTransport_ProbeErrorWritesNoCache(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 125})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}

	if _, _, err := a.RegistryProxyTransport(); err == nil {
		t.Fatal("RegistryProxyTransport: want an error on scripted exit 125")
	}
	if _, err := os.Stat(registryProbeCachePath(pwd)); !os.IsNotExist(err) {
		t.Errorf("registryProbeCachePath(pwd): want no file after a probe error, stat err = %v", err)
	}
}

// The helper's exact membership: only no-host-loopback and none deny
// host-loopback reachability; open, unset and any other value do not.
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

// A genuine inability to even start the runtime (CLI missing) must return an
// error rather than being folded into a clean "incapable" answer.
func TestRegistryProxyTransport_ExecFailure_ReturnsError(t *testing.T) {
	a := &ociAdapter{cli: filepath.Join(t.TempDir(), "nonexistent-cli-binary"), image: "spindrift:test"}

	_, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when the runtime CLI itself cannot be started")
	}
}

// A docker/podman "daemon error" exit (125, its convention for docker run
// itself failing) must return a Go error, not the clean "incapable" verdict:
// only registryprobe.ExitIncapable is probe-registry-socket's documented
// incapable answer (issue #3111 finding 2, issue #3120). The single scripted
// call repeats, so the control probe also exits 125 and callCount is 2.
func TestRegistryProxyTransport_ScriptedExitCode125_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 125, stdout: "boom"})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error on scripted exit 125, got nil")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted exit 125")
	}
	if !strings.Contains(err.Error(), "125") {
		t.Errorf("RegistryProxyTransport: error %q should mention the exit code 125", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2: a no-verdict socket exit must run the control probe but never the tcp-reachability sub-probe", got)
	}
}

// Pins issue #3120's core fix: an old driver-exec built before the probe verbs
// existed falls through to flag parsing and exits 1, which is no longer
// registryprobe.ExitIncapable (now 91), so it must return a hard error naming
// the exit code and a launcher/image version mismatch. The single scripted call
// repeats for the control probe, so callCount is 2 and the TCP probe never runs.
func TestRegistryProxyTransport_ScriptedExitCode1_OldImageDrift_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error on scripted exit 1, got nil")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted exit 1")
	}
	// "exited 1", not a bare "1": the message also names the two reserved
	// codes (90/91), so a bare substring would pass without the observed exit
	// code appearing at all.
	if !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("RegistryProxyTransport: error %q should mention the exit code 1", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("RegistryProxyTransport: error %q should name a launcher/image version mismatch", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2: a no-verdict socket exit must run the control probe but never the tcp-reachability sub-probe", got)
	}
}

// The other half of issue #3120's contract change: plain exit 0 is no longer a
// capable verdict on its own, only registryprobe.ExitCapable (90) is. A bare
// exit 0 must read as no-verdict, never as socket-capable. The single scripted
// call repeats for the control probe too, so this is a no-verdict-from-both
// outcome, matching the old-image-drift test's callCount shape.
func TestRegistryProxyTransport_ScriptedZeroExit_NoVerdict_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error on scripted exit 0, got nil")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint on scripted exit 0")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("RegistryProxyTransport: error %q should name a launcher/image version mismatch", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2: a no-verdict socket exit must run the control probe too", got)
	}
}

// Pins issue #3466's core scenario: a macOS host whose runtime rejects the
// socket mount itself (Rancher Desktop plus virtiofs) exits the socket probe
// container at 125 before probe-registry-socket runs, so there is no verdict.
// The control probe, identical minus the socket mount, then exits ExitIncapable,
// which must read as a clean incapable verdict and fall through to the TCP probe.
func TestRegistryProxyTransport_SocketNoVerdict_ControlIncapable_ReportsTCP(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 125},
		fakeCall{exit: registryprobe.ExitIncapable},
		fakeCall{exit: registryprobe.ExitCapable},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error when the control probe confirms incapable, got %v", err)
	}
	if !endpoint.IsTCP() {
		t.Fatalf("RegistryProxyTransport: want a TCP endpoint, got %+v", endpoint)
	}

	if got := callCount(t, dir); got != 3 {
		t.Fatalf("callCount = %d, want 3 (socket probe + control probe + tcp-reachability sub-probe)", got)
	}
	control := readCall(t, dir, 1)
	joined := strings.Join(control, " ")
	if strings.Contains(joined, ":"+RegistryProxySocketTarget) {
		t.Errorf("control probe must omit the socket mount, got: %v", control)
	}
	if !strings.Contains(joined, "--entrypoint driver-exec") ||
		!strings.Contains(joined, "probe-registry-socket -path "+RegistryProxySocketTarget) {
		t.Errorf("control probe must still run the socket probe verb, got: %v", control)
	}
}

// The other no-verdict socket outcome (a bare exit 0) confirmed incapable by
// the control probe, alongside the exit-125 case above.
func TestRegistryProxyTransport_SocketZeroExit_ControlIncapable_ReportsTCP(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 0},
		fakeCall{exit: registryprobe.ExitIncapable},
		fakeCall{exit: registryprobe.ExitCapable},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error when the control probe confirms incapable, got %v", err)
	}
	if !endpoint.IsTCP() {
		t.Fatalf("RegistryProxyTransport: want a TCP endpoint, got %+v", endpoint)
	}
	if got := callCount(t, dir); got != 3 {
		t.Fatalf("callCount = %d, want 3 (socket probe + control probe + tcp-reachability sub-probe)", got)
	}
}

// When the control probe, run without the socket mount, also produces no
// verdict, this is a genuine infrastructure failure: the hard error names both
// exit codes and keeps the "possible launcher/image version mismatch" hint,
// unlike the control-confirmed-91 case above, which must carry no such hint.
func TestRegistryProxyTransport_SocketAndControlBothNoVerdict_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 125}, fakeCall{exit: 126})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when both socket and control probes produce no verdict")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint")
	}
	if !strings.Contains(err.Error(), "125") || !strings.Contains(err.Error(), "126") {
		t.Errorf("RegistryProxyTransport: error %q should name both the socket (125) and control (126) exit codes", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("RegistryProxyTransport: error %q should name a launcher/image version mismatch", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2: a no-verdict-from-both outcome must never launch the tcp-reachability sub-probe", got)
	}
}

// The implausible-but-guarded case: a socket no-verdict result followed by the
// control probe (nothing mounted) reporting ExitCapable. The error must name
// that impossibility and must NOT carry socketErr's "version mismatch" hint,
// which belongs only to the both-no-verdict case, not to a path where the
// control probe did produce a verdict.
func TestRegistryProxyTransport_ControlReportsCapable_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 125}, fakeCall{exit: registryprobe.ExitCapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when the control probe unexpectedly reports capable")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint")
	}
	if !strings.Contains(err.Error(), "impossible") {
		t.Errorf("RegistryProxyTransport: error %q should say the control-confirmed capable verdict is impossible", err)
	}
	if strings.Contains(err.Error(), "version") {
		t.Errorf("RegistryProxyTransport: error %q should not carry the version-mismatch hint -- the control probe DID produce a verdict here", err)
	}
	if got := callCount(t, dir); got != 2 {
		t.Errorf("callCount = %d, want 2: the tcp-reachability sub-probe must never run when the control probe reports capable", got)
	}
}

// The other half of issue #3466's deny-host-loopback AC: a control-confirmed
// incapable verdict (socket no-verdict, control ExitIncapable) must hit the same
// deniesHostLoopback hard-error a direct ExitIncapable does, not fall through to
// TCP because the verdict came from the control probe. Scripts two calls and
// asserts callCount stays at 2: the TCP sub-probe must never run after that.
func TestRegistryProxyTransport_NoHostLoopback_ControlConfirmedIncapable_ReturnsError(t *testing.T) {
	for _, mode := range []string{NetworkModeNoHostLoopback, NetworkModeNone} {
		t.Run(mode, func(t *testing.T) {
			script, dir := newFakeCLI(t, fakeCall{exit: 125}, fakeCall{exit: registryprobe.ExitIncapable})
			a := &ociAdapter{cli: script, image: "spindrift:test", networkMode: mode}

			endpoint, _, err := a.RegistryProxyTransport()
			if err == nil {
				t.Fatalf("RegistryProxyTransport: want error for networkMode=%q + control-confirmed incapable, got endpoint=%+v", mode, endpoint)
			}
			if endpoint.IsUnix() {
				t.Error("RegistryProxyTransport: want a non-unix endpoint")
			}
			if !strings.Contains(err.Error(), a.cli) {
				t.Errorf("RegistryProxyTransport: error %q should name the CLI %q", err, a.cli)
			}
			if !strings.Contains(err.Error(), mode) {
				t.Errorf("RegistryProxyTransport: error %q should name the configured NETWORK_MODE %q", err, mode)
			}
			if got := callCount(t, dir); got != 2 {
				t.Errorf("callCount = %d, want 2 (socket probe + control probe): the tcp-reachability sub-probe must never run when deniesHostLoopback already hard-errored", got)
			}
		})
	}
}

// Issue #3466's cache AC: a control-probe-confirmed TCP decision (socket
// no-verdict, control ExitIncapable, tcp sub-probe ExitCapable) is cached
// exactly like a direct-91 TCP decision. A second adapter sharing pwd replays
// the endpoint and addHost without starting any of the three probe containers.
func TestRegistryProxyTransport_CachesControlConfirmedTCPVerdict(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 125},
		fakeCall{exit: registryprobe.ExitIncapable},
		fakeCall{exit: registryprobe.ExitCapable},
	)
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}

	endpoint, addHost, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (first call): %v", err)
	}
	if !endpoint.IsTCP() {
		t.Fatalf("RegistryProxyTransport (first call): want a TCP endpoint, got %+v", endpoint)
	}
	if got := callCount(t, dir); got != 3 {
		t.Fatalf("callCount after first call = %d, want 3 (socket probe + control probe + tcp sub-probe)", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	endpoint2, addHost2, err := b.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport (second call): %v", err)
	}
	if endpoint2.Host() != endpoint.Host() || !endpoint2.IsTCP() {
		t.Errorf("RegistryProxyTransport (second call): endpoint = %+v, want the cached %+v replayed", endpoint2, endpoint)
	}
	if addHost2 != addHost {
		t.Errorf("RegistryProxyTransport (second call): addHost = %v, want the cached %v replayed", addHost2, addHost)
	}
	if got := callCount(t, dir); got != 3 {
		t.Errorf("callCount after second call = %d, want still 3: a control-probe-confirmed cache hit must start no container", got)
	}
}

// A both-no-verdict probe error (socket 125, control 126) is never cached, per
// the RegistryProxyTransport doc comment's "a probe error is never cached"
// contract, so a second call under the same pwd re-probes from scratch.
func TestRegistryProxyTransport_BothNoVerdict_NeverCached(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 125}, fakeCall{exit: 126})
	pwd := t.TempDir()
	a := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}

	if _, _, err := a.RegistryProxyTransport(); err == nil {
		t.Fatal("RegistryProxyTransport (first call): want error when both socket and control probes produce no verdict")
	}
	if got := callCount(t, dir); got != 2 {
		t.Fatalf("callCount after first call = %d, want 2", got)
	}

	b := &ociAdapter{cli: script, image: "spindrift:test", pwd: pwd}
	if _, _, err := b.RegistryProxyTransport(); err == nil {
		t.Fatal("RegistryProxyTransport (second call): want error again -- a probe error must never be cached")
	}
	if got := callCount(t, dir); got != 4 {
		t.Errorf("callCount after second call = %d, want 4: an uncached error must re-run both probes on the next call", got)
	}
}

// A wedged probe container (never exits) must return a Go error naming the
// timeout rather than hanging dispatch (issue #3111 finding 1).
// registryProxyProbeTimeout is overridden to a short duration, following the
// execCommand-seam override idiom used elsewhere here. A socket-probe timeout
// is a no-verdict outcome, so the control probe wedges too: callCount is 2.
func TestRegistryProxyTransport_ProbeTimesOut_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-cli")
	callsFile := filepath.Join(dir, "calls.txt")
	// The echo records the call before exec replaces this shell with sleep
	// itself, so killing the *exec.Cmd's PID on timeout kills the sleeper
	// immediately instead of leaving an orphaned child holding the
	// CombinedOutput pipe open until it finishes on its own.
	if err := os.WriteFile(script, []byte(fmt.Sprintf(
		"#!/bin/sh\necho ok >> %q\nexec sleep 5\n", callsFile,
	)), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := registryProxyProbeTimeout
	// The budget must outlast the fake CLI's fork/exec, not merely be short:
	// a deadline that fires before the shell reaches its echo destroys the
	// call record counted below. 20ms lost a probe that way under CI load.
	registryProxyProbeTimeout = 250 * time.Millisecond
	t.Cleanup(func() { registryProxyProbeTimeout = orig })

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	endpoint, _, err := a.RegistryProxyTransport()
	if err == nil {
		t.Fatal("RegistryProxyTransport: want error when the probe times out")
	}
	if endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a non-unix endpoint when the probe times out")
	}
	if !strings.Contains(err.Error(), registryProxyProbeTimeout.String()) {
		t.Errorf("RegistryProxyTransport: error %q should mention the timeout duration %s", err, registryProxyProbeTimeout)
	}
	raw, readErr := os.ReadFile(callsFile)
	if readErr != nil {
		t.Fatalf("calls.txt not written: %v", readErr)
	}
	if got := strings.Count(string(raw), "ok\n"); got != 2 {
		t.Errorf("call count = %d, want 2: a socket-probe timeout must run the control probe too", got)
	}
}

// Calls probeRegistryTCPReachable directly, not through
// RegistryProxyTransport's two-probe chain, so a wedged tcp-reachability
// sub-probe is exercised in isolation, mirroring
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
	_, err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error when the sub-probe times out")
	}
	if !strings.Contains(err.Error(), registryProxyProbeTimeout.String()) {
		t.Errorf("probeRegistryTCPReachable: error %q should mention the timeout duration %s", err, registryProxyProbeTimeout)
	}
}

// A docker/podman "daemon error" exit (125) from the tcp-reachability sub-probe
// must return a Go error, not the clean "not reachable" verdict: only
// registryprobe.ExitIncapable is probe-registry-tcp's documented answer. The
// no-verdict outcome also short-circuits (callCount stays at 1); there is no
// reason to try the --add-host wiring when the route was never tested.
func TestProbeRegistryTCPReachable_NonOneExitCode_ReturnsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 125, stdout: "boom"})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	_, err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error on scripted exit 125, got nil")
	}
	if !strings.Contains(err.Error(), "125") {
		t.Errorf("probeRegistryTCPReachable: error %q should mention the exit code 125", err)
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1: a no-verdict outcome must short-circuit rather than also try the --add-host wiring", got)
	}
}

// Pins issue #3120's fix to the TCP sub-probe: an old driver-exec exits 1 on an
// unrelated flag-parsing path, not because the --add-host route was tested and
// found unreachable. That used to read as "not reachable", so the code tried the
// --add-host wiring and summarised both as unreachable, a claim about a route
// never probed. The no-verdict error returns straight out, callCount stays at 1.
func TestProbeRegistryTCPReachable_OldImageDrift_ShortCircuits(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	_, err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error on scripted exit 1, got nil")
	}
	if strings.Contains(err.Error(), "unreachable from the guest both with and without") {
		t.Errorf("probeRegistryTCPReachable: error %q must not summarise as unreachable both ways -- the route was never tested", err)
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1: a no-verdict exit must short-circuit before trying the --add-host wiring", got)
	}
}

// A genuine inability to even start the runtime (CLI missing) must return an
// error, mirroring TestRegistryProxyTransport_ExecFailure_ReturnsError's
// coverage of the first-stage probe's identical non-*exec.ExitError branch.
func TestProbeRegistryTCPReachable_ExecFailure_ReturnsError(t *testing.T) {
	a := &ociAdapter{cli: filepath.Join(t.TempDir(), "nonexistent-cli-binary"), image: "spindrift:test"}

	_, err := a.probeRegistryTCPReachable("host.docker.internal")
	if err == nil {
		t.Fatal("probeRegistryTCPReachable: want error when the runtime CLI itself cannot be started")
	}
}

// Pins issue #3120's fix to probeRegistryTCPOnce's pre-flight listener bind: no
// container dials anything if the bind never succeeds, so that failure must wrap
// errProbeNoVerdict like every other untested-route outcome rather than falling
// through toward the "not reachable" verdict.
func TestProbeRegistryTCPOnce_ListenFails_NoVerdict(t *testing.T) {
	orig := listenTCPProbe
	listenTCPProbe = func() (net.Listener, error) {
		return nil, errors.New("forced bind failure")
	}
	t.Cleanup(func() { listenTCPProbe = orig })

	a := &ociAdapter{cli: "unused", image: "spindrift:test"}
	err := a.probeRegistryTCPOnce("host.docker.internal", false)
	if err == nil {
		t.Fatal("probeRegistryTCPOnce: want error when the listener cannot bind, got nil")
	}
	if !errors.Is(err, errProbeNoVerdict) {
		t.Errorf("probeRegistryTCPOnce: error %q must wrap errProbeNoVerdict when the pre-flight listen fails", err)
	}
}

// Pins issue #3077's acceptance criterion for this probe too: a $TMPDIR long
// enough that an os.TempDir()-based candidate would overflow AF_UNIX's sun_path
// limit once "spindrift-registry-probe-*/probe.sock" is appended must not error
// out. probeSocketDir falls back to /tmp exactly as
// dispatch.registryProxySocketDir already does for the real proxy socket.
func TestRegistryProxyTransport_LongTMPDIR_StillWorks(t *testing.T) {
	longBase := filepath.Join(t.TempDir(), strings.Repeat("x", 200))
	if err := os.MkdirAll(longBase, 0o755); err != nil {
		t.Fatalf("MkdirAll long TMPDIR base: %v", err)
	}
	t.Setenv("TMPDIR", longBase)

	script, _ := newFakeCLI(t, fakeCall{exit: registryprobe.ExitCapable})
	a := &ociAdapter{cli: script, image: "spindrift:test"}

	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: want nil error under a long TMPDIR, got %v", err)
	}
	if !endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a unix endpoint on scripted capable exit")
	}
}

// The writable cache mount, scoped to /home/agent/.claude/projects, must not
// shadow /home/agent/.claude/skills baked into the image, the regression a mount
// at the parent /home/agent/.claude causes. OCI has no host-side path to
// re-mount baked skills over, unlike bwrap's agentFiles fallback.
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

// The box-side session-cache mount target comes from the adapter's
// driverSessionCacheDir field (populated by the Driver declaration, ADR 0009)
// rather than a hardcoded ".claude/projects" literal.
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

// A Driver declaring no session-state dir yields no cache mount even when a
// host DriverCacheDir is present: there is no in-box target to mount it over
// (issue #448).
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

// Run detects a same-named container already in the "running" state and returns
// ErrAlreadyRunning without ever invoking `podman/docker run`: the collision
// must not be attempted, only recognized (issue #562).
func TestRun_AlreadyRunningContainerSkipsLaunch(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "cid-1", "running", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Run: want 1 call (inspect only), got %d", calls)
	}
}

// The non-collision case: a genuinely stale (exited, not merely created) same-
// named container is reaped with `rm -f`, and the launch proceeds normally
// (issue #562 acceptance criterion 3).
func TestRun_ExitedContainerReapedThenLaunches(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "stale-id", "exited", time.Now())},
		fakeCall{},        // rm
		fakeCall{},        // run
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "stale-id") {
		t.Errorf("Run: did not reap the stale exited container: %v", rm)
	}
	run := readCall(t, dir, 2)
	if !containsArg(run, "run") {
		t.Errorf("Run: did not launch after reaping the stale container, call 2 was %v", run)
	}
}

// Run must target the ID a single inspect observed, not the box name, when
// reaping a stale container — the ID-pinning invariant inspectContainer's
// doc comment explains (issue #3633 acceptance criterion).
func TestRun_ReapsByIDNotByName(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "stale-id-42", "exited", time.Now())},
		fakeCall{},        // rm
		fakeCall{},        // run
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "stale-id-42") {
		t.Errorf("rm target: got %v, want the inspected ID stale-id-42", rm)
	}
	if containsArg(rm, box.Name) {
		t.Errorf("rm target: got %v, want it not to target the name %q", rm, box.Name)
	}
}

// The headline regression: a sibling's container still being created (state
// "created", not yet "running") must not be force-removed while it is still
// within midCreationGrace of its creation timestamp. The guard keyed on the
// literal "running" status before issue #3633, so a sibling mid-creation was
// invisible to it, silently destroyed, and this launcher's own `run` collided
// on the name anyway.
func TestRun_RecentlyCreatedContainerSkipsLaunchWithoutReap(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "cid-created", "created", time.Now().Add(-5*time.Second))},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning for a container still being created, got %v", err)
	}
	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Run: want 1 call (inspect only, no rm or run), got %d", calls)
	}
}

// A "created" container older than midCreationGrace was abandoned by a
// launcher that died between `podman run`'s create and start, not owned by a
// sibling about to start it. Pre-diff, an unconditional `rm -f <name>`
// cleared exactly this; the fix must still clear it, or the issue becomes
// permanently undispatchable (blocking finding, issue #3633).
func TestRun_StaleCreatedContainerReapedThenLaunches(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "stale-created-id", "created", time.Now().Add(-3*time.Hour))},
		fakeCall{},        // rm
		fakeCall{},        // run
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "stale-created-id") {
		t.Errorf("Run: want `rm -f stale-created-id`, got %v", rm)
	}
	run := readCall(t, dir, 2)
	if !containsArg(run, "run") {
		t.Errorf("Run: did not launch after reaping the stale created container, call 2 was %v", run)
	}
}

// An inspect that exits 0 but whose Created field cannot parse must still be
// treated as "container exists": a successful inspect proves that, regardless
// of whether every field parsed. But with no usable created timestamp,
// reapable's age check treats the age as infinite and reaps — restoring the
// pre-#3633 behaviour of clearing a container this launcher cannot classify,
// rather than wedging the issue permanently undispatchable (issue #3633).
func TestRun_UnparseableInspectOutputReapedThenLaunches(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: `{"Id":"cid-only","Created":"not-a-timestamp","State":{"Status":"created"}}`},
		fakeCall{},        // rm
		fakeCall{},        // run
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "cid-only") {
		t.Errorf("Run: want the container reaped by its ID, got rm call %v", rm)
	}
	run := readCall(t, dir, 2)
	if !containsArg(run, "run") {
		t.Errorf("Run: did not launch after reaping the unparseable-timestamp container, call 2 was %v", run)
	}
}

// A body that does not decode at all leaves no ID to remove. Removing by name
// instead would race a sibling launcher's in-flight create, so Run removes
// nothing and launches; if the name really was taken, the runtime's own
// refusal — confirmed by a fresh post-failure inspect finding the name still
// live — surfaces as ErrAlreadyRunning, so the issue is skipped rather than
// failed (issue #3633).
func TestRun_UndecodableInspectBodyRemovesNothing(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: "not json at all"},
		fakeCall{
			stderr: `Error: creating container storage: the container name "agent-issue-1" is already in use by abc123`,
			exit:   125,
		},
		fakeCall{stdout: inspectJSONStdout(t, "abc123", "running", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if call := readCall(t, dir, 1); containsArg(call, "rm") {
		t.Errorf("Run: removed a container it could not identify, call 1 was %v", call)
	}
}

// No container at all: Run launches straight through, no rm invoked.
func TestRun_NoContainerLaunchesDirectly(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: absent
		fakeCall{},        // run
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run := readCall(t, dir, 1)
	if !containsArg(run, "run") {
		t.Errorf("Run: want the second call to launch, call 1 was %v", run)
	}
	if calls := callCount(t, dir); calls != 3 {
		t.Errorf("Run: want 3 calls (inspect, run, reapAfterSuccess re-inspect; no rm), got %d", calls)
	}
}

// A non-zero exit from the scripted `podman/docker run` invocation must return
// a *RunError carrying that exit code, so later slices can detect signal-kill
// exit codes (128+N) through a runtime-agnostic type instead of a raw
// *exec.ExitError.
func TestRun_ExitCodeSurfacedAsRunError(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "cid-1", "exited", time.Now())},
		fakeCall{},          // rm
		fakeCall{exit: 143}, // run
	)
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

// When inspect can't see the container (transient daemon blip, or a sibling
// launcher's create is mid-flight and not yet visible) but the runtime still
// refuses to create it because the name is taken, that refusal is itself the
// concurrency signal: Run must report ErrAlreadyRunning, not a RunError, and
// must never rm -f the name (issue #3633).
func TestRun_LostCreateRacePodmanRefusalIsAlreadyRunning(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: container invisible to us
		fakeCall{exit: 125, stderr: `Error: creating container storage: the container name "agent-issue-1" is already in use by 0123456789ab`},
		// post-failure inspect: the sibling that actually won the race is live
		fakeCall{stdout: inspectJSONStdout(t, "0123456789ab", "running", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if calls := callCount(t, dir); calls != 3 {
		t.Errorf("Run: want 3 calls (inspect, run, post-failure inspect), got %d", calls)
	}
	for i := 0; i < callCount(t, dir); i++ {
		if argv := readCall(t, dir, i); len(argv) > 0 && argv[0] == "rm" {
			t.Errorf("Run: call %d issued rm -f on a name-collision refusal: %v", i, argv)
		}
	}
}

// Same as above but with docker's wording of the same refusal.
func TestRun_LostCreateRaceDockerRefusalIsAlreadyRunning(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: container invisible to us
		fakeCall{exit: 125, stderr: `docker: Error response from daemon: Conflict. The container name "/agent-issue-1" is already in use by container "0123456789ab". You have to remove (or rename) that container to be able to reuse that name.`},
		// post-failure inspect: the sibling that actually won the race is live
		fakeCall{stdout: inspectJSONStdout(t, "0123456789ab", "running", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
}

// The genuine-failure regression the blocking finding calls out: a Box that
// starts, echoes attacker-controlled text quoting the runtime's own
// "is already in use" refusal, and then exits non-zero must not be
// reclassified as ErrAlreadyRunning on text alone. The post-failure inspect
// proves this Box's own container ended up exited (reapable), not held live
// by a sibling, so the collision text is exposed as forged and Run must
// still surface a RunError.
func TestRun_EchoedCollisionTextWithGenuineFailureIsRunError(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: no pre-existing container
		fakeCall{
			exit:   7,
			stdout: `the container name "agent-issue-1" is already in use`,
		},
		// post-failure inspect: this Box's own container, now exited
		fakeCall{stdout: inspectJSONStdout(t, "own-id", "exited", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: attacker-echoed collision text steered classification to ErrAlreadyRunning, want RunError")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run: want *RunError, got %v (%T)", err, err)
	}
	if calls := callCount(t, dir); calls != 3 {
		t.Errorf("Run: want 3 calls (inspect, run, post-failure inspect), got %d", calls)
	}
}

// The real lost-create-race, restated with the post-failure inspect proof
// this issue adds: the pre-run inspect misses the container, the run itself
// is refused by name, and the post-failure inspect finds it freshly
// "created" (not yet "running") — still a live claim on the name, so Run
// must report ErrAlreadyRunning.
func TestRun_LostCreateRacePostFailureInspectSeesCreated(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: container invisible to us
		fakeCall{exit: 125, stderr: `Error: creating container storage: the container name "agent-issue-1" is already in use by 0123456789ab`},
		fakeCall{stdout: inspectJSONStdout(t, "0123456789ab", "created", time.Now())},
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if calls := callCount(t, dir); calls != 3 {
		t.Errorf("Run: want 3 calls (inspect, run, post-failure inspect), got %d", calls)
	}
}

// The tee must forward the Box's run output to box.Output unchanged, even
// past the retained head's bound, so callers relying on the full log
// (waves/engine.go) are unaffected by the ErrAlreadyRunning detection added
// for issue #3633.
func TestRun_TeesFullOutputToBoxOutput(t *testing.T) {
	big := strings.Repeat("x", 8192) + "-tail-marker"
	script, _ := newFakeCLI(t,
		fakeCall{exit: 1}, // inspect: container absent
		fakeCall{exit: 0, stdout: big},
		fakeCall{exit: 1}, // Run's own reapAfterSuccess re-inspects; report absent
	)
	a := &ociAdapter{cli: script, image: "spindrift:test"}
	var buf bytes.Buffer
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, Output: &buf}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got := buf.String(); got != big {
		t.Errorf("Run: box.Output mismatch: want %d bytes ending %q, got %d bytes ending %q",
			len(big), big[len(big)-16:], len(got), got[max(0, len(got)-16):])
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

// The safety guard: when the fake CLI reports the container is running, Reap
// must not issue `rm -f`.
func TestReap_NeverRemovesRunningContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "cid-1", "running", time.Now())},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Reap: want 1 call (inspect only), got %d", calls)
	}
}

// The mid-creation case (issue #3633): a container still in the "created"
// state, not yet running, and still within midCreationGrace of its creation
// timestamp, must also be left alone — a sibling launcher may own it.
func TestReap_NeverRemovesCreatedContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "cid-1", "created", time.Now().Add(-5*time.Second))},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Reap: want 1 call (inspect only), got %d", calls)
	}
}

// The other side of the guard: when the fake CLI reports the container is
// exited (reapable), Reap issues `rm -f` against the inspected ID, not the
// name passed in (issue #3633). A terminal status reaps regardless of age.
func TestReap_RemovesStaleContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "stale-cid", "exited", time.Now())},
		fakeCall{},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "stale-cid") {
		t.Errorf("Reap: want `rm -f stale-cid`, got %v", rm)
	}
	if containsArg(rm, "agent-issue-1") {
		t.Errorf("Reap: rm targeted the name, not the inspected ID: %v", rm)
	}
}

// A "created" container older than midCreationGrace was abandoned mid-create,
// not owned by a sibling about to start it, so Reap clears it just as it does
// a terminal container (issue #3633).
func TestReap_RemovesStaleCreatedContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "stale-created-cid", "created", time.Now().Add(-3*time.Hour))},
		fakeCall{},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "stale-created-cid") {
		t.Errorf("Reap: want `rm -f stale-created-cid`, got %v", rm)
	}
}

// The common settle-phase case (CI watch, merge gate): the initial Box already
// exited successfully and Run's own reapAfterSuccess already removed it, so
// there is no container left to kill. Runner.Kill's contract treats that as
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

// Kill's contract is the opposite of Reap's for a container that does exist: it
// issues `rm -f` unconditionally once existence is confirmed, so it reaches a
// genuinely live container Reap would refuse to touch.
func TestKill_RemovesExistingContainerRegardlessOfRunningState(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "kill-existing-id", "running", time.Now())},
		fakeCall{}, // rm -f
	)
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if calls := callCount(t, dir); calls != 2 {
		t.Errorf("Kill: want 2 calls (inspect then rm -f), got %d", calls)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "kill-existing-id") {
		t.Errorf("Kill: want `rm -f kill-existing-id`, got %v", rm)
	}
}

// Kill must target the ID inspectContainer observed, not the name passed in
// — the same ID-pinning invariant Run and Reap apply — so a sibling that
// replaced the container between inspect and rm is untouched (issue #3633).
func TestKill_RemovesByIDNotByName(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "kill-target-id", "running", time.Now())},
		fakeCall{}, // rm -f
	)
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "kill-target-id") {
		t.Errorf("rm target: got %v, want the inspected ID kill-target-id", rm)
	}
	if containsArg(rm, "agent-issue-1") {
		t.Errorf("rm target: got %v, want it not to target the name %q", rm, "agent-issue-1")
	}
}

// A scripted rm failure against a container confirmed to exist is returned, not
// swallowed: Terminate needs to know a genuine reap failure happened.
func TestKill_RemovalFailureOnExistingContainer_ReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{stdout: inspectJSONStdout(t, "kill-failure-id", "running", time.Now())},
		fakeCall{exit: 1}, // rm -f: fails
	)
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err == nil {
		t.Error("Kill: want error from scripted rm failure, got nil")
	}
}

// loadImage issues `load -i <archive>` followed by `tag spindrift:latest
// <imageTag>`, in that order.
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

// loadImage re-tags from "<repo>:latest" where repo is derived from the
// adapter's own imageTag, not a hardcoded "spindrift:latest", so a
// driver-scoped archive (an opencode image loads as
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

func TestIsReady_ImageAbsentReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err == nil {
		t.Error("IsReady: want error when image absent, got nil")
	}
}

func TestIsReady_ImagePresentReturnsNil(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady: want nil when image present, got %v", err)
	}
}

// IsRunning reports true for any live (non-reapable) status, not just the
// exact "running" string, and false for a reapable status or a failed
// inspect (issue #3633).
func TestIsRunning_ScriptedStatuses(t *testing.T) {
	t.Run("running reports true regardless of age", func(t *testing.T) {
		script, _ := newFakeCLI(t, fakeCall{stdout: inspectJSONStdout(t, "cid", "running", time.Now().Add(-3*time.Hour))})
		a := &ociAdapter{cli: script}
		if !a.IsRunning("c") {
			t.Error(`IsRunning: want true for "running" status`)
		}
	})

	t.Run("recently created reports true (mid-creation sibling)", func(t *testing.T) {
		script, _ := newFakeCLI(t, fakeCall{stdout: inspectJSONStdout(t, "cid", "created", time.Now().Add(-5*time.Second))})
		a := &ociAdapter{cli: script}
		if !a.IsRunning("c") {
			t.Error(`IsRunning: want true for a recently "created" status, issue #3633`)
		}
	})

	t.Run("stale created reports false (abandoned mid-create)", func(t *testing.T) {
		script, _ := newFakeCLI(t, fakeCall{stdout: inspectJSONStdout(t, "cid", "created", time.Now().Add(-3*time.Hour))})
		a := &ociAdapter{cli: script}
		if a.IsRunning("c") {
			t.Error(`IsRunning: want false for a "created" status older than midCreationGrace`)
		}
	})

	t.Run("exited reports false", func(t *testing.T) {
		script, _ := newFakeCLI(t, fakeCall{stdout: inspectJSONStdout(t, "cid", "exited", time.Now())})
		a := &ociAdapter{cli: script}
		if a.IsRunning("c") {
			t.Error(`IsRunning: want false for "exited" status`)
		}
	})

	t.Run("failed inspect reports false", func(t *testing.T) {
		script, _ := newFakeCLI(t, fakeCall{exit: 1})
		a := &ociAdapter{cli: script}
		if a.IsRunning("c") {
			t.Error("IsRunning: want false when inspect fails (exit 1)")
		}
	})
}

// container.reapable allowlists exited/stopped/dead as safe to rm -f
// regardless of age; "running" is never safe; every other status —
// recognised transient states and anything unrecognised — is safe only once
// it has outlived midCreationGrace, so a sibling launcher's container that is
// merely slow to start fails safe toward "do not destroy" while a genuinely
// abandoned one is still cleared (issue #3633).
func TestContainerReapable(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		status  string
		created time.Time
		want    bool
	}{
		{"exited, just created", "exited", now, true},
		{"exited, hours old", "exited", now.Add(-3 * time.Hour), true},
		{"stopped, just created", "stopped", now, true},
		{"dead, just created", "dead", now, true},
		{"running, just created", "running", now, false},
		{"running, hours old", "running", now.Add(-3 * time.Hour), false},
		{"created, within grace", "created", now.Add(-5 * time.Second), false},
		{"created, past grace", "created", now.Add(-3 * time.Hour), true},
		{"configuring, within grace", "configuring", now.Add(-5 * time.Second), false},
		{"configuring, past grace", "configuring", now.Add(-3 * time.Hour), true},
		{"unrecognised status, within grace", "some-unrecognised-future-status", now.Add(-5 * time.Second), false},
		{"unrecognised status, past grace", "some-unrecognised-future-status", now.Add(-3 * time.Hour), true},
		{"zero created timestamp reaps", "created", time.Time{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := container{status: c.status, created: c.created}.reapable(now)
			if got != c.want {
				t.Errorf("container{status: %q, created: %v}.reapable(now) = %v, want %v", c.status, c.created, got, c.want)
			}
		})
	}
}

// The image-absent branch: EnsureReady tries a host build first, and when that
// fails with a builder-missing error, falls back to buildInContainer, which
// emits the non-digest-pinned supply-chain warning for an unpinned builder
// image.
func TestEnsureReady_ImageAbsentFallsBackToContainerBuild(t *testing.T) {
	redirectImageLockDir(t)
	cliScript, _ := newFakeCLI(t,
		fakeCall{exit: 1}, // image inspect: absent (outside the lock)
		fakeCall{exit: 1}, // image inspect: still absent (re-probe under the lock)
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
		nixBuilderImage: "docker.io/nixos/nix:latest", // unpinned, so the warning fires
		nixVolume:       "spindrift-nix",
		pwd:             "/work",
		flakeImageAttr:  ".#packages.aarch64-linux.agent-image",
	}

	// The supply-chain warning goes straight to stderr, so capture it there.
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

// The host `nix build` step in EnsureReady goes through the execCommand seam,
// and a genuine (non-builder-missing) scripted failure returns an error without
// falling back to the container build.
func TestEnsureReady_HostNixBuildInvokedViaSeam(t *testing.T) {
	redirectImageLockDir(t)
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
