package forgejo

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitremote"
)

// Strategy is the forgejo backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor:        backend.Forgejo,
	ValidateTracker:   validateTracker,
	ValidateCodeForge: validate("CODE_FORGE"),
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed on " + s.RepoSlug
	},
	AutoMergeRemedy: func(s backendkit.Shared) string {
		return fmt.Sprintf(`enable the %q merge style (MERGE_METHOD) in the repo's settings, or change MERGE_METHOD`, MergeStyle(s.MergeMethod))
	},
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewForgejoClient(ForgejoConfig{
			BaseURL:       k.ForgejoBaseURL,
			Repo:          s.RepoSlug,
			Token:         k.ForgejoToken,
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
		}), nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, it forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewForgejoCodeForge(codeForgeConfig(s, k), it), nil
	},
	NewReadOnlyCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, it forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewReadOnlyForgejoCodeForge(codeForgeConfig(s, k), it), nil
	},
	NewLedger: func(s backendkit.Shared, l backendkit.Lookup) (backendkit.LedgerSite, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		return backendkit.LedgerSite{URL: GitRemoteURL(k.ForgejoBaseURL, s.RepoSlug, k.ForgejoToken)}, nil
	},
	// Hand-built rather than GitRemoteURL: this mirrors the Box's former shell
	// clone exactly, including the schemeless-base case GitRemoteURL handles
	// differently. The Box's envLookup maps a blank FORGEJO_BASE_URL to unset,
	// so the codeberg default here agrees with identityBase treating blank as
	// no identity.
	CloneURL: func(s backendkit.Shared, l backendkit.Lookup, _ string) (string, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return "", err
		}
		if k.ForgejoToken == "" {
			return "", errors.New("FORGEJO_TOKEN is required when CODE_FORGE=forgejo")
		}
		base := strings.TrimSuffix(k.ForgejoBaseURL, "/")
		scheme, rest, ok := strings.Cut(base, "://")
		if !ok {
			// Mirrors the shell's ${base%%://*} / ${base#*://}, which yield
			// the whole string for both when there is no scheme.
			scheme, rest = base, base
		}
		return fmt.Sprintf("%s://%s@%s/%s.git", scheme, k.ForgejoToken, rest, s.RepoSlug), nil
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
		return map[string]string{"FORGEJO_BASE_URL": "https://" + host}
	},
	AcquireToken: acquireToken,
}

// identityBase is FORGEJO_BASE_URL for the web and remote-match facets. An
// explicitly blank value yields "" (the launcher rejects it, so those facets
// stay silent), while an unset one takes ReadKnobs' codeberg default.
func identityBase(l backendkit.Lookup) string {
	if v, ok := l("FORGEJO_BASE_URL"); ok && v == "" {
		return ""
	}
	k, err := ReadKnobs(Lookup(l))
	if err != nil {
		return ""
	}
	return k.ForgejoBaseURL
}

// validate checks the forgejo knobs for one axis (ISSUE_TRACKER or
// CODE_FORGE); the axes differ only in the error text.
func validate(axis string) func(backendkit.Shared, backendkit.Lookup) error {
	return func(_ backendkit.Shared, l backendkit.Lookup) error {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return err
		}
		return ValidateForgejoEnv(axis, k.ForgejoBaseURL, k.ForgejoToken)
	}
}

func codeForgeConfig(s backendkit.Shared, k Knobs) ForgejoCodeForgeConfig {
	return ForgejoCodeForgeConfig{
		BaseURL:      k.ForgejoBaseURL,
		Repo:         s.RepoSlug,
		Token:        k.ForgejoToken,
		BaseBranch:   s.BaseBranch,
		UserName:     s.GitUserName,
		UserEmail:    s.GitUserEmail,
		BranchPrefix: s.BranchPrefix,
		MergeMethod:  s.MergeMethod,
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
