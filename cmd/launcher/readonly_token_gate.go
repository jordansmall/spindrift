package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge/github"
)

// tokenIntrospectionResult reports what checkReadOnlyTokenGate's GitHub-side
// probe learned about a Box token. Introspectable is false only for a
// fine-grained PAT (github_pat_ prefix), which GitHub exposes no endpoint to
// enumerate; WriteCapable is meaningful only when Introspectable is true.
type tokenIntrospectionResult struct {
	Introspectable bool
	WriteCapable   bool
}

// tokenIntrospector probes token (scoped to repoSlug where the probe itself
// is repo-scoped) for write capability. The production implementation is
// ghTokenIntrospector; tests fake it to avoid a live GitHub call.
type tokenIntrospector func(token, repoSlug string) (tokenIntrospectionResult, error)

// checkReadOnlyTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// startup token gate (issue #1950, sibling to checkReadOnlyCapabilityGate):
// under read-only, the Box must be handed a credential distinct from the
// Launcher's own GH_TOKEN -- otherwise read-only is a prompt-level fiction,
// since the Box would hold the very token that can write. read-write is
// untouched: this never inspects BOX_GH_TOKEN nor calls introspect outside
// read-only, exactly like the capability gate.
func checkReadOnlyTokenGate(c config, introspect tokenIntrospector, w io.Writer) error {
	if c.boxForgeAndIssueAccess != "read-only" {
		return nil
	}
	boxToken := os.Getenv("BOX_GH_TOKEN")
	if boxToken == "" {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to be set to a credential distinct from GH_TOKEN — the Box must never receive the Launcher's own write-capable token")
	}
	if boxToken == c.ghToken {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to differ from GH_TOKEN — it is byte-for-byte identical to the Launcher's own token, which defeats read-only")
	}
	result, err := introspect(boxToken, c.repoSlug)
	if err != nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: introspecting BOX_GH_TOKEN failed: %w", err)
	}
	if !result.Introspectable {
		fmt.Fprintln(w, "WARNING: BOX_GH_TOKEN looks like a fine-grained PAT — GitHub exposes no endpoint to introspect its granted permissions. read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it.")
		return nil
	}
	if result.WriteCapable {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to carry no write scopes; the provided token is write-capable")
	}
	return nil
}

// ghTokenWriteScopes are classic/OAuth scopes (ADR 0027's quickstart audit
// uses the same X-OAuth-Scopes signal) that grant repo write access. Classic
// tokens have no read-only variant of repo access, so either scope's
// presence alone means the token can push.
var ghTokenWriteScopes = map[string]bool{
	"repo":        true,
	"public_repo": true,
}

// ghTokenIntrospector is the production tokenIntrospector. GitHub exposes
// write capability through different signals depending on token shape, so
// this dispatches on the token's prefix rather than calling one endpoint.
// The actual gh calls live in internal/forge/github (TestNoGhExecOutsideForge
// keeps every gh invocation behind the forge seam):
//
//   - github_pat_ (fine-grained PAT): GitHub exposes no endpoint that reports
//     a fine-grained PAT's own restricted grant, so it is not introspectable.
//   - ghp_/gho_ (classic PAT / OAuth app token): github.TokenOAuthScopes
//     reads the X-OAuth-Scopes response header, which enumerates exactly
//     what was granted (ADR 0027's quickstart audit uses the same signal).
//   - ghs_ (GitHub App installation token): has no X-OAuth-Scopes header, but
//     an App identity has no ambient user role to blur the result the way a
//     fine-grained PAT's underlying account would, so
//     github.TokenRepoPushPermission's `permissions.push` field accurately
//     reflects the installation's own grant.
//   - any other/unknown prefix: treated as not introspectable, the same safe
//     default as a fine-grained PAT, rather than trusting a signal that may
//     not mean what it means for the shapes above.
func ghTokenIntrospector(token, repoSlug string) (tokenIntrospectionResult, error) {
	switch {
	case strings.HasPrefix(token, "github_pat_"):
		return tokenIntrospectionResult{Introspectable: false}, nil
	case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "gho_"):
		scopes, err := github.TokenOAuthScopes(token)
		if err != nil {
			return tokenIntrospectionResult{}, err
		}
		writeCapable := false
		for _, s := range scopes {
			if ghTokenWriteScopes[s] {
				writeCapable = true
				break
			}
		}
		return tokenIntrospectionResult{Introspectable: true, WriteCapable: writeCapable}, nil
	case strings.HasPrefix(token, "ghs_"):
		push, err := github.TokenRepoPushPermission(token, repoSlug)
		if err != nil {
			return tokenIntrospectionResult{}, err
		}
		return tokenIntrospectionResult{Introspectable: true, WriteCapable: push}, nil
	default:
		return tokenIntrospectionResult{Introspectable: false}, nil
	}
}
