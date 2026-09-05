package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// launcherChecks, launcherRequiredKnobChecks, launcherCrossKnobChecks, and
// doctorExtraChecks below are thin adapters onto the internal/launcherchecks
// package (issue #2725), which now owns the eight Required-tier row
// definitions shared with Quickstart — both are `package main` binaries and
// Go forbids a main-to-main import, so the shared row logic can't live in
// either directly. See internal/launcherchecks/launcherchecks.go for the row
// semantics; what stays here is only what's specific to this binary:
// launcherCheckConfig/launcherCheckDeps (launcherchecksadapter.go) translate
// cmd/launcher's own config and loadedDoc-aware resolveCapabilitySignals
// into the shared package's narrow Config/Deps shapes, and
// registryProxyRoutesCheck below is the one extra cross-knob row this
// binary alone has (Quickstart carries no REGISTRY_PROXY_ROUTES_FILE knob).
func launcherChecks(c config) []doctor.Check {
	return append(launcherRequiredKnobChecks(c), launcherCrossKnobChecks(c)...)
}

// launcherRequiredKnobChecks builds the six Required-tier rows that ran
// before validate()'s validateChoice calls on origin/main: repo-slug,
// git-user-name, git-user-email, gh-token, driver-credentials, runtime.
func launcherRequiredKnobChecks(c config) []doctor.Check {
	return launcherchecks.RequiredKnobChecks(launcherCheckConfig(c), launcherCheckDeps(c))
}

// launcherCrossKnobChecks builds the three Required-tier rows that run
// after validate()'s validateChoice calls: issue-tracker-config,
// code-forge-config, registry-proxy-routes.
func launcherCrossKnobChecks(c config) []doctor.Check {
	return launcherchecks.CrossKnobChecks(launcherCheckConfig(c), launcherCrossKnobDeps(c))
}

// doctorExtraChecks returns launcherChecks(c) with the "runtime" row
// removed: doctor.Run reports runtime validity itself via Config.Runtime's
// own advisory line (non-fatal, distinct from the MISSING framing generic
// extraChecks rows get), so the two never both print for one invocation.
//
// This is the row set validateConfig (main.go) also consumes to classify
// exit 2 "configuration invalid" — bwrapCapabilityChecks(c)'s rows must
// never be folded in here, even though they're Required-tier when
// applicable: a bwrap host missing pasta is an environment/installation
// concern (mirroring doctor.RuntimeCheck's own exclusion above), not a
// configuration fault, so it must not make `spindrift doctor` exit 2 (issue
// #2671 round-1 review finding). See doctorReportChecks
// (bwrap_doctor_checks.go) for the superset runDoctor reports.
func doctorExtraChecks(c config) []doctor.Check {
	return launcherchecks.WithoutRuntime(launcherChecks(c))
}

// registryProxyRoutesCheckName is the registry-proxy-routes row's Name,
// factored into a const so the row's Name field and its SuccessMsg closure
// can't drift apart on a future rename (issue #2853).
const registryProxyRoutesCheckName = "registry-proxy-routes"

// registryProxyRoutesCheck builds the registry-proxy-routes row: the retired-
// knobs gate (validateRetiredRegistryProxyKnobs), then read and Parse of
// c.registryProxyRoutesFile. peekCredentials controls whether Probe also
// walks every parsed route and Peeks its credential.
//
// This row stays in cmd/launcher rather than moving into internal/
// launcherchecks: it reads main-only state (loadRegistryRoutes,
// retiredRegistryProxyKnobsFromEnv, which reads loadedDoc) that Quickstart
// has no equivalent of. launcherCrossKnobDeps (launcherchecksadapter.go) wires
// it in as the one binary-specific extra cross-knob row.
//
// launcherCrossKnobChecks (the launch-gate / exit-2 row, consumed by
// validate() via RunChecksFailFast and by validateConfig via
// doctorExtraChecks) always passes true: those paths never see
// registryRouteChecks' per-route rows (registryroutes_doctor_checks.go), so
// this row is the only place that peeks a route's credential for them.
//
// doctorReportChecks (bwrap_doctor_checks.go) substitutes peekCredentials =
// false when c.registryProxyRoutesFile is set: registryRouteChecks already
// emits one registry-route-credential[<host>] row per route there, each
// Peeking that route's own credential, so leaving this loop enabled too
// would Peek every credential a second time per doctor run. That's not just
// redundant work -- an exec-sourced credential's Peek spawns the
// credential's subprocess (env/file sources are cheap by comparison), so a
// vault or biometric prompt would fire twice per invocation, and one
// unresolvable credential would render as two failing rows over the same
// cause instead of one (issue #3144 review finding).
func registryProxyRoutesCheck(c config, peekCredentials bool) doctor.Check {
	return doctor.Check{
		Name:   registryProxyRoutesCheckName,
		Tier:   doctor.Required,
		Remedy: "set REGISTRY_PROXY_ROUTES_FILE to a TOML routes file declaring registry routes (ADR 0045) -- run `spindrift registry discover <repo-dir> <routes-file>` to generate one from the Target repo's own committed registry config. If the failure instead names a retired scalar REGISTRY_PROXY_* knob (issue #3145), unset it and paste the printed [[routes]] stanza into the routes file (see MIGRATING.md)",
		Probe: func() (any, error) {
			// Fires before the c.registryProxyRoutesFile == "" early return
			// below, and reads the five retired knobs from the ambient
			// environment rather than from c: issue #3145 retires them, so a
			// stale operator setting must be caught whether or not a routes
			// file is set, not just when one happens to be present too.
			if err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobsFromEnv()); err != nil {
				return nil, err
			}
			if c.registryProxyRoutesFile == "" {
				return "not configured", nil
			}
			// Deferred to Probe time, not hoisted out like doctorCheckSets'
			// single load for the per-route/drift rows (bwrap_doctor_
			// checks.go): this row must re-report a read/parse failure on
			// every call, including validate()'s fail-fast path where the
			// per-route rows never run at all.
			routes, err := loadRegistryRoutes(c.registryProxyRoutesFile)
			if err != nil {
				return nil, err
			}
			if peekCredentials {
				for _, route := range routes {
					// Peek (not Resolve) deliberately: this Probe runs ahead
					// of the real per-route resolution
					// (resolveRegistryRoutesFromFile), and an env-sourced
					// credential's Resolve-time os.Unsetenv must fire
					// exactly once, at that later resolution site, not here.
					if _, err := credresolver.New(route.Credential).Peek(); err != nil {
						return nil, fmt.Errorf("route %q: %w", route.MatchHost, err)
					}
				}
			}
			return "configured", nil
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", registryProxyRoutesCheckName, output)
		},
	}
}
