package main

import (
	"fmt"

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
// operator: doctorExtraChecks(c) (launcher-startup validation rows) plus
// bwrapCapabilityChecks(c)'s bwrap-capability rows (empty unless
// c.runnerKind is bwrap). Kept separate from doctorExtraChecks itself —
// which validateConfig also consumes — because a bwrap host-capability
// gap is an environment/installation concern, not a configuration fault
// (main.go's validateConfig doc comment): folding these rows into
// validateConfig's Required-tier classification would make `spindrift
// doctor` exit 2 "configuration invalid" for e.g. a host merely missing
// the pasta binary (issue #2671 round-1 review finding). doctor.Run's
// extraChecks are reported informationally only regardless of Tier, so
// appending these rows here has no effect on spindrift doctor's exit
// code.
func doctorReportChecks(c config) []doctor.Check {
	return append(doctorExtraChecks(c), bwrapCapabilityChecks(c)...)
}
