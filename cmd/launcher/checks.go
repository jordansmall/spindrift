package main

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
)

// launcherChecks builds the Required-tier doctor.Check rows that are the
// launcher-startup validation: required-knob presence, driver
// credential/construction, runtime validity, and ISSUE_TRACKER/CODE_FORGE row
// validity plus cross-knob checks. Each Probe returns the exact error
// validate() (main.go) surfaces for that condition, by running these same rows
// through doctor.RunChecksFailFast.
//
// The nine rows are split into two ordered groups matching where validate()
// runs them: launcherRequiredKnobChecks (6) before its validateChoice calls,
// launcherCrossKnobChecks (3) after them. The validateChoice calls themselves,
// the --self-contained dispatch-kind check, and forge.ParseResearchVerdicts
// stay out of scope.
func launcherChecks(c config) []doctor.Check {
	return append(launcherRequiredKnobChecks(c), launcherCrossKnobChecks(c)...)
}

// doctorExtraChecks returns launcherChecks(c) with the "runtime" row removed:
// doctor.Run reports runtime validity itself via Config.Runtime's own advisory
// line, so the two never both print for one invocation.
//
// This is also the row set validateConfig (main.go) consumes to classify exit
// 2 "configuration invalid", so bwrapCapabilityChecks(c)'s rows must never be
// folded in here even though they are Required-tier when applicable: a bwrap
// host missing pasta is an environment/installation concern, not a
// configuration fault, and must not make `spindrift doctor` exit 2.
func doctorExtraChecks(c config) []doctor.Check {
	checks := launcherChecks(c)
	out := make([]doctor.Check, 0, len(checks))
	for _, ch := range checks {
		if ch.Name == doctor.RuntimeCheckName {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// repoRequirementExemptionFor reports whether c is exempt from the
// REPO_SLUG/GH_TOKEN presence requirement: a fully-local run (both seams
// local), or a self-contained research run whose issue tracker can't be
// reached from inside the Box. It takes an already-resolved sig so a caller
// building several rows for the same c resolves capability signals once
// rather than once per Probe.
func repoRequirementExemptionFor(sig capabilitySignals, c config) bool {
	noRepoResearch := c.dispatchKind == dispatchKindResearch && c.selfContained && sig.inBoxUnreachableTracker
	return sig.fullyLocal || noRepoResearch
}

// requiredValue builds a Required-tier Check row whose Remedy and Probe error
// text are the same message msg: name identifies the row, and missing reports
// whether the guarded knob is absent. Uses errors.New rather than
// fmt.Errorf(msg) because msg is a caller-supplied non-constant string, which
// would trip go vet's non-constant-format-string check.
func requiredValue(name, msg string, missing func() bool) doctor.Check {
	return doctor.Check{
		Name:   name,
		Tier:   doctor.Required,
		Remedy: msg,
		Probe: func() (any, error) {
			if missing() {
				return nil, errors.New(msg)
			}
			return nil, nil
		},
	}
}

// launcherRequiredKnobChecks builds the six Required-tier rows that run before
// validate()'s validateChoice calls: repo-slug, git-user-name, git-user-email,
// gh-token, driver-credentials, runtime.
func launcherRequiredKnobChecks(c config) []doctor.Check {
	sig := resolveCapabilitySignals(c.codeForge, c.issueTracker)
	return []doctor.Check{
		requiredValue("repo-slug", "set REPO_SLUG=owner/repo (the target GitHub repository)", func() bool {
			return !repoRequirementExemptionFor(sig, c) && c.repoSlug == ""
		}),
		requiredValue("git-user-name", "set GIT_USER_NAME, or configure git user.name on the host", func() bool {
			return c.gitUserName == ""
		}),
		requiredValue("git-user-email", "set GIT_USER_EMAIL, or configure git user.email on the host", func() bool {
			return c.gitUserEmail == ""
		}),
		requiredValue("gh-token", "set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)", func() bool {
			return !repoRequirementExemptionFor(sig, c) && c.ghToken == ""
		}),
		{
			Name:   "driver-credentials",
			Tier:   doctor.Required,
			Remedy: "set the credential required by DRIVER (CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY for claude, OPENCODE_AUTH_CONTENT for the opencode github-copilot Provider), or fix a typo'd DRIVER value",
			Probe: func() (any, error) {
				switch c.driver {
				case "", "claude":
					if c.claudeOAuthToken == "" && c.anthropicAPIKey == "" {
						return nil, fmt.Errorf("set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY")
					}
				case "opencode":
					// The github-copilot Provider is OAuth-only (ADR 0009 amendment):
					// opencode reads the credential from OPENCODE_AUTH_CONTENT. Required
					// only when that Provider is actually selected (MODEL github-copilot/…);
					// other opencode Providers carry their own apiKey via the {env:} leg.
					if strings.HasPrefix(c.model, "github-copilot/") && c.opencodeAuthContent == "" {
						return nil, fmt.Errorf("set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver")
					}
				default:
					// DRIVER is an operator-set *runtime* env var nix eval
					// never sees, and newDriver() silently falls back to the
					// claude Driver on driver.New's error rather than failing
					// the run -- without this arm an unrecognised DRIVER
					// produces a confusing wrong-Driver run instead of a clear
					// error. TestValidateDriver_RejectsUnknown pins it.
					if _, err := driver.New(c.driver); err != nil {
						return nil, err
					}
				}
				return nil, nil
			},
		},
		doctor.RuntimeCheck(c.runtime),
	}
}

// crossKnobCheck builds one cross-knob Required-tier row: look up
// backendByName(value), fail if the row doesn't exist or isn't valid for this
// knob (validAs), then run the row's own knob-specific validator if it has
// one. The issue-tracker-config and code-forge-config rows share this shape,
// differing only in which knob and backendRow field each checks.
//
// rowName is suffixed "-config" rather than bare "issue-tracker"/"code-forge"
// to avoid colliding with doctor.Run's own builtin rows of those names, which
// check live connectivity rather than knob validity — doctor's combined output
// needs distinct names for the two.
func crossKnobCheck(rowName, knobName, value, remedy string, validAs func(backendRow) bool, validNames func() []string, validateFn func(backendRow) func(config) error, c config) doctor.Check {
	return doctor.Check{
		Name:   rowName,
		Tier:   doctor.Required,
		Remedy: remedy,
		Probe: func() (any, error) {
			row, ok := backendByName(value)
			if !ok || !validAs(row) {
				return nil, fmt.Errorf("%s=%q is not valid; must be %s", knobName, value, joinOxford(validNames()))
			}
			if fn := validateFn(row); fn != nil {
				return nil, fn(c)
			}
			return nil, nil
		},
	}
}

// launcherCrossKnobChecks (below) builds the three Required-tier rows that run
// after validate()'s validateChoice calls: issue-tracker-config,
// code-forge-config, and registry-proxy-credential — the last folding in
// ADR 0044's credential mutual-exclusion check so it gets its own named row in
// the `spindrift doctor` table and the same fail-fast position as the others.
//
// registryProxyCredentialCheckName is that row's Name, a const so the Name
// field and the SuccessMsg closure can't drift apart on a rename.
const registryProxyCredentialCheckName = "registry-proxy-credential"

func launcherCrossKnobChecks(c config) []doctor.Check {
	return []doctor.Check{
		crossKnobCheck("issue-tracker-config", "ISSUE_TRACKER", c.issueTracker,
			"set ISSUE_TRACKER to a supported value and fill in any tracker-specific fields it requires",
			func(r backendRow) bool { return r.ValidAsTracker },
			validTrackerNames,
			func(r backendRow) func(config) error { return r.validateTracker },
			c),
		crossKnobCheck("code-forge-config", "CODE_FORGE", c.codeForge,
			"set CODE_FORGE to a supported value and fill in any forge-specific fields it requires",
			func(r backendRow) bool { return r.ValidAsCodeForge },
			validCodeForgeNames,
			func(r backendRow) func(config) error { return r.validateCodeForge },
			c),
		{
			Name:   registryProxyCredentialCheckName,
			Tier:   doctor.Required,
			Remedy: "set at most one of REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV: a registry proxy credential names exactly one source; if REGISTRY_PROXY_UPSTREAM_URL is set and a source is configured, that source must actually resolve (file present/non-empty/single-line, or env var set/non-empty)",
			Probe: func() (any, error) {
				if err := validateRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv); err != nil {
					return nil, err
				}
				// REGISTRY_PROXY_UPSTREAM_URL is runtime-only (never a flake
				// value, per lib/env-schema.nix), while the credential fields
				// may be committed in flake.nix as standing config. Leaving the
				// upstream URL unset disables the proxy entirely -- the
				// documented opt-out, not a broken declaration -- so a leftover
				// credential source is reported, not an error. It is still a
				// distinct situation from nothing being set, hence two
				// messages.
				if c.registryProxyUpstreamURL == "" {
					if c.registryProxyCredentialFile != "" || c.registryProxyCredentialEnv != "" {
						return "not configured (credential source set, REGISTRY_PROXY_UPSTREAM_URL unset)", nil
					}
					return "not configured", nil
				}
				if c.registryProxyCredentialFile == "" && c.registryProxyCredentialEnv == "" {
					return "unauthenticated", nil
				}
				// peekRegistryProxyCredential, not
				// resolveRegistryProxyCredential: this Probe runs ahead of
				// bootstrap.go's own resolve call, whose os.Unsetenv side
				// effect must fire exactly once, at the real resolution site.
				if _, err := peekRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv); err != nil {
					return nil, err
				}
				return "configured", nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("%s (%s)", registryProxyCredentialCheckName, output)
			},
		},
	}
}
