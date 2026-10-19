package main

import (
	"fmt"
	"os"
	"strconv"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/ledger"
)

// backendRow is one registry entry for a named backend: axis validity and
// validation, constructors, token knobs (ADR 0038), and the capability fields
// callers key off instead of comparing names (issue #2267). A nil or zero
// field means this backend does not participate in that axis or capability;
// jira's newCodeForge is nil because jira is a tracker only.
type backendRow struct {
	backend.Descriptor

	// validateTracker and validateCodeForge run only when this row is the
	// active ISSUE_TRACKER or CODE_FORGE selection; nil means no validation
	// beyond axis membership.
	validateTracker   func(c config) error
	validateCodeForge func(c config) error

	newIssueTracker      func(c config) forge.IssueTracker
	newCodeForge         func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge
	newReadOnlyCodeForge func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge

	// newLedger returns this CODE_FORGE's butler Ledger backend (issue #3876),
	// the butler.Tree a Chore run scans (issue #3995), and a cleanup func to
	// run once the run is over. nil means
	// this forge cannot host a butler Ledger yet (git).
	newLedger func(c config) (ledger.Backend, butler.Tree, func(), error)

	// remoteMatch reports whether a checkout's origin remote names c's Target
	// repo on this backend (doctor's registry-route-drift guard). nil means
	// the backend has no remote identity and never matches.
	remoteMatch func(c config, remote string) bool

	// introspectToken is the Strategy's IntrospectToken hook, which the
	// read-only token gate (read-only-token-<Name>, issue #1950) calls to probe
	// the Box token. nil means the backend cannot introspect, and the gate warns.
	introspectToken backendkit.TokenIntrospector
}

