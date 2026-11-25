package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/ledger"
)

// remoteLedger builds a Ledger against a hosted forge: a fresh scratch repo
// (ledger.NewRemote), then a butler.Tree fetched from the same url onto
// c.BaseBranch (butler.FetchTree) over that same scratch repo -- one
// checkout serving both the Ledger backend and the Chore scan. Shared by the
// github and forgejo rows, which differ only in url, credential and gitArgs.
func remoteLedger(c config, url string, cred gitexec.CredentialSource, gitArgs ...string) (ledger.Backend, butler.Tree, func(), error) {
	dir, err := os.MkdirTemp("", "spindrift-ledger-*")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("butler: create ledger scratch dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	r, err := ledger.NewRemoteFrom(dir, url, cred, gitArgs...)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	tree, err := butler.FetchTreeFrom(dir, url, c.BaseBranch, cred, gitArgs...)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	// CommitPatch refuses to commit without an identity; FetchTree leaves
	// Name/Email unset since a scan-only Tree never needs one (see GitTree's
	// doc), so production wiring stamps the launcher identity here.
	tree.Name = c.GitUserName
	tree.Email = c.GitUserEmail
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
		return remoteLedger(c, site.URL, site.Credential, site.GitArgs...)
	}
	return ledger.Local{Repo: site.Repo}, butler.GitTree{Repo: site.Repo}, func() {}, nil
}

// mustBuild unwraps a Strategy constructor. On a native row they fail only
// when ReadKnobs rejects a malformed knob; a bool reader cannot fail, and the
// loader hands ReadKnobs an int only as strconv.Itoa output, so the error is
// unreachable. Production callers return the error instead: a
// Connector row can fail either constructor at runtime.
func mustBuild[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func backendShared(c config) backendkit.Shared {
	return backendkit.Shared{
		RepoSlug:      c.RepoSlug,
		BaseBranch:    c.BaseBranch,
		GitUserName:   c.GitUserName,
		GitUserEmail:  c.GitUserEmail,
		Labels:        dispatchLabels(c),
		Route:         issueRoute(c),
		VerdictLabels: researchVerdictLabels(c),
		MergeMode:     c.MergeMode,
		MergeMethod:   c.MergeMethod,
		SyncMethod:    c.SyncMethod,
		Tracker:       c.kind().Tracker,
	}
}

// issueRoute is ISSUE_ROUTE parsed, nil when unset. A malformed value also
// yields nil: validateIssueRoute rejects it before anything dispatches, and
// backendShared also serves doctor checks, which must report that rejection
// rather than panic.
func issueRoute(c config) forge.Route {
	r, _ := forge.ParseRoute(c.IssueRoute)
	return r
}

// validateIssueRoute is the Go mirror of the Nix eval-time assert (ADR 0075).
// An unregistered tracker is left to the ISSUE_TRACKER checks, so only the
// route's syntax is checked then.
func validateIssueRoute(c config) error {
	s, ok := backendkit.ByName(c.IssueTracker)
	if !ok {
		_, err := forge.ParseIssueRoute(c.IssueRoute)
		return err
	}
	_, err := forge.ResolveRoute(c.IssueRoute, c.IssueTracker, s.RouteKinds)
	return err
}

// knobLookup serves the row-owned knobs (backend, runner, and chore) from the
// values the launcher resolved into c.RowKnobs, so a Strategy's ReadKnobs sees
// what the launcher resolved, not the process environment. A knob present with
// an empty value is set; ReadKnobs decides what a blank means.
func (c config) knobLookup(env string) (string, bool) {
	v, ok := c.RowKnobs[env]
	return v, ok
}

// hostRepoDir is the host-side repo path the code forge's Strategy resolves
// for a HostMediatedRemote backend (the local forge's Accumulation repo), ""
// for every other backend. A resolution error also yields "": the Strategy's
// NewCodeForge reports the same ReadKnobs error at startup.
func (c config) hostRepoDir() string {
	s, _ := backendkit.ByName(c.CodeForge)
	if s.HostRepoDir == nil {
		return ""
	}
	v, err := s.HostRepoDir(backendShared(c), c.knobLookup)
	if err != nil {
		return ""
	}
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
	if s, ok := backendkit.ByName(c.IssueTracker); !ok || !s.ValidAsTracker {
		return &unregisteredBackendError{launcherchecks.NotValidError("ISSUE_TRACKER", c.IssueTracker, validTrackerNames()).Error()}
	}
	if s, ok := backendkit.ByName(c.CodeForge); !ok || !s.ValidAsCodeForge {
		return &unregisteredBackendError{launcherchecks.NotValidError("CODE_FORGE", c.CodeForge, validCodeForgeNames()).Error()}
	}
	return nil
}
