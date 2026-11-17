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

// primaryGithubApplies reports whether the github backend row is primary and
// the issue tracker is github, the case where the Connector carries the issue
// writes. It looks the row up per call: backendrows loads after package init,
// when gateRegistry is built.
func primaryGithubApplies(c config) bool {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	return ok && row.Primary && c.IssueTracker == backend.GitHub.Name
}

// warnPrimaryWriteUndetermined reports that the Connector credential's issue-write
// capability could not be verified, without failing the launch.
func warnPrimaryWriteUndetermined(w io.Writer, env, reason string) {
	fmt.Fprintf(w, "WARNING: the primary github Connector's issue-write capability on %s could not be determined (%s).\n", env, reason)
}

// checkPrimaryConnectorWriteGate refuses a launch whose primary github
// Connector credential cannot write: the Connector sends comments, label
// transitions, the claim and verdicts with it, and a read-only token would
// fail them only after the claim. A credential whose capability cannot be
// read (a fine-grained PAT, an installation token, an unsupported hook, an
// unset env) warns rather than refuses, as the read-only token gate does. The
// env is final by now: mainRun resolves each row's _CMD form before any verb
// runs, so an empty value is not set, and backendrows.Use only checks that the
// row names a credential env.
func checkPrimaryConnectorWriteGate(c config, w io.Writer) error {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	if !ok {
		return nil
	}
	env := row.CredentialEnv
	token := os.Getenv(env)
	if token == "" {
		fmt.Fprintf(w, "WARNING: the primary github Connector's credential %s is not set, so its issue writes will fail.\n", env)
		return nil
	}
	// An installation token's introspection reports repository push (Contents
	// write), which says nothing about the Issues permission the Connector needs.
	if strings.HasPrefix(token, "ghs_") {
		warnPrimaryWriteUndetermined(w, env, "an installation token's introspection reports repository push (Contents write), not Issues")
		return nil
	}
	strategy, ok := backendkit.ByName(backend.GitHub.Name)
	if !ok {
		return fmt.Errorf("primary github Connector: no %s Strategy is registered to introspect %s", backend.GitHub.Name, env)
	}
	if strategy.IntrospectToken == nil {
		warnPrimaryWriteUndetermined(w, env, "no IntrospectToken hook")
		return nil
	}
	result, err := strategy.IntrospectToken(token, c.RepoSlug)
	if errors.Is(err, backendkit.ErrUnsupported) {
		warnPrimaryWriteUndetermined(w, env, doctor.UnsupportedReason(err))
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
		warnPrimaryWriteUndetermined(w, env, reason)
		return nil
	}
	if !result.WriteCapable {
		return newLaunchGateConfigError("the primary github Connector writes issues (comments, labels, the claim, verdicts) with %s, but that credential cannot write to %s: grant it Issues read and write (a classic token needs the repo or public_repo scope)", env, c.RepoSlug)
	}
	return nil
}
