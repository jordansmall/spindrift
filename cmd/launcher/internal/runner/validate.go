package runner

import (
	"fmt"
	"os/exec"
)

// ValidateRuntime checks that runtime ("podman", "docker", or "bwrap") names
// a binary available on PATH, guarding the same Config.Runtime field NewOCI
// and the adapter-selection switch consume.
func ValidateRuntime(runtime string) error {
	if runtime == "" {
		return fmt.Errorf("RUNTIME is not set")
	}
	cli := runtimeCLI(runtime)
	if _, err := exec.LookPath(cli); err != nil {
		if runtime == "rancher" {
			return fmt.Errorf("nerdctl not found on PATH — is Rancher Desktop running in containerd mode?")
		}
		return fmt.Errorf("%s not found on PATH.", cli)
	}
	return nil
}
