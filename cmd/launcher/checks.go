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
// credential/construction, runtime validity, and the ISSUE_TRACKER/CODE_FORGE
// row validity plus cross-knob checks. Each Probe returns the exact error
// validate() (main.go) surfaces for that condition by running these same
// rows through doctor.RunChecksFailFast (issue #2559). See doctor.go for
// runDoctor and main.go for validateConfig.
//
// The ten rows are split into two ordered groups —
// launcherRequiredKnobChecks (6 rows) and launcherCrossKnobChecks (4 rows) —
// matching where validate() runs them: the six required-knob rows run
// before validate()'s validateChoice calls (MERGE_MODE, MERGE_METHOD,
// SYNC_METHOD, OVERLAP_GATE), and the four cross-knob rows run after those
// calls (and before BOX_FORGE_AND_ISSUE_ACCESS), fail-fast and gating
// dispatch. launcherChecks concatenates both groups; doctorExtraChecks below
// (runtime filtered out) feeds two different `spindrift doctor` consumers:
// runDoctor, where every row runs informational-only rather than
// fail-fast, and validateConfig, which runs the same rows to classify exit 2
// "configuration invalid" (issue #2569) — the two never disagree about which
// rows count as "configuration" because both read the identical row set.
// validateChoice calls themselves, the --self-contained dispatch-kind check,
// and forge.ParseResearchVerdicts stay out of scope — not part of
// "required-knob presence, runtime validity, driver construction, credential
// presence, cross-knob conditional requirements" per the issue.
func launcherChecks(c config) []doctor.Check {
	return append(launcherRequiredKnobChecks(c), launcherCrossKnobChecks(c)...)
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
// reached from inside the Box. It takes an already-resolved sig, so a
// caller building multiple rows for the same c (launcherRequiredKnobChecks'
// repo-slug and gh-token rows) resolves capability signals once instead of
// once per Probe.
func repoRequirementExemptionFor(sig capabilitySignals, c config) bool {
	noRepoResearch := c.dispatchKind == dispatchKindResearch && c.selfContained && sig.inBoxUnreachableTracker
	return sig.fullyLocal || noRepoResearch
}

// requiredValue builds a Required-tier Check row whose Remedy and Probe
// error text are the same message msg: name identifies the row, and missing
// reports whether the guarded knob is absent. Extracted because the
// repo-slug/git-user-name/git-user-email/gh-token rows below each repeated
// their message byte-for-byte as both Remedy and the Probe's error text.
// Uses errors.New rather than fmt.Errorf(msg) since msg is a caller-supplied
// non-constant string, so fmt.Errorf(msg) would trip go vet's
// non-constant-format-string check.
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

// launcherRequiredKnobChecks builds the six Required-tier rows that ran
// before validate()'s validateChoice calls on origin/main: repo-slug,
// git-user-name, git-user-email, gh-token, driver-credentials, runtime.
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
					// The github-copilot Provider is OAuth-only (ADR 0009 amendment, #260):
					// opencode reads the credential from OPENCODE_AUTH_CONTENT. Require it only
					// when the Copilot Provider is actually selected (MODEL github-copilot/…);
					// other opencode Providers carry their own apiKey via the {env:} config leg.
					if strings.HasPrefix(c.model, "github-copilot/") && c.opencodeAuthContent == "" {
						return nil, fmt.Errorf("set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver")
					}
				default:
					// Deviation from issue #2534 AC4 ("the launcher's dead validation
					// arm is gone"): that AC assumed this arm was a pointless re-check
					// of a name newDriver() already re-derives and nix eval-time
					// generation already guarantees valid. Removing it (ba9472a5) was
					// reverted by 21a260db: DRIVER is an operator-set *runtime* env
					// var nix eval never sees, validate()'s switch had no default arm
					// to catch a typo, and newDriver() silently falls back to the
					// claude Driver on driver.New's error instead of failing the run —
					// so an unrecognised DRIVER produced a confusing wrong-Driver run,
					// not a clear error. This arm is a distinct, live guardrail, not
					// the dead one AC4 describes; TestValidateDriver_RejectsUnknown
					// pins it.
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
// backendByName(value), fail if the row doesn't exist or isn't valid for
// this knob (validAs), then run the row's own knob-specific validator if it
// has one. The issue-tracker-config and code-forge-config rows share this
// exact shape, differing only in which knob and backendRow field each
// checks.
//
// rowName is suffixed "-config" rather than bare "issue-tracker"/"code-forge"
// to avoid colliding with doctor.Run's own builtin "issue-tracker"/
// "code-forge" rows (doctor.go), which check live connectivity, not
// knob validity — the two are different checks and doctor's combined output
// (built-ins + launcherChecks via extraChecks) needs distinct names for them.
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

// launcherCrossKnobChecks builds the four Required-tier rows that run after
// validate()'s validateChoice calls: issue-tracker-config, code-forge-config,
// registry-proxy-credential, registry-proxy-upstream-url. The credential row
// folds in the registry-proxy-credential mutual-exclusion check (ADR 0044)
// that used to be a hand-written call in both validate() and
// validateConfig() (main.go) ahead of these rows; moving it here gives it
// its own named row in the `spindrift doctor` status table and puts it in
// the same fail-fast position as the other cross-knob rows, after the
// validateChoice calls, instead of ahead of all of them. The upstream-url
// row (issue #3084) gates REGISTRY_PROXY_UPSTREAM_URL the same way.
// registryProxyCredentialCheckName is the registry-proxy-credential row's
// Name, factored into a const so the row's Name field and its SuccessMsg
// closure can't drift apart on a future rename (issue #2853).
const registryProxyCredentialCheckName = "registry-proxy-credential"

// registryProxyUpstreamURLCheckName is the registry-proxy-upstream-url row's
// Name, factored into a const for the same reason as
// registryProxyCredentialCheckName above (issue #2853).
const registryProxyUpstreamURLCheckName = "registry-proxy-upstream-url"

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
			Remedy: "set at most one of REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV: a registry proxy credential names exactly one source; if REGISTRY_PROXY_UPSTREAM_URL is set and a source is configured, that source must actually resolve (file present/non-empty/single-line for the default REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=raw, or a machine entry matching REGISTRY_PROXY_UPSTREAM_URL's host for REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=netrc, or a [registries.<REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME>] table with a token field present for REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=cargo-credentials, or env var set/non-empty)",
			Probe: func() (any, error) {
				if err := validateRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv); err != nil {
					return nil, err
				}
				// REGISTRY_PROXY_UPSTREAM_URL is a runtime-only value (never a
				// flake value, per lib/env-schema.nix), while the credential
				// fields may be committed in flake.nix as standing config. A
				// run that leaves the upstream URL unset disables the proxy
				// entirely regardless of what the credential fields say --
				// that's the documented opt-out, not a broken declaration, so
				// a leftover credential source here is reported, not an error.
				// It's still a distinct situation from nothing being set at
				// all, so the two render different messages (issue #2853)
				// even though neither is fatal.
				if c.registryProxyUpstreamURL == "" {
					if c.registryProxyCredentialFile != "" || c.registryProxyCredentialEnv != "" {
						return "not configured (credential source set, REGISTRY_PROXY_UPSTREAM_URL unset)", nil
					}
					return "not configured", nil
				}
				if c.registryProxyCredentialFile == "" && c.registryProxyCredentialEnv == "" {
					return "unauthenticated", nil
				}
				// peekRegistryProxyCredential (not resolveRegistryProxyCredential)
				// deliberately: this Probe runs ahead of bootstrap.go's own
				// resolveRegistryProxyCredential call, and that call's
				// os.Unsetenv side effect must fire exactly once, at the real
				// resolution site, not here.
				if _, err := peekRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv, c.registryProxyCredentialFileFormat, c.registryProxyUpstreamURL, c.registryProxyCredentialCargoRegistryName); err != nil {
					return nil, err
				}
				return "configured", nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("%s (%s)", registryProxyCredentialCheckName, output)
			},
		},
		{
			Name:   registryProxyUpstreamURLCheckName,
			Tier:   doctor.Required,
			Remedy: "set REGISTRY_PROXY_UPSTREAM_URL to a bare origin with no path (e.g. https://registry.example.com) -- a path here doubles onto every proxied request and guarantees 404s upstream (ADR 0044)",
			Probe: func() (any, error) {
				if c.registryProxyUpstreamURL == "" {
					return "not configured", nil
				}
				if err := validateRegistryProxyUpstreamURL(c.registryProxyUpstreamURL); err != nil {
					return nil, err
				}
				return "configured", nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("%s (%s)", registryProxyUpstreamURLCheckName, output)
			},
		},
	}
}
