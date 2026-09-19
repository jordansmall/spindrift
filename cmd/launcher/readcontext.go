package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/reconcile"
)

// readContext is the read-only startup wiring doctor and reconcile share
// (issue #2941): config plus an independently-wired IssueTracker and
// CodeForge (ADR 0013). Unlike launchContext it never takes the accumulation
// lock, and construction builds no runner and runs no check: the liveness
// probe and the doctor validation are lazy methods instead (issue #2992).
type readContext struct {
	config       config
	issueTracker forge.IssueTracker
	codeForge    forge.CodeForge
	capabilities forge.Capabilities
}

// newReadContext loads config and wires the tracker and forge (issue #2941).
// It applies kind and selfContained the way bootstrap() does (issue #2944),
// so a caller needing the research label family or the no-repo sub-mode gets
// it through the same seam.
func newReadContext(kind string, selfContained bool) readContext {
	c := applyDispatchKind(loadConfig(), kind)
	c.selfContained = selfContained
	it := newIssueTracker(c)
	cf := newCodeForge(c, local.SanitizedParent{}, it)

	// CODE_FORGE and ISSUE_TRACKER select their backend.Descriptor rows
	// independently (ADR 0013). This calls internal/backend's Nix-generated
	// ByName directly rather than main.go's backendByName, so an
	// unregistered name misses and falls back to a zero-value Descriptor,
	// the same shape tolerated elsewhere.
	forgeDesc, _ := backend.ByName(c.codeForge)
	trackerDesc, _ := backend.ByName(c.issueTracker)
	caps := forge.ResolveCapabilities(cf, it, forgeDesc, trackerDesc)

	return readContext{
		config:       c,
		issueTracker: it,
		codeForge:    cf,
		capabilities: caps,
	}
}

// reconcileLivenessProbe builds reconcile's LivenessProbe only for an
// in-box-unreachable tracker (issue #2941 AC2). The runner it wraps matters
// only for the probe's container check, which reconcile reaches for such a
// tracker alone, so any other tracker skips building a runner entirely.
func (rc readContext) reconcileLivenessProbe(pwd string) reconcile.LivenessProbe {
	if !rc.capabilities.TrackerDescriptor.InBoxUnreachableTracker {
		return nil
	}
	runnerCfg := runnerConfig(rc.config)
	r := runnerForKind(rc.config, runnerCfg, pwd)
	return reconcile.NewFSProbe(pwd, r)
}

// readValidation pairs the full-report verdict on rc.config with the report
// half of the same doctorCheckSets call that produced it (issue #2992). The
// pairing keeps doctorReport from calling doctorCheckSets again for the rows
// it prints, which would rebuild unmemoized Probes and re-Peek every route
// credential it just Peeked (issue #3144).
type readValidation struct {
	configErr    error
	reportChecks []doctor.Check
}

// validation runs cmdDoctor's full-report config classification against
// rc.config through validateConfigChecks, the doctor-only variant that omits
// doctor.RuntimeCheck and the --self-contained check, never validate()'s
// fail-fast dispatch gating (issue #2992). It is a method so that reconcile,
// which never calls it, pays no validation I/O.
func (rc readContext) validation() readValidation {
	classify, report := doctorCheckSets(rc.config)
	return readValidation{
		configErr:    validateConfigChecks(rc.config, classify),
		reportChecks: report,
	}
}
