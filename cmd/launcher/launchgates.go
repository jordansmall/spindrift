package main

import (
	"errors"
	"io"

	"spindrift.dev/launcher/internal/doctor"
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
	Applicable func(c config) bool
	// Network marks a gate whose Check makes a live network call. Once one
	// fails, walkGateRegistry stops even in walkReport mode, since a later
	// probe is moot.
	Network bool
	Check   func(c config, w io.Writer) error
}

// walkMode selects what walkGateRegistry does with a failing gate.
type walkMode int

const (
	// walkEnforce is the launch path: stop at the first failure.
	walkEnforce walkMode = iota
	// walkReport is doctor's path: enumerate every broken non-network gate.
	walkReport
)

// gateRegistry is the ordered set of launch gates shared by the gated tier's
// enforcement path and doctor's reporting path. The runner family's launch
// gates are excluded on purpose: they are Runner prerequisites, run at launch
// by checkRunnerLaunchPrerequisites and reported by doctor through
// prerequisiteChecks, with richer dynamic Tier and remedy semantics.
var gateRegistry = append([]launchGate{
	{Name: "read-only-capability", Check: func(c config, _ io.Writer) error {
		return checkReadOnlyCapabilityGate(c)
	}},
	{Name: "network-mode-runtime", Check: func(c config, _ io.Writer) error {
		return checkNetworkModeRuntimeGate(c)
	}},
	{Name: "signal-carrier-network-mode", Check: func(c config, _ io.Writer) error {
		return checkSignalCarrierNetworkModeGate(c)
	}},
}, connectorWriteGate)

// connectorWriteGate closes gateRegistry (checkConnectorWriteGate).
var connectorWriteGate = launchGate{
	Name:       connectorWriteGateName,
	Applicable: githubApplies,
	Network:    true,
	Check:      checkConnectorWriteGate,
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
// caller's real writer even when rep is discarding the generic ok and MISSING
// lines (issue #2942 AC5). walkReport lets doctor enumerate every broken
// non-network gate instead of stopping at the first.
func walkGateRegistry(registry []launchGate, c config, checkW io.Writer, rep *doctor.Reporter, mode walkMode) error {
	var errs []error
	for _, g := range registry {
		if g.Applicable != nil && !g.Applicable(c) {
			continue
		}
		err := g.Check(c, checkW)
		if err != nil {
			errs = append(errs, err)
			rep.Finding(doctor.Required, "%s: %s", g.Name, err)
			if mode == walkEnforce || g.Network {
				return errors.Join(errs...)
			}
			continue
		}
		rep.Success("%s", g.Name)
	}
	return errors.Join(errs...)
}

// walkSplitGateRegistry runs registry's non-Network gates then its Network
// gates. It takes that order from splitGateRegistryByNetwork, the same split
// newGatedContext uses to interleave the bwrap gates (gatedcontext.go), so
// doctor's report order cannot diverge from enforcement's after a later edit
// to registry.
func walkSplitGateRegistry(registry []launchGate, c config, checkW io.Writer, rep *doctor.Reporter, mode walkMode) error {
	nonNetwork, network := splitGateRegistryByNetwork(registry)
	errNonNetwork := walkGateRegistry(nonNetwork, c, checkW, rep, mode)
	errNetwork := walkGateRegistry(network, c, checkW, rep, mode)
	return errors.Join(errNonNetwork, errNetwork)
}
