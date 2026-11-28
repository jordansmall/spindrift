// Package launcherchecks holds the doctor.Check row definitions for
// launcher-startup validation, shared between cmd/launcher and
// cmd/launcher/quickstart: both are package main, and Go forbids a
// main-to-main import (issue #2725).
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

	// Drivers answers for Driver: its credential rules, auth content and, for an
	// unrecognised name, the error naming the known ones.
	Drivers *driver.Set
	Driver  string
	// Models are every model tier the run uses (MODEL and the per-tier
	// *_MODEL), so a credential rule gated on a model prefix sees them all.
	Models []string
	// Credentials maps a credential env name to its value: the Driver's rule
	// variables and the selected backend rows' TokenEnvVars. The adapter fills
	// it from named config fields, so a new rule or row variable needs an
	// entry there.
	Credentials map[string]string

	IssueTracker string
	CodeForge    string

	// IssueRouted is true when ISSUE_ROUTE is set; see repoSlugRequired.
	IssueRouted bool
	// These two, with Signals.InBoxUnreachableTracker, feed the
	// REPO_SLUG/backend-credential exemption (noRepoResearch below).
	ResearchDispatch bool
	SelfContained    bool
}

// Signals carries the two capability bits the repo-slug/backend-credential
// exemption reads for one CodeForge/IssueTracker pairing.
type Signals struct {
	InBoxUnreachableTracker bool
	FullyLocal              bool
}

// Backend is one backend-registry row plus the validators a cross-knob row
// needs, already bound to the caller's own config so no config type crosses
// this seam. A nil validator means no validation beyond axis membership.
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

// noRepoResearch reports a self-contained research run whose issue tracker
// cannot be reached from inside the Box: it clones no Target repo.
func noRepoResearch(sig Signals, c Config) bool {
	return c.ResearchDispatch && c.SelfContained && sig.InBoxUnreachableTracker
}

// credentialExempt reports whether c is exempt from the backend credential
// requirement: a fully-local run, or a noRepoResearch run. REPO_SLUG uses
// repoSlugRequired.
func credentialExempt(sig Signals, c Config) bool {
	return sig.FullyLocal || noRepoResearch(sig, c)
}

// repoSlugRequired reports whether REPO_SLUG must be set. An ISSUE_ROUTE names
// its Target repo through REPO_SLUG, even on a fully-local run, so a routed
// run needs it despite the fully-local exemption. A self-contained research
// run keeps its exemption (no Target repo, no route fragment in its prompt);
// the backend credential has no route override.
func repoSlugRequired(sig Signals, c Config) bool {
	return !noRepoResearch(sig, c) && (!sig.FullyLocal || c.IssueRouted)
}

// missingBackendCredentials names each selected backend row's TokenEnvVar
// with no value in c.Credentials, once per variable. A backend with an empty
// TokenEnvVar (local, git) needs none, and a name that is unknown or not valid
// for its axis is left to the cross-knob rows to reject.
func missingBackendCredentials(c Config, d Deps) []error {
	var errs []error
	seen := map[string]bool{}
	forge, _ := d.Backend(c.CodeForge)
	tracker, _ := d.Backend(c.IssueTracker)
	for _, s := range []struct {
		b     Backend
		valid bool
	}{{forge, forge.ValidAsCodeForge}, {tracker, tracker.ValidAsTracker}} {
		b := s.b
		env := b.TokenEnvVar
		if env == "" || seen[env] || !s.valid {
			continue
		}
		seen[env] = true
		if c.Credentials[env] == "" {
			errs = append(errs, fmt.Errorf("set %s, the credential of the %s backend", env, b.Name))
		}
	}
	return errs
}

// requiredValue builds a Required-tier Check row whose Remedy and Probe error
// are both msg. errors.New, not fmt.Errorf: a non-constant msg trips go
// vet's format-string check.
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
	repoSlugMsg := "pass --repo-slug owner/repo or set forge.repoSlug (the target repository)"
	if c.IssueRouted {
		repoSlugMsg += "; an ISSUE_ROUTE names its Target repo through REPO_SLUG, even on a fully-local run"
	}
	return []doctor.Check{
		requiredValue("repo-slug", repoSlugMsg, func() bool {
			return repoSlugRequired(sig, c) && c.RepoSlug == ""
		}),
		requiredValue("git-user-name", "pass --user-name or set git.user.name, or configure git user.name on the host", func() bool {
			return c.GitUserName == ""
		}),
		requiredValue("git-user-email", "pass --user-email or set git.user.email, or configure git user.email on the host", func() bool {
			return c.GitUserEmail == ""
		}),
		{
			Name:   "backend-credential",
			Tier:   doctor.Required,
			Remedy: "set the credential each selected backend's row names (see the error for which)",
			Probe: func() (any, error) {
				if credentialExempt(sig, c) {
					return nil, nil
				}
				return nil, errors.Join(missingBackendCredentials(c, d)...)
			},
		},
		{
			Name:   "driver-credentials",
			Tier:   doctor.Required,
			Remedy: "set the credential DRIVER's rules require (see the error for which), or fix a typo'd DRIVER value",
			Probe: func() (any, error) {
				rules, ok := c.Drivers.CredentialRules(c.Driver)
				if !ok {
					// Not dead code (#2534 removed this arm; it was reverted):
					// DRIVER is an operator-set env var nix eval never sees,
					// and driver.New falls back to the
					// claude Driver on its own error, so an unrecognised DRIVER
					// would otherwise produce a silent wrong-Driver run.
					_, err := c.Drivers.New(c.Driver)
					return nil, err
				}
				var errs []error
				for _, r := range rules {
					if r.AppliesTo(c.Models...) && !r.Satisfied(func(env string) string { return c.Credentials[env] }) {
						errs = append(errs, errors.New(r.Remedy))
					}
				}
				if a, ok := c.Drivers.AuthContentFor(c.Driver); ok {
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
			remedy:     "pass --tracker or set issues.tracker to a supported value and fill in any tracker-specific fields it requires",
			validAs:    func(r Backend) bool { return r.ValidAsTracker },
			validNames: d.TrackerNames,
			validate:   func(r Backend) func() error { return withoutRepoSlugError(r.ValidateTracker) },
		}, d),
		crossKnobCheck(crossKnobSpec{
			rowName:    "code-forge-config",
			knobName:   "CODE_FORGE",
			value:      c.CodeForge,
			remedy:     "pass --forge-backend or set forge.backend to a supported value and fill in any forge-specific fields it requires",
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
// non-slug failure. A nil fn stays nil ("no validation").
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
