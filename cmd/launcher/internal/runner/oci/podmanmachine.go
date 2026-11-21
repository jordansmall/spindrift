package oci

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

// podmanMachineInspectTimeout bounds the `podman machine inspect` shell-out,
// a local metadata query, so it gets a much shorter budget than
// registryProxyProbeTimeout: a wedged podman VM, the state the
// podman-machine-memory row warns about, must not hang doctor. A var so a
// test can shorten it.
var podmanMachineInspectTimeout = 5 * time.Second

// PodmanMachineMemoryMiB shells out to `podman machine inspect` and reads its
// Resources.Memory field, which podman reports in MiB. found is false for
// every "no answer" the sizing check (issue #3537) treats as not applicable
// rather than a shortfall: podman missing, no active machine, no
// Resources.Memory, or a timeout (a wedged VM). It lives here because
// TestNoRunnerExecOutsidePackage forbids `exec.Command("podman"` elsewhere.
func PodmanMachineMemoryMiB() (mib int, found bool) {
	if _, err := exec.LookPath(cliPodman); err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), podmanMachineInspectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPodman, "machine", "inspect")
	cmd.WaitDelay = cliWaitDelay
	out, err := cmd.Output()
	if (err != nil && !errors.Is(err, exec.ErrWaitDelay)) || len(out) == 0 {
		return 0, false
	}
	return parsePodmanMachineMemoryMiB(out)
}

// parsePodmanMachineMemoryMiB decodes `podman machine inspect` JSON output,
// apart from the shell-out so fixtures can exercise it.
func parsePodmanMachineMemoryMiB(out []byte) (mib int, found bool) {
	var machines []struct {
		Resources struct {
			Memory int
		}
	}
	if err := json.Unmarshal(out, &machines); err != nil || len(machines) == 0 {
		return 0, false
	}
	if machines[0].Resources.Memory <= 0 {
		return 0, false
	}
	return machines[0].Resources.Memory, true
}
