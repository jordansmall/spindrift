package main

import (
	"fmt"
	"io"
	"os"
)

// checkReadOnlyForgejoTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// startup token gate on the Forgejo side (sibling to checkReadOnlyTokenGate,
// the GitHub gate): under read-only, the Box must be handed a credential
// distinct from the Launcher's own FORGEJO_TOKEN -- otherwise read-only is a
// prompt-level fiction, since the Box would hold the very token that can
// write. The gate governs FORGEJO_TOKEN, relevant only when forgejo is the
// active Code Forge or Issue Tracker; read-write is untouched, and a
// pure-github (or pure-local) read-only deployment has no FORGEJO_TOKEN to
// withhold, so this never inspects BOX_FORGEJO_TOKEN outside those
// conditions, exactly like the GitHub gate.
//
// Unlike GitHub, Forgejo exposes no endpoint to introspect a token's granted
// scopes, so this gate can never confirm the Box token's non-write-capability
// the way the GitHub gate's introspector can -- verified is therefore always
// false, and a distinct token is accepted on trust (with a printed warning)
// rather than checked.
func checkReadOnlyForgejoTokenGate(c config, w io.Writer) (verified bool, err error) {
	if c.boxForgeAndIssueAccess != "read-only" {
		return false, nil
	}
	if c.codeForge != "forgejo" && c.issueTracker != "forgejo" {
		return false, nil
	}
	boxToken := os.Getenv("BOX_FORGEJO_TOKEN")
	if boxToken == "" {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_FORGEJO_TOKEN to be set to a credential distinct from FORGEJO_TOKEN — the Box must never receive the Launcher's own write-capable token")
	}
	if boxToken == c.forgejoToken {
		return false, fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only requires BOX_FORGEJO_TOKEN to differ from FORGEJO_TOKEN — it is byte-for-byte identical to the Launcher's own token, which defeats read-only")
	}
	fmt.Fprintln(w, "WARNING: Forgejo exposes no endpoint to introspect a token's granted scopes, so BOX_FORGEJO_TOKEN's write capability could not be determined. read-only trusts that it was provisioned with read-only scope; verify this yourself before relying on it.")
	return false, nil
}
