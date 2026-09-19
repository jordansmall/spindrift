package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// launcherChecks and the helpers below adapt internal/launcherchecks, which
// owns the Required-tier rows shared with Quickstart (issue #2725): both are
// package main binaries and Go forbids a main-to-main import, so the shared
// rows cannot live in either.
func launcherChecks(c config) []doctor.Check {
	return append(launcherRequiredKnobChecks(c), launcherCrossKnobChecks(c)...)
}

// launcherRequiredKnobChecks builds the Required-tier rows that run before
// validate()'s validateChoice calls.
func launcherRequiredKnobChecks(c config) []doctor.Check {
	return launcherchecks.RequiredKnobChecks(launcherCheckConfig(c), launcherCheckDeps(c))
}

// launcherCrossKnobChecks builds the Required-tier rows that run after
// validate()'s validateChoice calls.
func launcherCrossKnobChecks(c config) []doctor.Check {
	return launcherchecks.CrossKnobChecks(launcherCheckConfig(c), launcherCrossKnobDeps(c))
}

// doctorExtraChecks returns launcherChecks(c) without the "runtime" row, which
// doctor.Run already reports through Config.Runtime's advisory line.
// validateConfig (main.go) also uses this set to classify exit 2, so
// bwrapCapabilityChecks rows must stay out: a bwrap host missing pasta is an
// installation fault, not a configuration one (issue #2671).
func doctorExtraChecks(c config) []doctor.Check {
	return launcherchecks.WithoutRuntime(launcherChecks(c))
}

// registryProxyRoutesCheckName keeps the row's Name field and its SuccessMsg
// closure from drifting apart on a rename (issue #2853).
const registryProxyRoutesCheckName = "registry-proxy-routes"

// registryProxyRoutesCheck builds the registry-proxy-routes row. It stays in
// cmd/launcher because it reads main-only state (loadRegistryRoutes,
// retiredRegistryProxyKnobsFromEnv) Quickstart has no equivalent of. Pass
// peekCredentials false where doctorCheckSets already emits a per-route
// credential row, so an exec credential's prompt cannot fire twice (#3144).
func registryProxyRoutesCheck(c config, peekCredentials bool) doctor.Check {
	return doctor.Check{
		Name:   registryProxyRoutesCheckName,
		Tier:   doctor.Required,
		Remedy: "set REGISTRY_PROXY_ROUTES_FILE to a TOML routes file declaring registry routes (ADR 0045) -- run `spindrift registry discover <repo-dir> <routes-file>` to generate one from the Target repo's own committed registry config. If the failure instead names a retired scalar REGISTRY_PROXY_* knob (issue #3145), unset it and paste the printed [[routes]] stanza into the routes file (see MIGRATING.md)",
		Probe: func() (any, error) {
			// This gate runs before the early return below and reads the retired
			// knobs from the environment rather than from c, so a stale operator
			// setting fails the check whether or not a routes file is set
			// (issue #3145).
			if err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobsFromEnv()); err != nil {
				return nil, err
			}
			if c.registryProxyRoutesFile == "" {
				return "not configured", nil
			}
			// The Probe loads the file on every call rather than reusing a
			// hoisted load: it must report a read or parse failure even on
			// validate()'s fail-fast path, where the per-route rows never run.
			routes, err := loadRegistryRoutes(c.registryProxyRoutesFile)
			if err != nil {
				return nil, err
			}
			if peekCredentials {
				for _, route := range routes {
					// This peeks rather than resolves: an env-sourced
					// credential's Resolve unsets the variable, and that must
					// happen exactly once, at resolveRegistryRoutesFromFile.
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
