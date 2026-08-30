package runner

import (
	"fmt"
	"os"
	"os/exec"
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
