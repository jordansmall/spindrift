package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge/github"
)

// errReadOnlyGateMisconfigured marks a read-only token gate misconfiguration
// (BOX_GH_TOKEN/BOX_FORGEJO_TOKEN unset, identical to the Launcher's own
// token, or write-capable). It stays distinct from bootstrap.go's
// errConfigInvalid so bootstrapExitCode does not award exit 6 to
// dispatch/recover/preview; doctorExitCodeFor classifies it as exit 2 (#2942).
var errReadOnlyGateMisconfigured = errors.New("read-only token gate misconfigured")

// tokenIntrospectionResult reports what a Box token probe learned.
// Introspectable is false only for a fine-grained PAT (github_pat_ prefix),
// whose granted permissions GitHub exposes no endpoint to enumerate.
// WriteCapable is meaningful only when Introspectable is true.
type tokenIntrospectionResult struct {
	Introspectable bool
	WriteCapable   bool
}

// tokenIntrospector probes token for write capability, scoped to repoSlug
// where the probe itself is repo-scoped.
type tokenIntrospector func(token, repoSlug string) (tokenIntrospectionResult, error)

// checkReadOnlyTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// startup token gate (issue #1950): the Box must hold a credential distinct
// from the Launcher's GH_TOKEN, or read-only is a fiction. read-write never
// reads BOX_GH_TOKEN. verified is true only when introspection confirmed the
// token has no write scopes, and is always false when err is non-nil.
func checkReadOnlyTokenGate(c config, introspect tokenIntrospector, w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
		return false, nil
	}
	// This gate governs GH_TOKEN, so it applies only when the active Code Forge
	// or Issue Tracker resolves to a backend sharing GitHub's TokenEnvVar.
	// gateRegistry's "read-only-token-github" Applicable closure keys off the
	// same check, so the two can never disagree. A pure-forgejo or pure-local
	// read-only deployment has no GH_TOKEN to withhold.
	if !tokenGateApplicable(c, backend.GitHub) {
		return false, nil
	}
	boxToken := os.Getenv("BOX_GH_TOKEN")
	if boxToken == "" {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to be set to a credential distinct from GH_TOKEN — the Box must never receive the Launcher's own write-capable token", errReadOnlyGateMisconfigured)
	}
	if boxToken == c.ghToken {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to differ from GH_TOKEN — it is byte-for-byte identical to the Launcher's own token, which defeats read-only", errReadOnlyGateMisconfigured)
	}
	result, err := introspect(boxToken, c.repoSlug)
	if err != nil {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only: introspecting BOX_GH_TOKEN failed: %w", doctor.ErrConnectivity, err)
	}
	if !result.Introspectable {
		fmt.Fprintln(w, "WARNING: BOX_GH_TOKEN's write capability could not be determined (e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect). read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it.")
		return false, nil
	}
	if result.WriteCapable {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_GH_TOKEN to carry no write scopes; the provided token is write-capable", errReadOnlyGateMisconfigured)
	}
	return true, nil
}

// ghTokenWriteScopes are the classic/OAuth scopes that grant repo write access
// (the same X-OAuth-Scopes signal ADR 0027's quickstart audit uses). Classic
// tokens have no read-only variant of repo access, so either scope's presence
// alone means the token can push.
var ghTokenWriteScopes = map[string]bool{
	"repo":        true,
	"public_repo": true,
}

// newGHTokenIntrospector builds a tokenIntrospector that dispatches on the
// token's prefix, since GitHub reports write capability through a different
// signal for each token shape. Callers inject oauthScopes and repoPush so tests
// can drive the dispatch without a live gh call, and so every gh invocation
// stays behind the forge seam (TestNoGhExecOutsideForge).
func newGHTokenIntrospector(oauthScopes func(token string) ([]string, error), repoPush func(token, repoSlug string) (bool, error)) tokenIntrospector {
	return func(token, repoSlug string) (tokenIntrospectionResult, error) {
		switch {
		case strings.HasPrefix(token, "github_pat_"):
			// GitHub exposes no endpoint reporting a fine-grained PAT's own
			// restricted grant.
			return tokenIntrospectionResult{Introspectable: false}, nil
		case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "gho_"):
			// X-OAuth-Scopes enumerates exactly what the token was granted.
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
			// An App installation token carries no X-OAuth-Scopes header, but it
			// also has no ambient user role to blur permissions.push the way a
			// fine-grained PAT's underlying account would.
			push, err := repoPush(token, repoSlug)
			if err != nil {
				return tokenIntrospectionResult{}, err
			}
			return tokenIntrospectionResult{Introspectable: true, WriteCapable: push}, nil
		default:
			// An unknown prefix is not introspectable: a signal that is reliable
			// for the shapes above may mean something else here.
			return tokenIntrospectionResult{Introspectable: false}, nil
		}
	}
}

// ghTokenIntrospector is the production tokenIntrospector.
var ghTokenIntrospector = newGHTokenIntrospector(github.TokenOAuthScopes, github.TokenRepoPushPermission)
