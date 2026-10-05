package main

import (
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
)

// Seams so a test can substitute a distinguishable fake per capability.
// Without them, swapping runner.ValidateOverlay and runner.ValidatePasta
// between the two rows would leave every test green (issue #2671).
var (
	validateOverlayFn          = runner.ValidateOverlay
	validatePastaFn            = runner.ValidatePasta
	validateCgroupDelegationFn = runner.ValidateCgroupDelegation
)

// bwrapCapabilityChecks builds the three bwrap-capability rows (issue #2671).
// Each Tier mirrors the equivalent launch-time gate in main.go, except
// bwrap-cgroup-delegation, which has no such gate (ADR 0042) and is always
// Advisory. doctorCheckSets puts these rows in the report half only, never in
// the classify half that can exit 2.
func bwrapCapabilityChecks(c config) []doctor.Check {
	if c.runnerKind != freshness.KindBwrap {
		return nil
	}

	overlayRequired := c.nixStoreWritable && c.nixConfigFile != ""
	overlayTier := doctor.Advisory
	if overlayRequired {
		overlayTier = doctor.Required
	}

	networkAdvisory := c.networkMode == runner.NetworkModeHost || c.networkMode == runner.NetworkModeNone
	networkTier := doctor.Required
	if networkAdvisory {
		networkTier = doctor.Advisory
	}

	overlayRemedy := "ensure the host kernel allows an unprivileged user namespace to mount overlayfs (e.g. the unprivileged_userns_clone sysctl on some distros)"
	if overlayRequired {
		overlayRemedy = "unset nixStoreWritable, or " + overlayRemedy
	}

	networkRemedy := "install pasta (the passt project) on PATH"
	if !networkAdvisory {
		networkRemedy += ", or set NETWORK_MODE=host to explicitly opt into the shared-network-namespace behaviour"
	}

	return []doctor.Check{
		{
			Name:   "bwrap-overlay-support",
			Tier:   overlayTier,
			Remedy: overlayRemedy,
			Probe: func() (any, error) {
				return nil, validateOverlayFn()
			},
		},
		{
			Name:   "bwrap-network-isolation",
			Tier:   networkTier,
			Remedy: networkRemedy,
			Probe: func() (any, error) {
				return nil, validatePastaFn()
			},
		},
		{
			Name:   "bwrap-cgroup-delegation",
			Tier:   doctor.Advisory,
			Remedy: "delegate a writable cgroup v2 subtree to this process (ADR 0042) -- without it, bwrap continues without PIDS_LIMIT/MEMORY_LIMIT enforcement",
			Probe: func() (any, error) {
				// The controller set comes from the same config values main.go
				// feeds runner.Config, so the row asks about exactly the
				// delegation this config needs and no more (issue #3273).
				return nil, validateCgroupDelegationFn(runner.CgroupControllers(c.memoryLimit, c.pidsLimit))
			},
		},
	}
}
