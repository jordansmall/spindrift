package forgejo

import (
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/gitremote"
)

// Strategy is the forgejo backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor:        backend.Forgejo,
	ConnectorRouted:   true,
	ValidateTracker:   validateTracker,
	ValidateCodeForge: validate("CODE_FORGE"),
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed on " + s.RepoSlug
	},
	IssueReadScope: func(s backendkit.Shared, _ backendkit.Lookup) string { return s.RepoSlug },
	AutoMergeRemedy: func(s backendkit.Shared) string {
		return fmt.Sprintf(`enable the %q merge style (MERGE_METHOD) in the repo's settings`, MergeStyle(s.MergeMethod))
	},
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return nil, err
		}
		row, err := connectorRow()
		if err != nil {
			return nil, err
		}
		return newConnectorTracker(row, s, k)
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return nil, err
		}
		row, err := connectorRow()
		if err != nil {
			return nil, err
		}
		return newConnectorCodeForge(row, s, k)
	},
	NewLedger: func(s backendkit.Shared, l backendkit.Lookup) (backendkit.LedgerSite, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		// The derivation Compose makes from the row's Git remote, so the Ledger
		// and the clone site name one URL for the path-scoped git credential.
		cloneURL, err := connectorforge.CloneURL(k.ForgejoBaseURL, s.RepoSlug)
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		return backendkit.LedgerSite{URL: cloneURL, Credential: gitexec.StaticCredential(gitCredential)}, nil
	},
	WebURL: func(s backendkit.Shared, l backendkit.Lookup) string {
		base := identityBase(l)
		if s.RepoSlug == "" || base == "" {
			return ""
		}
		return strings.TrimSuffix(base, "/") + "/" + s.RepoSlug
	},
	RemoteMatch: func(s backendkit.Shared, l backendkit.Lookup, remote string) bool {
		base := identityBase(l)
		if s.RepoSlug == "" || base == "" {
			return false
		}
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			return false
		}
		host, slug := gitremote.ParseHostSlug(remote)
		return strings.EqualFold(host, u.Host) && strings.EqualFold(slug, s.RepoSlug)
	},
	KnobsFromRemote: func(remote string) map[string]string {
		host, _ := gitremote.ParseHostSlug(remote)
		if host == "" {
			return nil
		}
		return map[string]string{ForgejoBaseURLEnv: "https://" + host}
	},
	AcquireToken: acquireToken,
}

// identityBase is FORGEJO_BASE_URL for the web and remote-match facets. An
// explicitly blank value yields "" (the launcher rejects it, so those facets
// stay silent), while an unset one takes ReadKnobs' codeberg default.
func identityBase(l backendkit.Lookup) string {
	if v, ok := l(ForgejoBaseURLEnv); ok && v == "" {
		return ""
	}
	k, err := ReadKnobs(l)
	if err != nil {
		return ""
	}
	return k.ForgejoBaseURL
}

// validate checks the forgejo knobs for one axis (ISSUE_TRACKER or
// CODE_FORGE); the axes differ only in the error text. An explicitly blank
// FORGEJO_BASE_URL (an input document can set it) is rejected before ReadKnobs
// re-defaults it to codeberg, which would send a self-hosted FORGEJO_TOKEN
// there.
func validate(axis string) func(backendkit.Shared, backendkit.Lookup) error {
	return func(_ backendkit.Shared, l backendkit.Lookup) error {
		if v, ok := l(ForgejoBaseURLEnv); ok && v == "" {
			return errBaseURLRequired(axis)
		}
		k, err := ReadKnobs(l)
		if err != nil {
			return err
		}
		return ValidateForgejoEnv(axis, k.ForgejoBaseURL, k.ForgejoToken)
	}
}

// validateTracker is the tracker axis's validate plus the REPO_SLUG the issue
// tracker adapter is keyed on.
func validateTracker(s backendkit.Shared, l backendkit.Lookup) error {
	if err := validate("ISSUE_TRACKER")(s, l); err != nil {
		return err
	}
	return backendkit.RequireRepoSlug(s, backend.Forgejo.Name)
}
