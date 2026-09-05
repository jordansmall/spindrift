// Package launcherchecks holds the doctor.Check row definitions for
// launcher-startup validation — required-knob presence, driver credential/
// construction, runtime validity, and ISSUE_TRACKER/CODE_FORGE row validity
// plus cross-knob checks — shared between cmd/launcher and
// cmd/launcher/quickstart. Both are `package main` binaries and Go forbids
// a main-to-main import, so the row builders that used to live solely in
// cmd/launcher/checks.go move here, parameterized on the narrow Config
// below plus a Deps bundle of caller-supplied seams, instead of on
// cmd/launcher's own ~100-field config (issue #2725).
package launcherchecks

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
)

// Config is the narrow slice of launcher-startup config the rows below
// read — deliberately not cmd/launcher's full config, which also carries
// image/build fields (imageArchive, nixBuilderImage, ...) that have no
// Quickstart equivalent.
type Config struct {
	RepoSlug     string
	GitUserName  string
	GitUserEmail string
	GHToken      string

	Driver              string
	Model               string
	ClaudeOAuthToken    string
	AnthropicAPIKey     string
	OpencodeAuthContent string

	Runtime string

	IssueTracker string
	CodeForge    string

	// ResearchDispatch and SelfContained, together with a
	// Signals.InBoxUnreachableTracker resolved for IssueTracker/CodeForge,
	// feed the REPO_SLUG/GH_TOKEN exemption (repoRequirementExempt below).
	ResearchDispatch bool
	SelfContained    bool
}

// Signals carries the two capability bits the repo-slug/gh-token exemption
// reads for one CodeForge/IssueTracker pairing — a narrower cut than
// cmd/launcher's own four-field capabilitySignals, which also feeds
// mount/box/outbox decisions this package has no part in.
type Signals struct {
	InBoxUnreachableTracker bool
	FullyLocal              bool
}

// Backend is what a cross-knob row needs from one backend-registry row.
// ValidateTracker/ValidateCodeForge arrive already bound to the caller's
// own config (a closure over it), so no config type crosses this seam; a
// nil validator means "no validation beyond axis membership".
type Backend struct {
	ValidAsTracker   bool
	ValidAsCodeForge bool

	ValidateTracker   func() error
	ValidateCodeForge func() error
}

// Deps holds the seams each binary supplies for itself: how to resolve
// capability Signals, how to look up a Backend by name, the valid-name
// lists for the Oxford-joined error text, and any additional cross-knob
// rows the caller wants appended (cmd/launcher's registry-proxy-routes row;
// Quickstart, which has no REGISTRY_PROXY_ROUTES_FILE knob, passes none).
type Deps struct {
	// Signals is required: RequiredKnobChecks calls it while building rows.
	// Backend is required: every cross-knob Probe calls it.
	Signals func(codeForge, issueTracker string) Signals
	Backend func(name string) (Backend, bool)

	// TrackerNames and CodeForgeNames are required too, reached only on a
	// cross-knob row's failure path to name the valid alternatives.
	TrackerNames   func() []string
	CodeForgeNames func() []string

	ExtraCrossKnob []doctor.Check
}

// repoRequirementExempt reports whether c is exempt from the REPO_SLUG/
// GH_TOKEN presence requirement: a fully-local run (both seams local), or a
// self-contained research run whose issue tracker can't be reached from
// inside the Box. It takes an already-resolved sig so a caller building
// multiple rows for the same c (the repo-slug and gh-token rows below)
// resolves capability signals once instead of once per Probe.
func repoRequirementExempt(sig Signals, c Config) bool {
	noRepoResearch := c.ResearchDispatch && c.SelfContained && sig.InBoxUnreachableTracker
	return sig.FullyLocal || noRepoResearch
}

// requiredValue builds a Required-tier Check row whose Remedy and Probe
// error text are the same message msg: name identifies the row, and missing
// reports whether the guarded knob is absent. Uses errors.New rather than
// fmt.Errorf(msg) since msg is a caller-supplied non-constant string, which
// would otherwise trip go vet's non-constant-format-string check.
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

// RequiredKnobChecks builds the six Required-tier rows that must pass
// before a launch is allowed to proceed at all: repo-slug, git-user-name,
// git-user-email, gh-token, driver-credentials, runtime. d.Signals is
// resolved once up front rather than once per Probe, since the repo-slug
// and gh-token rows both need it.
func RequiredKnobChecks(c Config, d Deps) []doctor.Check {
	sig := d.Signals(c.CodeForge, c.IssueTracker)
	return []doctor.Check{
		requiredValue("repo-slug", "set REPO_SLUG=owner/repo (the target GitHub repository)", func() bool {
			return !repoRequirementExempt(sig, c) && c.RepoSlug == ""
		}),
		requiredValue("git-user-name", "set GIT_USER_NAME, or configure git user.name on the host", func() bool {
			return c.GitUserName == ""
		}),
		requiredValue("git-user-email", "set GIT_USER_EMAIL, or configure git user.email on the host", func() bool {
			return c.GitUserEmail == ""
		}),
		requiredValue("gh-token", "set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)", func() bool {
			return !repoRequirementExempt(sig, c) && c.GHToken == ""
		}),
		{
			Name:   "driver-credentials",
			Tier:   doctor.Required,
			Remedy: "set the credential required by DRIVER (CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY for claude, OPENCODE_AUTH_CONTENT for the opencode github-copilot Provider), or fix a typo'd DRIVER value",
			Probe: func() (any, error) {
				switch c.Driver {
				case "", "claude":
					if c.ClaudeOAuthToken == "" && c.AnthropicAPIKey == "" {
						return nil, fmt.Errorf("set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY")
					}
				case "opencode":
					// The github-copilot Provider is OAuth-only (ADR 0009
					// amendment, #260): opencode reads the credential from
					// OPENCODE_AUTH_CONTENT. Require it only when the
					// Copilot Provider is actually selected (MODEL
					// github-copilot/…); other opencode Providers carry
					// their own apiKey via the {env:} config leg.
					if strings.HasPrefix(c.Model, "github-copilot/") && c.OpencodeAuthContent == "" {
						return nil, fmt.Errorf("set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver")
					}
				default:
					// A live guardrail, not dead code (issue #2534 AC4
					// removed this arm; 21a260db reverted that): DRIVER is
					// an operator-set runtime env var nix eval never sees,
					// and driver.New falls back to the claude Driver on its
					// own error rather than failing the run, so an
					// unrecognised DRIVER would otherwise silently produce
					// a confusing wrong-Driver run instead of a clear
					// error.
					if _, err := driver.New(c.Driver); err != nil {
						return nil, err
					}
				}
				return nil, nil
			},
		},
		doctor.RuntimeCheck(c.Runtime),
	}
}

