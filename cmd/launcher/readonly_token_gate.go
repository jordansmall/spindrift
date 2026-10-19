package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge/github"
)

// errReadOnlyGateMisconfigured marks a read-only token gate misconfiguration
// (BOX_GH_TOKEN/BOX_FORGEJO_TOKEN unset, identical to the Launcher's own
// token, or write-capable). It stays distinct from bootstrap.go's
// errConfigInvalid so bootstrapExitCode does not award exit 6 to
// dispatch/recover/preview; doctorExitCodeFor classifies it as exit 2 (#2942).
var errReadOnlyGateMisconfigured = errors.New("read-only token gate misconfigured")

// checkReadOnlyTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// startup token gate (issue #1950): the Box must hold a credential distinct
// from the Launcher's GH_TOKEN, or read-only is a fiction. read-write never
// reads BOX_GH_TOKEN. The access-mode guard is this check's own; whether the
// active backends use GH_TOKEN at all is gateRegistry's Applicable decision.
// verified is true only when introspection confirmed the token has no write
// scopes, and is always false when err is non-nil.
func checkReadOnlyTokenGate(c config, introspect func(token, repoSlug string) (backendkit.TokenIntrospection, error), w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
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

// ghTokenIntrospector is the production Box-token introspector, swapped by
// tests.
var ghTokenIntrospector = github.Strategy.IntrospectToken
