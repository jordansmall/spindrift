package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ValidateRuntime checks that runtime ("podman", "docker", "rancher", or
// "bwrap") names a binary available on PATH. "rancher" maps to "nerdctl" via
// BinaryFor.
func ValidateRuntime(runtime string) error {
	return ValidateRuntimeWithLookup(runtime, exec.LookPath)
}

// ValidateRuntimeWithLookup is ValidateRuntime with the PATH lookup
// injected, so callers with their own lookPath abstraction (e.g.
// quickstart's Environment.LookPath) reuse this validation and its exact
// message text rather than hand-rolling a check.
func ValidateRuntimeWithLookup(runtime string, lookPath func(string) (string, error)) error {
	if runtime == "" {
		return fmt.Errorf("RUNTIME is not set")
	}
	cli := BinaryFor(runtime)
	if _, err := lookPath(cli); err != nil {
		if runtime == "rancher" {
			return fmt.Errorf("nerdctl not found on PATH — is Rancher Desktop running in containerd mode?")
		}
		return fmt.Errorf("%s not found on PATH", cli)
	}
	return nil
}

// ValidatePasta checks that pasta is available on PATH — required whenever a
// bwrap Box isolates its network namespace with working egress. Without it
// the launcher must refuse to start rather than silently fall back to a
// weaker, shared-host-netns sandbox.
func ValidatePasta() error {
	return ValidatePastaWithLookup(exec.LookPath)
}

// ValidatePastaWithLookup is ValidatePasta with the PATH lookup injected,
// same shape as ValidateRuntimeWithLookup.
func ValidatePastaWithLookup(lookPath func(string) (string, error)) error {
	if _, err := lookPath("pasta"); err != nil {
		return fmt.Errorf("pasta not found on PATH — required to give a bwrap Box its own network namespace with the host loopback blocked (issue #2666); install pasta (the passt project) on PATH, or set NETWORK_MODE=host to explicitly opt into the pre-#2666 shared-network-namespace behaviour")
	}
	return nil
}

// ValidateOverlay checks that the host kernel/config allows an unprivileged
// user namespace to mount an overlayfs — required whenever a bwrap Box's
// in-box /nix/store is made writable via an ephemeral tmpfs overlay (ADR
// 0042). Without it, bwrap's --overlay-src/--tmp-overlay mount fails deep
// inside sandbox startup, so the launcher refuses to start instead.
func ValidateOverlay() error {
	return ValidateOverlayWithExec(execCommand)
}

// ValidateOverlayWithExec is ValidateOverlay with the exec seam injected.
// Unlike a PATH-existence check, overlayfs-in-userns support only answers to
// a functional probe, so this runs a real bwrap invocation that overlays a
// throwaway temp dir over itself and reports whether the mount succeeds.
func ValidateOverlayWithExec(run func(string, ...string) *exec.Cmd) error {
	dir, err := os.MkdirTemp("", "spindrift-overlay-probe-")
	if err != nil {
		return fmt.Errorf("nixStoreWritable overlay smoke test: failed to create a temp dir to probe with: %w", err)
	}
	defer os.RemoveAll(dir)

	// --ro-bind / / gives the sandboxed root a real filesystem to resolve
	// "true" from: an empty sandbox root fails to exec anything whether or
	// not the overlay mount succeeded, i.e. a false failure.
	cmd := run("bwrap", "--unshare-user", "--uid", "1000", "--gid", "1000", "--ro-bind", "/", "/", "--overlay-src", dir, "--tmp-overlay", dir, "--", "true")
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nixStoreWritable/NIX_STORE_WRITABLE is set but this host's kernel does not appear to allow an unprivileged user namespace to mount overlayfs (bwrap --overlay-src/--tmp-overlay smoke test failed: %w; output: %s) — either unset nixStoreWritable, or check kernel support for unprivileged user namespaces + overlayfs (e.g. the unprivileged_userns_clone sysctl on some distros)", err, stderr)
	}
	return nil
}

// statCgroupControllerFile is os.Stat, swappable in tests the same way
// readSelfCgroup/cgroupFSRoot are (bwrap.go): a temp-dir-backed test has no
// kernel to auto-populate pids.max/memory.max, so tests fake presence here.
var statCgroupControllerFile = os.Stat

// ValidateCgroupDelegation checks that the launcher's own cgroup v2 subtree
// is delegated (writable) to this process, AND that the pids/memory
// controllers are available in it — both required for bwrap to enforce
// PIDS_LIMIT/MEMORY_LIMIT under each Box's per-run cgroup (ADR 0042). It
// transiently mutates the cgroup filesystem: a throwaway subtree is created
// and removed, leaving nothing behind on success. It shares the
// readSelfCgroup/cgroupFSRoot seams with provisionCgroup (bwrap.go), so both
// probe the same parent subtree — but provisionCgroup writes
// pids.max/memory.max only when the corresponding limit is non-empty, so a
// controller reported present here may still be skipped there.
func ValidateCgroupDelegation() error {
	self, err := readSelfCgroup()
	if err != nil {
		return fmt.Errorf("cgroup v2 delegation cannot be determined (%w) — this host may be missing a unified cgroup v2 mount", err)
	}
	dir := filepath.Join(cgroupFSRoot, self, fmt.Sprintf("spindrift-doctor-probe-%d", os.Getpid()))
	mkErr := os.Mkdir(dir, 0o755)
	if errors.Is(mkErr, os.ErrExist) {
		// A doctor run killed between Mkdir and Remove (or a reused PID)
		// leaves this exact directory behind; clear and retry once rather
		// than misreporting a delegated host as non-delegated.
		if rmErr := os.Remove(dir); rmErr == nil {
			mkErr = os.Mkdir(dir, 0o755)
		}
	}
	if mkErr != nil {
		return fmt.Errorf("cgroup v2 subtree %s is not writable — this process's cgroup does not appear to be delegated to it (%w)", dir, mkErr)
	}
	for _, ctrlFile := range []string{"pids.max", "memory.max"} {
		if _, statErr := statCgroupControllerFile(filepath.Join(dir, ctrlFile)); statErr != nil {
			_ = os.Remove(dir)
			return fmt.Errorf("cgroup v2 subtree %s was created but is missing %s — this host's cgroup.subtree_control does not delegate that controller, so PIDS_LIMIT/MEMORY_LIMIT enforcement would silently fail even though subtree creation itself succeeded (%w)", dir, ctrlFile, statErr)
		}
	}
	// The capability question is already answered; cleanup is best-effort.
	_ = os.Remove(dir)
	return nil
}
