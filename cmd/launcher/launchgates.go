package main

import (
	"errors"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/backend"
)

// launchGate is one entry in the ordered gate registry (issue #2942).
// Enforcement (gatedContext) and reporting (doctor) both walk gateRegistry
// through walkGateRegistry, so enforce order equals report order by
// construction.
type launchGate struct {
	Name string
	// Applicable reports whether g is relevant to c at all; nil means always.
	// An inapplicable gate is skipped entirely, with no Check call and no
	// report line, so doctor never prints "ok" for a check that never ran.
	// The token gates reuse their own self-noop condition here.
	Applicable func(c config) bool
	// Network marks a gate whose Check makes a live network call. Once one
	// fails, walkGateRegistry stops even in collectAll mode, since a later
	// probe is moot.
	Network bool
	Check   func(c config, w io.Writer) error
}

// tokenGateApplicable reports whether c's codeForge or issueTracker resolves to
// a backend sharing desc's TokenEnvVar. Matching on TokenEnvVar rather than
// desc.Name makes an unregistered name miss, but only while every caller passes
// a desc with a non-empty TokenEnvVar; a token-less desc (backend.Local,
// backend.Git) would match every lookup miss, since both sides are empty.
func tokenGateApplicable(c config, desc backend.Descriptor) bool {
	forgeRow, _ := backendByName(c.codeForge)
	trackerRow, _ := backendByName(c.issueTracker)
	return forgeRow.TokenEnvVar == desc.TokenEnvVar || trackerRow.TokenEnvVar == desc.TokenEnvVar
}

// gateRegistry is the ordered set of launch gates shared by the gated tier's
// enforcement path and doctor's reporting path. The two bwrap gates are
// excluded on purpose: doctor reports them separately, with richer dynamic
// Tier and remedy semantics, via bwrapCapabilityChecks in
// bwrap_doctor_checks.go.
var gateRegistry = []launchGate{
	{Name: "read-only-capability", Check: func(c config, _ io.Writer) error {
		return checkReadOnlyCapabilityGate(c)
	}},
	{Name: "network-mode-runtime", Check: func(c config, _ io.Writer) error {
		return checkNetworkModeRuntimeGate(c)
	}},
	{Name: "signal-carrier-network-mode", Check: func(c config, _ io.Writer) error {
		return checkSignalCarrierNetworkModeGate(c)
	}},
	{
		Name: "read-only-token-github",
		Applicable: func(c config) bool {
			if c.boxForgeAndIssueAccess != "read-only" {
				return false
			}
			return tokenGateApplicable(c, backend.GitHub)
		},
		Network: true,
		Check: func(c config, w io.Writer) error {
			_, err := checkReadOnlyTokenGate(c, ghTokenIntrospector, w)
			return err
		},
	},
	{
		Name: "read-only-token-forgejo",
		Applicable: func(c config) bool {
			if c.boxForgeAndIssueAccess != "read-only" {
				return false
			}
			return tokenGateApplicable(c, backend.Forgejo)
		},
		Network: true,
		Check: func(c config, w io.Writer) error {
			_, err := checkReadOnlyForgejoTokenGate(c, w)
			return err
		},
	},
}

// splitGateRegistryByNetwork partitions registry into its non-network and
// network gates, preserving registry order within each. The split reads each
// gate's own Network field rather than a hardcoded index, so inserting or
// reordering a gate cannot silently break it.
func splitGateRegistryByNetwork(registry []launchGate) (nonNetwork, network []launchGate) {
	for _, g := range registry {
		if g.Network {
			network = append(network, g)
		} else {
			nonNetwork = append(nonNetwork, g)
		}
	}
	return nonNetwork, network
}

// walkGateRegistry runs registry's gates against c in order. checkW goes to
// each gate's own Check, so a gate's operator-facing output still reaches the
// caller's real writer even when reportW is discarded for the generic ok and
// MISSING lines (issue #2942 AC5). collectAll lets doctor enumerate every
// broken non-network gate instead of stopping at the first.
func walkGateRegistry(registry []launchGate, c config, checkW, reportW io.Writer, collectAll bool) error {
	var errs []error
	for _, g := range registry {
		if g.Applicable != nil && !g.Applicable(c) {
			continue
		}
		if err := g.Check(c, checkW); err != nil {
			errs = append(errs, err)
			fmt.Fprintf(reportW, "MISSING: %s: %s\n", g.Name, err)
			if !collectAll || g.Network {
				return errors.Join(errs...)
			}
			continue
		}
		fmt.Fprintf(reportW, "ok: %s\n", g.Name)
	}
	return errors.Join(errs...)
}

// walkSplitGateRegistry runs registry's non-Network gates then its Network
// gates. It takes that order from splitGateRegistryByNetwork, the same split
// newGatedContext uses to interleave the bwrap gates (gatedcontext.go), so
// doctor's report order cannot diverge from enforcement's after a later edit
// to registry.
func walkSplitGateRegistry(registry []launchGate, c config, checkW, reportW io.Writer, collectAll bool) error {
	nonNetwork, network := splitGateRegistryByNetwork(registry)
	errNonNetwork := walkGateRegistry(nonNetwork, c, checkW, reportW, collectAll)
	errNetwork := walkGateRegistry(network, c, checkW, reportW, collectAll)
	return errors.Join(errNonNetwork, errNetwork)
}
