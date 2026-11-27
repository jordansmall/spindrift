package github

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitremote"
)

// Strategy is the github backend's Go half (ADR 0068). It has no knobs, so
// its closures ignore the Lookup.
var Strategy = backendkit.Strategy{
	Descriptor:        backend.GitHub,
	ConnectorRouted:   true,
	RowGate:           onDefaultHost,
	HostAnswersOpenPR: true,
	ValidateTracker: func(s backendkit.Shared, _ backendkit.Lookup) error {
		return backendkit.RequireRepoSlug(s, backend.GitHub.Name)
	},
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed and PRs opened on " + s.RepoSlug
	},
	IssueReadScope: func(s backendkit.Shared, _ backendkit.Lookup) string { return s.RepoSlug },
	AutoMergeRemedy: func(backendkit.Shared) string {
		return `enable "Allow auto-merge" in repo Settings → General`
	},
	AcquireToken:        acquireTokenWith(introspectToken),
	RecordSandboxTotals: RecordRoutedSandboxTotals,
	// The tracker and Code Forge build without a row and fail each call with
	// ErrNoRoute (routeRow, #6811): a build that failed here would take down
	// every launch path that merely constructs one. A missing runtime is a
	// wiring error, not a missing row, so it does refuse.
	NewIssueTracker: func(s backendkit.Shared, _ backendkit.Lookup) (forge.IssueTracker, error) {
		if err := requireRuntime(s); err != nil {
			return nil, err
		}
		return NewExecClient(s.RepoSlug, s.Labels, WithConnectors(s.Connectors), WithVerdictLabels(s.VerdictLabels), WithRoute(s.Route)), nil
	},
	NewCodeForge: func(s backendkit.Shared, _ backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		if err := requireRuntime(s); err != nil {
			return nil, err
		}
		// Refuse an unhonourable MERGE_METHOD at launch, as the forgejo row
		// does, rather than at the first merge.
		if _, ok := routedRow(s.Connectors); ok {
			if err := checkRoutedMergeMethod(s.MergeMethod); err != nil {
				return nil, err
			}
		}
		return NewExecClient(s.RepoSlug, s.Labels, codeForgeOpts(s)...), nil
	},
	NewLedger: func(s backendkit.Shared, _ backendkit.Lookup) (backendkit.LedgerSite, error) {
		row, err := routeRow(s.Connectors)
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		if err := requireRuntime(s); err != nil {
			return backendkit.LedgerSite{}, err
		}
		return NewExecClient(s.RepoSlug, s.Labels, codeForgeOpts(s)...).ledgerSite(row)
	},
	WebURL: func(s backendkit.Shared, _ backendkit.Lookup) string {
		if s.RepoSlug == "" {
			return ""
		}
		return "https://" + Host() + "/" + s.RepoSlug
	},
	RemoteMatch: func(s backendkit.Shared, _ backendkit.Lookup, remote string) bool {
		if s.RepoSlug == "" {
			return false
		}
		// ParseHostSlug strips any port, so a GH_HOST carrying one never
		// matches and the check skips itself.
		host, slug := gitremote.ParseHostSlug(remote)
		return strings.EqualFold(host, Host()) && strings.EqualFold(slug, s.RepoSlug)
	},
	IntrospectToken: introspectToken,
}

func requireRuntime(s backendkit.Shared) error {
	if s.Connectors == nil {
		return fmt.Errorf("github: %w", connectorruntime.ErrNoRuntime)
	}
	return nil
}

func codeForgeOpts(s backendkit.Shared) []ExecOption {
	return []ExecOption{
		WithConnectors(s.Connectors),
		WithMergeMethod(s.MergeMethod),
		WithSyncMethod(s.SyncMethod),
		WithBaseBranch(s.BaseBranch),
		WithCommitIdentity(s.GitUserName, s.GitUserEmail),
	}
}
