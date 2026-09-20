package main

import (
	"sync"

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

// doctorCheckSets builds the classify and report halves (issue #3144).
// memoizeCheckProbes shares one *sync.Once per row, so a credential Peeks at
// most once across the one set this call builds -- run-wide that holds only
// at readContext.validation(), the single call site building one set and
// running both halves over it. classify omits the bwrap, drift, and
// transport rows, which must never make validateConfig exit 2 (issue #2671,
// ADR 0045, issue #3114); the per-route rows stay, so a bad credential still
// exits 2. podman-machine-memory joins classify too (issue #3544) -- it is
// the one extraCheck row whose Required tier must actually fail the run: the
// daemon's startup preflight reads doctor's exit code, and an undersized
// VM's OOM-killer fires before any container's --memory cap ever bites.
func doctorCheckSets(c config) (classify, report []doctor.Check) {
	extra := doctorExtraChecks(c)
	var perRoute, drift []doctor.Check
	// A config that declares no routes gets no route rows at all, not an
	// empty-routes report (issue #3145).
	if c.registryProxyRoutesFile != "" {
		extra = replaceCheckByName(extra, registryProxyRoutesCheckName, registryProxyRoutesCheck(c, false))
		// One parse feeds both row families. A read or parse failure
		// yields nil for both, leaving the failure to registryProxyRoutesCheck.
		if routes, err := loadRegistryRoutes(c.registryProxyRoutesFile); err == nil {
			perRoute = routeChecksFor(routes)
			drift = registryRouteDriftCheckForRoutes(c, routes)
		}
	}
	extra = memoizeCheckProbes(extra)
	perRoute = memoizeCheckProbes(perRoute)
	podmanMemory := memoizeCheckProbes(podmanMachineMemoryCheck(c))

	classify = make([]doctor.Check, 0, len(extra)+len(podmanMemory)+len(perRoute))
	classify = append(classify, extra...)
	// podmanMemory is the same memoized slice value appended to report below,
	// so an undersized machine's Required failure reaches validateConfigChecks
	// (hence exit 2) while the probe still shells out to `podman machine
	// inspect` only once per doctorCheckSets(c) call.
	classify = append(classify, podmanMemory...)
	classify = append(classify, perRoute...)

	bwrap := bwrapCapabilityChecks(c)
	report = make([]doctor.Check, 0, len(extra)+len(bwrap)+len(podmanMemory)+len(perRoute)+len(drift)+1)
	report = append(report, extra...)
	report = append(report, bwrap...)
	report = append(report, podmanMemory...)
	report = append(report, perRoute...)
	report = append(report, drift...)
	report = append(report, registryProxyTransportCheck(c))

	return classify, report
}

// memoizeCheckProbes returns copies whose Probe runs the original at most
// once and returns the cached (output, err) afterwards. It leaves a nil Probe
// unwrapped, since doctor.RunChecks and doctor.Run never call one.
func memoizeCheckProbes(checks []doctor.Check) []doctor.Check {
	out := make([]doctor.Check, len(checks))
	for i, ch := range checks {
		if ch.Probe == nil {
			out[i] = ch
			continue
		}
		probe := ch.Probe
		var once sync.Once
		var output any
		var err error
		ch.Probe = func() (any, error) {
			once.Do(func() {
				output, err = probe()
			})
			return output, err
		}
		out[i] = ch
	}
	return out
}

// replaceCheckByName returns a copy of checks with the row named name
// replaced, unchanged if no row matches. It copies rather than mutates so it
// never aliases doctorExtraChecks(c)'s array: that caller must keep getting
// the peeking variant.
func replaceCheckByName(checks []doctor.Check, name string, replacement doctor.Check) []doctor.Check {
	out := make([]doctor.Check, len(checks))
	copy(out, checks)
	for i, ch := range out {
		if ch.Name == name {
			out[i] = replacement
			break
		}
	}
	return out
}
