package bwrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hostNetOptOut is the shared tail of every refusal that offers
// NETWORK_MODE=host as the way out of the pasta requirement.
const hostNetOptOut = "set NETWORK_MODE=host to explicitly opt into the shared-network-namespace behaviour"

// ValidatePasta checks that pasta is on PATH, can create a network namespace
// on this host, and leaves it with an UP interface, an IPv4 address and a
// default route under the launcher's own pasta flags (issue #5983). The
// launcher refuses to start otherwise rather than fall back to the weaker
// shared-host-netns sandbox (issue #2666).
func ValidatePasta() error {
	return validatePastaWith(exec.LookPath, execCommand, pastaFlags())
}

// validatePastaWith is ValidatePasta with its PATH lookup, exec and flag
// seams injected. It re-executes this binary under pasta (see init in
// netns_probe.go) so the check needs no host tool inside the namespace.
func validatePastaWith(lookPath func(string) (string, error), run func(string, ...string) *exec.Cmd, flags []string) error {
	if _, err := lookPath("pasta"); err != nil {
		return fmt.Errorf("pasta not found on PATH — required to give a bwrap Box its own network namespace with the host loopback blocked (issue #2666); install pasta (the passt project) on PATH, or set NETWORK_MODE=host to explicitly opt into the pre-#2666 shared-network-namespace behaviour")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("pasta network namespace probe: cannot locate this executable to re-exec under pasta: %w", err)
	}
	cmd := run("pasta", append(append([]string{}, flags...), "--", exe, netnsProbeArg)...)
	cmd.Env = append(os.Environ(), netnsProbeEnv+"=1")
	out, runErr := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, netnsProbeMarker) {
			continue
		}
		if line == netnsProbeOK {
			return nil
		}
		reason := strings.TrimSpace(strings.TrimPrefix(line, netnsProbeMarker))
		return fmt.Errorf("pasta created a network namespace for the bwrap Box but it is unusable (%s) — a bwrap Box launched here would have no egress to its Provider (issue #5981); use a pasta (passt) that honours the launcher's --config-net, or check the passt install, or "+hostNetOptOut, reason)
	}
	output := strings.TrimSpace(string(out))
	if runErr == nil {
		return fmt.Errorf("pasta exited without running the in-namespace network probe (output: %s) — check the passt install, or "+hostNetOptOut, output)
	}
	return fmt.Errorf("pasta could not create a network namespace to run the probe in (%v; output: %s) — either pasta rejected one of the launcher's flags (check the passt version) or this host or sandbox bars unprivileged user/network namespaces; run on a host that allows them, or "+hostNetOptOut, runErr, output)
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
