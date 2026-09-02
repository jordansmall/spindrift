package main

import (
	"fmt"
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
// (ADR 0042: "spindrift doctor reports all three"). Tier is informational
// only for these extraChecks rows -- doctor.Run reports them regardless of
// Tier; only ErrDegraded-wrapping on the Probe controls whether a row renders
// as "advisory:" or "MISSING:".
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
			// Tier alone has no runtime effect on rendering (doctor.Run never
			// reads it for these rows) -- ReportResults keys "advisory:" vs
			// "MISSING:" off ErrDegraded, so the Advisory config-state must be
			// re-checked here to pick up the same framing (same reuse of
			// ErrDegraded as bwrap-cgroup-delegation below).
			Probe: func() (any, error) {
				err := validateOverlayFn()
				if err != nil && !overlayRequired {
					return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
				}
				return nil, err
			},
		},
		{
			Name:   "bwrap-network-isolation",
			Tier:   networkTier,
			Remedy: networkRemedy,
			Probe: func() (any, error) {
				err := validatePastaFn()
				if err != nil && networkAdvisory {
					return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
				}
				return nil, err
			},
		},
		{
			Name:   "bwrap-cgroup-delegation",
			Tier:   doctor.Advisory,
			Remedy: "delegate a writable cgroup v2 subtree to this process (ADR 0042) -- without it, bwrap continues without PIDS_LIMIT/MEMORY_LIMIT enforcement",
			// bwrap-cgroup-delegation's Tier is unconditionally Advisory, so this
			// never blocks regardless of framing. Wrapping a genuine absence in
			// doctor.ErrDegraded is a deliberate reuse of ReportResults' "advisory:"
			// rendering (not its literal "couldn't determine" meaning) so a failing
			// row here reads visibly different from the MISSING: framing a failing
			// Required-tier row above gets (issue #2671 AC2: blocking vs degrading
			// must be distinguishable in spindrift doctor's own output).
			Probe: func() (any, error) {
				if err := validateCgroupDelegationFn(); err != nil {
					return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
				}
				return nil, nil
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
// operator-facing status table. Both are built from the SAME underlying
// extra/perRoute Check values (copied into two independent slices, so
// neither aliases the other's backing array), each Probe wrapped by
// memoizeCheckProbes -- since a doctor.Check's Probe is a func value sharing
// its wrapped closure's *sync.Once and cached result across every copy, a
// credential's Peek fires at most once total even though both classify and
// report carry their own copy of the row that Peeks it.
//
// classify omits bwrapCapabilityChecks(c)'s rows: an environment/installation
// concern, not a configuration fault (doctorExtraChecks' own doc comment),
// so folding them into Required-tier classification would make `spindrift
// doctor` exit 2 for e.g. a host merely missing the pasta binary (issue
// #2671 round-1 review finding). The per-route rows DO belong in classify,
// despite being new with this issue: each is Required tier, so an
// unresolvable route credential must still make `spindrift doctor` exit 2,
// matching what the old double-Peeking registryProxyRoutesCheck(c, true)
// aggregate row used to guarantee on its own.
//
// report's row order -- extra, bwrap rows, per-route rows -- matches what
// doctorReportChecks produced before this split, so `spindrift doctor`'s
// printed order is unchanged.
func doctorCheckSets(c config) (classify, report []doctor.Check) {
	extra := doctorExtraChecks(c)
	var perRoute []doctor.Check
	if c.registryProxyRoutesFile != "" {
		extra = replaceCheckByName(extra, registryProxyRoutesCheckName, registryProxyRoutesCheck(c, false))
		// registryRouteChecks' own gate re-reads the file when called
		// directly (e.g. from a test); loading here keeps a real doctor
		// run to one parse. A read/parse failure yields nil rows, same
		// as that helper's own gate would.
		if routes, err := loadRegistryRoutes(c.registryProxyRoutesFile); err == nil {
			perRoute = routeChecksFor(routes)
		}
	}
	extra = memoizeCheckProbes(extra)
	perRoute = memoizeCheckProbes(perRoute)

	classify = make([]doctor.Check, 0, len(extra)+len(perRoute))
	classify = append(classify, extra...)
	classify = append(classify, perRoute...)

	report = make([]doctor.Check, 0, len(extra)+len(perRoute)+4)
	report = append(report, extra...)
	report = append(report, bwrapCapabilityChecks(c)...)
	report = append(report, perRoute...)

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
