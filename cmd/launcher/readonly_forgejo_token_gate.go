package main

import (
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/backend"
)

// checkReadOnlyForgejoTokenGate requires the Box to hold a FORGEJO_TOKEN distinct from
// the Launcher's own; otherwise the Box holds the very token that can write. It governs
// only an active backend keyed to Forgejo's TokenEnvVar, matching gateRegistry's
// "read-only-token-forgejo" Applicable closure so the two cannot disagree. Forgejo has
// no scope-introspection endpoint, so verified is always false and the token is trusted.
func checkReadOnlyForgejoTokenGate(c config, w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
		return false, nil
	}
	if !tokenGateApplicable(c, backend.Forgejo) {
		return false, nil
	}
	boxToken := os.Getenv("BOX_FORGEJO_TOKEN")
	if boxToken == "" {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_FORGEJO_TOKEN to be set to a credential distinct from FORGEJO_TOKEN — the Box must never receive the Launcher's own write-capable token", errReadOnlyGateMisconfigured)
	}
	if boxToken == c.forgejoToken {
		return false, fmt.Errorf("%w: BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_FORGEJO_TOKEN to differ from FORGEJO_TOKEN — it is byte-for-byte identical to the Launcher's own token, which defeats read-only", errReadOnlyGateMisconfigured)
	}
	fmt.Fprintln(w, "WARNING: Forgejo exposes no endpoint to introspect a token's granted scopes, so BOX_FORGEJO_TOKEN's write capability could not be determined. read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it.")
	return false, nil
}
