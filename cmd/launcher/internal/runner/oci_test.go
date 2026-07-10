package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureReady_ImagePresentPrintsMessage verifies that EnsureReady emits
// the "image present — no rebuild needed" line when the image is already loaded,
// so every loop iteration records whether a rebuild was required.
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
	if !strings.Contains(out, "present") {
		t.Errorf("expected 'present' in EnsureReady output when image loaded; got: %q", out)
	}
	if !strings.Contains(out, "no rebuild needed") {
		t.Errorf("expected 'no rebuild needed' in EnsureReady output; got: %q", out)
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
		cli:             "podman",
		image:           "spindrift:test",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/.claude/skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_SkillsMountTarget_FromDriverDeclaration verifies the
// box-side skills mount target comes from the adapter's driverSkillsDir
// field (populated by the Driver declaration, ADR 0009) rather than a
// hardcoded ".claude/skills" literal.
func TestBuildRunArgs_SkillsMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:             "podman",
		image:           "spindrift:test",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/custom-driver/skills",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/custom-driver/skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
	}
}

func TestBuildRunArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
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

// TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive verifies that the
// writable cache mount, scoped to /home/agent/.claude/projects, does not
// shadow /home/agent/.claude/skills baked into the image — the regression a
// mount at the parent /home/agent/.claude would cause (OCI has no host-side
// path to re-mount baked skills over, unlike bwrap's agentFiles fallback).
func TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
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
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
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
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/custom-driver/state",
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
		cli:       "podman",
		image:     "spindrift:test",
		skillsDir: "",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ".claude/skills") {
			t.Errorf("unexpected skills mount in args when skillsDir is empty: %v", args)
		}
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
