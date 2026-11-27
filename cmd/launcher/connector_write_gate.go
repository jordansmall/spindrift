package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
)

// connectorWriteGateName names the gate in gateRegistry.
const connectorWriteGateName = "connector-write-github"

// githubSide is one half of the Connector's github writes: what it sends, the
// permissions beyond Issues write the credential needs to send it, and why a
// refusal names them.
type githubSide struct {
	writes string
	perms  []string
	hint   string
}

// githubSides lists the sides of the Connector that carry github writes
// under c: the issue tracker (issue writes) and the code forge (PR writes; a
// Connector serves those, and the github forge package serves only
// CODE_FORGE=github). Empty means the gate does not apply.
func githubSides(c config) []githubSide {
	var sides []githubSide
	if c.IssueTracker == backend.GitHub.Name {
		sides = append(sides, githubSide{writes: "issues (comments, labels, the claim, verdicts)"})
	}
	if c.CodeForge == backend.GitHub.Name {
		sides = append(sides, githubSide{
			writes: "pull requests (the ready/draft flip, comments, draft PRs, branch deletes)",
			perms:  []string{"Pull requests", "Contents"},
			// GitHub refuses a fine-grained PAT the flip without Contents write.
			hint: "the PR ready/draft flip needs Contents write",
		})
	}
	return sides
}

// githubApplies reports whether the github backend row reads through the
// Connector and the Connector carries github writes. It looks the row up per
// call: the row lives in c.connectors, the config's own runtime.
func githubApplies(c config) bool {
	row, ok := c.connectors.Row(backend.GitHub.Name)
	readsThroughConnector := ok && backendkit.ForgeReadsThroughConnector(row.Name)
	return readsThroughConnector && len(githubSides(c)) > 0
}

// githubWrites describes what the Connector writes with the credential
// under c, so the refusal names only the sides that apply.
func githubWrites(c config) string {
	var parts []string
	for _, s := range githubSides(c) {
		parts = append(parts, s.writes)
	}
	return strings.Join(parts, " and ")
}

// connectorWritePermissions is the permission set the credential needs under c:
// what a fine-grained PAT or installation token must be granted, and what the
// refusal for an introspectable read-only token asks for.
func connectorWritePermissions(c config) string {
	// Both sides need Issues write: PR comments post through the issues
	// comments endpoint too.
	write := []string{"Issues"}
	for _, s := range githubSides(c) {
		write = append(write, s.perms...)
	}
	list := write[0]
	if n := len(write); n > 1 {
		list = strings.Join(write[:n-1], ", ") + " and " + write[n-1]
	}
	return "Metadata read; " + list + " read and write; Checks and Commit statuses read"
}

// warnConnectorWriteUndetermined reports that the Connector credential's write
// capability could not be verified, without failing the launch.
func warnConnectorWriteUndetermined(w io.Writer, c config, env, reason string) {
	fmt.Fprintf(w, "WARNING: the github Connector's write capability on %s could not be determined (%s); it needs %s.\n", env, reason, connectorWritePermissions(c))
}

// checkConnectorWriteGate refuses a launch whose github Connector
// credential cannot write: a read-only token would fail the Connector's writes
// only after the claim. A credential whose capability cannot be read (a
// fine-grained PAT, an installation token, an unsupported hook) warns rather
// than refuses. An unset env passes silently: the Required backend-credential
// row refuses it first. It reads the launch-time env, which parseFlags and
// applySecretCmdFallback fill for every core secret before any verb runs; a
// credential only a refresh file supplies is not seen, and so not introspected.
func checkConnectorWriteGate(c config, w io.Writer) error {
	row, ok := c.connectors.Row(backend.GitHub.Name)
	if !ok {
		return nil
	}
	env := row.CredentialEnv
	token := os.Getenv(env)
	if token == "" {
		return nil
	}
	// An installation token's introspection reports repository push (Contents
	// write), which says nothing about the Issues or Pull requests permissions
	// the Connector needs.
	if strings.HasPrefix(token, "ghs_") {
		warnConnectorWriteUndetermined(w, c, env, "an installation token's introspection reports repository push (Contents write), not Issues or Pull requests")
		return nil
	}
	strategy, ok := backendkit.ByName(backend.GitHub.Name)
	if !ok {
		return fmt.Errorf("github Connector: no %s Strategy is registered to introspect %s", backend.GitHub.Name, env)
	}
	if strategy.IntrospectToken == nil {
		warnConnectorWriteUndetermined(w, c, env, "no IntrospectToken hook")
		return nil
	}
	result, err := strategy.IntrospectToken(c.connectors, token, c.RepoSlug)
	if errors.Is(err, backendkit.ErrUnsupported) {
		warnConnectorWriteUndetermined(w, c, env, doctor.UnsupportedReason(err))
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: github Connector: introspecting %s failed: %w", doctor.ErrConnectivity, env, err)
	}
	if !result.Introspectable {
		reason := "token not introspectable"
		if result.Reason != "" {
			reason = result.Reason
		}
		warnConnectorWriteUndetermined(w, c, env, reason)
		return nil
	}
	if !result.WriteCapable {
		var hints []string
		for _, s := range githubSides(c) {
			if s.hint != "" {
				hints = append(hints, s.hint)
			}
		}
		hints = append(hints, "a classic token needs the repo or public_repo scope")
		return newLaunchGateConfigError("the github Connector writes %s with %s, but that credential cannot write to %s: grant it %s (%s)", githubWrites(c), env, c.RepoSlug, connectorWritePermissions(c), strings.Join(hints, "; "))
	}
	return nil
}
