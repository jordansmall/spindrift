package main

import (
	"errors"
	"io"

	"spindrift.dev/launcher/internal/backend"
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
	// The token gates derive it from their backend row (readOnlyTokenGates).
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

// readOnlyTokenGates returns one network gate per row carrying a
// readOnlyTokenGate, in row order and named read-only-token-<Name>. The gate
// applies under read-only access when c's codeForge or issueTracker shares the
// row's token (tokenGateApplicable), so a renamed row on the same token still
// triggers it.
func readOnlyTokenGates(rows []backendRow) []launchGate {
	var gates []launchGate
	for _, r := range rows {
		if r.readOnlyTokenGate == nil {
			continue
		}
		desc := r.Descriptor
		gates = append(gates, launchGate{
			Name: "read-only-token-" + r.Name,
			Applicable: func(c config) bool {
				return c.boxForgeAndIssueAccess == "read-only" && tokenGateApplicable(c, desc)
			},
			Network: true,
			Check:   r.readOnlyTokenGate,
		})
	}
	return gates
}

// gateRegistry is the ordered set of launch gates shared by the gated tier's
// enforcement path and doctor's reporting path. The two bwrap gates are
// excluded on purpose: doctor reports them separately, with richer dynamic
// Tier and remedy semantics, via bwrapCapabilityChecks in
// bwrap_doctor_checks.go. The read-only token gates follow, one per backend
// row that carries one. It is built once at package init, so a test that swaps
// backendRows at runtime changes tokenGateApplicable but not the gate set.
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
}, readOnlyTokenGates(backendRows)...)

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
// lines (issue #2942 AC5). collectAll lets doctor enumerate every broken
// non-network gate instead of stopping at the first.
func walkGateRegistry(registry []launchGate, c config, checkW io.Writer, rep *doctor.Reporter, collectAll bool) error {
	var errs []error
	for _, g := range registry {
		if g.Applicable != nil && !g.Applicable(c) {
			continue
		}
		if err := g.Check(c, checkW); err != nil {
			errs = append(errs, err)
			rep.Finding(doctor.Required, "%s: %s", g.Name, err)
			if !collectAll || g.Network {
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
func walkSplitGateRegistry(registry []launchGate, c config, checkW io.Writer, rep *doctor.Reporter, collectAll bool) error {
	nonNetwork, network := splitGateRegistryByNetwork(registry)
	errNonNetwork := walkGateRegistry(nonNetwork, c, checkW, rep, collectAll)
	errNetwork := walkGateRegistry(network, c, checkW, rep, collectAll)
	return errors.Join(errNonNetwork, errNetwork)
}
