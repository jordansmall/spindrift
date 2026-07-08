package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		cli:       "podman",
		image:     "spindrift:test",
		skillsDir: dir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/.claude/skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
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