// remoteLedger builds a Ledger against a hosted forge: a fresh scratch repo
// (ledger.NewRemote), then a butler.Tree fetched from the same url onto
// c.baseBranch (butler.FetchTree) over that same scratch repo -- one
// checkout serving both the Ledger backend and the Chore scan. Shared by the
// github and forgejo rows, which differ only in url and gitArgs.
func remoteLedger(c config, url string, gitArgs ...string) (ledger.Backend, butler.Tree, func(), error) {
	dir, err := os.MkdirTemp("", "spindrift-ledger-*")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("butler: create ledger scratch dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	r, err := ledger.NewRemote(dir, url, gitArgs...)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	tree, err := butler.FetchTree(dir, url, c.baseBranch, gitArgs...)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	// CommitPatch refuses to commit without an identity; FetchTree leaves
	// Name/Email unset since a scan-only Tree never needs one (see GitTree's
	// doc), so production wiring stamps the launcher identity here.
	tree.Name = c.gitUserName
	tree.Email = c.gitUserEmail
	return r, tree, cleanup, nil
}

// backendRows is a view over the Backend Strategies the Bundle registers (ADR
// 0068 decision 1), in the Bundle's registration order, kept so its callers don't
// change yet; callers resolve per-backend behavior through backendByName
// instead of a name switch. The imported Bundle's init runs before this
// initialiser, so the registry is populated by then.
var backendRows = strategyRows()

// rejectBlankForgejoKnobs validates the forgejo knobs from c's resolved
// fields. forgejo.ReadKnobs re-defaults a blank FORGEJO_BASE_URL to
// codeberg, and an input document can set it to "", which would send a
// self-hosted FORGEJO_TOKEN there; this fails closed instead until ADR 0068
// decision 3 drops the config's backend fields. A blank LOCAL_ISSUES_DIR
// is not checked: it re-defaults to .spindrift/issues (issue #5638).
func rejectBlankForgejoKnobs(c config, axis string) error {
	return forgejo.ValidateForgejoEnv(axis, c.forgejoBaseURL, c.forgejoToken)
}

// strategyRows builds one backendRow per registered Strategy, in the Bundle's
// registration order. TestBackendRowsCoverRegistry reports a Strategy missing
// from backend.Registry, and the reverse.
func strategyRows() []backendRow {
	var rows []backendRow
	for _, s := range backendkit.Strategies() {
		rows = append(rows, rowFromStrategy(s))
	}
	return rows
}

func rowFromStrategy(s backendkit.Strategy) backendRow {
	r := backendRow{
		Descriptor:      s.Descriptor,
		introspectToken: s.IntrospectToken,
	}
	var blank func(config, string) error
	if s.Name == backend.Forgejo.Name {
		blank = rejectBlankForgejoKnobs
	}
	if f := s.ValidateTracker; f != nil {
		r.validateTracker = func(c config) error {
			if blank != nil {
				if err := blank(c, "ISSUE_TRACKER"); err != nil {
					return err
				}
			}
			return f(backendShared(c), c.knobLookup)
		}
	}
	if f := s.ValidateCodeForge; f != nil {
		r.validateCodeForge = func(c config) error {
			if blank != nil {
				if err := blank(c, "CODE_FORGE"); err != nil {
					return err
				}
			}
			return f(backendShared(c), c.knobLookup)
		}
	}
	if f := s.NewIssueTracker; f != nil {
		r.newIssueTracker = func(c config) forge.IssueTracker {
			return mustBuild(f(backendShared(c), c.knobLookup))
		}
	}
	if f := s.NewCodeForge; f != nil {
		r.newCodeForge = func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge {
			return mustBuild(f(backendShared(c), c.knobLookup, parent, it))
		}
	}
	if f := s.NewReadOnlyCodeForge; f != nil {
		r.newReadOnlyCodeForge = func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge {
			return mustBuild(f(backendShared(c), c.knobLookup, parent, it))
		}
	}
	if f := s.RemoteMatch; f != nil {
		r.remoteMatch = func(c config, remote string) bool {
			return f(backendShared(c), c.knobLookup, remote)
		}
	}
	if f := s.NewLedger; f != nil {
		r.newLedger = func(c config) (ledger.Backend, butler.Tree, func(), error) {
			site, err := f(backendShared(c), c.knobLookup)
			if err != nil {
				return nil, nil, nil, err
			}
			if site.URL != "" {
				return remoteLedger(c, site.URL, site.GitArgs...)
			}
			return ledger.Local{Repo: site.Repo}, butler.GitTree{Repo: site.Repo}, func() {}, nil
		}
	}
	return r
}

// mustBuild unwraps a Strategy constructor. They fail only when ReadKnobs
// rejects a malformed knob, and knobLookup renders already-typed config
// fields, so the error is unreachable.
func mustBuild[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func backendShared(c config) backendkit.Shared {
	return backendkit.Shared{
		RepoSlug:      c.repoSlug,
		BaseBranch:    c.baseBranch,
		BranchPrefix:  c.branchPrefix,
		GitUserName:   c.gitUserName,
		GitUserEmail:  c.gitUserEmail,
		Labels:        dispatchLabels(c),
		VerdictLabels: researchVerdictLabels(c),
		MergeMode:     c.mergeMode,
		MergeMethod:   c.mergeMethod,
		SyncMethod:    c.syncMethod,
		Tracker:       c.kind().Tracker,
	}
}

// backendKnobs maps each knob a backend's ReadKnobs reads to the launcher
// config field that resolves it.
var backendKnobs = map[string]func(c config) string{
	"FORGEJO_BASE_URL":                 func(c config) string { return c.forgejoBaseURL },
	"FORGEJO_TOKEN":                    func(c config) string { return c.forgejoToken },
	"JIRA_BASE_URL":                    func(c config) string { return c.jiraBaseURL },
	"JIRA_PROJECT_KEY":                 func(c config) string { return c.jiraProjectKey },
	"JIRA_EMAIL":                       func(c config) string { return c.jiraEmail },
	"JIRA_TOKEN":                       func(c config) string { return c.jiraToken },
	"JIRA_STATUS_MAPPING":              func(c config) string { return c.jiraStatusMapping },
	"JIRA_INCLUDE_COMMENTS":            func(c config) string { return strconv.FormatBool(c.jiraIncludeComments) },
	"LOCAL_ISSUES_DIR":                 func(c config) string { return c.localIssuesDir },
	"CODE_FORGE_ACCUMULATION_REPO_DIR": func(c config) string { return c.codeForgeAccumulationRepoDir },
	"CODE_FORGE_REMOTE_URL":            func(c config) string { return c.codeForgeRemoteURL },
}

// knobLookup serves the backend row knobs from c's already-resolved fields,
// so a Strategy's ReadKnobs sees what the launcher resolved, not the process
// environment.
func (c config) knobLookup(env string) (string, bool) {
	if field, ok := backendKnobs[env]; ok {
		return field(c), true
	}
	return "", false
}

// backendByName looks up the registry row for an ISSUE_TRACKER or CODE_FORGE
// knob value; ok is false for an unregistered name.
func backendByName(name string) (backendRow, bool) {
	for _, r := range backendRows {
		if r.Name == name {
			return r, true
		}
	}
	return backendRow{}, false
}

// validTrackerNames returns every ISSUE_TRACKER-valid name in backendRows'
// declaration order. It reads backendRows directly rather than validateChoice,
// which is generated at build time, so a row appended at runtime shows up
// immediately (the issue #2267 AC5 extensibility guarantee).
func validTrackerNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsTracker {
			names = append(names, r.Name)
		}
	}
	return names
}

// validCodeForgeNames returns every CODE_FORGE-valid name in backendRows'
// declaration order. It reads backendRows directly for the same reason
// validTrackerNames does.
func validCodeForgeNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsCodeForge {
			names = append(names, r.Name)
		}
	}
	return names
}

// ledgerCapableNames returns every backendRows name with newLedger != nil, in
// registry order, for butlerPreflight's "supported" list.
func ledgerCapableNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.newLedger != nil {
			names = append(names, r.Name)
		}
	}
	return names
}
