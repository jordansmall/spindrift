package runner

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// podmanMachineInspectTimeout bounds the `podman machine inspect` shell-out,
// never a real Box's runtime. It's a local metadata query against podman's
// own state, not a container start, so it gets a much shorter budget than
// registryProxyProbeTimeout: a wedged podman VM -- the very state this row
// warns about -- must not hang doctor inside the probe meant to protect the
// operator. A var, not a const, for the same reason registryProxyProbeTimeout
// is one (oci.go): a probe budget a test can shorten in place.
var podmanMachineInspectTimeout = 5 * time.Second

// PodmanMachineMemoryMiB shells out to `podman machine inspect` and reads its
// Resources.Memory field, which podman already reports in MiB. found is
// false for every "no answer" case the doctor-side sizing check (issue
// #3537, porting the driving loop's old shell preflight) must treat as not
// applicable rather than a shortfall: podman missing from PATH, the command
// failing (no active machine), empty output, a payload with no
// Resources.Memory, or the command timing out (a wedged podman VM).
// Lives here, not in cmd/launcher, because TestNoRunnerExecOutsidePackage
// forbids `exec.Command("podman"` outside this package.
func PodmanMachineMemoryMiB() (mib int, found bool) {
	if _, err := exec.LookPath("podman"); err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), podmanMachineInspectTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "machine", "inspect").Output()
	if err != nil || len(out) == 0 {
		return 0, false
	}
	return parsePodmanMachineMemoryMiB(out)
}

// parsePodmanMachineMemoryMiB decodes `podman machine inspect` JSON output,
// isolated from the exec shell-out above so the field-binding and <= 0 rule
// are exercised directly against fixtures rather than only through a live
// podman binary.
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
