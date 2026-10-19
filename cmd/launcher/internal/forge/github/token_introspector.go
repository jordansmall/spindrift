package github

import (
	"strings"

	"spindrift.dev/launcher/internal/backendkit"
)

// ghTokenWriteScopes are the classic/OAuth scopes that grant repo write access
// (the same X-OAuth-Scopes signal ADR 0027's quickstart audit uses). Classic
// tokens have no read-only variant of repo access, so either scope's presence
// alone means the token can push.
var ghTokenWriteScopes = map[string]bool{
	"repo":        true,
	"public_repo": true,
}

// notIntrospectableReason is the read-only gate's explanation for a GitHub
// token whose grants cannot be determined.
const notIntrospectableReason = "e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect"

// newTokenIntrospector builds the Strategy's IntrospectToken, which dispatches
// on the token's prefix, since GitHub reports write capability through a
// different signal for each token shape. Callers inject oauthScopes and
// repoPush so tests can drive the dispatch without a live gh call, and so every
// gh invocation stays behind the forge seam (TestNoGhExecOutsideForge).
func newTokenIntrospector(oauthScopes func(token string) ([]string, error), repoPush func(token, repoSlug string) (bool, error)) backendkit.TokenIntrospector {
	return func(token, repoSlug string) (backendkit.TokenIntrospection, error) {
		switch {
		case strings.HasPrefix(token, "github_pat_"):
			// GitHub exposes no endpoint reporting a fine-grained PAT's own
			// restricted grant.
			return backendkit.TokenIntrospection{Introspectable: false, Reason: notIntrospectableReason}, nil
		case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "gho_"):
			// X-OAuth-Scopes enumerates exactly what the token was granted.
			scopes, err := oauthScopes(token)
			if err != nil {
				return backendkit.TokenIntrospection{}, err
			}
			writeCapable := false
			for _, s := range scopes {
				if ghTokenWriteScopes[s] {
					writeCapable = true
					break
				}
			}
			return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: writeCapable}, nil
		case strings.HasPrefix(token, "ghs_"):
			// An App installation token carries no X-OAuth-Scopes header, but it
			// also has no ambient user role to blur permissions.push the way a
			// fine-grained PAT's underlying account would.
			push, err := repoPush(token, repoSlug)
			if err != nil {
				return backendkit.TokenIntrospection{}, err
			}
			return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: push}, nil
		default:
			// An unknown prefix is not introspectable: a signal that is reliable
			// for the shapes above may mean something else here.
			return backendkit.TokenIntrospection{Introspectable: false, Reason: notIntrospectableReason}, nil
		}
	}
}
