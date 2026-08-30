package runner

import (
	"fmt"
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
