package main

import (
	"errors"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/backend"
)

// launchGate is one entry in the ordered gate registry. gatedContext
// (enforcement) and doctor (reporting) both walk the same gateRegistry slice
// through walkGateRegistry, so "enforce order equals report order" holds by
// construction rather than by two hand-maintained lists staying in sync.
type launchGate struct {
	Name string
	// Applicable reports whether g is relevant to c at all; nil means always
	// applicable. The two token gates set this to their own full self-noop
	// condition, so an inapplicable gate (an inactive backend, or a read-write
	// deployment where Check would silently no-op and return nil) is skipped
	// entirely — no Check call, no report line — rather than printing a false
	// "ok" for a check that never ran against anything real.
	Applicable func(c config) bool
	// Network marks a gate whose Check makes a live network call.
	// walkGateRegistry's collectAll mode stops at the first Network gate's
	// failure, since a later probe is moot once an earlier one has failed,
	// while still enumerating every failing non-network gate first.
	Network bool
	Check   func(c config, w io.Writer) error
}

// tokenGateApplicable reports whether c's codeForge or issueTracker resolves to
// a backend sharing desc's TokenEnvVar. It reads each resolved backend's
// TokenEnvVar rather than comparing codeForge/issueTracker to desc's literal
// Name -- a lookup miss yields a zero-value Descriptor with TokenEnvVar=="",
// which never matches, so an unregistered name is inapplicable the same as a
// genuinely non-matching one. Both gateRegistry's Applicable closures and the
// token gates' own self-noop checks call this helper, so the two can never
// disagree about which backend a gate governs.
//
// Gotcha: "unregistered name never matches" holds only because every caller
// passes a desc whose TokenEnvVar is non-empty (backend.GitHub/Forgejo).
// Calling this with a token-less desc (backend.Local, backend.Git) would make a
// lookup miss spuriously match via ""=="".
func tokenGateApplicable(c config, desc backend.Descriptor) bool {
	forgeRow, _ := backendByName(c.codeForge)
	trackerRow, _ := backendByName(c.issueTracker)
	return forgeRow.TokenEnvVar == desc.TokenEnvVar || trackerRow.TokenEnvVar == desc.TokenEnvVar
}

// gateRegistry is the ordered set of launch gates common to both the gated
// tier's enforcement path and doctor's reporting path. The two bwrap gates
// (checkBwrapPastaGate, checkBwrapOverlayGate) are deliberately excluded —
// doctor already reports them separately, with richer dynamic-Tier/remedy
// semantics, via bwrapCapabilityChecks in bwrap_doctor_checks.go.
var gateRegistry = []launchGate{
	{Name: "read-only-capability", Check: func(c config, _ io.Writer) error {
		return checkReadOnlyCapabilityGate(c)
	}},
	{Name: "network-mode-runtime", Check: func(c config, _ io.Writer) error {
		return checkNetworkModeRuntimeGate(c)
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
// network gates, preserving registry order within each. The split comes from
// each gate's own Network field rather than a hardcoded index, so inserting or
// reordering a gate can never silently break it.
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

// walkGateRegistry runs registry's gates against c in order. checkW is handed
// to each gate's own Check closure, so a gate's operator-facing output (e.g.
// the token gate's WARNING) keeps reaching the caller's real writer even when
// reportW is discarded. reportW receives only this function's own "ok: <name>"
// / "MISSING: <name>: <err>" lines — kept separate so enforcement callers can
// suppress the generic report noise without suppressing a gate's own messages.
//
// A gate whose Applicable(c) is false is skipped entirely: no Check call, no
// report line.
//
// collectAll controls how a failure is handled. When false, the first failure
// stops the walk immediately. When true, a failing non-Network gate is recorded
// but the walk continues — so `spindrift doctor` enumerates every
// simultaneously-broken non-network gate — while a failing Network gate still
// stops the walk, since a later live probe is moot once an earlier one failed.
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
// gates, via splitGateRegistryByNetwork — the same split newGatedContext uses
// to interleave the bwrap gates — so a caller that reports on registry's full
// membership can never diverge from enforcement's order, since both derive it
// from the same split rather than one reading raw declaration order.
func walkSplitGateRegistry(registry []launchGate, c config, checkW, reportW io.Writer, collectAll bool) error {
	nonNetwork, network := splitGateRegistryByNetwork(registry)
	errNonNetwork := walkGateRegistry(nonNetwork, c, checkW, reportW, collectAll)
	errNetwork := walkGateRegistry(network, c, checkW, reportW, collectAll)
	return errors.Join(errNonNetwork, errNetwork)
}
