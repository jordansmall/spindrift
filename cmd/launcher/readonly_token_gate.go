package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
)

// errReadOnlyGateMisconfigured marks a read-only token gate misconfiguration
// (a row's Box token unset, identical to the Launcher's own
// token, or write-capable). It stays distinct from bootstrap.go's
// errConfigInvalid so bootstrapExitCode does not award exit 6 to
// dispatch/recover/preview; doctorExitCodeFor classifies it as exit 2 (#2942).
var errReadOnlyGateMisconfigured = errors.New("read-only token gate misconfigured")

// readOnlyTrustNotice closes every non-verified warning; doctor goldens pin its
// bytes.
const readOnlyTrustNotice = "read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it."

// checkReadOnlyTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// startup token gate (issue #1950) for one backend row: the Box must hold a
// credential distinct from the Launcher's own, or read-only is a fiction.
// read-write never reads the Box token. The access-mode guard is this check's
// own; whether the active backends use the row's token at all is
// gateRegistry's Applicable decision.
//
// A row without an IntrospectToken hook warns and returns (false, nil) rather
// than failing (ADR 0068 decision 6), so a new backend gets this gate by
// default. verified is true only when introspection confirmed the token has no
// write scopes, and is always false when err is non-nil.
func checkReadOnlyTokenGate(c config, row backendkit.Strategy, w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
		return false, nil
	}
	boxEnv, hostEnv := row.BoxTokenEnvVar, row.TokenEnvVar
	boxToken := os.Getenv(boxEnv)
	if boxToken == "" {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires %s to be set to a credential distinct from %s — the Box must never receive the Launcher's own write-capable token", errReadOnlyGateMisconfigured, boxEnv, hostEnv)
	}
	// A host token resolved from a _FILE/_CMD form reads back as "", so this
	// check cannot fire for it.
	if boxToken == c.hostTokenValue(hostEnv) {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires %s to differ from %s — it is byte-for-byte identical to the Launcher's own token, which defeats read-only", errReadOnlyGateMisconfigured, boxEnv, hostEnv)
	}
	if row.IntrospectToken == nil {
		fmt.Fprintf(w, "WARNING: the %s backend cannot introspect a token's scopes (no IntrospectToken hook), so %s's write capability could not be determined. "+readOnlyTrustNotice+"\n", row.DisplayName, boxEnv)
		return false, nil
	}
	result, err := row.IntrospectToken(boxToken, c.repoSlug)
	if err != nil {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only: introspecting %s failed: %w", doctor.ErrConnectivity, boxEnv, err)
	}
	if !result.Introspectable {
		reason := ""
		if result.Reason != "" {
			reason = " (" + result.Reason + ")"
		}
		fmt.Fprintf(w, "WARNING: %s's write capability could not be determined%s. "+readOnlyTrustNotice+"\n", boxEnv, reason)
		return false, nil
	}
	if result.WriteCapable {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires %s to carry no write scopes; the provided token is write-capable", errReadOnlyGateMisconfigured, boxEnv)
	}
	return true, nil
}
