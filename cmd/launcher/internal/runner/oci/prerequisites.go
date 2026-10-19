package oci

import (
	"fmt"

	"spindrift.dev/launcher/internal/runner"
)

// cliPodman is the podman CLI name, shared by the adapter and the machine query.
const cliPodman = "podman"

// podmanMachineMemoryCheckName is the Name of the podman-machine-memory
// prerequisite row, so callers match on the constant rather than the bare
// string literal.
const podmanMachineMemoryCheckName = "podman-machine-memory"

// vmOverheadMiB is the RAM the machine's own OS and daemon compete for
// alongside the containers it runs.
const vmOverheadMiB = 512

// podmanMachineMemoryFn is a seam so a test can substitute a distinguishable
// fake instead of shelling out to a real podman machine (issue #3537).
var podmanMachineMemoryFn = PodmanMachineMemoryMiB

// memoryLimitMiB converts a podman/docker-style limit string to MiB via
// runner.MemoryLimitToBytes, so this doctor-side sizing check and the
// runtime's own cgroup enforcement never drift on unit parsing.
func memoryLimitMiB(limit string) (int, error) {
	b, err := runner.MemoryLimitToBytes(limit)
	if err != nil {
		return 0, err
	}
	return int(b / (1024 * 1024)), nil
}

// podmanMachineRequiredMiB is the RAM maxParallel containers each capped at
// memoryLimit need from the machine. The remedy and the Probe's failure
// message both name this figure, so both route through here rather than
// re-deriving it from a second parse.
func podmanMachineRequiredMiB(memoryLimit string, maxParallel int) (int, error) {
	limitMiB, err := memoryLimitMiB(memoryLimit)
	if err != nil {
		return 0, err
	}
	return limitMiB*maxParallel + vmOverheadMiB, nil
}

// podmanMachineMemoryRemedy names all three fixes an operator can reach for.
// An invalid memoryLimit (the schema applies no format validation, so
// unparseable garbage like "5x" or a non-positive "0" reaches here) leaves no
// figure to size the `podman machine set` command against, and lowering the
// limit would not help, so that branch points at MEMORY_LIMIT itself instead.
func podmanMachineMemoryRemedy(memoryLimit string, maxParallel int) string {
	required, err := podmanMachineRequiredMiB(memoryLimit, maxParallel)
	if err != nil {
		return "set MEMORY_LIMIT to a positive size such as 5g, or to empty to disable the limit, then re-run doctor"
	}
	return fmt.Sprintf(
		"lower MAX_PARALLEL, raise the podman machine's RAM (podman machine set --memory %d; then restart the machine), or lower MEMORY_LIMIT",
		required,
	)
}

// Prerequisites declares the oci family's runtime row and its
// "podman-machine-memory" row (issue #3537), porting the driving loop's old
// shell preflight (#580, parallelism-aware per #712) into doctor's own report.
// On macOS/Windows
// podman runs containers in a VM with fixed RAM, so when MAX_PARALLEL
// containers together want more than the machine has, the VM's OOM-killer
// fires before any single container's --memory cap ever bites (#565 killed
// an in-box `nix build`; #712 took down the whole VM). The row is declared for
// every runtime of the family; docker and rancher use no podman machine, so
// their Probe reports not applicable.
func Prerequisites(cfg runner.Config) []runner.Prerequisite {
	return []runner.Prerequisite{runner.RuntimePrerequisite(cfg.Runtime), {
		Name:     podmanMachineMemoryCheckName,
		Launch:   runner.DoctorOnly,
		Required: true,
		Remedy:   podmanMachineMemoryRemedy(cfg.MemoryLimit, cfg.MaxParallel),
		Probe: func() (any, error) {
			if cfg.Runtime != cliPodman {
				return fmt.Sprintf("not applicable (runtime %q uses no podman machine)", cfg.Runtime), nil
			}
			// "" is a deliberate opt-out (lib/env-schema.nix memoryLimit's
			// emptyDisables=true turns off the per-Box memory limit
			// entirely), distinct from an unset MEMORY_LIMIT -- the check
			// must not fire when there is no limit to size against.
			if cfg.MemoryLimit == "" {
				return "not applicable (MEMORY_LIMIT is empty: the per-Box memory limit is disabled)", nil
			}
			machineMiB, found := podmanMachineMemoryFn()
			if !found {
				return "not applicable (no active podman machine)", nil
			}
			required, err := podmanMachineRequiredMiB(cfg.MemoryLimit, cfg.MaxParallel)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, runner.ErrDegraded)
			}
			if machineMiB < required {
				return nil, fmt.Errorf(
					"podman machine has %dMiB RAM but MEMORY_LIMIT=%s x MAX_PARALLEL=%d needs %dMiB (incl. %dMiB VM overhead): the VM's own OOM-killer fires before any single container's --memory cgroup cap ever bites",
					machineMiB, cfg.MemoryLimit, cfg.MaxParallel, required, vmOverheadMiB,
				)
			}
			return fmt.Sprintf("podman machine has %dMiB RAM, %dMiB required", machineMiB, required), nil
		},
		// The not-a-string branch covers a SuccessMsg(nil) reach whose
		// closure's Probe never ran, which would otherwise render blank.
		SuccessMsg: func(output any) string {
			s, ok := output.(string)
			if !ok {
				return podmanMachineMemoryCheckName
			}
			return fmt.Sprintf("%s (%s)", podmanMachineMemoryCheckName, s)
		},
	}}
}