// crossKnobSpec describes one cross-knob row. The issue-tracker-config and
// code-forge-config rows differ only in these fields, so they travel as one
// value rather than as a run of same-typed positional arguments.
//
// rowName is suffixed "-config" rather than bare "issue-tracker"/
// "code-forge" to avoid colliding with doctor.Run's own builtin
// "issue-tracker"/"code-forge" rows, which check live connectivity rather
// than knob validity — doctor's combined output (built-ins plus these rows
// via extraChecks) needs distinct names for the two.
type crossKnobSpec struct {
	rowName  string
	knobName string
	value    string
	remedy   string

	validAs    func(Backend) bool
	validNames func() []string
	validate   func(Backend) func() error
}

// crossKnobCheck builds one cross-knob Required-tier row: look up
// d.Backend(s.value), fail if the row doesn't exist or isn't valid for this
// knob (s.validAs), then run the row's own knob-specific validator if it
// has one.
func crossKnobCheck(s crossKnobSpec, d Deps) doctor.Check {
	return doctor.Check{
		Name:   s.rowName,
		Tier:   doctor.Required,
		Remedy: s.remedy,
		Probe: func() (any, error) {
			row, ok := d.Backend(s.value)
			if !ok || !s.validAs(row) {
				return nil, fmt.Errorf("%s=%q is not valid; must be %s", s.knobName, s.value, JoinOxford(s.validNames()))
			}
			if fn := s.validate(row); fn != nil {
				return nil, fn()
			}
			return nil, nil
		},
	}
}

// CrossKnobChecks builds the two backend-config Required-tier rows —
// issue-tracker-config, code-forge-config — plus any rows d.ExtraCrossKnob
// supplies (cmd/launcher's registry-proxy-routes row; Quickstart passes
// none).
func CrossKnobChecks(c Config, d Deps) []doctor.Check {
	checks := []doctor.Check{
		crossKnobCheck(crossKnobSpec{
			rowName:    "issue-tracker-config",
			knobName:   "ISSUE_TRACKER",
			value:      c.IssueTracker,
			remedy:     "set ISSUE_TRACKER to a supported value and fill in any tracker-specific fields it requires",
			validAs:    func(r Backend) bool { return r.ValidAsTracker },
			validNames: d.TrackerNames,
			validate:   func(r Backend) func() error { return r.ValidateTracker },
		}, d),
		crossKnobCheck(crossKnobSpec{
			rowName:    "code-forge-config",
			knobName:   "CODE_FORGE",
			value:      c.CodeForge,
			remedy:     "set CODE_FORGE to a supported value and fill in any forge-specific fields it requires",
			validAs:    func(r Backend) bool { return r.ValidAsCodeForge },
			validNames: d.CodeForgeNames,
			validate:   func(r Backend) func() error { return r.ValidateCodeForge },
		}, d),
	}
	return append(checks, d.ExtraCrossKnob...)
}

// All concatenates RequiredKnobChecks and CrossKnobChecks, matching the
// order validate() (cmd/launcher's main.go) runs each group in relative to
// its own validateChoice calls: required-knob rows first, cross-knob rows
// after.
func All(c Config, d Deps) []doctor.Check {
	return append(RequiredKnobChecks(c, d), CrossKnobChecks(c, d)...)
}

// WithoutRuntime returns checks with the "runtime" row removed, as a new
// slice that never aliases checks' backing array. doctor.RuntimeCheck's own
// doc comment explains why: a caller like doctor.Run reports runtime
// validity itself via its own advisory line, so the two must never both
// report for one invocation.
func WithoutRuntime(checks []doctor.Check) []doctor.Check {
	out := make([]doctor.Check, 0, len(checks))
	for _, ch := range checks {
		if ch.Name == doctor.RuntimeCheckName {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// JoinOxford joins words into an Oxford-comma "a, b, or c" list: empty
// input yields "", a single word yields itself, two words join with a bare
// "or", and three or more join with commas plus a comma before the
// trailing "or".
func JoinOxford(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " or " + words[1]
	default:
		return strings.Join(words[:len(words)-1], ", ") + ", or " + words[len(words)-1]
	}
}
