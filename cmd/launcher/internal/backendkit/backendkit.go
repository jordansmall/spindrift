// Package backendkit is the type a backend author fills in and the registry
// of those fillings (ADR 0068 decision 1). It mirrors driverkit's role. The
// registry lives here rather than in internal/backend because backendkit
// imports internal/forge, which imports internal/backend.
//
// backendkit must not import internal/butler or internal/ledger: their
// closure would bloat every backend's per-package Nix test derivation.
package backendkit

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Lookup resolves one knob's env name to its already-resolved value;
// os.LookupEnv's shape. Each backend converts it to its generated Lookup type
// (e.g. forgejo.Lookup(l)) for ReadKnobs.
type Lookup func(env string) (value string, ok bool)

// Shared carries the cross-backend values every Strategy closure may read; a
// backend's own knobs come through Lookup and its generated ReadKnobs, never a
// field here.
type Shared struct {
	RepoSlug      string
	BaseBranch    string
	BranchPrefix  string
	GitUserName   string
	GitUserEmail  string
	Labels        forge.DispatchLabels
	VerdictLabels forge.VerdictLabels
	MergeMode     string
	MergeMethod   string
	SyncMethod    string
	// Tracker is the tracker instance being built: the research instance rides
	// the label lifecycle only (jira's status mapping is work-only, issue #4919).
	Tracker dispatchkind.Tracker
}

// LedgerSite is where a butler Ledger lives. A hosted forge names a URL (with
// optional git args) the Ledger fetches into a scratch clone; a host-mediated
// forge names a Repo read in place. The caller builds the ledger.Backend and
// butler.Tree from it, so backend packages never import butler.
type LedgerSite struct {
	URL     string
	GitArgs []string
	Repo    string
}

// Strategy is one backend's Go half. A nil closure means the backend does not
// take part in that axis or capability. Constructors return an error because
// ReadKnobs can reject a malformed bool or int knob.
type Strategy struct {
	backend.Descriptor

	ValidateTracker   func(Shared, Lookup) error
	ValidateCodeForge func(Shared, Lookup) error

	NewIssueTracker      func(Shared, Lookup) (forge.IssueTracker, error)
	NewCodeForge         func(Shared, Lookup, forge.SanitizedParent, forge.IssueTracker) (forge.CodeForge, error)
	NewReadOnlyCodeForge func(Shared, Lookup, forge.SanitizedParent, forge.IssueTracker) (forge.CodeForge, error)
	NewLedger            func(Shared, Lookup) (LedgerSite, error)
}

// registry keeps registration order: the Bundle's order is the launcher's
// valid-name order in its error messages.
var registry []Strategy

// Register adds s to the registry, keyed on s.Name. It panics on a duplicate
// name, which is a programming error rather than a runtime condition.
func Register(s Strategy) {
	if _, exists := ByName(s.Name); exists {
		panic("backendkit: duplicate registration for " + s.Name)
	}
	registry = append(registry, s)
}

// ByName returns the registered Strategy; ok is false for an unregistered
// name, with no fallback.
func ByName(name string) (Strategy, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return Strategy{}, false
}

// Strategies returns every registered Strategy in registration order.
func Strategies() []Strategy {
	return append([]Strategy(nil), registry...)
}
