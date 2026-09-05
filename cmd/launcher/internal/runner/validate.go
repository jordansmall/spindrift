package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ValidateRuntime checks that runtime ("podman", "docker", "rancher", or
// "bwrap") names a binary available on PATH — via BinaryFor for "rancher",
// which maps to "nerdctl" — guarding the same Config.Runtime field NewOCI
// and the adapter-selection switch consume.
func ValidateRuntime(runtime string) error {
	return ValidateRuntimeWithLookup(runtime, exec.LookPath)
}

// ValidateRuntimeWithLookup is ValidateRuntime with the PATH lookup
// injected, so callers with their own lookPath abstraction (e.g.
// quickstart's Environment.LookPath) can reuse this exact validation logic
// and message text — including the nerdctl/Rancher-Desktop-specific message
// for runtime="rancher" — instead of hand-rolling their own check
// (issue #2561).
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
// bwrap Box isolates its network namespace with working egress (issue
// #2666): without it, the sandbox's own network helper is missing and the
// launcher must refuse to start rather than silently falling back to a
// weaker (shared-host-netns) sandbox.
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
// 0042, issue #2665): without this support, bwrap's own --overlay-src /
// --tmp-overlay mount would fail deep inside sandbox startup, so the
// launcher must refuse to start with an actionable message instead.
func ValidateOverlay() error {
	return ValidateOverlayWithExec(execCommand)
}

// ValidateOverlayWithExec is ValidateOverlay with the exec seam injected, so
// tests can substitute a fake exec.Cmd constructor instead of depending on a
// real bwrap binary and real unprivileged-overlayfs kernel support. Unlike
// ValidatePasta's pure PATH-existence check, overlayfs-in-userns support
// can't be answered by looking for a binary on PATH — it takes an actual
// functional probe — so this runs a minimal, real bwrap invocation that
// overlays a throwaway temp dir over itself inside a fresh user namespace
// and reports whether the mount succeeds.
func ValidateOverlayWithExec(run func(string, ...string) *exec.Cmd) error {
	dir, err := os.MkdirTemp("", "spindrift-overlay-probe-")
	if err != nil {
		return fmt.Errorf("nixStoreWritable overlay smoke test: failed to create a temp dir to probe with: %w", err)
	}
	defer os.RemoveAll(dir)

	// --ro-bind / / gives the sandboxed root a real filesystem to resolve
	// "true" from -- without it, an otherwise-empty sandbox root fails to
	// exec anything regardless of whether the overlay mount itself
	// succeeded, producing a false failure that says nothing about overlay
	// support.
	cmd := run("bwrap", "--unshare-user", "--uid", "1000", "--gid", "1000", "--ro-bind", "/", "/", "--overlay-src", dir, "--tmp-overlay", dir, "--", "true")
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nixStoreWritable/NIX_STORE_WRITABLE is set but this host's kernel does not appear to allow an unprivileged user namespace to mount overlayfs (bwrap --overlay-src/--tmp-overlay smoke test failed: %w; output: %s) — either unset nixStoreWritable, or check kernel support for unprivileged user namespaces + overlayfs (e.g. the unprivileged_userns_clone sysctl on some distros)", err, stderr)
	}
	return nil
}

// statCgroupControllerFile is os.Stat, swappable in tests the same way
// readSelfCgroup/cgroupFSRoot are (bwrap.go): a plain temp-dir-backed test
// has no real kernel to auto-populate pids.max/memory.max the way a genuine
// delegated cgroup v2 subtree would, so tests fake presence through this
// seam instead of relying on real cgroup filesystem behaviour.
var statCgroupControllerFile = os.Stat

// cgroupControlFiles maps a cgroup v2 controller to the per-cgroup file
// provisionCgroup writes that controller's limit to, so the probe below
// checks for exactly the file the runner will later need.
var cgroupControlFiles = map[string]string{
	"pids":   "pids.max",
	"memory": "memory.max",
}

// ValidateCgroupDelegation checks that a cgroup v2 subtree is delegated
// (writable) to this process, AND that each controller in controllers is
// actually available in it -- both required for bwrap to enforce
// PIDS_LIMIT/MEMORY_LIMIT under each Box's per-run cgroup (ADR 0042). It
// creates a throwaway subtree, checks for those controllers' limit files,
// and immediately removes it -- this mutates the cgroup filesystem
// transiently but leaves nothing behind on success.
//
// controllers comes from the same CgroupControllers(memoryLimit, pidsLimit)
// mapping the adapter resolves its own set through, and is passed verbatim
// to cgroupParentDir, the same seam provisionCgroup anchors through, so the
// reported posture and the runtime behaviour can't disagree (issue #3273) --
// neither on which anchor is chosen nor on which controllers that anchor
// must carry. On a host with no delegation anywhere, cgroupParentDir's
// fallback puts both back at the launcher's own cgroup and this probe fails
// on the missing control file exactly as provisionCgroup's limit write
// would. An empty set means no limit is configured at all, so nothing will
// be enforced and no controller is asked for: the probe reduces to the
// writability question, mirroring provisionCgroup, which writes no limit
// file in that case either.
func ValidateCgroupDelegation(controllers []string) error {
	parent, err := cgroupParentDir(controllers)
	if err != nil {
		return fmt.Errorf("cgroup v2 delegation cannot be determined (%w) — this host may be missing a unified cgroup v2 mount", err)
	}
	dir := filepath.Join(parent, fmt.Sprintf("spindrift-doctor-probe-%d", os.Getpid()))
	mkErr := os.Mkdir(dir, 0o755)
	if errors.Is(mkErr, os.ErrExist) {
		// A prior doctor run killed between Mkdir and Remove below (or a
		// reused PID) can leave this exact directory behind; clear it and
		// retry once rather than permanently misreporting a delegated host
		// as non-delegated.
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
	// The capability question is already answered above; a failure to
	// remove the throwaway probe dir doesn't change that answer, so removal
	// is best-effort.
	_ = os.Remove(dir)
	return nil
}
