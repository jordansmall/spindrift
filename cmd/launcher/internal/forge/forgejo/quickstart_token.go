package forgejo

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/forge"
)

// acquireToken validates the pasted token with a live Probe rather than a
// prefix audit: Forgejo PATs have no sniffable prefix the way GitHub's do.
// There is no retry loop, so every failure path here aborts the run and tells
// the operator to rerun the wizard.
func acquireToken(s backendkit.Shared, l backendkit.Lookup, p backendkit.TokenPrompt) (string, error) {
	k, err := ReadKnobs(l)
	if err != nil {
		return "", err
	}
	baseURL, repoSlug := k.ForgejoBaseURL, s.RepoSlug
	token := p.PromptMasked("Forgejo token (paste a Forgejo personal access token)")
	if token == "" {
		return "", fmt.Errorf("no Forgejo token provided — rerun `%s` and paste a token when prompted", p.RerunCmd)
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
			return "", fmt.Errorf("Forgejo token rejected by the API at %s — if this is the wrong instance, point the git remote (or, for a Forgejo tracker beside another forge, the issue tracker repo URL answer) at the right one, then rerun `%s` and paste a valid token: %w", baseURL, p.RerunCmd, err)
		}
		return "", fmt.Errorf("Forgejo connectivity check to %s (repo slug %q) failed — either the instance is unreachable or the repo slug is wrong; check both, then rerun `%s`: %w", baseURL, repoSlug, p.RerunCmd, err)
	}
	fmt.Fprintln(p.Out, "ok: Forgejo token validated via live API ping")
	return token, nil
}
