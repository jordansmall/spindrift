package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
)

// primaryConnectorWriteGateName names the gate in gateRegistry.
const primaryConnectorWriteGateName = "primary-connector-write-github"

// githubSide is one half of the Connector's github writes: what it sends, the
// permissions beyond Issues write the credential needs to send it, and why a
// refusal names them.
type githubSide struct {
	writes string
	perms  []string
	hint   string
}

// routedGithubSides lists the sides of the Connector that carry github writes
// under c: the issue tracker (issue writes) and the code forge (PR writes; a
// routed row serves those, and the github forge package serves only
// CODE_FORGE=github). Empty means the gate does not apply.
func routedGithubSides(c config) []githubSide {
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

// routedGithubApplies reports whether the github backend row is routed
// and the Connector carries github writes. It looks the row up per call:
// backendrows loads after package init, when gateRegistry is built.
func routedGithubApplies(c config) bool {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	routed := ok && backendkit.ForgeReadsThroughConnector(row.Name)
	return routed && len(routedGithubSides(c)) > 0
}

// routedGithubWrites describes what the Connector writes with the credential
// under c, so the refusal names only the sides that apply.
func routedGithubWrites(c config) string {
	var parts []string
	for _, s := range routedGithubSides(c) {
		parts = append(parts, s.writes)
	}
	return strings.Join(parts, " and ")
}

// routedWritePermissions is the permission set the credential needs under c:
// what a fine-grained PAT or installation token must be granted, and what the
// refusal for an introspectable read-only token asks for.
func routedWritePermissions(c config) string {
	// Both sides need Issues write: PR comments post through the issues
	// comments endpoint too.
	write := []string{"Issues"}
	for _, s := range routedGithubSides(c) {
		write = append(write, s.perms...)
	}
	list := write[0]
	if n := len(write); n > 1 {
		list = strings.Join(write[:n-1], ", ") + " and " + write[n-1]
	}
	return "Metadata read; " + list + " read and write; Checks and Commit statuses read"
}

// warnRoutedWriteUndetermined reports that the Connector credential's write
// capability could not be verified, without failing the launch.
func warnRoutedWriteUndetermined(w io.Writer, c config, env, reason string) {
	fmt.Fprintf(w, "WARNING: the primary github Connector's write capability on %s could not be determined (%s); it needs %s.\n", env, reason, routedWritePermissions(c))
}

// checkRoutedConnectorWriteGate refuses a launch whose routed github Connector
// credential cannot write: a read-only token would fail the Connector's writes
// only after the claim. A credential whose capability cannot be read (a
// fine-grained PAT, an installation token, an unsupported hook, an unset env)
// warns rather than refuses. The env is final here: mainRun resolves each
// row's _CMD form before any verb runs, so an empty value is not set, and
// backendrows.Use only checks that the row names a credential env.
func checkRoutedConnectorWriteGate(c config, w io.Writer) error {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	if !ok {
		return nil
	}
	env := row.CredentialEnv
	token := os.Getenv(env)
	if token == "" {
		fmt.Fprintf(w, "WARNING: the primary github Connector's credential %s is not set, so its writes will fail; it needs %s.\n", env, routedWritePermissions(c))
		return nil
	}
	// An installation token's introspection reports repository push (Contents
	// write), which says nothing about the Issues or Pull requests permissions
	// the Connector needs.
	if strings.HasPrefix(token, "ghs_") {
		warnRoutedWriteUndetermined(w, c, env, "an installation token's introspection reports repository push (Contents write), not Issues or Pull requests")
		return nil
	}
	strategy, ok := backendkit.ByName(backend.GitHub.Name)
	if !ok {
		return fmt.Errorf("primary github Connector: no %s Strategy is registered to introspect %s", backend.GitHub.Name, env)
	}
	if strategy.IntrospectToken == nil {
		warnRoutedWriteUndetermined(w, c, env, "no IntrospectToken hook")
		return nil
	}
	result, err := strategy.IntrospectToken(token, c.RepoSlug)
	if errors.Is(err, backendkit.ErrUnsupported) {
		warnRoutedWriteUndetermined(w, c, env, doctor.UnsupportedReason(err))
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: primary github Connector: introspecting %s failed: %w", doctor.ErrConnectivity, env, err)
	}
	if !result.Introspectable {
		reason := "token not introspectable"
		if result.Reason != "" {
			reason = result.Reason
		}
		warnRoutedWriteUndetermined(w, c, env, reason)
		return nil
	}
	if !result.WriteCapable {
		var hints []string
		for _, s := range routedGithubSides(c) {
			if s.hint != "" {
				hints = append(hints, s.hint)
			}
		}
		hints = append(hints, "a classic token needs the repo or public_repo scope")
		return newLaunchGateConfigError("the primary github Connector writes %s with %s, but that credential cannot write to %s: grant it %s (%s)", routedGithubWrites(c), env, c.RepoSlug, routedWritePermissions(c), strings.Join(hints, "; "))
	}
	return nil
}
