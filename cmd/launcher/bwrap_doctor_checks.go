package main

import (
	"sync"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
)

// Seams so tests can substitute a distinguishable fake per capability and
// prove each row's Probe calls the correct one -- issue #2671 review found
// that without this, swapping runner.ValidateOverlay and runner.ValidatePasta
// between the two rows would leave every existing test green.
var (
	validateOverlayFn          = runner.ValidateOverlay
	validatePastaFn            = runner.ValidatePasta
	validateCgroupDelegationFn = runner.ValidateCgroupDelegation
)

// bwrapCapabilityChecks builds the three bwrap-capability doctor.Check rows
// (issue #2671): bwrap-overlay-support, bwrap-network-isolation, and
// bwrap-cgroup-delegation. Each row's Probe always runs the real capability
// probe regardless of Tier, so `spindrift doctor` reports the host's actual
// bwrap capability posture even when the current config doesn't require it
// (ADR 0042: "spindrift doctor reports all three"). doctor.Run reports every
// row regardless of Tier; ReportResults keys a row's "advisory:" vs
// "MISSING:" prefix directly off Tier.
//
// Each row's Tier mirrors the equivalent real launch-time gate in main.go
// (checkBwrapPastaGate, checkBwrapOverlayGate) so `spindrift doctor`'s
// reported severity for a given config never disagrees with what would
// actually block that config's launch -- except bwrap-cgroup-delegation,
// which has no launch-time gate at all (ADR 0042: "No cgroup delegation
// warns and continues"), so it is unconditionally Advisory.
//
// Self-gated on c.runnerKind == freshness.KindBwrap: returns nil for any
// other runnerKind, so these rows appear in `spindrift doctor` output only
// when the configured runtime is bwrap (issue #2671 slice 3). Wired into
// doctorReportChecks below, never into doctorExtraChecks (checks.go)
// directly -- that function feeds validateConfig (main.go), and these rows
// must never affect its exit-2 classification (issue #2671 round-1 review
// finding).
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
				// The controller set comes from the same two config values
				// main.go feeds runner.Config's PidsLimit/MemoryLimit, so the
				// row asks about exactly the delegation this config's runner
				// will need -- no more (issue #3273).
				return nil, validateCgroupDelegationFn(runner.CgroupControllers(c.memoryLimit, c.pidsLimit))
			},
		},
	}
}

// doctorReportChecks returns the rows `spindrift doctor` reports to an
// operator -- the report half of doctorCheckSets(c). Kept as a thin wrapper
// because existing tests call it directly to build runDoctor's extraChecks
// argument in isolation; a standalone call like that builds its own
// doctorCheckSets(c), so its memoized Probes are fresh to that call and
// unrelated to any other doctorCheckSets(c) call. The peek-once-per-credential
// guarantee (issue #3144) only holds at the doctorReport (doctor.go) call
// site, which builds exactly ONE doctorCheckSets(c) result and threads it
// through both validateConfigChecks and runDoctor: memoized Probes run once
// during classification, and the report re-render below returns each one's
// cached (output, err) rather than Peeking again.
func doctorReportChecks(c config) []doctor.Check {
	_, report := doctorCheckSets(c)
	return report
}

// doctorCheckSets builds the two doctor.Check slices `spindrift doctor`
// needs per invocation (issue #3144): classify feeds validateConfigChecks'
// exit-2 "configuration invalid" classification, report feeds runDoctor's
// operator-facing status table. Each Probe is wrapped by memoizeCheckProbes,
// so a shared closure's *sync.Once caches its result across both slices'
// copies -- a credential's Peek fires at most once total even though both
// classify and report carry their own copy of the row that Peeks it.
//
// classify omits bwrapCapabilityChecks(c)'s rows, the drift row, and the
// registry-proxy-transport row: all three are environment/staleness/advisory
// concerns, not configuration faults, so folding them into Required-tier
// classification would make `spindrift doctor` exit 2 for e.g. a host merely
// missing the pasta binary, a routes file that has drifted from its source
// config (issue #2671; ADR 0045), or a runtime that answers TCP instead of a
// unix socket (issue #3114: TCP is a passing outcome, not a failure). The
// per-route rows DO belong in classify: each is Required tier, so an
// unresolvable route credential must still make `spindrift doctor` exit 2.
//
// report's row order -- extra, bwrap rows, per-route rows, drift row,
// transport row -- matches what doctorReportChecks produced before this
// split, with the transport row (issue #3114) appended last.
func doctorCheckSets(c config) (classify, report []doctor.Check) {
	extra := doctorExtraChecks(c)
	var perRoute, drift []doctor.Check
	if c.registryProxyRoutesFile != "" {
		extra = replaceCheckByName(extra, registryProxyRoutesCheckName, registryProxyRoutesCheck(c, false))
		// One loadRegistryRoutes call feeds both row families below --
		// registryRouteChecks and registryRouteDriftCheck each parse the
		// same file on their own when called directly (e.g. from a test),
		// but a real doctor run only needs the one parse. A read/parse
		// failure here yields nil for both, same as either helper's own
		// gate would.
		if routes, err := loadRegistryRoutes(c.registryProxyRoutesFile); err == nil {
			perRoute = routeChecksFor(routes)
			drift = registryRouteDriftCheckForRoutes(c, routes)
		}
	}
	extra = memoizeCheckProbes(extra)
	perRoute = memoizeCheckProbes(perRoute)

	classify = make([]doctor.Check, 0, len(extra)+len(perRoute))
	classify = append(classify, extra...)
	classify = append(classify, perRoute...)

	bwrap := bwrapCapabilityChecks(c)
	report = make([]doctor.Check, 0, len(extra)+len(bwrap)+len(perRoute)+len(drift)+1)
	report = append(report, extra...)
	report = append(report, bwrap...)
	report = append(report, perRoute...)
	report = append(report, drift...)
	report = append(report, registryProxyTransportCheck(c))

	return classify, report
}

// memoizeCheckProbes returns copies of checks whose Probe runs the original
// Probe at most once (sync.Once) and returns the cached (output, err) on
// every later call -- the mechanism doctorCheckSets relies on to let the
// same route-credential Peek appear in both its classify and report slices
// without Peeking twice. A nil Probe is left nil rather than wrapped, since
// doctor.RunChecks and doctor.Run never call a nil Probe themselves.
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
// replaced by replacement, left unchanged if no row matches. Copies rather
// than mutates in place so it never aliases doctorExtraChecks(c)'s
// underlying array with the substituted row -- doctorExtraChecks(c) itself
// must keep returning the peeking variant for its own callers (validate(),
// validateConfig).
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
