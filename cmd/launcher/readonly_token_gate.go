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
//
// verified reports whether the Box token's non-write-capability was actually
// confirmed by introspection (true) versus accepted on trust because the
// token isn't introspectable, e.g. a fine-grained PAT (false) -- callers that
// print a success message use this to avoid claiming more certainty than the
// gate actually established. verified is always false when err != nil.
func checkReadOnlyTokenGate(c config, introspect tokenIntrospector, w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
		return false, nil
	}
	boxToken := os.Getenv("BOX_GH_TOKEN")
	if boxToken == "" {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to be set to a credential distinct from GH_TOKEN — the Box must never receive the Launcher's own write-capable token")
	}
	if boxToken == c.ghToken {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to differ from GH_TOKEN — it is byte-for-byte identical to the Launcher's own token, which defeats read-only")
	}
	result, err := introspect(boxToken, c.repoSlug)
	if err != nil {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: introspecting BOX_GH_TOKEN failed: %w", err)
	}
	if !result.Introspectable {
		fmt.Fprintln(w, "WARNING: BOX_GH_TOKEN's write capability could not be determined (e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect). read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it.")
		return false, nil
	}
	if result.WriteCapable {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to carry no write scopes; the provided token is write-capable")
	}
	return true, nil
}

// ghTokenWriteScopes are classic/OAuth scopes (ADR 0027's quickstart audit
// uses the same X-OAuth-Scopes signal) that grant repo write access. Classic
// tokens have no read-only variant of repo access, so either scope's
// presence alone means the token can push.
var ghTokenWriteScopes = map[string]bool{
	"repo":        true,
	"public_repo": true,
}

// newGHTokenIntrospector builds a tokenIntrospector that dispatches on the
// token's prefix rather than calling one endpoint, since GitHub exposes
// write capability through different signals depending on token shape.
// oauthScopes and repoPush are injected so the prefix-dispatch and
// write-scope-matching logic below can be unit-tested without a live gh
// call; ghTokenIntrospector is the production instance, wired to the real
// gh-shelling functions in internal/forge/github (TestNoGhExecOutsideForge
// keeps every gh invocation behind the forge seam):
//
//   - github_pat_ (fine-grained PAT): GitHub exposes no endpoint that reports
//     a fine-grained PAT's own restricted grant, so it is not introspectable.
//   - ghp_/gho_ (classic PAT / OAuth app token): oauthScopes reads the
//     X-OAuth-Scopes response header, which enumerates exactly what was
//     granted (ADR 0027's quickstart audit uses the same signal).
//   - ghs_ (GitHub App installation token): has no X-OAuth-Scopes header, but
//     an App identity has no ambient user role to blur the result the way a
//     fine-grained PAT's underlying account would, so repoPush's
//     `permissions.push` field accurately reflects the installation's own
//     grant.
//   - any other/unknown prefix: treated as not introspectable, the same safe
//     default as a fine-grained PAT, rather than trusting a signal that may
//     not mean what it means for the shapes above.
func newGHTokenIntrospector(oauthScopes func(token string) ([]string, error), repoPush func(token, repoSlug string) (bool, error)) tokenIntrospector {
	return func(token, repoSlug string) (tokenIntrospectionResult, error) {
		switch {
		case strings.HasPrefix(token, "github_pat_"):
			return tokenIntrospectionResult{Introspectable: false}, nil
		case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "gho_"):
			scopes, err := oauthScopes(token)
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
			push, err := repoPush(token, repoSlug)
			if err != nil {
				return tokenIntrospectionResult{}, err
			}
			return tokenIntrospectionResult{Introspectable: true, WriteCapable: push}, nil
		default:
			return tokenIntrospectionResult{Introspectable: false}, nil
		}
	}
}

// ghTokenIntrospector is the production tokenIntrospector, wired to the real
// gh-shelling functions in internal/forge/github.
var ghTokenIntrospector = newGHTokenIntrospector(github.TokenOAuthScopes, github.TokenRepoPushPermission)
