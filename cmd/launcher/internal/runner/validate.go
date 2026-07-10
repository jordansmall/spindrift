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
	if _, err := exec.LookPath(runtime); err != nil {
		return fmt.Errorf("%s not found on PATH.", runtime)
	}
	return nil
}
