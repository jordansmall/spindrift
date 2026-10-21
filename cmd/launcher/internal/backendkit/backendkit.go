// Package backendkit is the type a backend author fills in and the registry
// of those fillings (ADR 0068 decision 1). It mirrors driverkit's role. The
// registry lives here rather than in internal/backend because backendkit
// imports internal/forge, which imports internal/backend.
//
// backendkit must not import internal/butler or internal/ledger: their
// closure would bloat every backend's per-package Nix test derivation.
package backendkit

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Lookup resolves one knob's env name to its already-resolved value. An alias
// of os.LookupEnv's unnamed func type, identical to each generated Lookup.
type Lookup = func(env string) (value string, ok bool)

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

	// LandedBanner is the clause the all-delivered banner puts after "==> all
	// agents finished — ", for a backend that is a Code Forge. Nil on a
	// tracker-only backend.
	LandedBanner func(Shared) string
	// AutoMergeRemedy is the fix to name when the repo does not allow
	// auto-merge. Nil means the backend has no native auto-merge, so the
	// preflight names no remedy and leaves the backend out of its
	// "CODE_FORGE=..." list.
	AutoMergeRemedy func(Shared) string

	// Remote identity (ADR 0068 decision 1). It is logic, not Descriptor
	// data: forgejo's clone URL embeds its token and defaults to codeberg.
	// CloneURL is the URL the Box clones from; its string argument is the
	// repoMountDir, the Box's read-only mount of a host-mediated repo. WebURL
	// is the repo's browser URL ("" when unknown). RemoteMatch takes a git
	// remote URL and reports whether it names this backend's Target repo.
	CloneURL    func(Shared, Lookup, string) (string, error)
	WebURL      func(Shared, Lookup) string
	RemoteMatch func(Shared, Lookup, string) bool

	// ConfigureCLI sets up the backend's CLI inside the Box; nil means the
	// backend has none to set up.
	ConfigureCLI func(BoxCLI) error

	// OpenPRForBranch asks, from inside the Box, whether branch has an open PR
	// on repoSlug. Nil means the Box cannot ask this forge: it has no in-box
	// client and token for one (forgejo, issue #3942) or no PRs at all (local,
	// git; ADR 0033, ADR 0013).
	OpenPRForBranch func(repoSlug, branch string) (bool, error)

	// Quickstart hooks (ADR 0068 decision 8). KnobsFromRemote returns the
	// backend's own knob values (env name -> value) implied by a git remote
	// URL, or nil when the remote implies none. AcquireToken is the
	// backend's token setup in the wizard: it prompts for (or reuses an
	// ambient) bearer token and validates it however the backend requires,
	// reading its knobs from the Lookup and the wizard's seams from the
	// TokenPrompt.
	KnobsFromRemote func(remote string) map[string]string
	AcquireToken    func(Shared, Lookup, TokenPrompt) (string, error)

	// IntrospectToken probes a Box token for write capability, scoped to repoSlug
	// where the probe itself is repo-scoped. Nil means the backend cannot
	// introspect its Box token, and the read-only gate warns.
	IntrospectToken TokenIntrospector
}

// TokenPrompt is the wizard's side of AcquireToken: plain seams quickstart
// fills, so backend packages never import it and tests replace each one.
type TokenPrompt struct {
	Out          io.Writer
	Getenv       func(string) string
	Prompt       func(string) string
	PromptMasked func(string) string
	// AmbientToken is the host gh CLI's own token, the github fallback for an
	// operator who declines to paste a fine-grained PAT.
	AmbientToken func() (string, error)
	// TokenScopes reads the scopes GitHub reports for a classic token.
	TokenScopes func(token string) ([]string, error)
	// Probe validates token against the live API by building the backend's
	// tracker with it.
	Probe func(token string) error
	// RerunCmd is the command the error hints tell the operator to rerun.
	RerunCmd string
}

// BoxCLI is the Box's process seams a ConfigureCLI setup runs through.
type BoxCLI struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Run      func(*exec.Cmd) error
	Stderr   io.Writer
}

// TokenIntrospection reports what a Box token probe learned. Introspectable is
// false when the hook cannot determine the token's grants. WriteCapable is
// meaningful only when Introspectable is true.
type TokenIntrospection struct {
	Introspectable bool
	WriteCapable   bool
	// Reason is an optional operator-facing explanation shown when
	// Introspectable is false.
	Reason string
}

// TokenIntrospector probes token for write capability, scoped to repoSlug where
// the probe itself is repo-scoped.
type TokenIntrospector func(token, repoSlug string) (TokenIntrospection, error)

// AutoMergeForges names, in slice order, every Strategy that offers an
// auto-merge remedy, joined with " or ".
func AutoMergeForges(ss []Strategy) string {
	var names []string
	for _, s := range ss {
		if s.AutoMergeRemedy != nil {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, " or ")
}

// AutoMergeRemedyClause is " — <remedy>", or "" without the hook so the
// caller can splice it in without a dangling dash.
func (s Strategy) AutoMergeRemedyClause(sh Shared) string {
	if s.AutoMergeRemedy == nil {
		return ""
	}
	return " — " + s.AutoMergeRemedy(sh)
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

// ErrRepoSlugRequired is the blank-REPO_SLUG failure RequireRepoSlug wraps.
var ErrRepoSlugRequired = errors.New("set REPO_SLUG")

// RequireRepoSlug is the ValidateTracker check shared by the backends whose
// issue tracker is keyed on REPO_SLUG.
func RequireRepoSlug(s Shared, trackerName string) error {
	if s.RepoSlug == "" {
		return fmt.Errorf("%w when ISSUE_TRACKER=%s", ErrRepoSlugRequired, trackerName)
	}
	return nil
}

// ReplaceRegistryForTest swaps the registry for ss and returns a func that
// restores the original. It exists so a test in another package can register a
// fake backend or hide a real one; production code registers only through
// Register.
func ReplaceRegistryForTest(ss []Strategy) (restore func()) {
	original := registry
	registry = append([]Strategy(nil), ss...)
	return func() { registry = original }
}
