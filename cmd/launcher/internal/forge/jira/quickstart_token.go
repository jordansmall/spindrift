package jira

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/forge"
)

// acquireToken validates the pasted token with a live Probe when a Connector
// sandbox is installed, else defers the check to `spindrift doctor`. Any other
// failure aborts the run: there is no retry loop.
func acquireToken(_ backendkit.Shared, _ backendkit.Lookup, p backendkit.TokenPrompt) (string, error) {
	token := p.PromptMasked("Jira API token (Jira Cloud API token; a password or token on Server/Data Center)")
	if token == "" {
		return "", fmt.Errorf("no Jira token provided — rerun `%s` and paste a token when prompted", p.RerunCmd)
	}
	// Quickstart installs no Connector sandbox, so the paths below the
	// deferral are reachable only from tests injecting a probe error;
	// `spindrift doctor` owns the live token check.
	if err := p.Probe(token); err != nil {
		if errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
			fmt.Fprintln(p.Out, "the token check needs the Connector sandbox, which quickstart does not run — deferred to `spindrift doctor`.")
			return token, nil
		}
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", fmt.Errorf("Jira token rejected by the API — check the token, SPINDRIFT_JIRA_CONNECTOR_EMAIL and JIRA_BASE_URL (issues.jira.baseURL), then rerun `%s` and paste a valid token: %w", p.RerunCmd, err)
		}
		return "", fmt.Errorf("Jira connectivity check failed — either the instance is unreachable or the project key is wrong; check JIRA_BASE_URL (issues.jira.baseURL) and JIRA_PROJECT_KEY (issues.jira.projectKey), then rerun `%s`: %w", p.RerunCmd, err)
	}
	fmt.Fprintln(p.Out, "ok: Jira token validated via live API ping")
	return token, nil
}
