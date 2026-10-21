package bwrap

import "spindrift.dev/launcher/internal/runner"

// Seams so a test can substitute a distinguishable fake per capability.
// Without them, swapping ValidateOverlay and ValidatePasta between the two
// rows would leave every test green (issue #2671).
var (
	validateOverlayFn          = ValidateOverlay
	validatePastaFn            = ValidatePasta
	validateCgroupDelegationFn = ValidateCgroupDelegation
)

// Prerequisites declares the runtime check and the three bwrap-capability
// checks, in report order (issue #2671). The two LaunchGate rows are also
// enforced at launch when Required; bwrap-cgroup-delegation has no launch gate
// (ADR 0042) and is always Advisory. A launch-gate Probe returns the raw
// Validate* error, which the launch path surfaces verbatim.
func Prerequisites(cfg runner.Config, l runner.KnobLookup) []runner.Prerequisite {
	// The overlay flags only render under this AND-gate in bwrap.go, so the
	// check is enforced exactly when they would.
	overlayRequired := cfg.NixStoreWritable && cfg.NixConfigFile != ""

	// The Pasta decision comes from network, the same helper New renders from,
	// so the check never fires for a Box that will not invoke pasta. A malformed
	// knob, which New would also refuse, fails the network row rather than
	// guess at the decision.
	net, knobErr := network(cfg, l)
	networkAdvisory := knobErr == nil && !net.Pasta

	overlayRemedy := "ensure the host kernel allows an unprivileged user namespace to mount overlayfs (e.g. the unprivileged_userns_clone sysctl on some distros)"
	if overlayRequired {
		overlayRemedy = "unset nixStoreWritable, or " + overlayRemedy
	}

	networkRemedy := "install pasta (the passt project) on PATH"
	if !networkAdvisory {
		networkRemedy += ", or set NETWORK_MODE=host to explicitly opt into the shared-network-namespace behaviour"
	}

	return []runner.Prerequisite{
		// RUNTIME=bwrap names the bwrap binary, which this check validates.
		runner.RuntimePrerequisite(cfg.Runtime),
		{
			// Launch refuses rather than let bwrap fail deep in sandbox startup
			// when the in-box /nix/store needs an ephemeral tmpfs overlay (ADR
			// 0042, issue #2665) but the host kernel bars an unprivileged user
			// namespace from mounting overlayfs.
			Name: "bwrap-overlay-support",
			// Launch checks pasta before overlay, while doctor reports
			// overlay first.
			LaunchOrder: 1,
			Launch:      runner.LaunchGate,
			Required:    overlayRequired,
			Remedy:      overlayRemedy,
			Probe: func() (any, error) {
				return nil, validateOverlayFn()
			},
		},
		{
			// Launch refuses rather than silently share the host network
			// namespace when pasta is missing (issue #2666).
			Name:     "bwrap-network-isolation",
			Launch:   runner.LaunchGate,
			Required: !networkAdvisory,
			Remedy:   networkRemedy,
			Probe: func() (any, error) {
				if knobErr != nil {
					return nil, knobErr
				}
				return nil, validatePastaFn()
			},
		},
		{
			Name:   "bwrap-cgroup-delegation",
			Launch: runner.DoctorOnly,
			Remedy: "delegate a writable cgroup v2 subtree to this process (ADR 0042) -- without it, bwrap continues without PIDS_LIMIT/MEMORY_LIMIT enforcement",
			Probe: func() (any, error) {
				// The controller set comes from the same limits that feed
				// runner.Config, so the row asks about exactly the delegation
				// this config needs and no more (issue #3273).
				return nil, validateCgroupDelegationFn(CgroupControllers(cfg.MemoryLimit, cfg.PidsLimit))
			},
		},
	}
}

// network resolves the plan's Box network from cfg and the family knobs read
// through l. New and Prerequisites share it so the doctor check inspects the
// decision the adapter renders.
func network(cfg runner.Config, l runner.KnobLookup) (runner.BwrapNetwork, error) {
	k, err := ReadKnobs(l)
	if err != nil {
		return runner.BwrapNetwork{}, err
	}
	return cfg.BwrapNetwork(k.BwrapUnshareNet), nil
}

// RawNetworkKnobs names BwrapUnshareNetEnv when l sets it.
func RawNetworkKnobs(l runner.KnobLookup) ([]string, error) {
	k, err := ReadKnobs(Lookup(l))
	if err != nil {
		return nil, err
	}
	if k.BwrapUnshareNet {
		return []string{BwrapUnshareNetEnv}, nil
	}
	return nil, nil
}
