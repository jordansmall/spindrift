package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/forge"
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
	if f := s.ValidateTracker; f != nil {
		r.validateTracker = func(c config) error {
			return f(backendShared(c), c.knobLookup)
		}
	}
	if f := s.ValidateCodeForge; f != nil {
		r.validateCodeForge = func(c config) error {
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
// rejects a malformed knob, and the loader hands ReadKnobs only strings it
// already normalised (a bool is "true"/"false"), so the error is unreachable.
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

// knobLookup serves the row-owned knobs (backend, runner, and chore) from the
// values the launcher resolved into c.rowKnobs, so a Strategy's ReadKnobs sees
// what the launcher resolved, not the process environment. A knob present with
// an empty value is set; ReadKnobs decides what a blank means.
func (c config) knobLookup(env string) (string, bool) {
	v, ok := c.rowKnobs[env]
	return v, ok
}

// knobBool reports whether the row knob env resolved to "true"; the launcher
// normalises boolean knobs with strconv.FormatBool.
func (c config) knobBool(env string) bool {
	v, _ := c.knobLookup(env)
	return v == "true"
}

// setRowKnob stores a row knob's resolved value. It copies the map first so a
// config copied by value never shares writes with the original.
func (c *schemaConfig) setRowKnob(env, value string) {
	m := make(map[string]string, len(c.rowKnobs)+1)
	for k, v := range c.rowKnobs {
		m[k] = v
	}
	m[env] = value
	c.rowKnobs = m
}

// accumulationRepoDirEnv names the local-only knob core code reads by name.
const accumulationRepoDirEnv = "CODE_FORGE_ACCUMULATION_REPO_DIR"

// accumulationRepoDir is the resolved CODE_FORGE_ACCUMULATION_REPO_DIR: the
// absolute Accumulation repo path under CODE_FORGE=local, "" elsewhere unless
// the knob is set. Core code reads it here rather than from the local
// Strategy, which sees the same value through knobLookup.
func (c config) accumulationRepoDir() string {
	v, _ := c.knobLookup(accumulationRepoDirEnv)
	return v
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
