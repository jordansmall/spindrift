package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/ledger"
)

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

// ledgerFor builds s's butler Ledger backend, the butler.Tree a Chore run scans
// (issue #3995), and a cleanup func to run once the run is over. The Strategy's
// NewLedger names where the Ledger lives; this turns that into the concrete
// backends, which backendkit may not import.
func ledgerFor(c config, s backendkit.Strategy) (ledger.Backend, butler.Tree, func(), error) {
	site, err := s.NewLedger(backendShared(c), c.knobLookup)
	if err != nil {
		return nil, nil, nil, err
	}
	if site.URL != "" {
		return remoteLedger(c, site.URL, site.GitArgs...)
	}
	return ledger.Local{Repo: site.Repo}, butler.GitTree{Repo: site.Repo}, func() {}, nil
}

// mustBuild unwraps a Strategy constructor. They fail only when ReadKnobs
// rejects a malformed knob; a bool reader cannot fail, and the loader hands
// ReadKnobs an int only as strconv.Itoa output, so the error is unreachable.
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

// hostTokenValue returns the Launcher's own value of the token env var a row
// names. The GitHub token is a core config field rather than a row knob, so
// knobLookup does not serve it. Any other unserved name falls back to the
// environment, which is empty when the host token came from a _FILE/_CMD form.
func (c config) hostTokenValue(env string) string {
	if v, ok := c.knobLookup(env); ok {
		return v
	}
	if env == backend.GitHub.TokenEnvVar {
		return c.ghToken
	}
	return os.Getenv(env)
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

// strategyNames returns the name of every registered Strategy satisfying keep,
// in registration order. It reads the registry on each call rather than
// validateChoice, which is generated at build time, so a Strategy registered at
// runtime shows up immediately (the issue #2267 AC5 extensibility guarantee).
func strategyNames(keep func(backendkit.Strategy) bool) []string {
	var names []string
	for _, s := range backendkit.Strategies() {
		if keep(s) {
			names = append(names, s.Name)
		}
	}
	return names
}

func validTrackerNames() []string {
	return strategyNames(func(s backendkit.Strategy) bool { return s.ValidAsTracker })
}

func validCodeForgeNames() []string {
	return strategyNames(func(s backendkit.Strategy) bool { return s.ValidAsCodeForge })
}

// ledgerCapableNames is butlerPreflight's "supported" list.
func ledgerCapableNames() []string {
	return strategyNames(func(s backendkit.Strategy) bool { return s.NewLedger != nil })
}

// unregisteredBackendError is checkBackendsRegistered's typed failure, so a
// caller that must still report the bad name as a config finding (doctor) can
// tell it from other read-context errors.
type unregisteredBackendError struct{ msg string }

func (e *unregisteredBackendError) Error() string { return e.msg }

// checkBackendsRegistered rejects an ISSUE_TRACKER or CODE_FORGE that names no
// registered backend of that axis, with validation's wording. newIssueTracker
// and newCodeForge panic on such a name, so a caller that builds them before
// validate() runs must check here first.
func checkBackendsRegistered(c config) error {
	if s, ok := backendkit.ByName(c.issueTracker); !ok || !s.ValidAsTracker {
		return &unregisteredBackendError{launcherchecks.NotValidError("ISSUE_TRACKER", c.issueTracker, validTrackerNames()).Error()}
	}
	if s, ok := backendkit.ByName(c.codeForge); !ok || !s.ValidAsCodeForge {
		return &unregisteredBackendError{launcherchecks.NotValidError("CODE_FORGE", c.codeForge, validCodeForgeNames()).Error()}
	}
	return nil
}
