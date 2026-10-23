// Package launcherchecks holds the doctor.Check row definitions for
// launcher-startup validation, shared between cmd/launcher and
// cmd/launcher/quickstart. Both are package main and Go forbids a
// main-to-main import, so the rows live here, parameterized on the narrow
// Config below plus a Deps bundle of caller-supplied seams (issue #2725).
package launcherchecks

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
)

// Config is the narrow slice of launcher-startup config these rows read, not
// cmd/launcher's full config, whose image/build fields Quickstart lacks.
type Config struct {
	RepoSlug     string
	GitUserName  string
	GitUserEmail string
	GHToken      string

	Driver string
	// Models are every model tier the run uses (MODEL and the per-tier
	// *_MODEL), so a credential rule gated on a model prefix sees them all.
	Models []string
	// Credentials maps a credential env name to its value. The adapter fills it
	// from named config fields, so a new rule variable needs an entry there.
	Credentials map[string]string

	IssueTracker string
	CodeForge    string

	// These two, with Signals.InBoxUnreachableTracker, feed the
	// REPO_SLUG/GH_TOKEN exemption (repoRequirementExempt below).
	ResearchDispatch bool
	SelfContained    bool
}

// Signals carries the two capability bits the repo-slug/gh-token exemption
// reads for one CodeForge/IssueTracker pairing.
type Signals struct {
	InBoxUnreachableTracker bool
	FullyLocal              bool
}

// Backend is one backend-registry row (the embedded Descriptor) plus the
// validators a cross-knob row needs. The validators arrive already bound to the
// caller's own config, so no config type crosses this seam; a nil validator
// means no validation beyond axis membership.
type Backend struct {
	backend.Descriptor

	ValidateTracker   func() error
	ValidateCodeForge func() error
}

// Deps holds the seams each binary supplies for itself. cmd/launcher passes a
// registry-proxy-routes row in ExtraCrossKnob; Quickstart, which has no
// REGISTRY_PROXY_ROUTES_FILE knob, passes none.
type Deps struct {
	// All four functions are required. Only a cross-knob row's failure path
	// calls the name lists, to name the valid alternatives.
	Signals func(codeForge, issueTracker string) Signals
	Backend func(name string) (Backend, bool)

	TrackerNames   func() []string
	CodeForgeNames func() []string

	ExtraCrossKnob []doctor.Check
}

// repoRequirementExempt reports whether c is exempt from the REPO_SLUG/
// GH_TOKEN presence requirement: a fully-local run, or a self-contained
// research run whose issue tracker cannot be reached from inside the Box.
// The caller passes sig in so both guarded rows resolve signals once.
func repoRequirementExempt(sig Signals, c Config) bool {
	noRepoResearch := c.ResearchDispatch && c.SelfContained && sig.InBoxUnreachableTracker
	return sig.FullyLocal || noRepoResearch
}

// ghTokenRequired resolves both backends' descriptors for backend.NeedsGHToken.
// Jira and forgejo trackers check their own tokens through their cross-knob
// validators.
func ghTokenRequired(c Config, d Deps) bool {
	tracker, _ := d.Backend(c.IssueTracker)
	forge, _ := d.Backend(c.CodeForge)
	return backend.NeedsGHToken(forge.Descriptor, tracker.Descriptor)
}

// requiredValue builds a Required-tier Check row whose Remedy and Probe error
// are both msg. It calls errors.New because fmt.Errorf with the
// caller-supplied non-constant msg trips go vet's non-constant-format-string
// check.
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

