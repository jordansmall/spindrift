package main

import (
	"sync"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/ecosystem"
)

// doctorCheckSets builds the classify and report halves (issue #3144).
// memoizeCheckProbes shares one *sync.Once per row, so a credential Peeks at
// most once across the one set this call builds -- run-wide that holds only
// at readContext.validation(), the single call site building one set and
// running both halves over it. classify omits the drift and transport rows
// (both the registry-proxy and signal-socket transport rows) and the runner's
// launch-gate prerequisites, which must never make validateConfig exit 2
// (issue #2671, ADR 0045, issue #3114, issue #3728); the per-route rows stay,
// so a bad credential still exits 2. The runner's doctor-only prerequisites
// join classify (issue #3544; see prerequisiteChecks).
func doctorCheckSets(c config) (classify, report []doctor.Check) {
	extra := doctorExtraChecks(c)
	var perRoute, drift []doctor.Check
	// A config that declares no routes gets no route rows at all, not an
	// empty-routes report (issue #3145).
	if c.registryProxyRoutesFile != "" {
		extra = replaceCheckByName(extra, registryProxyRoutesCheckName, registryProxyRoutesCheck(c, false))
		// One parse feeds both row families. A read or parse failure
		// yields nil for both, leaving the failure to registryProxyRoutesCheck.
		if routes, err := loadRegistryRoutes(c.registryProxyRoutesFile, ecosystem.Table); err == nil {
			perRoute = routeChecksFor(routes)
			drift = registryRouteDriftCheckForRoutes(c, routes, ecosystem.Table)
		}
	}
	extra = memoizeCheckProbes(extra)
	perRoute = memoizeCheckProbes(perRoute)
	// Both halves share these memoized rows, so a probe such as `podman
	// machine inspect` runs once per doctorCheckSets(c) call.
	prereqClassify, prereqReport := prerequisiteChecks(c)

	classify = make([]doctor.Check, 0, len(extra)+len(prereqClassify)+len(perRoute))
	classify = append(classify, extra...)
	classify = append(classify, prereqClassify...)
	classify = append(classify, perRoute...)

	signalSocket := signalSocketTransportChecks(c)
	report = make([]doctor.Check, 0, len(extra)+len(prereqReport)+len(perRoute)+len(drift)+len(signalSocket)+1)
	report = append(report, extra...)
	report = append(report, prereqReport...)
	report = append(report, perRoute...)
	report = append(report, drift...)
	report = append(report, registryProxyTransportCheck(c))
	report = append(report, signalSocket...)

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
