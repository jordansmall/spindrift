package runner

import (
	"fmt"
	"os/exec"
)

// ValidateRuntime checks that runtime ("podman", "docker", "rancher", or
// "bwrap") names a binary on PATH, guarding the same Config.Runtime field
// the adapter constructors and the adapter-selection switch consume.
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

// RuntimePrerequisiteName is the Name of the RuntimePrerequisite row.
const RuntimePrerequisiteName = "runtime"

// RuntimePrerequisite declares that runtime's CLI is on PATH. Every family
// declares it first; the launcher enforces it with its config checks, ahead of
// the launch gates.
func RuntimePrerequisite(runtime string) Prerequisite {
	return Prerequisite{
		Name:     RuntimePrerequisiteName,
		Launch:   LaunchWithConfig,
		Required: true,
		Remedy:   "set RUNTIME to podman, docker, rancher, or bwrap, and ensure the matching CLI is on PATH",
		Probe: func() (any, error) {
			return nil, ValidateRuntime(runtime)
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("runtime %q found on PATH", runtime)
		},
	}
}
