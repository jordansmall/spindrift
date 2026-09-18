package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ValidateRuntime checks that runtime ("podman", "docker", "rancher", or
// "bwrap") names a binary on PATH, guarding the same Config.Runtime field
// NewOCI and the adapter-selection switch consume.
func ValidateRuntime(runtime string) error {
	return ValidateRuntimeWithLookup(runtime, exec.LookPath)
}

// ValidateRuntimeWithLookup is ValidateRuntime with the PATH lookup injected,
// so callers with their own lookPath abstraction (quickstart's
// Environment.LookPath) reuse this logic and message text instead of
// hand-rolling a check that drifts from it (issue #2561).
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

// ValidatePasta checks that pasta is on PATH, required whenever a bwrap Box
// isolates its network namespace with working egress (issue #2666). Without
// it the launcher must refuse to start rather than fall back to the weaker
// shared-host-netns sandbox.
func ValidatePasta() error {
	return ValidatePastaWithLookup(exec.LookPath)
}

// ValidatePastaWithLookup is ValidatePasta with the PATH lookup injected.
func ValidatePastaWithLookup(lookPath func(string) (string, error)) error {
	if _, err := lookPath("pasta"); err != nil {
		return fmt.Errorf("pasta not found on PATH — required to give a bwrap Box its own network namespace with the host loopback blocked (issue #2666); install pasta (the passt project) on PATH, or set NETWORK_MODE=host to explicitly opt into the pre-#2666 shared-network-namespace behaviour")
	}
	return nil
}

// ValidateOverlay checks that the host kernel allows an unprivileged user
// namespace to mount an overlayfs, required whenever a bwrap Box's in-box
// /nix/store is made writable through an ephemeral tmpfs overlay (ADR 0042,
// issue #2665). Without the check, bwrap's --overlay-src/--tmp-overlay mount
// fails deep inside sandbox startup and the launcher cannot say why.
func ValidateOverlay() error {
	return ValidateOverlayWithExec(execCommand)
}

// ValidateOverlayWithExec is ValidateOverlay with the exec seam injected for
// tests. No PATH check can answer this one: overlayfs-in-userns support shows
// up only under a real bwrap invocation, so this probes with one.
func ValidateOverlayWithExec(run func(string, ...string) *exec.Cmd) error {
	dir, err := os.MkdirTemp("", "spindrift-overlay-probe-")
	if err != nil {
		return fmt.Errorf("nixStoreWritable overlay smoke test: failed to create a temp dir to probe with: %w", err)
	}
	defer os.RemoveAll(dir)

	// --ro-bind / / gives the sandboxed root a real filesystem to resolve
	// "true" from. Without it an empty sandbox root fails to exec anything
	// whether or not the overlay mounted, so the probe reports a false
	// failure.
	cmd := run("bwrap", "--unshare-user", "--uid", "1000", "--gid", "1000", "--ro-bind", "/", "/", "--overlay-src", dir, "--tmp-overlay", dir, "--", "true")
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nixStoreWritable/NIX_STORE_WRITABLE is set but this host's kernel does not appear to allow an unprivileged user namespace to mount overlayfs (bwrap --overlay-src/--tmp-overlay smoke test failed: %w; output: %s) — either unset nixStoreWritable, or check kernel support for unprivileged user namespaces + overlayfs (e.g. the unprivileged_userns_clone sysctl on some distros)", err, stderr)
	}
	return nil
}

// statCgroupControllerFile is os.Stat, swapped in tests: a temp-dir-backed
// test has no kernel to populate pids.max/memory.max the way a delegated
// cgroup v2 subtree would, so tests fake their presence through this seam.
var statCgroupControllerFile = os.Stat

// cgroupControlFiles maps a controller to the file provisionCgroup writes its
// limit to, so the probe below checks for exactly the file the runner needs.
var cgroupControlFiles = map[string]string{
	"pids":   "pids.max",
	"memory": "memory.max",
}

// ValidateCgroupDelegation checks that a cgroup v2 subtree is delegated to
// this process and that each controller in controllers is available in it,
// both required for bwrap to enforce PIDS_LIMIT/MEMORY_LIMIT (ADR 0042). It
// creates and removes a throwaway subtree, so it changes the cgroup
// filesystem transiently. An empty controllers slice checks writability only.
func ValidateCgroupDelegation(controllers []string) error {
	// cgroupParentDir is the seam provisionCgroup anchors through, so what
	// this probe reports and what the runner enforces cannot disagree, on
	// either the anchor or the controllers it must carry (issue #3273).
	parent, err := cgroupParentDir(controllers)
	if err != nil {
		return fmt.Errorf("cgroup v2 delegation cannot be determined (%w) — this host may be missing a unified cgroup v2 mount", err)
	}
	dir := filepath.Join(parent, fmt.Sprintf("spindrift-doctor-probe-%d", os.Getpid()))
	mkErr := os.Mkdir(dir, 0o755)
	if errors.Is(mkErr, os.ErrExist) {
		// A doctor run killed between Mkdir and Remove (or a reused PID)
		// leaves this directory behind; clear it and retry once rather than
		// misreporting a delegated host as non-delegated forever.
		if rmErr := os.Remove(dir); rmErr == nil {
			mkErr = os.Mkdir(dir, 0o755)
		}
	}
	if mkErr != nil {
		return fmt.Errorf("cgroup v2 subtree %s is not writable — this process's cgroup does not appear to be delegated to it (%w)", dir, mkErr)
	}
	for _, ctrl := range controllers {
		ctrlFile, ok := cgroupControlFiles[ctrl]
		if !ok {
			continue
		}
		if _, statErr := statCgroupControllerFile(filepath.Join(dir, ctrlFile)); statErr != nil {
			_ = os.Remove(dir)
			return fmt.Errorf("cgroup v2 subtree %s was created but is missing %s — this host's cgroup.subtree_control does not delegate that controller, so PIDS_LIMIT/MEMORY_LIMIT enforcement would silently fail even though subtree creation itself succeeded (%w)", dir, ctrlFile, statErr)
		}
	}
	// The answer is settled above, so removing the probe dir is best-effort.
	_ = os.Remove(dir)
	return nil
}
