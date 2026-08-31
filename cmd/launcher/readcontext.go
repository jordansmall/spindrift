package main

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/reconcile"
)

// readContext bundles the read-only startup wiring shared by doctor and
// reconcile (issue #2941): the loaded config and the independently-wired
// IssueTracker/CodeForge (ADR 0013). Unlike launchContext (bootstrap.go), it
// never acquires the accumulation lock, and construction itself never builds
// a runner — reconcile's own optional liveness-probe runner is a method on
// this type (reconcileLivenessProbe), built lazily only when called.
type readContext struct {
	config       config
	issueTracker forge.IssueTracker
	codeForge    forge.CodeForge
}

// newReadContext loads config and wires the IssueTracker and CodeForge,
// the read prefix doctor and reconcile now share instead of each building
// its own copy inline (issue #2941). kind and selfContained are applied the
// same way bootstrap() applies them today (issue #2944), so a caller that
// needs the research label family or the no-repo sub-mode gets it through
// the same seam a gatedContext built on top of this validates and gates.
func newReadContext(kind string, selfContained bool) readContext {
	c := applyDispatchKind(loadConfig(), kind)
	c.selfContained = selfContained
	it := newIssueTracker(c)
	return readContext{
		config:       c,
		issueTracker: it,
		codeForge:    newCodeForge(c, local.SanitizedParent{}, it),
	}
}

// reconcileLivenessProbe builds reconcile's LivenessProbe only for a local
// tracker (issue #2941 AC2) — the runner it wraps matters solely for the
// probe's container check, which reconcile only reaches for ISSUE_TRACKER=
// local; any other tracker gets a clean no-op refusal without ever
// constructing a runner.
func (rc readContext) reconcileLivenessProbe(pwd string) reconcile.LivenessProbe {
	if rc.config.issueTracker != "local" {
		return nil
	}
	runnerCfg := runnerConfig(rc.config)
	r := runnerForKind(rc.config, runnerCfg, pwd)
	return reconcile.NewFSProbe(pwd, r)
}
