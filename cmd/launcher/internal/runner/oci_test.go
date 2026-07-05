package runner

import "testing"

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

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