// RequiredKnobChecks builds the Required-tier rows that must pass before a
// launch proceeds. d.Signals resolves once up front because the repo-slug and
// gh-token rows both need it.
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
			return !repoRequirementExempt(sig, c) && ghTokenRequired(c, d) && c.GHToken == ""
		}),
		{
			Name:   "driver-credentials",
			Tier:   doctor.Required,
			Remedy: "set the credential DRIVER's rules require (see the error for which), or fix a typo'd DRIVER value",
			Probe: func() (any, error) {
				rules, ok := driver.CredentialRules(c.Driver)
				if !ok {
					// A live guardrail, not dead code (#2534 AC4 removed this
					// arm; 21a260db reverted it): DRIVER is an operator-set env
					// var nix eval never sees, and driver.New falls back to the
					// claude Driver on its own error, so an unrecognised DRIVER
					// would otherwise produce a silent wrong-Driver run.
					_, err := driver.New(c.Driver)
					return nil, err
				}
				lookup := func(env string) string { return c.Credentials[env] }
				var errs []error
				for _, r := range rules {
					if r.AppliesTo(c.Models...) && !r.Satisfied(lookup) {
						errs = append(errs, errors.New(r.Remedy))
					}
				}
				if err := driver.CheckClosedCredentials(c.Driver, c.Models, lookup); err != nil {
					errs = append(errs, err)
				}
				if a, ok := driver.AuthContentFor(c.Driver); ok {
					if content := c.Credentials[a.Env]; content != "" {
						if err := a.Check(content, rules); err != nil {
							errs = append(errs, err)
						}
					}
				}
				return nil, errors.Join(errs...)
			},
		},
	}
}

// crossKnobSpec describes one cross-knob row. rowName carries the "-config"
// suffix to avoid colliding with doctor.Run's own builtin "issue-tracker"/
// "code-forge" rows, which check live connectivity rather than knob validity.
type crossKnobSpec struct {
	rowName  string
	knobName string
	value    string
	remedy   string

	validAs    func(Backend) bool
	validNames func() []string
	validate   func(Backend) func() error
}

func crossKnobCheck(s crossKnobSpec, d Deps) doctor.Check {
	return doctor.Check{
		Name:   s.rowName,
		Tier:   doctor.Required,
		Remedy: s.remedy,
		Probe: func() (any, error) {
			row, ok := d.Backend(s.value)
			if !ok || !s.validAs(row) {
				return nil, NotValidError(s.knobName, s.value, s.validNames())
			}
			if fn := s.validate(row); fn != nil {
				return nil, fn()
			}
			return nil, nil
		},
	}
}

// CrossKnobChecks builds the issue-tracker-config and code-forge-config rows,
// plus any rows d.ExtraCrossKnob supplies.
func CrossKnobChecks(c Config, d Deps) []doctor.Check {
	checks := []doctor.Check{
		crossKnobCheck(crossKnobSpec{
			rowName:    "issue-tracker-config",
			knobName:   "ISSUE_TRACKER",
			value:      c.IssueTracker,
			remedy:     "set ISSUE_TRACKER to a supported value and fill in any tracker-specific fields it requires",
			validAs:    func(r Backend) bool { return r.ValidAsTracker },
			validNames: d.TrackerNames,
			validate:   func(r Backend) func() error { return withoutRepoSlugError(r.ValidateTracker) },
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

// All concatenates the two groups in the order cmd/launcher's validate() runs
// them relative to its own validateChoice calls: required-knob rows first.
func All(c Config, d Deps) []doctor.Check {
	return append(RequiredKnobChecks(c, d), CrossKnobChecks(c, d)...)
}

// NotValidError is the "is not valid; must be" failure for a knob whose value
// names no backend of its axis, shared by doctor and launch-time validation so
// the two wordings cannot drift.
func NotValidError(knob, value string, valid []string) error {
	return fmt.Errorf("%s=%q is not valid; must be %s", knob, value, JoinOxford(valid))
}

// JoinOxford joins words into an Oxford-comma "a, b, or c" list; two words
// join with a bare "or".
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

// withoutRepoSlugError drops a blank-REPO_SLUG failure from fn: the repo-slug
// row owns it, so one root cause shows on one row. It matches only the error
// RequireRepoSlug returns, not errors.Is, so a joined error never hides a
// non-slug failure. A nil fn must stay nil: crossKnobCheck reads nil as "no
// validation".
func withoutRepoSlugError(fn func() error) func() error {
	if fn == nil {
		return nil
	}
	return func() error {
		if err := fn(); errors.Unwrap(err) != backendkit.ErrRepoSlugRequired {
			return err
		}
		return nil
	}
}
